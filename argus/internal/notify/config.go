// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"regexp"
	"strings"
)

// Types are the channel types a global channel can be.
var Types = map[string]bool{
	"discord": true, "telegram": true, "email": true, "teams": true, "slack": true,
	"ntfy": true, "gotify": true, "pushover": true, "webhook": true,
}

// PersonalTypes are the types a personal channel can be: the ones that live on a public service. A
// generic webhook and Gotify are usually on the core's own network, which a personal channel can't
// reach (see publicClient), so they are global only; so is email, which needs the core's SMTP server.
var PersonalTypes = map[string]bool{
	"discord": true, "telegram": true, "teams": true, "slack": true, "ntfy": true, "pushover": true,
}

// SecretKeys are the config keys that are credentials: stored encrypted, never sent back to a browser.
var SecretKeys = map[string]bool{
	"password": true, "bot_token": true, "webhook_url": true, "token": true, "topic": true, "user_key": true, "auth_header": true,
}

var ntfyTopicRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// CheckConfig reports what's wrong with a channel's config, or "" when it can be sent to. personal is
// a personal channel, which must use https. Email is checked by its own form.
func CheckConfig(typ string, cfg map[string]string, personal bool) string {
	get := func(k string) string { return strings.TrimSpace(cfg[k]) }
	switch typ {
	case "discord":
		if get("webhook_url") == "" {
			return "Discord needs a webhook URL"
		}
		if !ValidDiscordWebhook(get("webhook_url")) {
			return DiscordWebhookHint
		}
	case "telegram":
		if get("bot_token") == "" || get("chat_id") == "" {
			return "Telegram needs a bot token and chat ID"
		}
	case "teams":
		if get("webhook_url") == "" {
			return "Teams needs the webhook URL of a Teams Workflow"
		}
		if !ValidTeamsWebhook(get("webhook_url")) {
			return TeamsWebhookHint
		}
	case "slack":
		if get("webhook_url") == "" {
			return "Slack needs an incoming webhook URL"
		}
		if !ValidSlackWebhook(get("webhook_url")) {
			return SlackWebhookHint
		}
	case "ntfy":
		if !ntfyTopicRe.MatchString(get("topic")) {
			return "ntfy needs a topic: up to 64 letters, digits, - or _"
		}
		if s := get("server"); s != "" && !ValidServerURL(s) {
			return "the ntfy server must be an http(s) address, like https://ntfy.sh"
		}
		if s := get("server"); personal && s != "" && !strings.HasPrefix(strings.ToLower(s), "https://") {
			return "a personal ntfy server must use https"
		}
	case "gotify":
		if !ValidServerURL(get("server")) {
			return "Gotify needs the server's http(s) address"
		}
		if get("token") == "" {
			return "Gotify needs an application token"
		}
	case "pushover":
		if get("user_key") == "" || get("token") == "" {
			return "Pushover needs your user key and an application API token"
		}
	case "webhook":
		if !ValidServerURL(get("webhook_url")) {
			return "the webhook needs an http(s) URL"
		}
	}
	return ""
}
