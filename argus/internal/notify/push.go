// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Phone push services: ntfy, Gotify and Pushover. Each gets the title, the card's lines and the links,
// with the service's own priority, so an error can sound louder than a warning on the phone.

// ntfy config keys:
//
//	server - the ntfy server (blank = https://ntfy.sh)
//	topic  - the topic the phone subscribes to (on a public server, anyone who knows it can read it)
//	token  - an access token, for a protected topic (optional)
func sendNtfy(ctx context.Context, cfg map[string]string, e Event) error {
	topic := strings.TrimSpace(cfg["topic"])
	if topic == "" {
		return fmt.Errorf("ntfy: topic is not set")
	}
	server := strings.TrimRight(strings.TrimSpace(cfg["server"]), "/")
	if server == "" {
		server = "https://ntfy.sh"
	}
	if !ValidServerURL(server) {
		return fmt.Errorf("ntfy: the server must be an http(s) address")
	}
	body, err := json.Marshal(ntfyMessage(topic, e))
	if err != nil {
		return err
	}
	headers := map[string]string{}
	if t := strings.TrimSpace(cfg["token"]); t != "" {
		headers["Authorization"] = "Bearer " + t
	}
	// ntfy takes a JSON message posted to the server's root, with the topic inside it.
	return hideURL(doPost(ctx, server+"/", "application/json", body, headers), server+"/")
}

func ntfyMessage(topic string, e Event) map[string]any {
	m := map[string]any{
		"topic":    topic,
		"title":    e.subject(),
		"message":  strings.Join(e.cardLines(), "\n"),
		"priority": ntfyPriority(e),
		"tags":     []string{ntfyTag(e)},
	}
	var actions []any
	for _, l := range e.links() {
		actions = append(actions, map[string]any{"action": "view", "label": l.label, "url": l.url})
	}
	if len(actions) > 0 {
		m["actions"] = actions
	}
	if isHTTP(e.OpenURL) {
		m["click"] = e.OpenURL
	}
	return m
}

// ntfyPriority is ntfy's 1..5: a Disaster is urgent, other errors high, a warning normal, the follow-ups low.
func ntfyPriority(e Event) int {
	switch {
	case e.Kind == "ack":
		return 2
	case !e.isAlert():
		return 3
	case e.Severity >= 5:
		return 5
	case e.State == "error":
		return 4
	default:
		return 3
	}
}

// ntfyTag is the emoji ntfy shows before the title (a tag that names an emoji turns into it).
func ntfyTag(e Event) string {
	switch {
	case e.Kind == "recovery":
		return "white_check_mark"
	case e.Kind == "ack":
		return "large_blue_circle"
	case e.Kind == "info":
		return "information_source"
	case e.State == "error":
		return "red_circle"
	default:
		return "yellow_circle"
	}
}

// Gotify config keys:
//
//	server - the Gotify server's address
//	token  - an application token (Gotify -> Apps -> Create application)
func sendGotify(ctx context.Context, cfg map[string]string, e Event) error {
	server := strings.TrimRight(strings.TrimSpace(cfg["server"]), "/")
	token := strings.TrimSpace(cfg["token"])
	if server == "" || token == "" {
		return fmt.Errorf("gotify: server and token are required")
	}
	if !ValidServerURL(server) {
		return fmt.Errorf("gotify: the server must be an http(s) address")
	}
	lines := e.cardLines()
	for _, l := range e.links() {
		lines = append(lines, l.label+": "+l.url)
	}
	msg := map[string]any{
		"title":    e.title(),
		"message":  strings.Join(lines, "\n"),
		"priority": gotifyPriority(e),
		"extras": map[string]any{
			"client::display": map[string]any{"contentType": "text/plain"},
		},
	}
	if isHTTP(e.OpenURL) {
		msg["extras"].(map[string]any)["client::notification"] = map[string]any{"click": map[string]any{"url": e.OpenURL}}
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return hideURL(doPost(ctx, server+"/message", "application/json", body, map[string]string{"X-Gotify-Key": token}), server+"/message")
}

// gotifyPriority is Gotify's 0..10: the Android app sounds from 4 and stands out from 8.
func gotifyPriority(e Event) int {
	switch {
	case e.Kind == "ack":
		return 3
	case e.Kind == "info":
		return 4
	case !e.isAlert():
		return 5
	case e.Severity >= 5:
		return 10
	case e.Severity >= 4:
		return 8
	case e.State == "error":
		return 7
	default:
		return 5
	}
}

// Pushover config keys:
//
//	user_key - the user (or group) key from the Pushover dashboard
//	token    - the API token of an application created in Pushover
//	device   - send to this device only (optional)
//
// Pushover takes the chart as an attachment, like Telegram and Discord.
func sendPushover(ctx context.Context, cfg map[string]string, e Event) error {
	user := strings.TrimSpace(cfg["user_key"])
	token := strings.TrimSpace(cfg["token"])
	if user == "" || token == "" {
		return fmt.Errorf("pushover: user_key and token are required")
	}
	fields := pushoverFields(e)
	fields["token"], fields["user"] = token, user
	if d := strings.TrimSpace(cfg["device"]); d != "" {
		fields["device"] = d
	}
	var files []filePart
	if len(e.ChartPNG) > 0 {
		files = append(files, filePart{field: "attachment", filename: "chart.png", contentType: "image/png", data: e.ChartPNG})
	}
	return postMultipart(ctx, pushoverAPI, fields, files)
}

var pushoverAPI = "https://api.pushover.net/1/messages.json"

// pushoverFields are the message's form fields (without the keys): an HTML message (the subset
// Pushover shows: bold and links), the Open in Argus link as the message's own URL, and the priority.
func pushoverFields(e Event) map[string]string {
	// Cut before escaping, so the cut never splits an entity, and leave room for the link.
	msg := htmlEscape(truncate(strings.Join(e.cardLines(), "\n"), 700))
	if e.isAlert() && isHTTP(e.AckURL) {
		msg += "\n" + `<a href="` + htmlEscape(e.AckURL) + `">Acknowledge</a>`
	}
	f := map[string]string{
		"title":     truncate(e.title(), 250),
		"message":   msg,
		"html":      "1",
		"priority":  strconv.Itoa(pushoverPriority(e)),
		"timestamp": strconv.FormatInt(e.When.Unix(), 10),
	}
	if isHTTP(e.OpenURL) {
		f["url"], f["url_title"] = e.OpenURL, "Open in Argus"
	}
	return f
}

// pushoverPriority is Pushover's -2..2: High and Disaster get 1 (they sound through quiet hours), an
// acknowledgement is quiet. Emergency (2) needs a retry policy, so Argus doesn't use it.
func pushoverPriority(e Event) int {
	switch {
	case e.Kind == "ack":
		return -1
	case e.isAlert() && e.Severity >= 4:
		return 1
	default:
		return 0
	}
}

// truncate cuts s to at most n runes, ending with an ellipsis when it cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
