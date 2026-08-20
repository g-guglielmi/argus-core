package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"argus/internal/auth"
	"argus/internal/config"
	"argus/internal/mfa"
	"argus/internal/pki"
	"argus/internal/ratelimit"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
	"argus/web"

	"github.com/go-webauthn/webauthn/webauthn"
)

const mfaChallengeTTL = 10 * time.Minute

type Server struct {
	cfg           config.Config
	zbx           *zabbix.Client
	st            *store.Store
	logger        *slog.Logger
	mgr           *settings.Manager  // runtime-editable settings (Zabbix conn, public URL, tz, limits)
	dummyHash     string             // for constant-ish login timing when a user doesn't exist
	wa            *webauthn.WebAuthn // nil when passkeys are not configured
	signingSecret string             // HMAC secret for signed alert links
	loginLimiter  *ratelimit.Limiter // brute-force protection for login (owned by mgr)
	ca            *pki.CA            // nil when probe enrollment is not configured
	probeLatest   *probeLatestCache  // newest published probe version, polled from public GHCR
	appLatest     *appLatestCache    // newest published app release, polled from public GHCR
}

func New(cfg config.Config, zbx *zabbix.Client, st *store.Store, logger *slog.Logger, mgr *settings.Manager) http.Handler {
	dummy, _ := auth.HashPassword("argus-nonexistent-user")
	s := &Server{cfg: cfg, zbx: zbx, st: st, logger: logger, mgr: mgr, dummyHash: dummy,
		signingSecret: GetSigningSecret(context.Background(), st),
		loginLimiter:  mgr.Limiter(), probeLatest: &probeLatestCache{}, appLatest: &appLatestCache{}}
	// Poll public GHCR for the newest probe revision so the fleet view can flag "-rN available"
	// even when the target is "latest". Background; a failure just leaves it unknown.
	s.startProbeLatestRefresh(context.Background())
	// Same for the app image, so the UI can show whether this instance is on the newest release.
	s.startAppLatestRefresh(context.Background())

	// Probe enrollment is available only when the CA is mounted. A load failure disables it
	// (logged) rather than blocking startup.
	if cfg.CACertFile != "" || cfg.CAKeyFile != "" {
		if ca, err := pki.Load(cfg.CACertFile, cfg.CAKeyFile); err != nil {
			logger.Error("probe enrollment disabled: could not load CA", "err", err)
		} else {
			s.ca = ca
			logger.Info("probe enrollment enabled", "ca_subject", ca.SubjectCN())
		}
	}

	if cfg.PasskeysEnabled() {
		wa, err := webauthn.New(&webauthn.Config{
			RPID:          cfg.RPID,
			RPDisplayName: cfg.RPDisplayName,
			RPOrigins:     cfg.RPOrigins,
		})
		if err != nil {
			logger.Error("webauthn init failed; passkeys disabled", "err", err)
		} else {
			s.wa = wa
			logger.Info("passkeys enabled", "rp_id", cfg.RPID, "origins", cfg.RPOrigins)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/health", s.handleAPIHealth)
	mux.HandleFunc("GET /api/features", s.handleFeatures)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	// self-service password reset (public; email-delivered single-use token)
	mux.HandleFunc("POST /api/password-reset/request", s.handleRequestPasswordReset)
	mux.HandleFunc("POST /api/password-reset/confirm", s.handleConfirmPasswordReset)
	// probe enrollment (public; authenticated by a single-use enrollment token)
	mux.HandleFunc("POST /api/enroll", s.handleEnroll)
	// probe fleet check-in (public; authenticated by the long-lived probe token from enrollment)
	mux.HandleFunc("POST /api/probes/checkin", s.handleProbeCheckin)
	// signed one-click acknowledge link from notifications (public; HMAC-verified, GET confirms)
	mux.HandleFunc("GET /api/alert/ack", s.handleAlertAck)
	mux.HandleFunc("POST /api/alert/ack", s.handleAlertAck)
	mux.HandleFunc("POST /api/login/totp", s.handleLoginTOTP)
	mux.HandleFunc("POST /api/login/passkey/begin", s.handlePasskeyLoginBegin)
	mux.HandleFunc("POST /api/login/passkey/finish", s.handlePasskeyLoginFinish)
	mux.HandleFunc("GET /api/version", auth.RequireAuth(s.handleVersion))
	mux.HandleFunc("GET /api/version/notes", auth.RequireAuth(s.handleVersionNotes))
	mux.HandleFunc("POST /api/version/check", auth.RequireRole("admin", s.handleVersionCheck))
	mux.HandleFunc("GET /api/version/tags", auth.RequireAuth(s.handleVersionTags))
	mux.HandleFunc("GET /api/me", auth.RequireAuth(s.handleMe))
	mux.HandleFunc("POST /api/me/password", auth.RequireAuth(s.handleChangeOwnPassword))
	mux.HandleFunc("POST /api/me/preferences", auth.RequireAuth(s.handleUpdatePreferences))

	// monitoring read path (any signed-in user)
	mux.HandleFunc("GET /api/problems", auth.RequireAuth(s.handleProblems))
	mux.HandleFunc("GET /api/sensors", auth.RequireAuth(s.handleSensors))
	mux.HandleFunc("GET /api/spark", auth.RequireAuth(s.handleSpark))
	mux.HandleFunc("GET /api/proxies", auth.RequireAuth(s.handleProxies))
	mux.HandleFunc("GET /api/hosts", auth.RequireAuth(s.handleHosts))
	mux.HandleFunc("GET /api/hosts/{id}/items", auth.RequireAuth(s.handleHostItems))
	mux.HandleFunc("GET /api/hosts/{id}/problems", auth.RequireAuth(s.handleHostProblems))
	mux.HandleFunc("GET /api/items/{id}/history", auth.RequireAuth(s.handleItemHistory))
	// states: acknowledge (any user); pause = Zabbix enable/disable, hide = Argus suppression
	// (both helpdesk/admin)
	mux.HandleFunc("POST /api/events/{id}/ack", auth.RequireAuth(s.handleAckEvent))
	mux.HandleFunc("DELETE /api/events/{id}/ack", auth.RequireAuth(s.handleUnackEvent))
	mux.HandleFunc("POST /api/hosts/{id}/pause", auth.RequireRoles(s.zbxEnableHandler("host", false), "admin", "helpdesk"))
	mux.HandleFunc("DELETE /api/hosts/{id}/pause", auth.RequireRoles(s.zbxEnableHandler("host", true), "admin", "helpdesk"))
	mux.HandleFunc("POST /api/items/{id}/pause", auth.RequireRoles(s.zbxEnableHandler("item", false), "admin", "helpdesk"))
	mux.HandleFunc("DELETE /api/items/{id}/pause", auth.RequireRoles(s.zbxEnableHandler("item", true), "admin", "helpdesk"))
	mux.HandleFunc("POST /api/hosts/{id}/hide", auth.RequireRoles(s.hideHandler("host"), "admin", "helpdesk"))
	mux.HandleFunc("DELETE /api/hosts/{id}/hide", auth.RequireRoles(s.unhideHandler("host"), "admin", "helpdesk"))
	mux.HandleFunc("POST /api/items/{id}/hide", auth.RequireRoles(s.hideHandler("item"), "admin", "helpdesk"))
	mux.HandleFunc("DELETE /api/items/{id}/hide", auth.RequireRoles(s.unhideHandler("item"), "admin", "helpdesk"))

	// self-service MFA (any signed-in user)
	mux.HandleFunc("GET /api/me/mfa", auth.RequireAuth(s.handleMFAStatus))
	mux.HandleFunc("POST /api/me/mfa/setup", auth.RequireAuth(s.handleMFASetup))
	mux.HandleFunc("POST /api/me/mfa/enable", auth.RequireAuth(s.handleMFAEnable))
	mux.HandleFunc("POST /api/me/mfa/disable", auth.RequireAuth(s.handleMFADisable))
	mux.HandleFunc("POST /api/me/mfa/recovery-codes", auth.RequireAuth(s.handleMFARegenRecovery))

	// self-service passkeys (any signed-in user)
	mux.HandleFunc("GET /api/me/passkeys", auth.RequireAuth(s.handleListPasskeys))
	mux.HandleFunc("POST /api/me/passkeys/register/begin", auth.RequireAuth(s.handlePasskeyRegisterBegin))
	mux.HandleFunc("POST /api/me/passkeys/register/finish", auth.RequireAuth(s.handlePasskeyRegisterFinish))
	mux.HandleFunc("DELETE /api/me/passkeys/{id}", auth.RequireAuth(s.handleDeletePasskey))

	// notifications (admin only)
	mux.HandleFunc("GET /api/notify/channels", auth.RequireRole("admin", s.handleListChannels))
	mux.HandleFunc("POST /api/notify/channels", auth.RequireRole("admin", s.handleCreateChannel))
	mux.HandleFunc("PATCH /api/notify/channels/{id}", auth.RequireRole("admin", s.handleUpdateChannel))
	mux.HandleFunc("POST /api/notify/channels/{id}/enabled", auth.RequireRole("admin", s.handleSetChannelEnabled))
	mux.HandleFunc("POST /api/notify/channels/{id}/test", auth.RequireRole("admin", s.handleTestChannel))
	mux.HandleFunc("DELETE /api/notify/channels/{id}", auth.RequireRole("admin", s.handleDeleteChannel))
	mux.HandleFunc("GET /api/notify/sites", auth.RequireRole("admin", s.handleNotifySites))

	// system settings (admin only) - runtime-editable config
	mux.HandleFunc("GET /api/settings", auth.RequireRole("admin", s.handleListSettings))
	mux.HandleFunc("PATCH /api/settings", auth.RequireRole("admin", s.handleUpdateSettings))

	// probe enrollment tokens (admin only)
	mux.HandleFunc("GET /api/probes/tokens", auth.RequireRole("admin", s.handleListEnrollTokens))
	mux.HandleFunc("POST /api/probes/tokens", auth.RequireRole("admin", s.handleCreateEnrollToken))
	mux.HandleFunc("DELETE /api/probes/tokens/{id}", auth.RequireRole("admin", s.handleDeleteEnrollToken))

	// probe fleet target version (admin only) - the version probes should converge on
	mux.HandleFunc("GET /api/probes/target", auth.RequireRole("admin", s.handleGetProbeTarget))
	mux.HandleFunc("PUT /api/probes/target", auth.RequireRole("admin", s.handleSetProbeTarget))
	// trigger a dashboard-driven self-update for one probe (admin; probe must be socket-enabled)
	mux.HandleFunc("POST /api/probes/{name}/update", auth.RequireRole("admin", s.handleTriggerProbeUpdate))
	// issue a check-in credential for an already-enrolled probe (admin) - turns on version reporting
	mux.HandleFunc("POST /api/probes/{name}/checkin-token", auth.RequireRole("admin", s.handleIssueCheckinToken))

	// one-click core self-update via the argus-updater sidecar (admin triggers; anyone signed in can
	// read the state, since the banner is shown in the shell). See coreupdate.go.
	mux.HandleFunc("POST /api/update/start", auth.RequireRole("admin", s.handleUpdateStart))
	mux.HandleFunc("GET /api/update/state", auth.RequireAuth(s.handleUpdateState))
	mux.HandleFunc("POST /api/update/dismiss", auth.RequireRole("admin", s.handleUpdateDismiss))

	// user management (admin only)
	mux.HandleFunc("GET /api/users", auth.RequireRole("admin", s.handleListUsers))
	mux.HandleFunc("POST /api/users", auth.RequireRole("admin", s.handleCreateUser))
	mux.HandleFunc("PATCH /api/users/{id}", auth.RequireRole("admin", s.handleUpdateUser))
	mux.HandleFunc("POST /api/users/{id}/disabled", auth.RequireRole("admin", s.handleSetUserDisabled))
	mux.HandleFunc("DELETE /api/users/{id}", auth.RequireRole("admin", s.handleDeleteUser))
	mux.HandleFunc("POST /api/users/{id}/password", auth.RequireRole("admin", s.handleResetPassword))
	mux.HandleFunc("POST /api/users/{id}/mfa/reset", auth.RequireRole("admin", s.handleAdminResetMFA))
	mux.HandleFunc("POST /api/users/{id}/passkeys/reset", auth.RequireRole("admin", s.handleAdminResetPasskeys))

	mux.Handle("/", spaHandler())

	// Every request passes through session resolution first (idle timeout read live from settings).
	return auth.Middleware(s.st, s.mgr.SessionIdleTimeout, s.mgr.SessionMaxLifetime)(mux)
}

// --- health ---

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

type healthResponse struct {
	Status string       `json:"status"`
	Zabbix zabbixHealth `json:"zabbix"`
}

type zabbixHealth struct {
	Reachable bool   `json:"reachable"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) handleAPIHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	resp := healthResponse{Status: "ok"}
	if ver, err := s.zbx.APIVersion(ctx); err != nil {
		resp.Zabbix = zabbixHealth{Reachable: false, Error: err.Error()}
	} else {
		resp.Zabbix = zabbixHealth{Reachable: true, Version: ver}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleFeatures advertises optional capabilities so the UI can adapt (public).
func (s *Server) handleFeatures(w http.ResponseWriter, r *http.Request) {
	// Self-service password reset needs an email channel to deliver the link.
	resetReady := s.firstEmailChannel(r.Context()) != nil
	writeJSON(w, http.StatusOK, map[string]bool{"passkeys": s.wa != nil, "password_reset": resetReady, "probe_enroll": s.ca != nil})
}

// --- auth ---

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userResponse struct {
	Email      string `json:"email"`
	Name       string `json:"name"`
	Surname    string `json:"surname"`
	Role       string `json:"role"`
	MFAEnabled bool   `json:"mfa_enabled"`
	Landing    string `json:"landing"`
}

func toUserResponse(u *store.User) userResponse {
	return userResponse{Email: u.Email, Name: u.Name, Surname: u.Surname, Role: u.Role, MFAEnabled: u.TOTPEnabled, Landing: normalizeLanding(u.Landing)}
}

// normalizeLanding coerces a stored landing value to a known option (defensive against a blank
// column on a pre-migration row).
func normalizeLanding(v string) string {
	if v == "errors" {
		return "errors"
	}
	return "overview"
}

// clientIP returns the caller's IP, honouring X-Forwarded-For only when TrustProxy is set
// (Argus behind a reverse proxy like HAProxy). Otherwise it uses the direct socket address.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// rateBlocked returns true (and writes a 429 with Retry-After) if any key is currently throttled.
func (s *Server) rateBlocked(w http.ResponseWriter, keys ...string) bool {
	for _, k := range keys {
		if blocked, retry := s.loginLimiter.Blocked(k); blocked {
			secs := int(retry.Seconds())
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error": fmt.Sprintf("Too many attempts. Try again in about %d seconds.", secs)})
			return true
		}
	}
	return false
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	ipKey := "login:ip:" + s.clientIP(r)
	acctKey := "login:acct:" + strings.ToLower(strings.TrimSpace(req.Email))
	if s.rateBlocked(w, ipKey, acctKey) {
		return
	}
	loginFailed := func() { s.loginLimiter.Fail(ipKey); s.loginLimiter.Fail(acctKey) }

	u, err := s.st.UserByEmail(r.Context(), req.Email)
	if err != nil {
		_, _ = auth.VerifyPassword(req.Password, s.dummyHash) // equalize timing
		loginFailed()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	ok, err := auth.VerifyPassword(req.Password, u.PasswordHash)
	if err != nil || !ok {
		loginFailed()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	// Password correct - clear the failure counters for this IP and account.
	s.loginLimiter.Reset(ipKey)
	s.loginLimiter.Reset(acctKey)

	// If MFA is enabled, the password alone doesn't authenticate: issue a short-lived
	// challenge and make the client complete the second factor.
	if u.TOTPEnabled {
		raw, id, err := auth.NewSessionToken()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		if err := s.st.CreateMFAChallenge(r.Context(), id, u.ID, time.Now().Add(mfaChallengeTTL)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mfa_required": true, "mfa_token": raw})
		return
	}

	s.issueSession(w, r, u)
}

// handleLoginTOTP completes a login that requires a second factor.
func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MFAToken string `json:"mfa_token"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	challengeID := auth.HashToken(req.MFAToken)
	uid, err := s.st.MFAChallengeUserID(r.Context(), challengeID)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "this sign-in has expired; please start again"})
		return
	}

	ipKey := "totp:ip:" + s.clientIP(r)
	userKey := "totp:uid:" + strconv.FormatInt(uid, 10)
	if s.rateBlocked(w, ipKey, userKey) {
		return
	}

	u, err := s.st.UserByID(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "this sign-in has expired; please start again"})
		return
	}

	// Accept either a current TOTP code or one of the user's one-time recovery codes.
	valid := mfa.Validate(req.Code, u.TOTPSecret)
	if !valid {
		if consumed, _ := s.st.ConsumeRecoveryCode(r.Context(), u.ID, mfa.HashRecoveryCode(req.Code)); consumed {
			valid = true
		}
	}
	if !valid {
		// Throttle code-guessing; keep the challenge alive so the user can retry in its window.
		s.loginLimiter.Fail(ipKey)
		s.loginLimiter.Fail(userKey)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid code"})
		return
	}

	s.loginLimiter.Reset(ipKey)
	s.loginLimiter.Reset(userKey)
	_ = s.st.DeleteMFAChallenge(r.Context(), challengeID) // one-time use
	s.issueSession(w, r, u)
}

// issueSession creates a server-side session, sets the cookie, and returns the user.
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, u *store.User) {
	if u.Disabled {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this account has been disabled"})
		return
	}
	raw, id, err := auth.NewSessionToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	ttl := s.mgr.SessionMaxLifetime()
	if err := s.st.CreateSession(r.Context(), id, u.ID, time.Now().Add(ttl)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	auth.SetSessionCookie(w, raw, s.cfg.CookieSecure, ttl)
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil && c.Value != "" {
		_ = s.st.DeleteSession(r.Context(), auth.HashToken(c.Value))
	}
	auth.ClearSessionCookie(w, s.cfg.CookieSecure)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context()) // guaranteed present by RequireAuth
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

// handleUpdatePreferences stores per-user UI preferences (currently just the landing view).
func (s *Server) handleUpdatePreferences(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context()) // guaranteed present by RequireAuth
	var req struct {
		Landing string `json:"landing"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.Landing != "overview" && req.Landing != "errors" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "landing must be 'overview' or 'errors'"})
		return
	}
	if err := s.st.UpdateUserLanding(r.Context(), u.ID, req.Landing); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save preference"})
		return
	}
	u.Landing = req.Landing
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

// --- helpers / static ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func spaHandler() http.Handler {
	sub, _ := fs.Sub(web.Dist, "dist")
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.ServeFileFS(w, r, sub, "index.html")
			return
		}
		name := r.URL.Path[1:]
		if _, err := fs.Stat(sub, name); err != nil {
			http.ServeFileFS(w, r, sub, "index.html")
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
