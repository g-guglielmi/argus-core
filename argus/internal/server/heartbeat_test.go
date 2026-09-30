// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// The heartbeat holds while no probe delivered data lately, unless no probe ever connected.
func TestHeartbeatDataFlow(t *testing.T) {
	now := time.Unix(100000, 0)
	fresh := zabbix.Proxy{LastAccess: "99900"}
	stale := zabbix.Proxy{LastAccess: "99000"}
	never := zabbix.Proxy{LastAccess: "0"}
	if why := heartbeatDataFlow([]zabbix.Proxy{stale, fresh, never}, now); why != "" {
		t.Fatalf("one fresh probe is enough, got %q", why)
	}
	if why := heartbeatDataFlow([]zabbix.Proxy{stale, never}, now); !strings.Contains(why, "no probe has delivered data for 16 minutes") {
		t.Fatalf("all stale should hold, got %q", why)
	}
	if why := heartbeatDataFlow([]zabbix.Proxy{never}, now); why != "" {
		t.Fatalf("no probe ever connected: nothing to judge by, got %q", why)
	}
	if why := heartbeatDataFlow(nil, now); why != "" {
		t.Fatalf("no probes: nothing to judge by, got %q", why)
	}
}

// Only when every enabled alert channel failed its last send does the heartbeat hold.
func TestHeartbeatChannels(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if why := heartbeatChannels(ctx, st); why != "" {
		t.Fatalf("no channels: %q", why)
	}
	a, _ := st.CreateNotifyChannel(ctx, store.NotifyChannel{Type: "telegram", Name: "ops", Enabled: true, Alerts: true})
	b, _ := st.CreateNotifyChannel(ctx, store.NotifyChannel{Type: "email", Name: "mail", Enabled: true, Alerts: true})
	notices, _ := st.CreateNotifyChannel(ctx, store.NotifyChannel{Type: "discord", Name: "notices", Enabled: true, Notices: true})
	_ = st.RecordNotifyDelivery(ctx, a, errors.New("401 Unauthorized"))
	_ = st.RecordNotifyDelivery(ctx, notices, errors.New("boom"))
	if why := heartbeatChannels(ctx, st); why != "" {
		t.Fatalf("the mail channel never sent yet: fine, got %q", why)
	}
	_ = st.RecordNotifyDelivery(ctx, b, errors.New("dial tcp: timeout"))
	if why := heartbeatChannels(ctx, st); why != "every alert channel failed its last send" {
		t.Fatalf("both alert channels failing should hold, got %q", why)
	}
	_ = st.RecordNotifyDelivery(ctx, b, nil) // mail delivered again (same second: counts as recovered)
	if why := heartbeatChannels(ctx, st); why != "" {
		t.Fatalf("a delivering channel is enough, got %q", why)
	}
}

// A healthy Argus pings; a stalled alert loop holds the ping; a failing monitor is reported without
// its URL (which carries the check's secret).
func TestHeartbeatBeat(t *testing.T) {
	now := time.Now().Unix()
	zbx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID any `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result := []map[string]string{{"proxyid": "1", "name": "site1", "lastaccess": itoa64(now - 30)}}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
	}))
	defer zbx.Close()
	var pings int
	status := http.StatusOK
	monitor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pings++
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "Argus/") {
			t.Errorf("user agent %q", r.Header.Get("User-Agent"))
		}
		w.WriteHeader(status)
	}))
	defer monitor.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	client := zabbix.New(zbx.URL, "test-token")

	notifierRanAt.Store(0)
	if why := heartbeatHealth(ctx, st, client, time.Now()); why != "the alert loop hasn't read the problem list lately" {
		t.Fatalf("a loop that never ran should hold, got %q", why)
	}
	notifierRanAt.Store(time.Now().Unix())
	if why := heartbeatHealth(ctx, st, client, time.Now()); why != "" {
		t.Fatalf("healthy Argus held: %q", why)
	}
	if code, err := sendHeartbeat(ctx, monitor.URL+"/ping/secret-uuid"); err != nil || code != 200 || pings != 1 {
		t.Fatalf("ping: code %d err %v pings %d", code, err, pings)
	}
	status = http.StatusNotFound
	code, err := sendHeartbeat(ctx, monitor.URL+"/ping/secret-uuid")
	if err == nil || code != 404 || strings.Contains(err.Error(), "secret-uuid") {
		t.Fatalf("a 404 should fail without the URL: code %d err %v", code, err)
	}
	monitor.Close()
	if _, err := sendHeartbeat(ctx, monitor.URL+"/ping/secret-uuid"); err == nil || strings.Contains(err.Error(), "secret-uuid") {
		t.Fatalf("an unreachable monitor should fail without the URL: %v", err)
	}
	if !client.Authenticated() {
		t.Fatal("client should be authenticated")
	}
	unauth := zabbix.New(zbx.URL, "")
	if why := heartbeatHealth(ctx, st, unauth, time.Now()); why != "the Zabbix API token isn't set" {
		t.Fatalf("no token: %q", why)
	}
}
