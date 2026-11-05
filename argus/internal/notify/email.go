// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTP describes an SMTP transport (host/port/auth/from/tls), independent of message content -
// so both alert emails and transactional mail (e.g. password resets) can share one sender.
//
// Config keys (from an email channel's config map):
//
//	host     - SMTP server hostname
//	port     - SMTP port (default 587)
//	username - SMTP auth user ("" = no auth)
//	password - SMTP auth password
//	from     - envelope + header From address
//	to       - comma-separated recipient list (alerts only; transactional mail passes its own)
//	tls      - "starttls" (default), "tls" (implicit, e.g. port 465), or "none"
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
		// The upgrade is the point of this mode: a server (or someone in between) that doesn't offer
		// it must not get the message in clear. Use tls "none" to send in clear on purpose.
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("email: the server does not offer STARTTLS (set the channel's tls to \"none\" to send in clear on purpose)")
		}
		if err := c.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
			return fmt.Errorf("email: starttls: %w", err)
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

// headerAddrs joins recipients for a To: line. Every caller validates its addresses first (the
// reports parse them with net/mail, a reset goes to the user's stored address, a channel's list is
// an admin's); the CR and LF are stripped here all the same, so no list that reaches a builder can
// end the header early. The subject is Q-encoded for the same reason.
func headerAddrs(to []string) string {
	out := make([]string, 0, len(to))
	for _, a := range to {
		out = append(out, strings.NewReplacer("\r", "", "\n", "").Replace(a))
	}
	return strings.Join(out, ", ")
}

// SimpleMessage builds a minimal multipart/alternative email (plain + HTML) for transactional
// mail such as password resets, with a Q-encoded subject.
func SimpleMessage(from string, to []string, subject, text, html string) []byte {
	var b strings.Builder
	b.WriteString("From: Argus <" + from + ">\r\n")
	b.WriteString("To: " + headerAddrs(to) + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	writeTextAndHTML(&b, text, html)
	return []byte(b.String())
}

// AttachmentMessage builds a multipart/mixed email: the plain + HTML body, then one file attached
// (a report's PDF).
func AttachmentMessage(from string, to []string, subject, text, html, filename, contentType string, data []byte) []byte {
	var b strings.Builder
	mix := newBoundary("argus-mix-")
	b.WriteString("From: Argus <" + from + ">\r\n")
	b.WriteString("To: " + headerAddrs(to) + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=\"" + mix + "\"\r\n\r\n")
	b.WriteString("--" + mix + "\r\n")
	writeTextAndHTML(&b, text, html)
	b.WriteString("\r\n--" + mix + "\r\n")
	name := mime.QEncoding.Encode("UTF-8", filename)
	b.WriteString("Content-Type: " + contentType + "; name=\"" + name + "\"\r\nContent-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-Disposition: attachment; filename=\"" + name + "\"\r\n\r\n")
	b.WriteString(wrap76(base64.StdEncoding.EncodeToString(data)))
	b.WriteString("\r\n--" + mix + "--\r\n")
	return []byte(b.String())
}

// newBoundary is a MIME boundary for one message: random, so no text a user typed into a note can
// be made to end a part early and start one of its own (an HTML part, say). A fixed boundary
// would be in the public source for anyone to copy.
func newBoundary(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("email: crypto/rand failed: " + err.Error())
	}
	return prefix + hex.EncodeToString(b[:])
}

// qp encodes a part's body as quoted-printable: lines stay under the SMTP limit, bytes outside
// ASCII travel safely, and a line of the body can never read as a boundary or a header.
func qp(s string) string {
	var b strings.Builder
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.String()
}

// writeTextAndHTML writes a complete multipart/alternative block (its own Content-Type header, the
// plain and HTML parts, each quoted-printable, and its closing boundary).
func writeTextAndHTML(b *strings.Builder, text, html string) {
	alt := newBoundary("argus-alt-")
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + alt + "\"\r\n\r\n")
	b.WriteString("--" + alt + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	b.WriteString(qp(text))
	b.WriteString("\r\n--" + alt + "\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	b.WriteString(qp(html))
	b.WriteString("\r\n--" + alt + "--\r\n")
}

// buildMessage assembles the email: a multipart/alternative (plain + styled HTML), wrapped in a
// multipart/related with the inline chart image (referenced as cid:chart) when one is present.
func buildMessage(from string, to []string, e Event) []byte {
	var b strings.Builder
	b.WriteString("From: Argus <" + from + ">\r\n")
	b.WriteString("To: " + headerAddrs(to) + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("UTF-8", e.emoji()+" "+e.subject()) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")

	if len(e.ChartPNG) > 0 {
		rel := newBoundary("argus-rel-")
		b.WriteString("Content-Type: multipart/related; type=\"multipart/alternative\"; boundary=\"" + rel + "\"\r\n\r\n")
		b.WriteString("--" + rel + "\r\n")
		writeAlternative(&b, e)
		b.WriteString("\r\n--" + rel + "\r\n")
		b.WriteString("Content-Type: image/png\r\nContent-Transfer-Encoding: base64\r\n")
		b.WriteString("Content-ID: <chart@argus>\r\nContent-Disposition: inline; filename=\"chart.png\"\r\n\r\n")
		b.WriteString(wrap76(base64.StdEncoding.EncodeToString(e.ChartPNG)))
		b.WriteString("\r\n--" + rel + "--\r\n")
		return []byte(b.String())
	}

	writeAlternative(&b, e)
	return []byte(b.String())
}

// writeAlternative writes an alert's plain + HTML block.
func writeAlternative(b *strings.Builder, e Event) {
	text := strings.Join(e.bodyLines(), "\r\n")
	if e.OpenURL != "" {
		text += "\r\nOpen in Argus: " + e.OpenURL
	}
	if e.isAlert() && e.AckURL != "" {
		text += "\r\nAcknowledge: " + e.AckURL
	}
	writeTextAndHTML(b, text, htmlBody(e))
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
	if e.Kind == "ack" {
		return "#3b82f6"
	}
	if e.plainInfo() {
		return "#2ea8c9"
	}
	switch e.State {
	case "error":
		return "#e2564d"
	case "warning":
		return "#e0a53a"
	default:
		return "#3fa66a"
	}
}

// settingsURL derives the Notifications-page link from a sensor deep link: same origin and path, with
// ?view=notifications instead of the sensor query. "" when there is no public URL.
func settingsURL(openURL string) string {
	if openURL == "" {
		return ""
	}
	base := openURL
	if i := strings.IndexAny(base, "?#"); i >= 0 {
		base = base[:i]
	}
	return base + "?view=notifications"
}

// htmlBody renders an alert or notice as an email Card (emailcard.go): the header in the event's
// colour carrying the subject, the lead paragraphs (a recovery's "Back after", a reminder's "Still
// open", a notice's explanation, the acknowledger and their note, the sensor's note, the hosts
// behind, the vendor's word, who to call, the runbook), the details table (severity, host, site,
// reading, time), the inline chart, the buttons, and a footer linking back to the channel settings.
// A problem's header already names the trigger, so the body doesn't repeat it.
func htmlBody(e Event) string {
	c := htmlColor(e)
	var body []string
	switch e.Kind {
	case "recovery":
		if bl := e.backLine(); bl != "" {
			body = append(body, CardText(htmlEscape(bl+".")))
		}
	case "reminder":
		body = append(body, CardText(htmlEscape(e.stillOpen())))
	case "info":
		for _, l := range e.detailLines() {
			body = append(body, CardText(htmlEscape(l)))
		}
	case "ack":
		body = append(body, CardText(htmlEscape(e.ackLine())))
		if e.AckNote != "" {
			body = append(body, CardMuted(`<i>`+htmlEscape(e.AckNote)+`</i>`))
		}
	}
	if nl := e.noteLine(); nl != "" {
		body = append(body, CardCallout(htmlEscape(nl)))
	}
	if bl := e.behindLine(); bl != "" {
		body = append(body, CardText(htmlEscape(bl)))
	}
	if vl := e.vendorLine(); vl != "" {
		body = append(body, CardMuted(htmlEscape(vl)))
	}
	if cls := e.callLines(); len(cls) > 0 {
		parts := make([]string, len(cls))
		for i, cl := range cls {
			parts[i] = htmlEscape(cl)
		}
		body = append(body, CardText(`<b>`+strings.Join(parts, `<br>`)+`</b>`))
	}
	if rls := e.runbookLines(); len(rls) > 0 {
		h := `<b>` + htmlEscape(rls[0]) + `</b>`
		for _, rl := range rls[1:] {
			h += `<br>` + htmlEscape(rl)
		}
		body = append(body, CardCallout(h))
	}

	var rows []cardDetail
	if e.isAlert() {
		rows = append(rows, cardDetail{"Severity", severityLabel(e.Severity), c})
	}
	if e.Host != "" {
		rows = append(rows, cardDetail{"Host", e.Host, ""})
	}
	if e.Site != "" {
		rows = append(rows, cardDetail{"Site", e.Site, ""})
	}
	if v := e.valueLine(); v != "" && e.showsReading() {
		rows = append(rows, cardDetail{"Reading", strings.TrimPrefix(v, "Value: "), ""})
	}
	when := "Problem since"
	switch e.Kind {
	case "recovery":
		when = "Recovered at"
	case "ack":
		when = "Acknowledged at"
	case "info":
		when = "Time"
	}
	rows = append(rows, cardDetail{when, e.When.Format("2006-01-02 15:04:05 MST"), ""})
	body = append(body, cardDetails(rows))
	if len(e.ChartPNG) > 0 {
		body = append(body, cardImage("cid:chart@argus", "2-hour trend"))
	}

	var buttons []Button
	if e.OpenURL != "" {
		buttons = append(buttons, Button{"Open in Argus", e.OpenURL, AccentColor})
	}
	if e.isAlert() && e.AckURL != "" {
		buttons = append(buttons, Button{"Acknowledge", e.AckURL, "#6b7280"})
	}

	foot := "Sent by Argus"
	if u := settingsURL(e.OpenURL); u != "" {
		foot += ` · <a href="` + htmlEscape(u) + `" style="color:#6b7280">Manage channels</a>`
	}
	return Card{Color: c, Title: e.emoji() + " " + e.subject(), Body: body, Buttons: buttons, Foot: foot}.HTML()
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
