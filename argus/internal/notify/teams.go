// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Teams config keys:
//
//	webhook_url - the URL of a Teams Workflow made from the "Send webhook alerts to a channel" template
//
// The message is an Adaptive Card: a coloured header with the title and "site · host", the details, and
// Open in Argus / Acknowledge buttons. Text is laid out with RichTextBlock runs, which Teams shows as
// typed (no Markdown), so a host name with underscores or a note with brackets reads as written. A
// Workflow message is limited to about 28 KB, so the chart isn't attached.
func sendTeams(ctx context.Context, cfg map[string]string, e Event) error {
	url := strings.TrimSpace(cfg["webhook_url"])
	if url == "" {
		return fmt.Errorf("teams: webhook_url is not set")
	}
	if !ValidTeamsWebhook(url) {
		return fmt.Errorf("teams: %s", TeamsWebhookHint)
	}
	body, err := json.Marshal(teamsMessage(e))
	if err != nil {
		return err
	}
	return hideURL(postJSON(ctx, url, body), url)
}

// teamsStyle is the header container's style, the card's equivalent of the status colour.
func (e Event) teamsStyle() string {
	switch {
	case e.Kind == "ack":
		return "accent"
	case e.Kind == "info":
		return "emphasis"
	case e.Kind == "recovery" || e.State == "ok":
		return "good"
	case e.State == "error":
		return "attention"
	default:
		return "warning"
	}
}

func teamsText(text string, opts map[string]any) map[string]any {
	run := map[string]any{"type": "TextRun", "text": text}
	for k, v := range opts {
		run[k] = v
	}
	return map[string]any{"type": "RichTextBlock", "inlines": []any{run}}
}

func teamsMessage(e Event) map[string]any {
	lines := e.cardLines()
	header := []any{teamsText(e.title(), map[string]any{"weight": "bolder", "size": "medium"})}
	if e.Kind != "info" || e.Host != "" {
		header = append(header, teamsText(lines[0], map[string]any{"isSubtle": true}))
		lines = lines[1:]
	}
	body := []any{map[string]any{"type": "Container", "style": e.teamsStyle(), "bleed": true, "items": header}}
	if e.isAlert() {
		lines = append([]string{"Severity: " + severityLabel(e.Severity)}, lines...)
	}
	for _, l := range lines {
		body = append(body, map[string]any{"type": "RichTextBlock", "spacing": "small", "inlines": []any{
			map[string]any{"type": "TextRun", "text": l},
		}})
	}
	var actions []any
	for _, l := range e.links() {
		actions = append(actions, map[string]any{"type": "Action.OpenUrl", "title": l.label, "url": l.url})
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"msteams": map[string]any{"width": "Full"},
		"body":    body,
	}
	if len(actions) > 0 {
		card["actions"] = actions
	}
	return map[string]any{
		"type": "message",
		"attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"contentUrl":  nil,
			"content":     card,
		}},
	}
}
