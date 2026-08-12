package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Email config keys:
//   host     — SMTP server hostname
//   port     — SMTP port (default 587)
//   username — SMTP auth user ("" = no auth)
//   password — SMTP auth password
//   from     — envelope + header From address
//   to       — comma-separated recipient list
//   tls      — "starttls" (default), "tls" (implicit, e.g. port 465), or "none"
func sendEmail(ctx context.Context, cfg map[string]string, e Event) error {
	host := strings.TrimSpace(cfg["host"])
	from := strings.TrimSpace(cfg["from"])
	to := splitList(cfg["to"])
	if host == "" || from == "" || len(to) == 0 {
		return fmt.Errorf("email: host, from and to are required")
	}
	port := strings.TrimSpace(cfg["port"])
	if port == "" {
		port = "587"
	}
	mode := strings.TrimSpace(cfg["tls"])
	if mode == "" {
		mode = "starttls"
	}
	addr := net.JoinHostPort(host, port)

	msg := buildMessage(from, to, e.subject(), strings.Join(e.bodyLines(), "\r\n"))

	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if mode == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: host})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("email: connect %s: %w", addr, err)
	}
	defer conn.Close()

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("email: smtp client: %w", err)
	}
	defer c.Close()

	if mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				return fmt.Errorf("email: starttls: %w", err)
			}
		}
	}

	if user := strings.TrimSpace(cfg["username"]); user != "" {
		auth := smtp.PlainAuth("", user, cfg["password"], host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("email: auth: %w", err)
		}
	}

	if err := c.Mail(from); err != nil {
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

// buildMessage assembles a minimal RFC 5322 message (plain text).
func buildMessage(from string, to []string, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")
	return []byte(b.String())
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
