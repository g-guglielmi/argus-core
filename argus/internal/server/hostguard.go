// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"

	"argus/internal/settings"
)

// Allowed FQDNs and IPs (Settings expansion, §D; DESIGN §4 "CSRF / allowed FQDNs and IPs"). The session cookie is
// SameSite=Lax, which already keeps other sites from sending signed-in requests. The allow-list
// closes what Lax leaves open:
//   - DNS rebinding: a hostile name resolving to the Argus IP reaches the API with its own Host
//     header, so API requests for any host outside the list are refused;
//   - same-site origins: a sibling subdomain counts as "same site" for Lax, so a state-changing
//     request whose Origin is outside the list is refused too.
//
// It is off until an admin fills the list in (the user's call: an upgrade must never lock anyone
// out). The Public URL's host and loopback are always allowed, and the probes' machine endpoints
// are never checked - they authenticate with their own tokens and often dial the core by IP, so a
// list typo must not take the fleet offline.

// machineAPIPaths are called by probes, not browsers.
var machineAPIPaths = map[string]bool{
	"/api/enroll":              true,
	"/api/probes/checkin":      true,
	"/api/probes/os-status":    true,
	"/api/probes/break-glass":  true,
	"/api/probes/scan-results": true,
}

// formAPIPaths take a browser form post rather than JSON; they authenticate with a signed link,
// never the session cookie, so a cross-site post gains nothing.
var formAPIPaths = map[string]bool{
	"/api/alert/ack": true,
}

// crossSiteVerdict is the part of the guard that runs whether or not an allowed-hosts list exists:
// a state-changing API request from a browser must come from Argus's own pages, and must be JSON.
// The session cookie is SameSite=Lax, which stops a foreign site from sending it, but not a
// sibling subdomain (same site for Lax) and not a top-level form post to a cookie-less endpoint
// like /api/login: a page elsewhere could sign the victim into an account of its choosing with a
// text/plain form whose body happens to parse as JSON. So: the browser's Sec-Fetch-Site must not
// say cross-site, an Origin must be one of Argus's own (the list, the Public URL, or the host the
// request was addressed to), and a body must be application/json.
func crossSiteVerdict(r *http.Request, list []string, publicURL string, trustProxy bool) (bool, string) {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true, ""
	}
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false, "Cross-origin request refused."
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		oh := ""
		if err == nil {
			oh, _ = settings.NormalizeHost(u.Host)
		}
		if oh == "" || !(hostAllowed(oh, list, publicURL) || oh == requestHost(r, trustProxy)) {
			return false, "Cross-origin request refused."
		}
	}
	if formAPIPaths[r.URL.Path] {
		return true, ""
	}
	if r.ContentLength != 0 {
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if ct != "application/json" {
			return false, "This endpoint takes a JSON body."
		}
	}
	return true, ""
}

// hostAllowed reports whether a normalized host may be used to reach the browser-facing API.
func hostAllowed(host string, list []string, publicURL string) bool {
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	if u, err := url.Parse(publicURL); err == nil && u.Hostname() != "" && strings.EqualFold(u.Hostname(), host) {
		return true
	}
	for _, h := range list {
		if h == host {
			return true
		}
	}
	return false
}

// requestHost is the host the browser addressed: X-Forwarded-Host behind a trusted reverse proxy
// (which may rewrite Host), else the Host header. Normalized; "" when unparseable.
func requestHost(r *http.Request, trustProxy bool) string {
	raw := r.Host
	if trustProxy {
		if xf := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); xf != "" {
			raw = xf
		}
	}
	h, err := settings.NormalizeHost(raw)
	if err != nil {
		return ""
	}
	return h
}

// hostGuardVerdict decides one request. ok=false carries the reason for the 403.
func hostGuardVerdict(r *http.Request, list []string, publicURL string, trustProxy bool) (bool, string) {
	if !strings.HasPrefix(r.URL.Path, "/api/") || machineAPIPaths[r.URL.Path] {
		return true, ""
	}
	if len(list) > 0 {
		host := requestHost(r, trustProxy)
		if !hostAllowed(host, list, publicURL) {
			return false, "Argus doesn't accept requests addressed to \"" + host + "\". An admin can add it under Settings -> Allowed FQDNs and IPs."
		}
	}
	return crossSiteVerdict(r, list, publicURL, trustProxy)
}

// hostGuard applies hostGuardVerdict in front of every route.
func (s *Server) hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, msg := hostGuardVerdict(r, s.mgr.AllowedHosts(), s.mgr.PublicURL(), s.fromTrustedProxy(r)); !ok {
			s.logger.Warn("request refused by the allowed FQDNs and IPs list", "host", r.Host, "origin", r.Header.Get("Origin"), "path", r.URL.Path, "ip", s.clientIP(r))
			writeJSON(w, http.StatusForbidden, map[string]string{"error": msg})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowedHostsLockout returns an error message when saving these settings would lock out the admin
// making the change: the list is (or stays) on, and the address they are using isn't covered by the
// new list plus the (possibly new) Public URL.
func allowedHostsLockout(r *http.Request, values map[string]string, current []string, publicURL string, trustProxy bool) string {
	list := current
	if v, ok := values[settings.KeyAllowedHosts]; ok {
		parsed, err := settings.ParseHostList(v)
		if err != nil {
			return "" // the settings validation reports it
		}
		list = parsed
	}
	if v, ok := values[settings.KeyPublicURL]; ok {
		publicURL = strings.TrimRight(strings.TrimSpace(v), "/")
	}
	if len(list) == 0 {
		return ""
	}
	host := requestHost(r, trustProxy)
	if hostAllowed(host, list, publicURL) {
		return ""
	}
	return "You're using Argus at \"" + host + "\", which this change would lock out. Add it to Allowed FQDNs and IPs first."
}
