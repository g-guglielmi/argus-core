// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus/internal/notify"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// The per-host notice names what looks renamed and what is just gone, the renamed first, and says
// when Zabbix deletes the old sensors.
func TestGoneNoticeWords(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	item := func(id, name string) zabbix.Item {
		return zabbix.Item{ItemID: id, Name: name, Discovery: zabbix.ItemDiscovery{Status: "1", TSDelete: itoa64(now.Add(6 * 24 * time.Hour).Unix())}}
	}
	ev := goneNoticeEvent("media1", "site1", nil, []goneLost{{it: item("1", "Cloudflared"), renamedTo: "Cloudflared agent"}}, "", now)
	if ev.Name != "Cloudflared looks renamed to Cloudflared agent" || ev.Detail != "Review on the host page to keep the history: Zabbix deletes the old sensors on Nov 17." || ev.Kind != "info" {
		t.Fatalf("one rename: %q / %q", ev.Name, ev.Detail)
	}
	ev = goneNoticeEvent("media1", "", nil, []goneLost{
		{it: item("3", "Old disk")},
		{it: item("2", "RustDesk"), renamedTo: "RustDesk Service"},
		{it: item("1", "Cloudflared"), renamedTo: "Cloudflared agent"},
	}, "", now)
	want := "Cloudflared, now Cloudflared agent\nRustDesk, now RustDesk Service\nGone: Old disk\nReview on the host page to keep the history: Zabbix deletes the old sensors on Nov 17."
	if ev.Name != "2 sensors look renamed, 1 gone from the device" || ev.Detail != want {
		t.Fatalf("mixed: %q / %q", ev.Name, ev.Detail)
	}
	ev = goneNoticeEvent("media1", "", nil, []goneLost{{it: item("3", "Old disk")}}, "", now)
	if ev.Name != "Gone from the device: Old disk" || ev.Detail != "Zabbix no longer finds it and deletes it with its history on Nov 17." {
		t.Fatalf("one gone: %q / %q", ev.Name, ev.Detail)
	}
	closed := goneClosedEvent("Service not running: Cloudflared", "media1", "", nil, 4, item("1", "Cloudflared"), "Cloudflared agent", false, "", now)
	if closed.Name != "Alert closed: Service not running: Cloudflared" || !strings.Contains(closed.Detail, "It looks renamed to Cloudflared agent.") {
		t.Fatalf("closed: %q / %q", closed.Name, closed.Detail)
	}
}

// The notice loop tells of a host's lost sensors once: the first pass only notes what is lost
// already, a sensor lost after it is told (with what it looks renamed to), and never twice.
func TestGoneTickTellsOnce(t *testing.T) {
	now := time.Now()
	var lost []map[string]any
	standing := map[string]map[string]any{} // the reported sensors that still exist, as Zabbix has them now
	lostItem := func(id, name string) map[string]any {
		return map[string]any{"itemid": id, "hostid": "10", "name": name, "key_": "win.service.state[" + name + "]", "status": "1", "value_type": "3",
			"itemDiscovery": map[string]string{"status": "1", "lastcheck": itoa64(now.Add(-4 * time.Hour).Unix()), "ts_delete": itoa64(now.Add(6 * 24 * time.Hour).Unix()), "parent_itemid": "50"}}
	}
	liveItem := func(id, name string) map[string]any {
		return map[string]any{"itemid": id, "hostid": "10", "name": name, "key_": "win.service.state[" + name + "]", "status": "0", "value_type": "3",
			"itemDiscovery": map[string]string{"status": "0", "parent_itemid": "50"}}
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			ID     any            `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any = []any{}
		switch req.Method {
		case "item.get":
			if _, ok := req.Params["filter"]; ok {
				result = lost
			} else if ids, ok := req.Params["itemids"].([]any); ok { // what became of reported sensors
				var out []map[string]any
				for _, id := range ids {
					if row, ok := standing[id.(string)]; ok {
						out = append(out, row)
					}
				}
				result = out
			} else if req.Params["hostids"] == "10" {
				result = append([]map[string]any{liveItem("201", "Cloudflared agent"), liveItem("202", "RustDesk Service")}, lost...)
			}
		case "host.get":
			result = []map[string]any{{"hostid": "10", "name": "media1", "status": "0", "proxyid": "0", "hostgroups": []map[string]string{{"groupid": "1", "name": "site1"}}}}
		case "trend.get": // the lost sensors read for days; the new ones are new
			var out []map[string]string
			for _, id := range req.Params["itemids"].([]any) {
				if id.(string) < "200" {
					for h := 0; h < 48; h++ {
						out = append(out, map[string]string{"itemid": id.(string), "clock": itoa64(now.Unix()/3600*3600 - int64(h+1)*3600), "num": "60", "value_avg": "1"})
					}
				}
			}
			result = out
		case "trigger.get":
			result = []map[string]any{{"triggerid": "900", "priority": "4", "status": "0", "value": "1", "lastchange": "0", "expression": "x", "items": []map[string]string{{"itemid": "101"}}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
	}))
	t.Cleanup(mock.Close)
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr, err := settings.New(t.Context(), st, zabbix.New("", ""))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, zbx: zabbix.New(mock.URL, "test-token"), mgr: mgr, ren: &renameIndex{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var got []notify.Event
	dests := []notifyDest{
		{key: "high", alerts: true, minSev: 4, send: func(_ context.Context, ev notify.Event) error { got = append(got, ev); return nil }},
		{key: "disaster", alerts: true, minSev: 5, send: func(_ context.Context, ev notify.Event) error {
			t.Error("a Disaster-only channel heard of a High sensor")
			return nil
		}},
	}

	lost = []map[string]any{lostItem("100", "Old disk")}
	s.goneTick(t.Context(), dests, now) // first pass: what is lost already was known
	if len(got) != 0 {
		t.Fatalf("the first pass told: %+v", got)
	}
	lost = append(lost, lostItem("101", "Cloudflared"), lostItem("102", "RustDesk"))
	s.goneTick(t.Context(), dests, now)
	if len(got) != 1 || got[0].Name != "2 sensors look renamed" || got[0].Host != "media1" ||
		!strings.HasPrefix(got[0].Detail, "Cloudflared, now Cloudflared agent\nRustDesk, now RustDesk Service\n") {
		t.Fatalf("want one notice of the two renames, got %+v", got)
	}
	s.goneTick(t.Context(), dests, now)
	if len(got) != 1 {
		t.Fatalf("told twice: %+v", got)
	}

	// A Linux box rebooting lists no cores for a moment: its core sensors are lost and switched off,
	// and found again a minute later. Not told while the loss is young; told once it has lasted.
	core := lostItem("103", "Core 1 load")
	core["key_"] = "system.cpu.core[1]"
	core["itemDiscovery"] = map[string]string{"status": "1", "lastcheck": itoa64(now.Add(-2 * time.Minute).Unix()), "ts_delete": itoa64(now.Add(6 * 24 * time.Hour).Unix()), "parent_itemid": "51"}
	lost = append(lost, core)
	s.goneTick(t.Context(), dests, now)
	s.goneTick(t.Context(), dests, now.Add(time.Hour))
	if len(got) != 1 {
		t.Fatalf("a young loss was told: %+v", got[1:])
	}
	s.goneTick(t.Context(), dests, now.Add(goneSettle))
	if len(got) != 2 || got[1].Name != "Gone from the device: Core 1 load" {
		t.Fatalf("a loss that lasted: want it told, got %+v", got[1:])
	}

	// The core is found again and switched back on: told back, once. Cloudflared, told of as renamed,
	// is deleted by Zabbix meanwhile: forgotten without a word.
	lost = []map[string]any{lostItem("100", "Old disk"), lostItem("102", "RustDesk")}
	standing["103"] = map[string]any{"itemid": "103", "hostid": "10", "name": "Core 1 load", "key_": "system.cpu.core[1]", "status": "0",
		"itemDiscovery": map[string]string{"status": "0", "parent_itemid": "51"}}
	later := now.Add(goneSettle + time.Hour)
	s.goneTick(t.Context(), dests, later)
	if len(got) != 3 || got[2].Name != "Back on the device: Core 1 load" || got[2].Detail != "Zabbix finds it again: it is checked again and keeps its history." {
		t.Fatalf("want the core told back, got %+v", got[2:])
	}
	s.goneTick(t.Context(), dests, later)
	if len(got) != 3 {
		t.Fatalf("told back twice: %+v", got[3:])
	}
	raw, _, _ := st.MetaGet(t.Context(), goneReportedKey)
	if raw != `{"102":"10"}` {
		t.Fatalf("still reported: %s", raw)
	}
}

func TestBackNoticeWords(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	ev := backNoticeEvent("vm1", "site1", nil, []zabbix.Item{{Name: "Core 2 load"}, {Name: "Core 1 load"}}, "", now)
	if ev.Name != "2 sensors back on the device" || ev.Detail != "Back: Core 1 load\nBack: Core 2 load\nZabbix finds them again: they are checked again and keep their history." || ev.Kind != "info" {
		t.Fatalf("%q / %q", ev.Name, ev.Detail)
	}
}

// A problem counts as gone only when every sensor of its trigger is lost.
func TestAllLost(t *testing.T) {
	lost := map[string]zabbix.Item{"1": {}, "2": {}}
	if !allLost([]string{"1", "2"}, lost) || allLost([]string{"1", "3"}, lost) || allLost(nil, lost) || allLost([]string{"1"}, nil) {
		t.Fatal("allLost")
	}
}
