package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTP describes an SMTP transport (host/port/auth/from/tls), independent of message content —
// so both alert emails and transactional mail (e.g. password resets) can share one sender.
//
// Config keys (from an email channel's config map):
//   host     — SMTP server hostname
//   port     — SMTP port (default 587)
//   username — SMTP auth user ("" = no auth)
//   password — SMTP auth password
//   from     — envelope + header From address
//   to       — comma-separated recipient list (alerts only; transactional mail passes its own)
//   tls      — "starttls" (default), "tls" (implicit, e.g. port 465), or "none"
type SMTP struct {
	Host, Port, Username, Password, From, TLS string
}

// SMTPFromConfig extracts transport settings from an email channel's config map.
func SMTPFromConfig(cfg map[string]string) SMTP {
	return SMTP{
		Host:     strings.TrimSpace(cfg["host"]),
		Port:     strings.TrimSpace(cfg["port"]),
		Username: strings.TrimSpace(cfg["username"]),
		Password: cfg["password"],
		From:     strings.TrimSpace(cfg["from"]),
		TLS:      strings.TrimSpace(cfg["tls"]),
	}
}

// Send delivers a pre-built RFC 5322 message to the given recipients over this transport.
func (s SMTP) Send(ctx context.Context, to []string, msg []byte) error {
	if s.Host == "" || s.From == "" || len(to) == 0 {
		return fmt.Errorf("email: host, from and to are required")
	}
	port := s.Port
	if port == "" {
		port = "587"
	}
	mode := s.TLS
	if mode == "" {
		mode = "starttls"
	}
	addr := net.JoinHostPort(s.Host, port)

	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if mode == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: s.Host})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("email: connect %s: %w", addr, err)
	}
	defer conn.Close()

	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("email: smtp client: %w", err)
	}
	defer c.Close()

	if mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
				return fmt.Errorf("email: starttls: %w", err)
			}
		}
	}

	if s.Username != "" {
		auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("email: auth: %w", err)
		}
	}

	if err := c.Mail(s.From); err != nil {
		return fmt.Errorf("email: MAIL FROM: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("email: RCPT %s: %w", rcpt, err)
		}
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("email: DATA: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		return fmt.Errorf("email: write: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("email: close: %w", err)
	}
	return c.Quit()
}

// sendEmail delivers an alert Event to an email channel's recipients.
func sendEmail(ctx context.Context, cfg map[string]string, e Event) error {
	s := SMTPFromConfig(cfg)
	to := splitList(cfg["to"])
	if s.Host == "" || s.From == "" || len(to) == 0 {
		return fmt.Errorf("email: host, from and to are required")
	}
	return s.Send(ctx, to, buildMessage(s.From, to, e))
}

// SimpleMessage builds a minimal multipart/alternative email (plain + HTML) for transactional
// mail such as password resets, with a Q-encoded subject.
func SimpleMessage(from string, to []string, subject, text, html string) []byte {
	var b strings.Builder
	b.WriteString("From: Argus <" + from + ">\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + altBoundary + "\"\r\n\r\n")
	b.WriteString("--" + altBoundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(text)
	b.WriteString("\r\n\r\n--" + altBoundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	b.WriteString(html)
	b.WriteString("\r\n--" + altBoundary + "--\r\n")
	return []byte(b.String())
}

const (
	altBoundary = "argus-alt-boundary-4f2c9a"
	relBoundary = "argus-rel-boundary-7b1e30"
)

// buildMessage assembles the email: a multipart/alternative (plain + styled HTML), wrapped in a
// multipart/related with the inline chart image (referenced as cid:chart) when one is present.
func buildMessage(from string, to []string, e Event) []byte {
	var b strings.Builder
	b.WriteString("From: Argus <" + from + ">\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("UTF-8", e.emoji()+" "+e.subject()) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")

	if len(e.ChartPNG) > 0 {
		b.WriteString("Content-Type: multipart/related; type=\"multipart/alternative\"; boundary=\"" + relBoundary + "\"\r\n\r\n")
		b.WriteString("--" + relBoundary + "\r\n")
		writeAlternative(&b, e)
		b.WriteString("\r\n--" + relBoundary + "\r\n")
		b.WriteString("Content-Type: image/png\r\nContent-Transfer-Encoding: base64\r\n")
		b.WriteString("Content-ID: <chart@argus>\r\nContent-Disposition: inline; filename=\"chart.png\"\r\n\r\n")
		b.WriteString(wrap76(base64.StdEncoding.EncodeToString(e.ChartPNG)))
		b.WriteString("\r\n--" + relBoundary + "--\r\n")
		return []byte(b.String())
	}

	writeAlternative(&b, e)
	return []byte(b.String())
}

// writeAlternative writes a complete multipart/alternative block (its own Content-Type header,
// the plain + HTML parts, and its closing boundary).
func writeAlternative(b *strings.Builder, e Event) {
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + altBoundary + "\"\r\n\r\n")

	b.WriteString("--" + altBoundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(strings.Join(e.bodyLines(), "\r\n"))
	if e.OpenURL != "" {
		b.WriteString("\r\nOpen in Argus: " + e.OpenURL)
	}
	if e.Kind != "recovery" && e.AckURL != "" {
		b.WriteString("\r\nAcknowledge: " + e.AckURL)
	}
	b.WriteString("\r\n\r\n")

	b.WriteString("--" + altBoundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	b.WriteString(htmlBody(e))
	b.WriteString("\r\n--" + altBoundary + "--\r\n")
}

// wrap76 breaks a base64 string into 76-character lines per MIME.
func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76])
		b.WriteString("\r\n")
		s = s[76:]
	}
	b.WriteString(s)
	return b.String()
}

func htmlColor(e Event) string {
	switch e.State {
	case "error":
		return "#e2564d"
	case "warning":
		return "#e0a53a"
	default:
		return "#3fa66a"
	}
}

func htmlBody(e Event) string {
	var rows strings.Builder
	row := func(k, v string) {
		rows.WriteString(`<tr><td style="padding:4px 0;color:#6b7280;width:110px">` + htmlEscape(k) + `</td><td style="padding:4px 0;color:#111827">` + htmlEscape(v) + `</td></tr>`)
	}
	row("Host", e.Host)
	if e.Site != "" {
		row("Site", e.Site)
	}
	if v := e.valueLine(); v != "" && e.Kind != "recovery" {
		row("Reading", strings.TrimPrefix(v, "Value: "))
	}
	if e.Kind == "recovery" && e.SinceSecs > 0 {
		row("Duration", fmtDur(e.SinceSecs))
	}
	when := "Problem since"
	if e.Kind == "recovery" {
		when = "Recovered at"
	}
	row(when, e.When.Format("2006-01-02 15:04:05 MST"))

	var buttons strings.Builder
	btn := func(label, href, bg string) {
		buttons.WriteString(`<a href="` + htmlEscape(href) + `" style="display:inline-block;padding:8px 16px;margin-right:8px;border-radius:7px;background:` + bg + `;color:#fff;text-decoration:none;font-size:13px;font-weight:600">` + label + `</a>`)
	}
	if e.OpenURL != "" {
		btn("Open in Argus", e.OpenURL, "#2ea8c9")
	}
	if e.Kind != "recovery" && e.AckURL != "" {
		btn("Acknowledge", e.AckURL, "#6b7280")
	}

	chart := ""
	if len(e.ChartPNG) > 0 {
		chart = `<img src="cid:chart@argus" alt="2-hour trend" style="width:100%;max-width:524px;margin-top:14px;border:1px solid #e5e7eb;border-radius:8px">`
	}

	c := htmlColor(e)
	return `<div style="font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;max-width:560px;margin:0 auto;border:1px solid #e5e7eb;border-radius:12px;overflow:hidden">` +
		`<div style="background:` + c + `;color:#fff;padding:14px 18px;font-size:16px;font-weight:600">` + e.emoji() + " " + htmlEscape(e.subject()) + `</div>` +
		`<div style="padding:16px 18px">` +
		`<div style="font-size:14px;color:#111827;margin-bottom:12px">` + htmlEscape(e.bodyLines()[0]) + `</div>` +
		`<table style="width:100%;font-size:13px;border-collapse:collapse">` + rows.String() + `</table>` +
		chart +
		`<div style="margin-top:16px">` + buttons.String() + `</div>` +
		`</div></div>`
}

// splitList parses a comma-separated list into trimmed, non-empty values.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
