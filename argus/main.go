// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
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
	// The Docker HEALTHCHECK runs the binary itself (healthcheck.go): check, exit, touch nothing else.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}
	// The core host's backup script asks for its plan and a database copy (backupcmd.go).
	if len(os.Args) > 1 && (os.Args[1] == "backup-plan" || os.Args[1] == "backup-db") {
		os.Exit(runBackupCommand(os.Args[1:]))
	}
	// The argus-updater copies this image's collectors into the core host's Zabbix (collectors.go).
	if len(os.Args) > 1 && os.Args[1] == "install-collectors" {
		os.Exit(runInstallCollectors(os.Args[1:]))
	}
	// The argus-updater writes this image's host scripts and units onto the core host (hosttools.go).
	if len(os.Args) > 1 && os.Args[1] == "install-host-tools" {
		os.Exit(runInstallHostTools(os.Args[1:]))
	}
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
	// A short ARGUS_SECRET_KEY is guessable offline from a copy of the database (the salt is a
	// public constant). A new install is refused one; an install that already holds data under
	// it must keep starting, and is told loudly instead.
	_, hadCanary, _ := st.MetaGet(context.Background(), "cipher_canary")
	if k := strings.TrimSpace(cfg.SecretKey); k != "" && len(k) < secret.MinEnvKeyLen {
		if !hadCanary {
			logger.Error("ARGUS_SECRET_KEY is too short: use at least 20 characters (openssl rand -hex 32), or unset it to use a generated secret.key", "length", len(k))
			os.Exit(1)
		}
		logger.Warn("ARGUS_SECRET_KEY is short: a copy of the database could be opened by guessing it. Set a longer key (at least 20 characters) and re-enter the stored secrets, or move to the generated secret.key", "length", len(k))
	}
	// The key must be the one the database was encrypted with; otherwise every stored credential
	// is unreadable and the app must not run as if they were blank (or worse, use the ciphertext).
	verr := st.VerifyCipher(context.Background())
	if verr != nil && strings.TrimSpace(cfg.SecretKey) != "" {
		// A database written by an earlier release under ARGUS_SECRET_KEY used a plain SHA-256 of
		// the value as its key; the same variable now derives the key with argon2id. When the old
		// derivation still opens the canary, re-encrypt everything once and carry on.
		if old := secret.LegacyEnvCipher(cfg.SecretKey); old != nil && st.VerifyCipherWith(context.Background(), old) == nil {
			n, rerr := st.RotateEncryptedSecrets(context.Background(), old)
			if rerr != nil {
				logger.Error("re-encrypt stored secrets under the new key derivation", "err", rerr)
				os.Exit(1)
			}
			logger.Info("stored secrets re-encrypted under the stronger ARGUS_SECRET_KEY derivation", "rows", n)
			verr = nil
		}
	}
	if verr != nil {
		if !cfg.SecretKeyReset {
			logger.Error("at-rest encryption key does not match the database: restore secret.key / ARGUS_SECRET_KEY, or set ARGUS_SECRET_KEY_RESET=true once to drop the unreadable secrets and re-enter them",
				"key_source", keySrc, "err", verr)
			os.Exit(1)
		}
		n, rerr := st.ResetEncryptedSecrets(context.Background())
		if rerr != nil {
			logger.Error("reset encrypted secrets", "err", rerr)
			os.Exit(1)
		}
		logger.Warn("ARGUS_SECRET_KEY_RESET: dropped every secret the current key can't read: notification channel credentials, SNMP and UniFi credentials, the Zabbix API token kept in Settings, the backup passphrase and its SMB, S3 and SSH credentials, status-page and push-sensor links, and users' two-factor (set it up again). Re-enter them, and remove the variable now.", "rows", n)
	} else if cfg.SecretKeyReset {
		logger.Warn("ARGUS_SECRET_KEY_RESET is set but the key matches the database; nothing was reset. Remove the variable.")
	}
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

	// The Zabbix API token is a super-admin credential sent as a Bearer header on every poll: over
	// plain http to another host it crosses the LAN in clear. The appliance serves the API over
	// https on :8443 for this case, with a certificate from the monitoring CA, which the client
	// trusts when the CA file is at hand (ARGUS_ZABBIX_CA_FILE, else the enrollment CA's cert).
	if cfg.ZabbixCAFile != "" {
		pem, err := os.ReadFile(cfg.ZabbixCAFile)
		if err != nil {
			logger.Warn("Zabbix CA file not read; the API's certificate must then be signed by a system-trusted CA", "file", cfg.ZabbixCAFile, "err", err)
		} else if err := zbx.TrustCA(pem); err != nil {
			logger.Warn("Zabbix CA file not usable", "file", cfg.ZabbixCAFile, "err", err)
		} else {
			logger.Info("Zabbix API certificates signed by the CA in this file are trusted", "file", cfg.ZabbixCAFile)
		}
		// The core VM renews the CA in place (argus-cert-renew): the renewed file is trusted without a restart.
		go func(last []byte) {
			for range time.Tick(5 * time.Minute) {
				cur, err := os.ReadFile(cfg.ZabbixCAFile)
				if err != nil || bytes.Equal(cur, last) {
					continue
				}
				if err := zbx.TrustCA(cur); err != nil {
					logger.Warn("Zabbix CA file changed but is not usable; the one read before stays trusted", "file", cfg.ZabbixCAFile, "err", err)
				} else {
					logger.Info("Zabbix CA file changed: the CA in it is trusted now", "file", cfg.ZabbixCAFile)
				}
				last = cur
			}
		}(pem)
	}
	if u, err := url.Parse(zbx.URL()); err == nil && u.Scheme == "http" {
		switch u.Hostname() {
		case "127.0.0.1", "::1", "localhost", "host.docker.internal":
		default:
			logger.Warn("the Zabbix API URL is plain http to another host: the API token travels in clear on every poll; put TLS on the Zabbix frontend (the core appliance serves it on :8443) and use https", "zabbix_api", zbx.URL())
		}
	}

	// Background sweeper: re-enables timed pauses and prunes expired suppressions.
	sweepCtx, sweepCancel := context.WithCancel(context.Background())
	defer sweepCancel()
	go server.StartExpirySweeper(sweepCtx, st, zbx, logger)

	// The alert-link signing secret, shared by the notifier (signs) and the server (verifies). A
	// secret that can't be read is a stop, not an empty key anyone could sign with.
	notifySecret, err := server.GetSigningSecret(context.Background(), st)
	if err != nil {
		logger.Error("alert-link signing secret", "err", err)
		os.Exit(1)
	}
	// Background notifier: polls problems and dispatches alerts to the configured channels.
	go server.StartNotifier(sweepCtx, st, zbx, logger, mgr, notifySecret)

	// One-time backfill: give each already-enrolled proxy its own site host group.
	go server.EnsureSiteGroups(sweepCtx, st, zbx, logger)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, zbx, st, logger, mgr, notifySecret),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second, // above the longest handler budget (a status build, a census)
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
		if cfg.AdminPassword != "" {
			logger.Warn("ARGUS_ADMIN_PASSWORD is still set although users exist: it is ignored, and a human's password has no place in a container's environment (docker inspect shows it); remove it")
		}
		return nil
	}
	// The same shape the Users page gives an account: a trimmed, lowercased email (sign-in matches
	// it as typed) and a password of at least 8 characters.
	email := strings.ToLower(strings.TrimSpace(cfg.AdminEmail))
	if email == "" || cfg.AdminPassword == "" {
		logger.Warn("no users exist; set ARGUS_ADMIN_EMAIL and ARGUS_ADMIN_PASSWORD and restart to create the first admin")
		return nil
	}
	if len(cfg.AdminPassword) < 8 {
		return errors.New("ARGUS_ADMIN_PASSWORD must be at least 8 characters")
	}
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return err
	}
	if _, err := st.CreateUser(ctx, store.User{Email: email, Role: "admin", PasswordHash: hash}); err != nil {
		return err
	}
	logger.Info("created initial admin user", "email", email)
	return nil
}
