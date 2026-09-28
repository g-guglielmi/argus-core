// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package zabbix

import "strings"

// Zabbix's technical host name allows only ASCII letters, digits, spaces, dots, dashes and
// underscores (at most 128 characters, no leading or trailing space). The visible name has no such
// limit, so a device called "U6+ Salotto" keeps that as its visible name and gets a technical name
// that Zabbix accepts.

const maxTechnicalName = 128

// accentFold maps common accented letters to their plain ASCII letter, so "Città" becomes "Citta"
// rather than "Citt_".
var accentFold = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
	"è", "e", "é", "e", "ê", "e", "ë", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i",
	"ò", "o", "ó", "o", "ô", "o", "ö", "o", "õ", "o",
	"ù", "u", "ú", "u", "û", "u", "ü", "u",
	"ç", "c", "ñ", "n", "ß", "ss",
	"À", "A", "Á", "A", "Â", "A", "Ä", "A", "Ã", "A", "Å", "A",
	"È", "E", "É", "E", "Ê", "E", "Ë", "E",
	"Ì", "I", "Í", "I", "Î", "I", "Ï", "I",
	"Ò", "O", "Ó", "O", "Ô", "O", "Ö", "O", "Õ", "O",
	"Ù", "U", "Ú", "U", "Û", "U", "Ü", "U",
	"Ç", "C", "Ñ", "N",
)

func technicalNameChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		r == ' ' || r == '.' || r == '-' || r == '_'
}

// ValidTechnicalName reports whether Zabbix accepts s as a technical host name.
func ValidTechnicalName(s string) bool {
	if s == "" || len(s) > maxTechnicalName || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if !technicalNameChar(r) {
			return false
		}
	}
	return true
}

// TechnicalName turns a display name into one Zabbix accepts as a technical host name: accented
// letters lose their accent, any other disallowed character becomes "_" (a run of them one "_"), and
// the result is trimmed and capped at 128 characters. A valid name comes back unchanged.
func TechnicalName(s string) string {
	s = strings.TrimSpace(s)
	if ValidTechnicalName(s) {
		return s
	}
	s = accentFold.Replace(s)
	var b strings.Builder
	lastUnderscore := false
	for _, r := range s {
		if technicalNameChar(r) {
			b.WriteRune(r)
			lastUnderscore = r == '_'
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := b.String()
	if len(out) > maxTechnicalName {
		out = out[:maxTechnicalName]
	}
	out = strings.TrimSpace(out)
	if strings.Trim(out, "_ ") == "" {
		return "host"
	}
	return out
}
