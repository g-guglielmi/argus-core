// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Discord config keys:
//   webhook_url - the channel webhook (Server Settings → Integrations → Webhooks)
func sendDiscord(ctx context.Context, cfg map[string]string, e Event) error {
	url := strings.TrimSpace(cfg["webhook_url"])
	if url == "" {
		return fmt.Errorf("discord: webhook_url is not set")
	}
	if !ValidDiscordWebhook(url) {
		return fmt.Errorf("discord: %s", DiscordWebhookHint)
	}

	// Structured fields for the at-a-glance context. Severity leads (the same label the UI shows).
	var fields []map[string]any
	if e.isAlert() {
		fields = append(fields, map[string]any{"name": "Severity", "value": severityLabel(e.Severity), "inline": true})
	}
	if e.Host != "" {
		fields = append(fields, map[string]any{"name": "Host", "value": mdEscape(e.Host), "inline": true})
	}
	if e.Site != "" {
		fields = append(fields, map[string]any{"name": "Site", "value": mdEscape(e.Site), "inline": true})
	}
	if v := e.valueLine(); v != "" && e.isAlert() {
		fields = append(fields, map[string]any{"name": "Reading", "value": strings.TrimPrefix(v, "Value: "), "inline": true})
	}

	// Description: recovery duration + action links.
	var desc []string
	switch {
	case e.Kind == "recovery" && e.SinceSecs > 0:
		desc = append(desc, fmt.Sprintf("Recovered after %s.", fmtDur(e.SinceSecs)))
	case e.Kind == "reminder":
		desc = append(desc, e.stillOpen())
	case e.Kind == "info":
		desc = append(desc, e.detailLines()...)
	case e.Kind == "ack":
		desc = append(desc, e.ackLine())
		if e.AckNote != "" {
			desc = append(desc, "> "+mdEscape(e.AckNote))
		}
	}
	if nl := e.noteLine(); nl != "" {
		desc = append(desc, "> "+mdEscape(nl))
	}
	if bl := e.behindLine(); bl != "" {
		desc = append(desc, mdEscape(bl))
	}
	for _, cl := range e.callLines() {
		desc = append(desc, "**"+mdEscape(cl)+"**")
	}
	var links []string
	if e.OpenURL != "" {
		links = append(links, "[Open in Argus]("+e.OpenURL+")")
	}
	if e.isAlert() && e.AckURL != "" {
		links = append(links, "[Acknowledge]("+e.AckURL+")")
	}
	if len(links) > 0 {
		desc = append(desc, strings.Join(links, " · "))
	}

	embed := map[string]any{
		"title":     e.title(),
		"color":     e.color(),
		"fields":    fields,
		"timestamp": e.When.UTC().Format(time.RFC3339),
		"footer":    map[string]any{"text": "Argus"},
	}
	if e.OpenURL != "" {
		embed["url"] = e.OpenURL // makes the title clickable
	}
	if len(desc) > 0 {
		embed["description"] = strings.Join(desc, "\n")
	}
	if len(e.ChartPNG) > 0 {
		embed["image"] = map[string]any{"url": "attachment://chart.png"}
	}

	body, err := json.Marshal(map[string]any{"username": "Argus", "embeds": []any{embed}})
	if err != nil {
		return err
	}
	// With a chart, upload the PNG alongside the embed (attachment://); otherwise a plain webhook.
	if len(e.ChartPNG) > 0 {
		return postMultipart(ctx, url, map[string]string{"payload_json": string(body)},
			[]filePart{{field: "files[0]", filename: "chart.png", contentType: "image/png", data: e.ChartPNG}})
	}
	return postJSON(ctx, url, body)
}

// mdEscape neutralises Discord Markdown in text that people typed (an acknowledgement note, a
// host name), so it can't smuggle a masked link or formatting into the channel.
var mdEscaper = strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "~", "\\~", "`", "\\`", "|", "\\|", "[", "\\[", "]", "\\]", ">", "\\>", "#", "\\#", "-", "\\-")

func mdEscape(s string) string { return mdEscaper.Replace(s) }

// postJSON POSTs a JSON body and treats any 2xx as success. Shared by the webhook dispatchers.
func postJSON(ctx context.Context, url string, body []byte) error {
	return doPost(ctx, url, "application/json", body, nil)
}

// doPost POSTs body with the given content type and extra headers, treating any 2xx as success; a
// failure carries the start of the answer, which is usually the service's own reason.
func doPost(ctx context.Context, url, contentType string, body []byte, headers map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "Argus")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := clientFor(ctx).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}
