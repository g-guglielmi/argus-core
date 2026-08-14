package server

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"argus/internal/auth"
	"argus/internal/notify"
	"argus/internal/store"
)

const passwordResetTTL = time.Hour

// firstEmailChannel returns the first enabled email notification channel, used as the transport
// for transactional mail (password resets). nil when none is configured.
func (s *Server) firstEmailChannel(ctx context.Context) *store.NotifyChannel {
	channels, err := s.st.EnabledNotifyChannels(ctx)
	if err != nil {
		return nil
	}
	for i := range channels {
		if channels[i].Type == "email" {
			return &channels[i]
		}
	}
	return nil
}

// baseURL is the external origin for building links in emails: the configured Public URL, else
// the request's own scheme+host.
func (s *Server) baseURL(r *http.Request) string {
	if p := s.mgr.PublicURL(); p != "" {
		return p
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if s.cfg.TrustProxy {
		if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xf != "" {
			scheme = xf
		}
	}
	if r.Host != "" {
		return scheme + "://" + r.Host
	}
	return ""
}

// handleRequestPasswordReset starts a self-service reset. It always responds the same way
// regardless of whether the email exists (anti-enumeration) and does the work in the background.
func (s *Server) handleRequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	// Keep the email as-typed for the lookup (login matches case-sensitively); normalise only
	// the rate-limit key so case variations can't sidestep the throttle.
	email := strings.TrimSpace(req.Email)

	// Rate-limit by IP and by account so the endpoint can't be used to blast reset mail.
	ipKey := "pwreq:ip:" + s.clientIP(r)
	acctKey := "pwreq:acct:" + strings.ToLower(email)
	ipBlocked, _ := s.loginLimiter.Blocked(ipKey)
	acctBlocked, _ := s.loginLimiter.Blocked(acctKey)
	if !ipBlocked && !acctBlocked && email != "" {
		s.loginLimiter.Fail(ipKey)
		s.loginLimiter.Fail(acctKey)
		go s.sendPasswordReset(email, s.baseURL(r))
	}
	// Generic response either way — never reveal whether the account exists.
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// sendPasswordReset (background) mints a single-use token and emails the reset link. All failure
// paths are silent to the caller — the request already returned a generic response.
func (s *Server) sendPasswordReset(email, baseURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	u, err := s.st.UserByEmail(ctx, email)
	if err != nil || u.Disabled {
		return
	}
	ch := s.firstEmailChannel(ctx)
	if ch == nil || baseURL == "" {
		s.logger.Warn("password reset requested but no email channel / public URL is configured", "email", email)
		return
	}
	raw, id, err := auth.NewSessionToken()
	if err != nil {
		return
	}
	if err := s.st.CreatePasswordReset(ctx, id, u.ID, time.Now().Add(passwordResetTTL)); err != nil {
		s.logger.Warn("password reset: store token", "err", err)
		return
	}
	link := strings.TrimRight(baseURL, "/") + "/?reset=" + url.QueryEscape(raw)
	from := strings.TrimSpace(ch.Config["from"])
	text, htmlBody := resetEmailBodies(link)
	msg := notify.SimpleMessage(from, []string{u.Email}, "Reset your Argus password", text, htmlBody)
	if err := notify.SMTPFromConfig(ch.Config).Send(ctx, []string{u.Email}, msg); err != nil {
		s.logger.Warn("password reset: send email", "err", err)
	}
}

func resetEmailBodies(link string) (text, htmlBody string) {
	text = "Someone requested a password reset for your Argus account.\r\n\r\n" +
		"Reset your password (link valid for 1 hour):\r\n" + link + "\r\n\r\n" +
		"If you didn't request this, you can ignore this email — your password stays unchanged."
	htmlBody = `<div style="font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;max-width:520px;margin:0 auto">` +
		`<h2 style="font-size:17px;color:#111827">Reset your Argus password</h2>` +
		`<p style="font-size:14px;color:#374151">Someone requested a password reset for your account. The link is valid for <strong>1 hour</strong>.</p>` +
		`<p style="margin:20px 0"><a href="` + html.EscapeString(link) + `" style="display:inline-block;padding:10px 18px;border-radius:7px;background:#2ea8c9;color:#fff;text-decoration:none;font-size:14px;font-weight:600">Reset password</a></p>` +
		`<p style="font-size:12px;color:#6b7280">If you didn't request this, you can ignore this email — your password stays unchanged.</p>` +
		`</div>`
	return text, htmlBody
}

// handleConfirmPasswordReset validates a reset token and sets the new password, consuming the
// token and signing the user out of all existing sessions.
func (s *Server) handleConfirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if len(req.NewPassword) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Password must be at least 8 characters."})
		return
	}
	// Light throttle on token submission (tokens are 256-bit, so this is belt-and-braces).
	ipKey := "pwconf:ip:" + s.clientIP(r)
	if s.rateBlocked(w, ipKey) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	uid, err := s.st.PasswordResetUserID(ctx, auth.HashToken(req.Token))
	if err != nil {
		s.loginLimiter.Fail(ipKey)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "This reset link is invalid or has expired. Request a new one."})
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.st.UpdatePassword(ctx, uid, hash); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	// Consume every outstanding token for this user and revoke all sessions.
	_ = s.st.DeleteUserPasswordResets(ctx, uid)
	_ = s.st.DeleteUserSessions(ctx, uid)
	s.loginLimiter.Reset(ipKey)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
