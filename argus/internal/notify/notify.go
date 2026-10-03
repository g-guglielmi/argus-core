// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package notify delivers alert events to external channels (Discord, Telegram, email, Microsoft
// Teams, Slack, ntfy, Gotify, Pushover and a generic JSON webhook).
// It is a leaf package: it knows how to render and send a single Event to a single Channel,
// and holds no state. The polling/state-machine logic lives in the server package.
package notify

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Channel is a delivery target with its type-specific configuration.
type Channel struct {
	ID      int64
	Type    string // one of Types
	Name    string
	Enabled bool
	Config  map[string]string // type-specific keys (see each dispatcher)
	// PublicOnly limits the send to public internet addresses: set for a personal channel, which any
	// signed-in user can point anywhere (see publicClient).
	PublicOnly bool
}

// Event is a single alert to deliver.
type Event struct {
	Kind      string    // "problem" | "reminder" | "ack" | "recovery" | "info" (a system notice)
	Severity  int       // Zabbix severity 0..5
	State     string    // "warning" | "error" | "ok" (an "ack" keeps its problem's state)
	Host      string    // host display name
	Name      string    // trigger / problem name
	Site      string    // primary site (host group) for context, may be ""
	Groups    []string  // every host group of the host, for routing (who an email-to-users channel reaches); not shown
	When      time.Time // when the problem started (problem) or cleared (recovery)
	Value     string    // current reading incl. units, e.g. "96 %" (optional)
	Threshold string    // parsed trigger threshold, e.g. ">90" (optional)
	SinceSecs int64     // how long it has been (reminder) or was (recovery) in problem (optional)
	Reminder  int       // which reminder this is, 1-based (reminders only)
	AckBy     string    // who acknowledged it (ack notices only)
	AckNote   string    // their note, if any (ack notices only)
	Note      string    // the note left on the sensor (alerts, reminders and RESOLVED), if any
	NoteBy    string    // who left it
	Detail    string    // a system notice's explanation, one or more lines (info only)
	OpenURL   string    // deep link to the sensor in Argus (optional)
	AckURL    string    // signed one-click acknowledge link (problem alerts only, optional)
	ChartPNG  []byte    // rendered 2-hour trend graph, uploaded inline (optional)
}

// Colors for rich channels, matching the Argus status palette.
const (
	colorError   = 0xE2564D
	colorWarning = 0xE0A53A
	colorOK      = 0x3FA66A
	colorAck     = 0x3B82F6
	colorInfo    = 0x2EA8C9 // the Argus accent: a notice, not a status
)

// isAlert reports whether the event announces an open problem (the first alert or a reminder): those
// carry the severity, the reading and the Acknowledge action.
func (e Event) isAlert() bool { return e.Kind == "problem" || e.Kind == "reminder" }

func (e Event) color() int {
	if e.Kind == "ack" {
		return colorAck
	}
	if e.Kind == "info" {
		return colorInfo
	}
	switch e.State {
	case "error":
		return colorError
	case "warning":
		return colorWarning
	default:
		return colorOK
	}
}

// emoji is the status indicator prefixed to titles across every channel.
func (e Event) emoji() string {
	if e.Kind == "recovery" {
		return "🟢"
	}
	if e.Kind == "ack" {
		return "🔵"
	}
	if e.Kind == "info" {
		return "ℹ️"
	}
	switch e.State {
	case "error":
		return "🔴"
	case "warning":
		return "🟡"
	default:
		return "🟢"
	}
}

// severityLabel is the Zabbix severity name (0..5) - the same labels the UI shows next to a problem.
func severityLabel(sev int) string {
	switch sev {
	case 1:
		return "Information"
	case 2:
		return "Warning"
	case 3:
		return "Average"
	case 4:
		return "High"
	case 5:
		return "Disaster"
	default:
		return "Not classified"
	}
}

// tag is the bracketed prefix of the subject: the Zabbix severity for a problem ("HIGH", "DISASTER"), with
// REMINDER for a repeat, ACKNOWLEDGED for an acknowledged notice and RESOLVED for a recovery. It used to be the coarse ERROR/WARNING state; the UI has always shown the
// severity, so the messages now say the same thing the screen does.
func (e Event) tag() string {
	switch e.Kind {
	case "recovery":
		return "RESOLVED"
	case "ack":
		return "ACKNOWLEDGED"
	case "info":
		return "INFO"
	case "reminder":
		return strings.ToUpper(severityLabel(e.Severity)) + " REMINDER"
	}
	return strings.ToUpper(severityLabel(e.Severity))
}

// stillOpen is a reminder's lead line: "Still open after 1h 5m (reminder 2)."
func (e Event) stillOpen() string {
	s := "Still open"
	if e.SinceSecs > 0 {
		s += " after " + fmtDur(e.SinceSecs)
	}
	if e.Reminder > 0 {
		s += fmt.Sprintf(" (reminder %d)", e.Reminder)
	}
	return s + "."
}

// ackLine is an acknowledged notice's lead line: "Acknowledged by alice." (the note follows separately).
func (e Event) ackLine() string {
	if e.AckBy == "" {
		return "Acknowledged."
	}
	return "Acknowledged by " + e.AckBy + "."
}

// noteLine is the sensor's note as alerts show it: "Note: ticket open (alice)". "" without one, and
// never on an acknowledged notice (that carries the acknowledger's own note) or a system notice.
func (e Event) noteLine() string {
	if e.Note == "" || e.Kind == "ack" || e.Kind == "info" {
		return ""
	}
	if e.NoteBy != "" {
		return "Note: " + e.Note + " (" + e.NoteBy + ")"
	}
	return "Note: " + e.Note
}

// subject is the one-line summary (no emoji) used as the email subject and message title.
func (e Event) subject() string {
	if e.Host == "" { // a notice about Argus itself has no host
		return fmt.Sprintf("[%s] %s", e.tag(), e.Name)
	}
	return fmt.Sprintf("[%s] %s - %s", e.tag(), e.Host, e.Name)
}

// detailLines splits a notice's explanation into its lines.
func (e Event) detailLines() []string {
	var out []string
	for _, l := range strings.Split(e.Detail, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// whereLine is the compact "site · host" location line for the chat channels.
func (e Event) whereLine() string {
	if e.Site != "" {
		return e.Site + " · " + e.Host
	}
	return e.Host
}

// title is the subject with its status emoji, for chat channels.
func (e Event) title() string { return e.emoji() + " " + e.subject() }

// valueLine renders the reading + threshold context, or "" when there's no value.
func (e Event) valueLine() string {
	if e.Value == "" {
		return ""
	}
	if e.Threshold != "" {
		return fmt.Sprintf("Value: %s (threshold %s)", e.Value, e.Threshold)
	}
	return "Value: " + e.Value
}

// bodyLines returns the human-readable detail lines shared across channels (plain text).
func (e Event) bodyLines() []string {
	var lines []string
	if e.Kind == "info" {
		lines = append(lines, e.Name)
		lines = append(lines, e.detailLines()...)
		if e.Host != "" {
			lines = append(lines, "Host: "+e.Host)
		}
		if e.Site != "" {
			lines = append(lines, "Site: "+e.Site)
		}
		return append(lines, "Time: "+e.When.Format("2006-01-02 15:04:05 MST"))
	}
	switch e.Kind {
	case "recovery":
		if e.SinceSecs > 0 {
			lines = append(lines, fmt.Sprintf("%s has recovered after %s.", e.Name, fmtDur(e.SinceSecs)))
		} else {
			lines = append(lines, e.Name+" has recovered.")
		}
	case "ack":
		lines = append(lines, e.Name, e.ackLine())
		if e.AckNote != "" {
			lines = append(lines, "Note: "+e.AckNote)
		}
	case "reminder":
		lines = append(lines, e.Name, e.stillOpen(), "Severity: "+severityLabel(e.Severity))
	default:
		lines = append(lines, e.Name)
		lines = append(lines, "Severity: "+severityLabel(e.Severity))
	}
	if nl := e.noteLine(); nl != "" {
		lines = append(lines, nl)
	}
	lines = append(lines, "Host: "+e.Host)
	if e.Site != "" {
		lines = append(lines, "Site: "+e.Site)
	}
	if v := e.valueLine(); v != "" && e.isAlert() {
		lines = append(lines, v)
	}
	when := "problem"
	switch e.Kind {
	case "recovery":
		when = "recovery"
	case "ack":
		when = "acknowledged"
	}
	lines = append(lines, fmt.Sprintf("Time (%s): %s", when, e.When.Format("2006-01-02 15:04:05 MST")))
	return lines
}

// cardLines are the compact card's lines under the title, as plain text: "site · host", then what
// happened (still open, recovered after, acknowledged by and the note, a notice's detail), the
// reading, and the time. The channels without a layout of their own (Teams, Slack, ntfy, Gotify,
// Pushover, the webhook's text) share them, the way Telegram's card reads.
func (e Event) cardLines() []string {
	var out []string
	if e.Kind != "info" || e.Host != "" {
		out = append(out, e.whereLine())
	}
	at := "At "
	switch e.Kind {
	case "info":
		out = append(out, e.detailLines()...)
	case "recovery":
		if e.SinceSecs > 0 {
			out = append(out, "Recovered after "+fmtDur(e.SinceSecs))
		}
	case "ack":
		out = append(out, e.ackLine())
		if e.AckNote != "" {
			out = append(out, "Note: "+e.AckNote)
		}
	default:
		if e.Kind == "reminder" {
			out = append(out, e.stillOpen())
		}
		if v := e.valueLine(); v != "" {
			out = append(out, v)
		}
		at = "Since "
	}
	if nl := e.noteLine(); nl != "" {
		out = append(out, nl)
	}
	return append(out, at+e.When.Format("2006-01-02 15:04 MST"))
}

// links are the event's actions with an http(s) address: Open in Argus, and Acknowledge on an alert.
func (e Event) links() []link {
	var out []link
	if isHTTP(e.OpenURL) {
		out = append(out, link{"Open in Argus", e.OpenURL})
	}
	if e.isAlert() && isHTTP(e.AckURL) {
		out = append(out, link{"Acknowledge", e.AckURL})
	}
	return out
}

type link struct{ label, url string }

// Send delivers one Event through one Channel. Returns an error on delivery failure so the
// caller can log it; it never panics on bad config (missing keys yield a descriptive error).
func Send(ctx context.Context, ch Channel, e Event) error {
	if ch.PublicOnly {
		ctx = context.WithValue(ctx, publicKey{}, true)
	}
	switch ch.Type {
	case "discord":
		return redactErr(sendDiscord(ctx, ch.Config, e))
	case "telegram":
		return redactErr(sendTelegram(ctx, ch.Config, e))
	case "email":
		return sendEmail(ctx, ch.Config, e)
	case "teams":
		return redactErr(sendTeams(ctx, ch.Config, e))
	case "slack":
		return redactErr(sendSlack(ctx, ch.Config, e))
	case "ntfy":
		return redactErr(sendNtfy(ctx, ch.Config, e))
	case "gotify":
		return redactErr(sendGotify(ctx, ch.Config, e))
	case "pushover":
		return redactErr(sendPushover(ctx, ch.Config, e))
	case "webhook":
		return redactErr(sendWebhook(ctx, ch.Config, e))
	default:
		return fmt.Errorf("unknown channel type %q", ch.Type)
	}
}

// SampleEvent builds a representative Event for the "Test" button.
func SampleEvent(now time.Time, openURL string) Event {
	return Event{
		Kind: "problem", Severity: 4, State: "error",
		Host: "argus-test-host", Name: "Argus test notification", Site: "",
		Value: "96 %", Threshold: ">90", When: now, OpenURL: openURL,
	}
}

// FormatDuration is fmtDur for callers outside the package (e.g. a "no data for 4m" reading).
func FormatDuration(secs int64) string { return fmtDur(secs) }

// fmtDur renders a duration in seconds as a compact "1d 3h", "4h 12m", or "45s" string.
func fmtDur(secs int64) string {
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	d := secs / 86400
	h := (secs % 86400) / 3600
	m := (secs % 3600) / 60
	switch {
	case d > 0:
		if h > 0 {
			return fmt.Sprintf("%dd %dh", d, h)
		}
		return fmt.Sprintf("%dd", d)
	case h > 0:
		if m > 0 {
			return fmt.Sprintf("%dh %dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dm", m)
	}
}
