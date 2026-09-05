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

	text, keyboard := telegramMessage(e)
	thread := strings.TrimSpace(cfg["thread_id"])
	var markup []byte
	if keyboard != nil {
		markup, _ = json.Marshal(map[string]any{"inline_keyboard": keyboard})
	}

	// With a chart, push the PNG via sendPhoto (bytes uploaded directly, so no public URL is
	// needed); the message text becomes the photo caption. Otherwise a plain sendMessage.
	if len(e.ChartPNG) > 0 {
		fields := map[string]string{"chat_id": chatID, "caption": text, "parse_mode": "HTML"}
		if thread != "" {
			fields["message_thread_id"] = thread
		}
		if markup != nil {
			fields["reply_markup"] = string(markup)
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
	if markup != nil {
		payload["reply_markup"] = json.RawMessage(markup)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return postJSON(ctx, "https://api.telegram.org/bot"+token+"/sendMessage", body)
}

// telegramMessage renders the HTML-mode text and the inline keyboard for an event. The text is a compact
// card - bold title with the severity, "site · host", the reading, the time - and the actions are URL
// buttons under the message (a tap on a phone) rather than links buried in the text. Telegram only
// accepts http(s) URLs in buttons; without one, the message falls back to inline links.
func telegramMessage(e Event) (string, [][]map[string]string) {
	var b strings.Builder
	b.WriteString(e.emoji() + " <b>" + htmlEscape(e.subject()) + "</b>\n")
	b.WriteString(htmlEscape(e.whereLine()) + "\n")
	if e.Kind == "recovery" {
		if e.SinceSecs > 0 {
			b.WriteString("Recovered after " + fmtDur(e.SinceSecs) + "\n")
		}
		b.WriteString("At " + e.When.Format("2006-01-02 15:04 MST") + "\n")
	} else {
		if v := e.valueLine(); v != "" {
			b.WriteString(htmlEscape(v) + "\n")
		}
		b.WriteString("Since " + e.When.Format("2006-01-02 15:04 MST") + "\n")
	}
	var row []map[string]string
	var links []string
	if e.OpenURL != "" {
		if isHTTP(e.OpenURL) {
			row = append(row, map[string]string{"text": "Open in Argus", "url": e.OpenURL})
		} else {
			links = append(links, `<a href="`+htmlEscape(e.OpenURL)+`">Open in Argus</a>`)
		}
	}
	if e.Kind != "recovery" && e.AckURL != "" {
		if isHTTP(e.AckURL) {
			row = append(row, map[string]string{"text": "Acknowledge", "url": e.AckURL})
		} else {
			links = append(links, `<a href="`+htmlEscape(e.AckURL)+`">Acknowledge</a>`)
		}
	}
	if len(links) > 0 {
		b.WriteString(strings.Join(links, " · ") + "\n")
	}
	var kb [][]map[string]string
	if len(row) > 0 {
		kb = [][]map[string]string{row}
	}
	return strings.TrimRight(b.String(), "\n"), kb
}

func isHTTP(u string) bool { return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") }

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
