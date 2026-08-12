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
//   webhook_url — the channel webhook (Server Settings → Integrations → Webhooks)
func sendDiscord(ctx context.Context, cfg map[string]string, e Event) error {
	url := strings.TrimSpace(cfg["webhook_url"])
	if url == "" {
		return fmt.Errorf("discord: webhook_url is not set")
	}

	// Structured fields for the at-a-glance context.
	fields := []map[string]any{{"name": "Host", "value": e.Host, "inline": true}}
	if e.Site != "" {
		fields = append(fields, map[string]any{"name": "Site", "value": e.Site, "inline": true})
	}
	if v := e.valueLine(); v != "" && e.Kind != "recovery" {
		fields = append(fields, map[string]any{"name": "Reading", "value": strings.TrimPrefix(v, "Value: "), "inline": true})
	}

	// Description: recovery duration + action links.
	var desc []string
	if e.Kind == "recovery" && e.SinceSecs > 0 {
		desc = append(desc, fmt.Sprintf("Recovered after %s.", fmtDur(e.SinceSecs)))
	}
	var links []string
	if e.OpenURL != "" {
		links = append(links, "[Open in Argus]("+e.OpenURL+")")
	}
	if e.Kind != "recovery" && e.AckURL != "" {
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

	body, err := json.Marshal(map[string]any{"username": "Argus", "embeds": []any{embed}})
	if err != nil {
		return err
	}
	return postJSON(ctx, url, body)
}

// postJSON POSTs a JSON body and treats any 2xx as success. Shared by the webhook dispatchers.
func postJSON(ctx context.Context, url string, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
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
