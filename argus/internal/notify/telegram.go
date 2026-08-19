package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Telegram config keys:
//   bot_token - from @BotFather
//   chat_id   - target chat/channel/group id (e.g. "-1001234567890")
//   thread_id - optional forum topic id (message_thread_id) for per-site threads
func sendTelegram(ctx context.Context, cfg map[string]string, e Event) error {
	token := strings.TrimSpace(cfg["bot_token"])
	chatID := strings.TrimSpace(cfg["chat_id"])
	if token == "" || chatID == "" {
		return fmt.Errorf("telegram: bot_token and chat_id are required")
	}

	// Bold title (with status emoji) + detail lines, then action links.
	var b strings.Builder
	b.WriteString(e.emoji() + " <b>" + htmlEscape(e.subject()) + "</b>\n")
	for _, line := range e.bodyLines() {
		b.WriteString(htmlEscape(line) + "\n")
	}
	var links []string
	if e.OpenURL != "" {
		links = append(links, `<a href="`+htmlEscape(e.OpenURL)+`">Open in Argus</a>`)
	}
	if e.Kind != "recovery" && e.AckURL != "" {
		links = append(links, `<a href="`+htmlEscape(e.AckURL)+`">Acknowledge</a>`)
	}
	if len(links) > 0 {
		b.WriteString(strings.Join(links, " · "))
	}
	text := b.String()
	thread := strings.TrimSpace(cfg["thread_id"])

	// With a chart, push the PNG via sendPhoto (bytes uploaded directly, so no public URL is
	// needed); the message text becomes the photo caption. Otherwise a plain sendMessage.
	if len(e.ChartPNG) > 0 {
		fields := map[string]string{"chat_id": chatID, "caption": text, "parse_mode": "HTML"}
		if thread != "" {
			fields["message_thread_id"] = thread
		}
		return postMultipart(ctx, "https://api.telegram.org/bot"+token+"/sendPhoto", fields,
			[]filePart{{field: "photo", filename: "chart.png", contentType: "image/png", data: e.ChartPNG}})
	}

	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if thread != "" {
		payload["message_thread_id"] = thread
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return postJSON(ctx, "https://api.telegram.org/bot"+token+"/sendMessage", body)
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
