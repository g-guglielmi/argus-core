// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Slack config keys:
//
//	webhook_url - an incoming webhook (a Slack app's Incoming Webhooks page)
//
// The message is a classic attachment: the status colour down the side, the title linked to Argus,
// the details, Severity / Host / Site / Reading fields and Open in Argus / Acknowledge buttons. An
// attachment's text isn't read as Markdown unless asked, so what people typed shows as typed. An
// incoming webhook can't upload a file, so the chart isn't attached.
func sendSlack(ctx context.Context, cfg map[string]string, e Event) error {
	url := strings.TrimSpace(cfg["webhook_url"])
	if url == "" {
		return fmt.Errorf("slack: webhook_url is not set")
	}
	if !ValidSlackWebhook(url) {
		return fmt.Errorf("slack: %s", SlackWebhookHint)
	}
	body, err := json.Marshal(slackMessage(e))
	if err != nil {
		return err
	}
	return hideURL(postJSON(ctx, url, body), url)
}

// slackEscape escapes the three characters Slack reads as control sequences (links, mentions).
var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func slackEscape(s string) string { return slackEscaper.Replace(s) }

func slackMessage(e Event) map[string]any {
	var fields []map[string]any
	if e.isAlert() {
		fields = append(fields, map[string]any{"title": "Severity", "value": severityLabel(e.Severity), "short": true})
	}
	if e.Host != "" {
		fields = append(fields, map[string]any{"title": "Host", "value": slackEscape(e.Host), "short": true})
	}
	if e.Site != "" {
		fields = append(fields, map[string]any{"title": "Site", "value": slackEscape(e.Site), "short": true})
	}
	if v := e.valueLine(); v != "" && e.isAlert() {
		fields = append(fields, map[string]any{"title": "Reading", "value": slackEscape(strings.TrimPrefix(v, "Value: ")), "short": true})
	}
	// The card's lines without the ones the fields already carry: where it is and the reading.
	var text []string
	for _, l := range e.cardLines() {
		if l == e.whereLine() || (e.isAlert() && l == e.valueLine()) {
			continue
		}
		text = append(text, slackEscape(l))
	}
	att := map[string]any{
		"fallback":  slackEscape(e.title()),
		"color":     fmt.Sprintf("#%06X", e.color()),
		"title":     slackEscape(e.title()),
		"text":      strings.Join(text, "\n"),
		"fields":    fields,
		"footer":    "Argus",
		"ts":        e.When.Unix(),
		"mrkdwn_in": []string{},
	}
	if isHTTP(e.OpenURL) {
		att["title_link"] = e.OpenURL
	}
	var actions []any
	for _, l := range e.links() {
		actions = append(actions, map[string]any{"type": "button", "text": l.label, "url": l.url})
	}
	if len(actions) > 0 {
		att["actions"] = actions
	}
	return map[string]any{"text": "", "attachments": []any{att}}
}
