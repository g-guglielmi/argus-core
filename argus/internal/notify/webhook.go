// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// webhookClient is the HTTP client behind every outbound webhook. It never follows a redirect: a
// destination that answers with one gets the error, not a second request to wherever it pointed.
var webhookClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// discordHosts are the hosts a Discord webhook lives on.
var discordHosts = map[string]bool{
	"discord.com": true, "discordapp.com": true, "ptb.discord.com": true, "canary.discord.com": true,
}

// ValidDiscordWebhook reports whether raw is an https Discord webhook URL. Nothing else is
// accepted: the core posts alerts to this address on behalf of whoever saved it (viewers can save
// personal channels), so it must not be able to point at something on the core's own network.
func ValidDiscordWebhook(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	return discordHosts[strings.ToLower(u.Hostname())] && strings.HasPrefix(u.Path, "/api/webhooks/")
}

// DiscordWebhookHint is the validation message for a webhook that isn't one.
const DiscordWebhookHint = "the Discord webhook must be an https://discord.com/api/webhooks/... URL"

// secretPatterns match credentials that can end up inside an error message: a transport error
// prints the request URL, which for Telegram carries the bot token and for Discord the webhook's
// own token. Every error notify returns is passed through Redact so the token never reaches the
// log, the channel's health line or a system notice.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`bot[0-9]+:[A-Za-z0-9_-]+`),
	regexp.MustCompile(`(/api/webhooks/[0-9]+/)[A-Za-z0-9_.-]+`),
}

// Redact strips known secret shapes from s.
func Redact(s string) string {
	s = secretPatterns[0].ReplaceAllString(s, "bot<redacted>")
	s = secretPatterns[1].ReplaceAllString(s, "${1}<redacted>")
	return s
}

// redactErr is Redact for an error (nil stays nil).
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	if r := Redact(err.Error()); r != err.Error() {
		return errors.New(r)
	}
	return err
}
