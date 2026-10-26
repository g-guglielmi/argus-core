// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package mailread reads a mailbox for the push sensors that report by email: the mails that arrived
// after a watermark, oldest first, with their sender, subject, the start of their text and when they
// arrived. It only reads: an IMAP folder is opened read-only (EXAMINE, so nothing is marked seen) and
// Graph needs only Mail.Read. Nothing is marked, moved or deleted.
package mailread

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Mail is one mail as a push sensor's rule reads it.
type Mail struct {
	ID      string    // the server's id for it (an IMAP UID, a Graph message id)
	From    string    // "Name <address>"
	Subject string    // decoded
	Text    string    // the start of its text (a few KB), as plain text
	At      time.Time // when it arrived
}

// Config is where a mailbox is and how to sign in (store.Mailbox without its state).
type Config struct {
	Kind     string // imap | graph
	Host     string
	Port     int
	Security string // tls | starttls
	Username string
	Secret   string
	Tenant   string
	ClientID string
	Address  string
	Folder   string
}

const (
	// MaxPerRead is how many mails one read takes; the rest wait for the next (oldest first).
	MaxPerRead = 200
	textMax    = 4096
	headMax    = 64 << 10 // what an IMAP read fetches of each mail
)

// Read returns the mails that arrived after watermark (oldest first, at most MaxPerRead) and the
// watermark to give the next read. With no watermark (a new mailbox, or one whose place changed), it
// reads what arrived in the last lookback.
func Read(ctx context.Context, c Config, watermark string, lookback time.Duration) ([]Mail, string, error) {
	switch c.Kind {
	case "imap":
		return readIMAP(ctx, c, watermark, lookback)
	case "graph":
		return readGraph(ctx, c, watermark, lookback)
	}
	return nil, watermark, errors.New("unknown mailbox kind " + c.Kind)
}

// Probe signs in and says how many mails the folder holds and when the newest arrived (zero when it is
// empty), for a mailbox's Test button.
func Probe(ctx context.Context, c Config) (int, time.Time, error) {
	switch c.Kind {
	case "imap":
		return probeIMAP(ctx, c)
	case "graph":
		return probeGraph(ctx, c)
	}
	return 0, time.Time{}, errors.New("unknown mailbox kind " + c.Kind)
}

var wordDecoder = &mime.WordDecoder{}

// decodeHeader decodes a header's encoded words ("=?UTF-8?Q?...?="), keeping the raw text when a
// charset is unknown.
func decodeHeader(v string) string {
	if d, err := wordDecoder.DecodeHeader(v); err == nil {
		v = d
	}
	return strings.Join(strings.Fields(v), " ")
}

// parseMessage reads a raw mail (possibly cut short) into its sender, subject and the start of its
// text: the first text/plain part, else the first text/html part without its tags.
func parseMessage(raw []byte) (from, subject, text string) {
	m, err := mail.ReadMessage(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return "", "", ""
	}
	from = decodeHeader(m.Header.Get("From"))
	subject = decodeHeader(m.Header.Get("Subject"))
	text = bodyText(m.Header.Get("Content-Type"), m.Header.Get("Content-Transfer-Encoding"), m.Body, 0)
	return from, subject, clip(text)
}

func bodyText(ctype, cte string, body io.Reader, depth int) string {
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil || ctype == "" {
		mt = "text/plain"
	}
	switch {
	case strings.HasPrefix(mt, "multipart/") && depth < 4:
		mr := multipart.NewReader(body, params["boundary"])
		var html string
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			ct := p.Header.Get("Content-Type")
			pt, _, _ := mime.ParseMediaType(ct)
			if ct == "" {
				pt = "text/plain"
			}
			t := bodyText(ct, p.Header.Get("Content-Transfer-Encoding"), p, depth+1)
			if t != "" && (pt == "text/plain" || strings.HasPrefix(pt, "multipart/")) {
				return t
			}
			if html == "" && pt == "text/html" {
				html = t
			}
		}
		return html
	case mt == "text/plain" || mt == "text/html":
		b, _ := io.ReadAll(io.LimitReader(decodeCTE(cte, body), headMax))
		t := string(b)
		if mt == "text/html" {
			t = stripHTML(t)
		}
		return strings.Join(strings.Fields(t), " ")
	}
	return ""
}

func decodeCTE(cte string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, r) // it skips the line breaks itself
	}
	return r
}

var (
	reTags    = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>|<[^>]+>`)
	reEntites = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'")
)

func stripHTML(s string) string { return reEntites.Replace(reTags.ReplaceAllString(s, " ")) }

func clip(s string) string {
	if len(s) <= textMax {
		return s
	}
	s = s[:textMax]
	for i := 0; i < 3 && len(s) > 0 && !utf8.ValidString(s); i++ { // a character cut in two
		s = s[:len(s)-1]
	}
	return strings.ToValidUTF8(s, "")
}
