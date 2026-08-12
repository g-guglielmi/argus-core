// Package notify delivers alert events to external channels (Discord, Telegram, email).
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
	Type    string // "discord" | "telegram" | "email"
	Name    string
	Enabled bool
	Site    string            // host-group name this channel serves; "" = all sites
	Config  map[string]string // type-specific keys (see each dispatcher)
}

// Event is a single alert to deliver.
type Event struct {
	Kind     string    // "problem" | "recovery"
	Severity int       // Zabbix severity 0..5
	State    string    // "warning" | "error" | "ok"
	Host     string    // host display name
	Name     string    // trigger / problem name
	Site     string    // primary site (host group) for context, may be ""
	When     time.Time // when the problem started (problem) or cleared (recovery)
}

// Colors for rich channels, matching the Argus status palette.
const (
	colorError   = 0xE2564D
	colorWarning = 0xE0A53A
	colorOK      = 0x3FA66A
)

func (e Event) color() int {
	switch e.State {
	case "error":
		return colorError
	case "warning":
		return colorWarning
	default:
		return colorOK
	}
}

// tag is the bracketed status prefix, e.g. "[ERROR]" or "[RESOLVED]".
func (e Event) tag() string {
	if e.Kind == "recovery" {
		return "RESOLVED"
	}
	return strings.ToUpper(e.State)
}

// subject is a one-line summary used as the email subject and message title.
func (e Event) subject() string {
	return fmt.Sprintf("[%s] %s — %s", e.tag(), e.Host, e.Name)
}

// bodyLines returns the human-readable detail lines shared across channels.
func (e Event) bodyLines() []string {
	var lines []string
	if e.Kind == "recovery" {
		lines = append(lines, fmt.Sprintf("%s has recovered.", e.Name))
	} else {
		lines = append(lines, e.Name)
	}
	lines = append(lines, "Host: "+e.Host)
	if e.Site != "" {
		lines = append(lines, "Site: "+e.Site)
	}
	when := "problem"
	if e.Kind == "recovery" {
		when = "recovery"
	}
	lines = append(lines, fmt.Sprintf("Time (%s): %s", when, e.When.Format("2006-01-02 15:04:05 MST")))
	return lines
}

// Send delivers one Event through one Channel. Returns an error on delivery failure so the
// caller can log it; it never panics on bad config (missing keys yield a descriptive error).
func Send(ctx context.Context, ch Channel, e Event) error {
	switch ch.Type {
	case "discord":
		return sendDiscord(ctx, ch.Config, e)
	case "telegram":
		return sendTelegram(ctx, ch.Config, e)
	case "email":
		return sendEmail(ctx, ch.Config, e)
	default:
		return fmt.Errorf("unknown channel type %q", ch.Type)
	}
}

// SampleEvent builds a representative Event for the "Test" button.
func SampleEvent(now time.Time) Event {
	return Event{
		Kind: "problem", Severity: 4, State: "error",
		Host: "argus-test-host", Name: "Argus test notification", Site: "", When: now,
	}
}
