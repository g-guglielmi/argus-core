// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"
)

// parts reads a message back the way a mail client does: the top-level parts, each one's media
// type and decoded body, descending into a nested multipart.
func parts(t *testing.T, raw []byte) []struct{ Type, Body string } {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("not a message: %v", err)
	}
	var out []struct{ Type, Body string }
	var walk func(r io.Reader, ct string)
	walk = func(r io.Reader, ct string) {
		mt, params, err := mime.ParseMediaType(ct)
		if err != nil {
			t.Fatalf("content type %q: %v", ct, err)
		}
		if !strings.HasPrefix(mt, "multipart/") {
			body, _ := io.ReadAll(r)
			out = append(out, struct{ Type, Body string }{mt, string(body)})
			return
		}
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Fatalf("part: %v", err)
			}
			var body io.Reader = p
			if strings.EqualFold(p.Header.Get("Content-Transfer-Encoding"), "quoted-printable") {
				body = quotedprintable.NewReader(p)
			}
			walk(body, p.Header.Get("Content-Type"))
		}
	}
	walk(msg.Body, msg.Header.Get("Content-Type"))
	return out
}

// A report's email: the body, then the PDF attached (base64, wrapped), its name Q-encoded when it
// isn't plain ASCII.
func TestAttachmentMessage(t *testing.T) {
	raw := AttachmentMessage("argus@example.com", []string{"it@example.com"}, "Città report", "text", "<p>html</p>", "città.pdf", "application/pdf", []byte("%PDF-1.4 fake"))
	msg := string(raw)
	for _, want := range []string{
		"Content-Type: multipart/mixed; boundary=\"argus-mix-",
		"Content-Type: multipart/alternative; boundary=\"argus-alt-",
		"Content-Type: application/pdf; name=\"=?UTF-8?q?citt=C3=A0.pdf?=\"",
		"Content-Disposition: attachment;",
		"JVBERi0xLjQgZmFrZQ==",
		"Subject: =?UTF-8?q?Citt=C3=A0_report?=",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	got := parts(t, raw)
	if len(got) != 3 || got[0].Type != "text/plain" || got[0].Body != "text" || got[1].Type != "text/html" || got[1].Body != "<p>html</p>" || got[2].Type != "application/pdf" {
		t.Fatalf("parts: %+v", got)
	}
}

// A note someone typed can't end the plain part and add a part of its own: boundaries are random
// per message and the parts are quoted-printable, so the "boundary" stays text inside the note.
// Here the note carries the boundary of an earlier message, as an attacker who read the source
// or captured a mail would use it.
func TestEmailNoteCannotInjectAPart(t *testing.T) {
	first := string(buildMessage("argus@example.com", []string{"it@example.com"}, Event{Kind: "ack", Name: "Ping", Host: "web1", When: time.Now()}))
	i := strings.Index(first, "boundary=\"") + len("boundary=\"")
	stolen := first[i : i+strings.Index(first[i:], "\"")]
	note := "ok\r\n--" + stolen + "\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\nPGI+cHdu\r\n--" + stolen + "--\r\n"
	raw := buildMessage("argus@example.com", []string{"it@example.com"}, Event{Kind: "ack", Name: "Ping", Host: "web1", AckNote: note, When: time.Now()})
	if strings.Contains(string(raw), "boundary=\""+stolen+"\"") {
		t.Fatal("a second message reused the first one's boundary")
	}
	got := parts(t, raw)
	if len(got) != 2 || got[0].Type != "text/plain" || got[1].Type != "text/html" {
		t.Fatalf("the note added a part: %+v", got)
	}
	if !strings.Contains(got[0].Body, "Note: "+note) {
		t.Fatalf("the note was not carried whole in the plain part:\n%s", got[0].Body)
	}
}

// Every line of a part stays under the SMTP limit however long a value is, and bytes outside
// ASCII survive the trip.
func TestEmailPartsAreQuotedPrintable(t *testing.T) {
	long := strings.Repeat("x", 1200) + " città"
	raw := SimpleMessage("argus@example.com", []string{"it@example.com"}, "s", long, "<p>"+long+"</p>")
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("a line of %d bytes", len(line))
		}
	}
	got := parts(t, raw)
	if len(got) != 2 || got[0].Body != long || got[1].Body != "<p>"+long+"</p>" {
		t.Fatalf("parts did not decode back: %d parts", len(got))
	}
}
