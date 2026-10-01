// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capture is a test server that records the last request it got.
type capture struct {
	srv    *httptest.Server
	path   string
	header http.Header
	body   []byte
	form   map[string]string
	files  map[string]int
}

func newCapture(t *testing.T) *capture {
	c := &capture{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.path, c.header = r.URL.Path, r.Header.Clone()
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			_ = r.ParseMultipartForm(1 << 20)
			c.form, c.files = map[string]string{}, map[string]int{}
			for k, v := range r.MultipartForm.Value {
				c.form[k] = v[0]
			}
			for k, v := range r.MultipartForm.File {
				c.files[k] = int(v[0].Size)
			}
		} else {
			c.body, _ = io.ReadAll(r.Body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *capture) json(t *testing.T) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(c.body, &m); err != nil {
		t.Fatalf("body isn't JSON: %v\n%s", err, c.body)
	}
	return m
}

func TestWebhookValidators(t *testing.T) {
	for _, u := range []string{
		"https://prod-12.westeurope.logic.azure.com:443/workflows/abc/triggers/manual/paths/invoke?api-version=2016-06-01&sig=x",
		"https://default1234.56.environment.api.powerplatform.com/powerautomate/automations/direct/workflows/abc/triggers/manual/paths/invoke?sig=x",
		"https://example.webhook.office.com/webhookb2/abc",
	} {
		if !ValidTeamsWebhook(u) {
			t.Errorf("Teams webhook refused: %s", u)
		}
	}
	for _, u := range []string{
		"http://prod-12.westeurope.logic.azure.com/workflows/abc", // not https
		"https://logic.azure.com.evil.example/workflows/abc",      // look-alike host
		"https://evil.example/prod.logic.azure.com/",              // the host in the path
		"https://user:pw@prod-1.westeurope.logic.azure.com/x",     // user info
		"https://prod-1.westeurope.logic.azure.com:8443/x",        // another port
	} {
		if ValidTeamsWebhook(u) {
			t.Errorf("Teams webhook accepted: %s", u)
		}
	}
	if !ValidSlackWebhook("https://hooks.slack.com/services/T000/B000/XXXX") || ValidSlackWebhook("https://hooks.slack.com/triggers/T000/1/x") ||
		ValidSlackWebhook("https://hooks.slack.com.evil.example/services/T/B/x") {
		t.Error("Slack webhook validation")
	}
	if !ValidServerURL("http://10.0.0.20:8080") || ValidServerURL("ftp://10.0.0.20") || ValidServerURL("https://a:b@ntfy.example.com") {
		t.Error("server URL validation")
	}
}

func TestCheckConfig(t *testing.T) {
	cases := []struct {
		typ      string
		cfg      map[string]string
		personal bool
		ok       bool
	}{
		{"teams", map[string]string{"webhook_url": "https://prod-1.westeurope.logic.azure.com/workflows/a"}, true, true},
		{"teams", map[string]string{}, false, false},
		{"slack", map[string]string{"webhook_url": "https://hooks.slack.com/services/T/B/x"}, true, true},
		{"ntfy", map[string]string{"topic": "argus-alerts"}, true, true},
		{"ntfy", map[string]string{"topic": "argus alerts"}, false, false},
		{"ntfy", map[string]string{"topic": "a", "server": "http://10.0.0.20"}, false, true},
		{"ntfy", map[string]string{"topic": "a", "server": "http://ntfy.example.com"}, true, false}, // personal: https only
		{"gotify", map[string]string{"server": "http://10.0.0.20", "token": "t"}, false, true},
		{"gotify", map[string]string{"server": "http://10.0.0.20"}, false, false},
		{"pushover", map[string]string{"user_key": "u", "token": "t"}, true, true},
		{"pushover", map[string]string{"user_key": "u"}, true, false},
		{"webhook", map[string]string{"webhook_url": "http://10.0.0.20:5678/webhook/argus"}, false, true},
		{"webhook", map[string]string{"webhook_url": "10.0.0.20"}, false, false},
		{"telegram", map[string]string{"bot_token": "1:a"}, true, false},
		{"discord", map[string]string{"webhook_url": "https://discord.com/api/webhooks/1/x"}, true, true},
	}
	for _, c := range cases {
		if got := CheckConfig(c.typ, c.cfg, c.personal); (got == "") != c.ok {
			t.Errorf("CheckConfig(%s, %v, personal=%v) = %q, want ok=%v", c.typ, c.cfg, c.personal, got, c.ok)
		}
	}
	for typ := range PersonalTypes {
		if !Types[typ] {
			t.Errorf("personal type %s isn't a channel type", typ)
		}
	}
}

func TestIsPublicIP(t *testing.T) {
	for _, s := range []string{"10.0.0.10", "172.16.4.1", "192.168.1.1", "127.0.0.1", "100.101.102.103", "169.254.169.254", "0.0.0.0", "::1", "fd00::1", "fe80::1", "::ffff:10.0.0.1"} {
		if IsPublicIP(net.ParseIP(s)) {
			t.Errorf("%s counted as public", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !IsPublicIP(net.ParseIP(s)) {
			t.Errorf("%s counted as private", s)
		}
	}
}

// A personal channel can't reach the core's own network, even by an address that looks harmless;
// the same webhook from a global channel goes through.
func TestPersonalChannelStaysPublic(t *testing.T) {
	c := newCapture(t)
	ch := Channel{Type: "webhook", Config: map[string]string{"webhook_url": c.srv.URL + "/hook"}, PublicOnly: true}
	err := Send(context.Background(), ch, sampleProblem())
	if err == nil || !errors.Is(err, ErrNotPublic) && !strings.Contains(err.Error(), ErrNotPublic.Error()) {
		t.Fatalf("personal send to %s: err = %v, want ErrNotPublic", c.srv.URL, err)
	}
	if c.path != "" {
		t.Fatal("the request reached the server")
	}
	ch.PublicOnly = false
	if err := Send(context.Background(), ch, sampleProblem()); err != nil || c.path != "/hook" {
		t.Fatalf("global send: err = %v, path %q", err, c.path)
	}
}

func TestGenericWebhook(t *testing.T) {
	c := newCapture(t)
	ch := Channel{Type: "webhook", Config: map[string]string{"webhook_url": c.srv.URL + "/hook", "auth_header": "Bearer s3cret"}}
	if err := Send(context.Background(), ch, sampleProblem()); err != nil {
		t.Fatal(err)
	}
	if c.header.Get("Authorization") != "Bearer s3cret" {
		t.Errorf("Authorization = %q", c.header.Get("Authorization"))
	}
	m := c.json(t)
	for k, want := range map[string]any{"source": "argus", "kind": "problem", "state": "error", "severity": 4.0, "severity_label": "High",
		"title": "[HIGH] sw-site2 - Unavailable by ICMP ping", "host": "sw-site2", "site": "site2", "value": "100 %", "threshold": ">0",
		"time": "2026-09-05T14:02:11Z", "ack_url": "https://monitoring.example.com/api/ack?token=abc"} {
		if m[k] != want {
			t.Errorf("%s = %v, want %v", k, m[k], want)
		}
	}
	if !strings.HasPrefix(m["text"].(string), "🔴 [HIGH] sw-site2 - Unavailable by ICMP ping\nsite2 · sw-site2\nValue: 100 % (threshold >0)") {
		t.Errorf("text = %q", m["text"])
	}
	_ = Send(context.Background(), ch, sampleRecovery())
	m = c.json(t)
	if _, ok := m["ack_url"]; ok || m["duration_seconds"] != float64(3*3600+12*60) || m["severity"] != nil {
		t.Errorf("recovery payload: %v", m)
	}
}

func TestTeamsMessage(t *testing.T) {
	b, _ := json.Marshal(teamsMessage(sampleProblem()))
	s := string(b)
	for _, want := range []string{`"contentType":"application/vnd.microsoft.card.adaptive"`, `"style":"attention"`,
		`"text":"🔴 [HIGH] sw-site2 - Unavailable by ICMP ping"`, `"text":"site2 · sw-site2"`, `"text":"Severity: High"`,
		`"title":"Open in Argus"`, `"title":"Acknowledge"`, `"width":"Full"`} {
		if !strings.Contains(s, want) {
			t.Errorf("card missing %s\n%s", want, s)
		}
	}
	b, _ = json.Marshal(teamsMessage(sampleRecovery()))
	if s = string(b); !strings.Contains(s, `"style":"good"`) || strings.Contains(s, "Acknowledge") || !strings.Contains(s, "Recovered after 3h 12m") {
		t.Errorf("recovery card: %s", s)
	}
	e := sampleAck()
	e.AckNote = "on it_now [see](https://evil.example)"
	b, _ = json.Marshal(teamsMessage(e))
	if s = string(b); !strings.Contains(s, `"style":"accent"`) || !strings.Contains(s, `"text":"Note: on it_now [see](https://evil.example)"`) {
		t.Errorf("ack card: %s", s)
	}
}

func TestSlackMessage(t *testing.T) {
	e := sampleProblem()
	e.Host = "sw<site2>"
	m := slackMessage(e)
	att := m["attachments"].([]any)[0].(map[string]any)
	if att["color"] != "#E2564D" || att["title_link"] != e.OpenURL || !strings.Contains(att["title"].(string), "sw&lt;site2&gt;") {
		t.Errorf("attachment: %v", att)
	}
	if acts := att["actions"].([]any); len(acts) != 2 || acts[1].(map[string]any)["text"] != "Acknowledge" {
		t.Errorf("actions: %v", acts)
	}
	if text := att["text"].(string); strings.Contains(text, "Value:") || !strings.HasPrefix(text, "Since ") {
		t.Errorf("text should leave the reading to the fields: %q", text)
	}
	att = slackMessage(sampleRecovery())["attachments"].([]any)[0].(map[string]any)
	if att["color"] != "#3FA66A" || len(att["actions"].([]any)) != 1 {
		t.Errorf("recovery attachment: %v", att)
	}
}

func TestNtfy(t *testing.T) {
	c := newCapture(t)
	ch := Channel{Type: "ntfy", Config: map[string]string{"server": c.srv.URL + "/", "topic": "argus-alerts", "token": "tk_abc"}}
	if err := Send(context.Background(), ch, sampleProblem()); err != nil {
		t.Fatal(err)
	}
	m := c.json(t)
	if c.path != "/" || c.header.Get("Authorization") != "Bearer tk_abc" || m["topic"] != "argus-alerts" || m["priority"] != 4.0 ||
		m["title"] != "[HIGH] sw-site2 - Unavailable by ICMP ping" || m["click"] != sampleProblem().OpenURL {
		t.Errorf("ntfy request: path %q auth %q body %v", c.path, c.header.Get("Authorization"), m)
	}
	if acts := m["actions"].([]any); len(acts) != 2 {
		t.Errorf("actions: %v", acts)
	}
	if tags := m["tags"].([]any); tags[0] != "red_circle" {
		t.Errorf("tags: %v", tags)
	}
	e := sampleProblem()
	e.Severity = 5
	if ntfyPriority(e) != 5 || ntfyPriority(sampleRecovery()) != 3 || ntfyPriority(sampleAck()) != 2 {
		t.Error("ntfy priorities")
	}
}

func TestGotify(t *testing.T) {
	c := newCapture(t)
	ch := Channel{Type: "gotify", Config: map[string]string{"server": c.srv.URL + "/gotify/", "token": "AbC.123"}}
	if err := Send(context.Background(), ch, sampleProblem()); err != nil {
		t.Fatal(err)
	}
	m := c.json(t)
	if c.path != "/gotify/message" || c.header.Get("X-Gotify-Key") != "AbC.123" || m["priority"] != 8.0 {
		t.Errorf("gotify request: path %q key %q body %v", c.path, c.header.Get("X-Gotify-Key"), m)
	}
	if msg := m["message"].(string); !strings.Contains(msg, "Acknowledge: https://monitoring.example.com/api/ack?token=abc") {
		t.Errorf("message: %q", msg)
	}
}

func TestPushover(t *testing.T) {
	c := newCapture(t)
	old := pushoverAPI
	pushoverAPI = c.srv.URL + "/1/messages.json"
	defer func() { pushoverAPI = old }()
	e := sampleProblem()
	e.Name = "Disk <sda> & more"
	e.ChartPNG = []byte("\x89PNG fake")
	ch := Channel{Type: "pushover", Config: map[string]string{"user_key": "uKey", "token": "aTok", "device": "phone"}}
	if err := Send(context.Background(), ch, e); err != nil {
		t.Fatal(err)
	}
	f := c.form
	if f["token"] != "aTok" || f["user"] != "uKey" || f["device"] != "phone" || f["html"] != "1" || f["priority"] != "1" ||
		f["url"] != e.OpenURL || f["url_title"] != "Open in Argus" || c.files["attachment"] != len(e.ChartPNG) {
		t.Errorf("pushover form: %v files %v", f, c.files)
	}
	if !strings.Contains(f["title"], "Disk <sda> & more") || !strings.Contains(f["message"], `<a href="https://monitoring.example.com/api/ack?token=abc">Acknowledge</a>`) {
		t.Errorf("title %q message %q", f["title"], f["message"])
	}
	if pushoverPriority(sampleAck()) != -1 || pushoverPriority(sampleRecovery()) != 0 {
		t.Error("pushover priorities")
	}
}

func TestRedactNewSecrets(t *testing.T) {
	in := `Post "https://hooks.slack.com/services/T0001/B0002/abcDEF123": dial tcp; Post "https://prod-1.westeurope.logic.azure.com/workflows/x/invoke?api-version=1&sig=SIGVALUE&sp=y"`
	out := Redact(in)
	if strings.Contains(out, "abcDEF123") || strings.Contains(out, "SIGVALUE") || !strings.Contains(out, "/services/T0001/B0002/<redacted>") {
		t.Errorf("Redact = %s", out)
	}
	err := hideURL(errors.New(`Post "http://10.0.0.20:5678/webhook/7f3c-secret": connection refused`), "http://10.0.0.20:5678/webhook/7f3c-secret")
	if strings.Contains(err.Error(), "7f3c") || !strings.Contains(err.Error(), "http://10.0.0.20:5678/...") {
		t.Errorf("hideURL = %v", err)
	}
}
