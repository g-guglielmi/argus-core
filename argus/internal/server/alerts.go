package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"argus/internal/store"
)

const alertLinkTTL = 14 * 24 * time.Hour

// GetSigningSecret returns the persistent HMAC secret used to sign alert links, generating and
// storing one on first use. Both the server and the notifier resolve the same value from it.
func GetSigningSecret(ctx context.Context, st *store.Store) string {
	if v, ok, _ := st.MetaGet(ctx, "signing_secret"); ok && v != "" {
		return v
	}
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	secret := hex.EncodeToString(buf)
	_ = st.MetaSet(ctx, "signing_secret", secret)
	return secret
}

func signAlert(secret, eventID string, exp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(eventID + "." + strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

func verifyAlert(secret, eventID string, exp int64, sig string) bool {
	if eventID == "" || exp < time.Now().Unix() {
		return false
	}
	return hmac.Equal([]byte(signAlert(secret, eventID, exp)), []byte(sig))
}

// AckLink builds a signed one-click acknowledge URL ("" when no public URL is configured).
func AckLink(publicURL, secret, eventID string) string {
	if publicURL == "" {
		return ""
	}
	exp := time.Now().Add(alertLinkTTL).Unix()
	return publicURL + "/api/alert/ack?" + ackQuery(eventID, exp, signAlert(secret, eventID, exp))
}

func ackQuery(eventID string, exp int64, sig string) string {
	return "e=" + url.QueryEscape(eventID) + "&x=" + strconv.FormatInt(exp, 10) + "&s=" + sig
}

// OpenLink builds a deep link that opens a sensor (or host) in the Argus UI.
func OpenLink(publicURL, hostID, itemID string) string {
	if publicURL == "" || hostID == "" {
		return ""
	}
	u := publicURL + "/?host=" + url.QueryEscape(hostID)
	if itemID != "" {
		u += "&item=" + url.QueryEscape(itemID)
	}
	return u
}

// handleAlertAck serves the signed one-click acknowledge link. GET shows a confirmation page
// (so link previewers / prefetchers can't silently acknowledge); POST performs the ack.
func (s *Server) handleAlertAck(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	eventID := q.Get("e")
	exp, _ := strconv.ParseInt(q.Get("x"), 10, 64)
	sig := q.Get("s")
	if !verifyAlert(s.signingSecret, eventID, exp, sig) {
		alertPage(w, http.StatusForbidden, "Link invalid or expired",
			"This acknowledge link is no longer valid. Open Argus and acknowledge the problem there.")
		return
	}
	if r.Method == http.MethodPost {
		if err := s.ackEvent(r.Context(), eventID, 0, "Acknowledged from alert link", nil); err != nil {
			alertPage(w, http.StatusInternalServerError, "Something went wrong",
				"Argus couldn't record the acknowledgement. Try again from the app.")
			return
		}
		alertPage(w, http.StatusOK, "Acknowledged", "This problem is now acknowledged in Argus.")
		return
	}
	action := "/api/alert/ack?" + ackQuery(eventID, exp, sig)
	body := `<h2 style="margin:0 0 8px">Acknowledge this alert?</h2>` +
		`<p style="color:#6b7280;margin:0 0 20px">This silences further notifications for this problem until it recovers.</p>` +
		`<form method="post" action="` + html.EscapeString(action) + `">` +
		`<button style="padding:11px 22px;font-size:15px;font-weight:600;border:0;border-radius:8px;background:#2ea8c9;color:#fff;cursor:pointer">Acknowledge</button></form>`
	writeAlertHTML(w, http.StatusOK, "Acknowledge alert", body)
}

func alertPage(w http.ResponseWriter, status int, title, msg string) {
	body := `<h2 style="margin:0 0 8px">` + html.EscapeString(title) + `</h2>` +
		`<p style="color:#6b7280;margin:0">` + html.EscapeString(msg) + `</p>`
	writeAlertHTML(w, status, title, body)
}

func writeAlertHTML(w http.ResponseWriter, status int, title, body string) {
	page := `<!doctype html><html><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1"><title>` + html.EscapeString(title) + ` · Argus</title></head>` +
		`<body style="font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;background:#f6f7f9;margin:0">` +
		`<div style="max-width:440px;margin:14vh auto;background:#fff;border:1px solid #e5e7eb;border-radius:12px;padding:28px 26px;text-align:center">` +
		body + `</div></body></html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(page))
}
