package notify

import (
	"strings"
	"testing"
	"time"
)

func sampleProblem() Event {
	return Event{
		Kind: "problem", Severity: 4, State: "error",
		Host: "sw-site2", Name: "Unavailable by ICMP ping", Site: "site2",
		Value: "100 %", Threshold: ">0", When: time.Date(2026, 9, 5, 14, 2, 11, 0, time.UTC),
		OpenURL: "https://monitoring.example.com/?view=monitoring&host=1&item=2",
		AckURL:  "https://monitoring.example.com/api/ack?token=abc",
	}
}

func sampleRecovery() Event {
	e := sampleProblem()
	e.Kind, e.State, e.SinceSecs, e.AckURL = "recovery", "ok", 3*3600+12*60, ""
	return e
}

// FormatReading scales a reading the way the UI does (seconds → ms/µs/ns, bytes, etc.).
func TestFormatReading(t *testing.T) {
	cases := []struct{ val, units, want string }{
		{"0.0138", "s", "13.8 ms"},
		{"0.0000138", "s", "13.8 µs"},
		{"0.0000005", "s", "500 ns"},
		{"1.5", "s", "1.5 s"},
		{"0", "s", "0 s"},
		{"1073741824", "B", "1 GB"},
		{"96", "%", "96 %"},
		{"0", "%", "0 %"},
		{"up", "", "up"},
	}
	for _, c := range cases {
		if got := FormatReading(c.val, c.units); got != c.want {
			t.Errorf("FormatReading(%q,%q)=%q want %q", c.val, c.units, got, c.want)
		}
	}
}

// The subject carries the Zabbix severity (what the UI shows), not the coarse ERROR/WARNING state.
func TestSubjectUsesSeverity(t *testing.T) {
	if got := sampleProblem().subject(); got != "[HIGH] sw-site2 - Unavailable by ICMP ping" {
		t.Fatalf("problem subject = %q", got)
	}
	if got := sampleRecovery().subject(); got != "[RESOLVED] sw-site2 - Unavailable by ICMP ping" {
		t.Fatalf("recovery subject = %q", got)
	}
	e := sampleProblem()
	e.Severity = 2
	if got := e.tag(); got != "WARNING" {
		t.Fatalf("warning tag = %q", got)
	}
}

// Plain-text body names the severity once, for problems only.
func TestBodyLinesSeverity(t *testing.T) {
	lines := strings.Join(sampleProblem().bodyLines(), "\n")
	if strings.Count(lines, "Severity: High") != 1 {
		t.Fatalf("problem body should name the severity once:\n%s", lines)
	}
	rec := strings.Join(sampleRecovery().bodyLines(), "\n")
	if strings.Contains(rec, "Severity:") || !strings.Contains(rec, "has recovered after 3h 12m") {
		t.Fatalf("recovery body:\n%s", rec)
	}
}

// The email: a full document with dark-mode hints, severity row, no duplicated trigger name, and a footer
// that links back to the channel settings derived from the deep link.
func TestHTMLBody(t *testing.T) {
	h := htmlBody(sampleProblem())
	for _, want := range []string{`<meta name="color-scheme" content="light dark">`, "prefers-color-scheme: dark", ">Severity<", ">High<", "Sent by Argus",
		`href="https://monitoring.example.com/?view=notifications"`, ">Acknowledge<", "cid:"} {
		if want == "cid:" {
			continue // no chart on this event
		}
		if !strings.Contains(h, want) {
			t.Errorf("html missing %q", want)
		}
	}
	if n := strings.Count(h, "Unavailable by ICMP ping"); n != 1 {
		t.Errorf("trigger name should appear once (in the header), got %d", n)
	}
	r := htmlBody(sampleRecovery())
	if !strings.Contains(r, "has recovered after 3h 12m") || strings.Contains(r, ">Acknowledge<") || strings.Contains(r, ">Severity<") {
		t.Errorf("recovery html: %s", r)
	}
}

// Telegram: compact text plus URL buttons; a recovery has no Acknowledge; non-http links stay inline.
func TestTelegramMessage(t *testing.T) {
	text, kb := telegramMessage(sampleProblem())
	if !strings.HasPrefix(text, "🔴 <b>[HIGH] sw-site2 - Unavailable by ICMP ping</b>\nsite2 · sw-site2\nValue: 100 % (threshold &gt;0)\nSince 2026-09-05 14:02 UTC") {
		t.Fatalf("text:\n%s", text)
	}
	if strings.Contains(text, "<a href") {
		t.Fatalf("http links should be buttons, not inline: %s", text)
	}
	if len(kb) != 1 || len(kb[0]) != 2 || kb[0][0]["text"] != "Open in Argus" || kb[0][1]["text"] != "Acknowledge" {
		t.Fatalf("keyboard = %v", kb)
	}
	_, kb = telegramMessage(sampleRecovery())
	if len(kb) != 1 || len(kb[0]) != 1 || kb[0][0]["text"] != "Open in Argus" {
		t.Fatalf("recovery keyboard = %v", kb)
	}
	e := sampleProblem()
	e.OpenURL, e.AckURL = "argus://open", ""
	text, kb = telegramMessage(e)
	if kb != nil || !strings.Contains(text, `<a href="argus://open">Open in Argus</a>`) {
		t.Fatalf("non-http URL should fall back to an inline link: %v %s", kb, text)
	}
}

func TestSettingsURL(t *testing.T) {
	if got := settingsURL("https://monitoring.example.com/?view=monitoring&host=1"); got != "https://monitoring.example.com/?view=notifications" {
		t.Fatalf("got %q", got)
	}
	if got := settingsURL(""); got != "" {
		t.Fatalf("empty should stay empty, got %q", got)
	}
}
