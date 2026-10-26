// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package mailread

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/server"
)

// A mail's sender and subject are decoded, and its text is the first text/plain part (quoted-printable
// or base64), else the HTML part without its tags.
func TestParseMessage(t *testing.T) {
	plain := "From: =?UTF-8?Q?Backup_=C3=A9quipe?= <veeam@example.com>\r\n" +
		"Subject: =?UTF-8?B?W1N1Y2Nlc3NdIEJhY2t1cCBqb2IgTkFT?=\r\n" +
		"Content-Type: multipart/alternative; boundary=b1\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"Job finished: 12 VMs, 0 warnings, dur=\r\nation 01:12\r\n" +
		"--b1\r\nContent-Type: text/html\r\n\r\n<p>ignored</p>\r\n--b1--\r\n"
	from, subject, text := parseMessage([]byte(plain))
	if from != "Backup équipe <veeam@example.com>" || subject != "[Success] Backup job NAS" || text != "Job finished: 12 VMs, 0 warnings, duration 01:12" {
		t.Fatalf("plain: %q %q %q", from, subject, text)
	}
	html := "From: nas@example.com\r\nSubject: Hyper Backup\r\nContent-Type: text/html\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		"PGh0bWw+PGJvZHk+PHN0eWxlPnB7fTwvc3R5bGU+PHA+VGFzayA8Yj5PZmZzaXRlPC9i\r\nPiBmYWlsZWQgJmFtcDsgc3RvcHBlZDwvcD48L2JvZHk+PC9odG1sPg==\r\n"
	_, _, text = parseMessage([]byte(html))
	if text != "Task Offsite failed & stopped" {
		t.Fatalf("html: %q", text)
	}
}

func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// IMAP: the first read takes what arrived in the lookback (not the old mail), later reads take only
// what came after, oldest first; the folder is opened read-only, so nothing gets marked seen.
func TestReadIMAP(t *testing.T) {
	be := memory.New()
	u, _ := be.Login(nil, "username", "password")
	inbox, _ := u.GetMailbox("INBOX")
	box := inbox.(*memory.Mailbox)
	box.Messages[0].Date = time.Now().Add(-30 * 24 * time.Hour) // an old mail, before the lookback
	add := func(subject string, at time.Time) {
		body := "From: veeam@example.com\r\nSubject: " + subject + "\r\nContent-Type: text/plain\r\n\r\nbody of " + subject + "\r\n"
		if err := box.CreateMessage(nil, at, bytes.NewBufferString(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("[Success] Backup job NAS", time.Now().Add(-3*time.Hour))

	cert, pool := selfSigned(t)
	srv := server.New(be)
	srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	l, err := tls.Listen("tcp", "127.0.0.1:0", srv.TLSConfig)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()
	imapRoots = pool
	defer func() { imapRoots = nil }()
	port := l.Addr().(*net.TCPAddr).Port
	c := Config{Kind: "imap", Host: "localhost", Port: port, Security: "tls", Username: "username", Secret: "password", Folder: "INBOX"}
	ctx := context.Background()

	mails, wm, err := Read(ctx, c, "", 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(mails) != 1 || mails[0].Subject != "[Success] Backup job NAS" || mails[0].From != "veeam@example.com" || mails[0].Text != "body of [Success] Backup job NAS" {
		t.Fatalf("first read: %+v (wm %q)", mails, wm)
	}
	again, wm2, err := Read(ctx, c, wm, 48*time.Hour)
	if err != nil || len(again) != 0 || wm2 != wm {
		t.Fatalf("nothing new: %+v %q %v", again, wm2, err)
	}
	add("[Failed] Backup job NAS", time.Now())
	add("[Success] Offsite copy", time.Now())
	more, _, err := Read(ctx, c, wm2, 48*time.Hour)
	if err != nil || len(more) != 2 || more[0].Subject != "[Failed] Backup job NAS" || more[1].Subject != "[Success] Offsite copy" {
		t.Fatalf("later: %+v %v", more, err)
	}
	for _, m := range box.Messages[1:] {
		for _, f := range m.Flags {
			if f == `\Seen` {
				t.Fatalf("a mail was marked seen: %+v", m.Flags)
			}
		}
	}
	if n, newest, err := Probe(ctx, c); err != nil || n != 4 || newest.IsZero() {
		t.Fatalf("probe: %d %v %v", n, newest, err)
	}
	c.Secret = "wrong"
	if _, _, err := Read(ctx, c, wm, time.Hour); err == nil || !strings.Contains(err.Error(), "refused the login") {
		t.Fatalf("a wrong password: %v", err)
	}
}

// Graph: the app signs in once (the token is kept), the folder's mails after the watermark come oldest
// first across pages, and the mails at exactly the watermark's time that were read already are skipped.
func TestReadGraph(t *testing.T) {
	t0 := time.Date(2026, 10, 25, 14, 0, 0, 0, time.UTC)
	msgs := []map[string]any{
		{"id": "a", "subject": "[Success] Job 1", "receivedDateTime": t0.Format(time.RFC3339), "bodyPreview": "done", "from": map[string]any{"emailAddress": map[string]any{"name": "Veeam", "address": "veeam@example.com"}}},
		{"id": "b", "subject": "[Failed] Job 2", "receivedDateTime": t0.Format(time.RFC3339), "bodyPreview": "error", "from": map[string]any{"emailAddress": map[string]any{"address": "veeam@example.com"}}},
		{"id": "c", "subject": "[Success] Job 3", "receivedDateTime": t0.Add(time.Hour).Format(time.RFC3339), "bodyPreview": "", "from": map[string]any{"emailAddress": map[string]any{"address": "nas@example.com"}}},
	}
	tokenCalls := 0
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /tenant-1/oauth2/v2.0/token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		if r.FormValue("client_secret") != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client", "error_description": "AADSTS7000215: Invalid client secret provided.\r\nTrace ID: x"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	})
	mux.HandleFunc("GET /v1.0/users/backups@example.com/mailFolders/inbox/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f := r.URL.Query().Get("$filter")
		since, _ := time.Parse(time.RFC3339, strings.TrimPrefix(f, "receivedDateTime ge "))
		var out []map[string]any
		for _, m := range msgs {
			at, _ := time.Parse(time.RFC3339, m["receivedDateTime"].(string))
			if !at.Before(since) {
				out = append(out, m)
			}
		}
		if r.URL.Query().Get("page") == "" && len(out) > 2 {
			_ = json.NewEncoder(w).Encode(map[string]any{"value": out[:2], "@odata.nextLink": srvURL + "/v1.0/users/backups@example.com/mailFolders/inbox/messages?page=2&$filter=" + strings.ReplaceAll(f, " ", "%20")})
			return
		}
		if r.URL.Query().Get("page") == "2" {
			out = out[2:]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": out})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL
	oldG, oldL := graphBase, loginBase
	graphBase, loginBase = srv.URL+"/v1.0", srv.URL
	defer func() { graphBase, loginBase = oldG, oldL }()

	c := Config{Kind: "graph", Tenant: "tenant-1", ClientID: "app", Secret: "s3cret", Address: "backups@example.com", Folder: "Inbox"}
	wm := t0.Add(-time.Minute).Format(time.RFC3339Nano) + "|"
	mails, wm, err := Read(context.Background(), c, wm, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(mails) != 3 || mails[0].From != "Veeam <veeam@example.com>" || mails[2].Subject != "[Success] Job 3" {
		t.Fatalf("all three, across pages: %+v", mails)
	}
	if wm != t0.Add(time.Hour).Format(time.RFC3339Nano)+"|c" {
		t.Fatalf("watermark: %q", wm)
	}
	again, wm2, err := Read(context.Background(), c, t0.Format(time.RFC3339Nano)+"|a", time.Hour)
	if err != nil || len(again) != 2 || again[0].ID != "b" || wm2 != t0.Add(time.Hour).Format(time.RFC3339Nano)+"|c" {
		t.Fatalf("a was read already: %+v %q %v", again, wm2, err)
	}
	if tokenCalls != 1 {
		t.Fatalf("the token is kept: %d sign-ins", tokenCalls)
	}
	c.Secret = "wrong"
	if _, _, err := Read(context.Background(), c, wm, time.Hour); err == nil || err.Error() != "Microsoft refused the app's sign-in: AADSTS7000215: Invalid client secret provided." {
		t.Fatalf("a wrong secret: %v", err)
	}
}
