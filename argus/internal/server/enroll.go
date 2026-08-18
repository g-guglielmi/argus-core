package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"argus/internal/auth"
)

// Signed probe certificates last as long as the manual gen-certs.sh leaves (5 years), capped by
// the CA's own expiry inside pki.SignCSR.
const proxyCertTTL = 5 * 365 * 24 * time.Hour

// slugify reduces a site label to a proxy-name-safe slug: lowercase letters, digits, hyphens.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '_':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// probeCoreHost is the address a probe dials for the Zabbix server (:10051): the configured
// value (Settings / ARGUS_PROBE_CORE_HOST), else the Public URL's hostname.
func (s *Server) probeCoreHost() string {
	if h := s.mgr.ProbeCoreHost(); h != "" {
		return h
	}
	if p := s.mgr.PublicURL(); p != "" {
		if u, err := url.Parse(p); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	return ""
}

// --- probe enrollment (public; token-authenticated) ---

type enrollRequest struct {
	Token string `json:"token"`
	CSR   string `json:"csr"`
}

type enrollResponse struct {
	Certificate string `json:"certificate"` // the signed leaf (PEM)
	CA          string `json:"ca"`          // the CA cert (PEM), the probe's trust anchor
	ProxyName   string `json:"proxy_name"`
	CoreHost    string `json:"core_host"`
	ProbeToken  string `json:"probe_token"` // long-lived credential for version check-ins
	CheckinURL  string `json:"checkin_url"` // where the probe reports its version / reads the target
}

// handleEnroll signs a probe's CSR and registers its active proxy in Zabbix, in exchange for a
// valid single-use enrollment token. The probe's private key never reaches Argus.
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if s.ca == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "probe enrollment is not configured on this server"})
		return
	}
	if s.rateBlocked(w, "enroll:ip:"+s.clientIP(r)) {
		return
	}
	var req enrollRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" || strings.TrimSpace(req.CSR) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token and csr are required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	t, err := s.st.EnrollTokenByHash(ctx, auth.HashToken(token))
	invalid := func() {
		s.loginLimiter.Fail("enroll:ip:" + s.clientIP(r))
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid, used, or expired enrollment token"})
	}
	if err != nil || t.UsedAt != nil || time.Now().After(t.ExpiresAt) {
		invalid()
		return
	}

	certPEM, err := s.ca.SignCSR([]byte(req.CSR), t.ProxyName, proxyCertTTL)
	if err != nil {
		s.logger.Warn("enroll: sign CSR failed", "proxy", t.ProxyName, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not sign CSR: " + err.Error()})
		return
	}
	// Register (or update) the active proxy, pinned to our CA + this proxy's subject.
	issuer := "CN=" + s.ca.SubjectCN()
	subject := "CN=" + t.ProxyName
	if err := s.zbx.EnsureActiveProxyCert(ctx, t.ProxyName, issuer, subject); err != nil {
		s.logger.Warn("enroll: register proxy in Zabbix failed", "proxy", t.ProxyName, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not register the proxy in Zabbix: " + err.Error()})
		return
	}
	_ = s.st.MarkEnrollTokenUsed(ctx, t.ID)

	// Issue a long-lived check-in credential so the probe can report its running version and read
	// the fleet target version (powers the fleet-update view + opt-in self-updater). Best-effort:
	// a failure here shouldn't block a successful enrollment, it just leaves the probe invisible to
	// fleet updates until it re-enrolls.
	probeToken, probeHash, tErr := auth.NewSessionToken()
	if tErr != nil {
		probeToken = ""
	} else if err := s.st.UpsertProbeCredential(ctx, t.ProxyName, probeHash); err != nil {
		s.logger.Warn("enroll: could not store probe check-in credential", "proxy", t.ProxyName, "err", err)
		probeToken = ""
	}
	s.logger.Info("probe enrolled", "proxy", t.ProxyName)

	writeJSON(w, http.StatusOK, enrollResponse{
		Certificate: string(certPEM),
		CA:          string(s.ca.CertPEM()),
		ProxyName:   t.ProxyName,
		CoreHost:    s.probeCoreHost(),
		ProbeToken:  probeToken,
		CheckinURL:  s.baseURL(r) + "/api/probes/checkin",
	})
}

// --- enrollment token management (admin) ---

type enrollTokenView struct {
	ID        int64  `json:"id"`
	ProxyName string `json:"proxy_name"`
	Site      string `json:"site"`
	Status    string `json:"status"` // "pending" | "enrolled" | "expired"
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
}

func (s *Server) handleListEnrollTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.st.ListEnrollTokens(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list tokens"})
		return
	}
	out := make([]enrollTokenView, 0, len(tokens))
	for _, t := range tokens {
		status := "pending"
		if t.UsedAt != nil {
			status = "enrolled"
		} else if time.Now().After(t.ExpiresAt) {
			status = "expired"
		}
		out = append(out, enrollTokenView{
			ID: t.ID, ProxyName: t.ProxyName, Site: t.Site, Status: status,
			CreatedAt: t.CreatedAt.Unix(), ExpiresAt: t.ExpiresAt.Unix(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateEnrollToken mints a one-time token for a new probe and returns everything the UI
// needs to build the deploy command (the raw token is shown exactly once).
func (s *Server) handleCreateEnrollToken(w http.ResponseWriter, r *http.Request) {
	if s.ca == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "probe enrollment is not configured on this server (mount the CA and set ARGUS_CA_CERT_FILE / ARGUS_CA_KEY_FILE)"})
		return
	}
	var req struct {
		Site     string `json:"site"`
		TTLHours int    `json:"ttl_hours"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	site := slugify(req.Site)
	if site == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "site is required (letters, digits and hyphens)"})
		return
	}
	ttl := req.TTLHours
	if ttl <= 0 {
		ttl = 24
	}
	if ttl > 24*30 {
		ttl = 24 * 30 // cap at 30 days
	}
	proxyName := "proxy-" + site

	raw, hash, err := auth.NewSessionToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	u, _ := auth.UserFrom(r.Context())
	var createdBy int64
	if u != nil {
		createdBy = u.ID
	}
	expires := time.Now().Add(time.Duration(ttl) * time.Hour)
	id, err := s.st.CreateEnrollToken(r.Context(), hash, proxyName, site, createdBy, expires)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create token"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         id,
		"token":      raw, // shown once
		"proxy_name": proxyName,
		"site":       site,
		"expires_at": expires.Unix(),
		"enroll_url": s.baseURL(r) + "/api/enroll",
		"core_host":  s.probeCoreHost(),
	})
}

func (s *Server) handleDeleteEnrollToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := s.st.DeleteEnrollToken(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete token"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
