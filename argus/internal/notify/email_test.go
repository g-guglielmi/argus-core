// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"strings"
	"testing"
)

// A report's email: the body, then the PDF attached (base64, wrapped), its name Q-encoded when it
// isn't plain ASCII.
func TestAttachmentMessage(t *testing.T) {
	msg := string(AttachmentMessage("argus@example.com", []string{"it@example.com"}, "Città report", "text", "<p>html</p>", "città.pdf", "application/pdf", []byte("%PDF-1.4 fake")))
	for _, want := range []string{
		"Content-Type: multipart/mixed; boundary=\"" + mixBoundary + "\"",
		"Content-Type: multipart/alternative; boundary=\"" + altBoundary + "\"",
		"Content-Type: application/pdf; name=\"=?UTF-8?q?citt=C3=A0.pdf?=\"",
		"Content-Disposition: attachment;",
		"JVBERi0xLjQgZmFrZQ==",
		"--" + mixBoundary + "--",
		"Subject: =?UTF-8?q?Citt=C3=A0_report?=",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
}
