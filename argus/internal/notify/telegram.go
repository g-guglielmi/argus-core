package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Telegram config keys:
//   bot_token — from @BotFather
//   chat_id   — target chat/channel/group id (e.g. "-1001234567890")
//   thread_id — optional forum topic id (message_thread_id) for per-site threads
func sendTelegram(ctx context.Context, cfg map[string]string, e Event) error {
	token := strings.TrimSpace(cfg["bot_token"])
	chatID := strings.TrimSpace(cfg["chat_id"])
	if token == "" || chatID == "" {
		return fmt.Errorf("telegram: bot_token and chat_id are required")
	}

	// Build an HTML-formatted message (bold title + detail lines).
	var b strings.Builder
	b.WriteString("<b>" + htmlEscape(e.subject()) + "</b>\n")
	for _, line := range e.bodyLines() {
		b.WriteString(htmlEscape(line) + "\n")
	}

	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     b.String(),
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if tid := strings.TrimSpace(cfg["thread_id"]); tid != "" {
		payload["message_thread_id"] = tid
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := "https://api.telegram.org/bot" + token + "/sendMessage"
	return postJSON(ctx, url, body)
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
