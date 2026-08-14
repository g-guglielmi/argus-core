package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // embed the IANA tz database so ARGUS_TZ works on distroless

	"argus/internal/auth"
	"argus/internal/config"
	"argus/internal/secret"
	"argus/internal/server"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg := config.Load()

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		logger.Error("open database", "path", cfg.DBPath(), "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// At-rest encryption for stored secrets (channel creds, TOTP seeds, signing key).
	cipher, keySrc, err := secret.Load(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		logger.Error("init at-rest encryption", "err", err)
		os.Exit(1)
	}
	st.SetCipher(cipher)
	if n, err := st.EncryptPlaintextSecrets(context.Background()); err != nil {
		logger.Warn("encrypt existing secrets", "err", err)
	} else {
		logger.Info("at-rest encryption enabled", "key_source", keySrc, "migrated_rows", n)
	}

	if err := bootstrapAdmin(context.Background(), st, cfg, logger); err != nil {
		logger.Error("bootstrap admin", "err", err)
		os.Exit(1)
	}

	zbx := zabbix.New(cfg.ZabbixAPIURL, cfg.ZabbixAPIToken)

	// Runtime-editable settings (Zabbix connection, public URL, timezone, login limits). Loads
	// any stored overrides and applies the effective values to the Zabbix client + rate limiter.
	// Env vars, when set, take precedence and lock the corresponding field in the UI.
	mgr, err := settings.New(context.Background(), st, zbx)
	if err != nil {
		logger.Error("init settings", "err", err)
		os.Exit(1)
	}

	// Background sweeper: re-enables timed pauses and prunes expired suppressions.
	sweepCtx, sweepCancel := context.WithCancel(context.Background())
	defer sweepCancel()
	go server.StartExpirySweeper(sweepCtx, st, zbx, logger)

	// Background notifier: polls problems and dispatches alerts to the configured channels.
	notifySecret := server.GetSigningSecret(context.Background(), st)
	go server.StartNotifier(sweepCtx, st, zbx, logger, mgr, notifySecret)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, zbx, st, logger, mgr),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Info("argus starting", "listen", cfg.Listen, "zabbix_api", cfg.ZabbixAPIURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// bootstrapAdmin seeds the first admin user from env vars on an empty database.
func bootstrapAdmin(ctx context.Context, st *store.Store, cfg config.Config, logger *slog.Logger) error {
	n, err := st.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if cfg.AdminEmail == "" || cfg.AdminPassword == "" {
		logger.Warn("no users exist; set ARGUS_ADMIN_EMAIL and ARGUS_ADMIN_PASSWORD and restart to create the first admin")
		return nil
	}
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return err
	}
	if _, err := st.CreateUser(ctx, store.User{Email: cfg.AdminEmail, Role: "admin", PasswordHash: hash}); err != nil {
		return err
	}
	logger.Info("created initial admin user", "email", cfg.AdminEmail)
	return nil
}
