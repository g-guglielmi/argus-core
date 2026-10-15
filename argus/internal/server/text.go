// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// noteMax is how long a note may be: an acknowledgement's message, a hide's reason, a sensor note.
const noteMax = sensorNoteMax

// oneLine trims s and reports whether it is one printable line of at most max runes. Every note
// that ends up in a notification takes this shape: a line of its own inside an email could pass
// for a MIME boundary or a header, and control characters have no place in a message anyway.
func oneLine(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return s, utf8.RuneCountInString(s) <= max
}

// printable drops control characters (line breaks, tabs, NUL, escapes) from text that a machine
// reported and people will read in a notice, a title or a log line.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// versionShape is a version or image tag as a probe reports it; anything else is kept as "".
var versionShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// reportedVersion keeps a probe-reported version string only when it is shaped like one: it is
// shown in notice titles and the Probes page.
func reportedVersion(s string) string {
	s = strings.TrimSpace(s)
	if !versionShape.MatchString(s) {
		return ""
	}
	return s
}

// noteOrRefuse applies oneLine to a note and, when it fails, writes the 400 that says why.
// Returns the clean note and whether the caller may go on.
func noteOrRefuse(w http.ResponseWriter, raw string) (string, bool) {
	note, ok := oneLine(raw, noteMax)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a note is one line of up to 500 characters"})
		return "", false
	}
	return note, true
}
