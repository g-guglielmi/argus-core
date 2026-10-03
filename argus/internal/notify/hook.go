// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Generic webhook config keys:
//
//	webhook_url - where Argus POSTs the event
//	auth_header - the value of an Authorization header to send with it (optional), e.g. "Bearer ..."
//
// The body is the event as JSON (WebhookPayload), for automation tools (n8n, Node-RED, Home Assistant)
// and anything else that takes a JSON POST. Its "text" field is the whole message as plain text, for a
// chat service that reads one.
func sendWebhook(ctx context.Context, cfg map[string]string, e Event) error {
	url := strings.TrimSpace(cfg["webhook_url"])
	if url == "" {
		return fmt.Errorf("webhook: webhook_url is not set")
	}
	if !ValidServerURL(url) {
		return fmt.Errorf("webhook: the URL must be an http(s) address")
	}
	body, err := json.Marshal(webhookPayload(e))
	if err != nil {
		return err
	}
	var headers map[string]string
	if a := strings.TrimSpace(cfg["auth_header"]); a != "" {
		headers = map[string]string{"Authorization": a}
	}
	return hideURL(doPost(ctx, url, "application/json", body, headers), url)
}

// WebhookPayload is the generic webhook's body. Fields that don't apply to the event are left out.
type WebhookPayload struct {
	Source        string `json:"source"`                   // always "argus"
	Kind          string `json:"kind"`                     // problem, reminder, ack, recovery or info
	State         string `json:"state"`                    // error, warning or ok
	Severity      int    `json:"severity,omitempty"`       // the Zabbix severity, 2 (Warning) to 5 (Disaster)
	SeverityLabel string `json:"severity_label,omitempty"` // its name, as Argus shows it
	Title         string `json:"title"`                    // "[HIGH] host - sensor"
	Text          string `json:"text"`                     // the title and the details, as plain text
	Host          string `json:"host,omitempty"`
	Site          string `json:"site,omitempty"`
	Name          string `json:"name"`                       // the problem, or the notice
	Value         string `json:"value,omitempty"`            // the reading, with its units
	Threshold     string `json:"threshold,omitempty"`        // e.g. ">90"
	Time          string `json:"time"`                       // when it started (problem, reminder) or happened (RFC 3339)
	DurationSecs  int64  `json:"duration_seconds,omitempty"` // how long it's been open (reminder) or was (recovery)
	Reminder      int    `json:"reminder,omitempty"`         // which reminder this is
	AckBy         string `json:"ack_by,omitempty"`
	AckNote       string `json:"ack_note,omitempty"`
	Note          string `json:"note,omitempty"`     // the note left on the sensor (alerts, reminders, RESOLVED)
	NoteBy        string `json:"note_by,omitempty"`  // who left it
	Detail        string `json:"detail,omitempty"`   // a notice's explanation
	OpenURL       string `json:"open_url,omitempty"` // the sensor in Argus
	AckURL        string `json:"ack_url,omitempty"`  // the signed acknowledge link (open alerts)
}

func webhookPayload(e Event) WebhookPayload {
	p := WebhookPayload{
		Source: "argus", Kind: e.Kind, State: e.State, Title: e.subject(),
		Text: strings.Join(append([]string{e.title()}, e.cardLines()...), "\n"),
		Host: e.Host, Site: e.Site, Name: e.Name, Time: e.When.Format(time.RFC3339),
		DurationSecs: e.SinceSecs, Reminder: e.Reminder, AckBy: e.AckBy, AckNote: e.AckNote, Detail: e.Detail,
		OpenURL: e.OpenURL,
	}
	if e.noteLine() != "" {
		p.Note, p.NoteBy = e.Note, e.NoteBy
	}
	if e.isAlert() {
		p.Severity, p.SeverityLabel, p.Value, p.Threshold, p.AckURL = e.Severity, severityLabel(e.Severity), e.Value, e.Threshold, e.AckURL
	}
	return p
}
