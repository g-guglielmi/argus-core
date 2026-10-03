// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"argus/internal/zabbix"
)

func testImportEnv() *importEnv {
	return &importEnv{
		techNames: map[string]string{"web1": "web1"},
		visible:   map[string]string{"web1": "web1", "nas one": "NAS One"},
		atIP:      map[string][]ipHost{"10.0.0.30": {{name: "nas-old", class: "linux-snmp"}}},
		proxies:   map[string]zabbix.Proxy{"proxy-site1": {ProxyID: "10", Name: "proxy-site1"}, "proxy-site4": {ProxyID: "14", Name: "proxy-site4"}},
		groups:    map[string]bool{"site1": true, "site4": true},
		tags:      map[string]string{},
	}
}

// Every row is checked before anything is created: a ready row resolves its class and probe, one
// already monitored (by name, or at its address as the same class) is skipped, and the rest say what
// to fix.
func TestImportCheck(t *testing.T) {
	env := testImportEnv()
	rows := []importRow{
		{Row: 2, Name: "sw-site4", Address: "10.0.4.2", Class: "Linux (SNMP)", Site: "site4"},
		{Row: 3, Name: "nas-site4", Address: "10.0.4.30", Class: "ugreen-nass", Site: "site4"},
		{Row: 4, Name: "printer", Address: "10.0.4.40", Class: "base", Site: "site4", Probe: "proxy-site9"},
		{Row: 5, Name: "WEB1", Address: "10.0.0.9", Class: "linux-snmp", Site: "site1"},
		{Row: 6, Name: "nas-new", Address: "10.0.0.30", Class: "linux-snmp", Site: "site1"},
		{Row: 7, Name: "dns-a", Address: "dns-a.example.lan", Class: "adguard", Site: "site4/Servers", Probe: "server", Tags: "critical; customer-a"},
		{Row: 8, Name: "ups-a", Address: "10.0.4.50", Class: "nut-collector", Site: "site4"},
		{Row: 9, Name: "ups-b", Address: "10.0.4.51", Class: "nut-collector", Site: "site4", Macros: map[string]string{"NUT.UPS": "ups"}},
		{Row: 10, Name: "sw-site4", Address: "10.0.4.3", Class: "base", Site: "site4"},
		{Row: 11, Name: "bad", Address: "not an address!", Class: "base", Site: "", Tags: "ok, !bad"},
	}
	reqs, cks := checkImport(env, rows)
	byRow := map[int]importCheck{}
	for _, c := range cks {
		byRow[c.Row] = c
	}
	if c := byRow[2]; c.Status != "ready" || c.ClassID != "linux-snmp" || c.Probe != "proxy-site4" || reqs[0].ProxyID != "14" {
		t.Fatalf("row 2: %+v %+v", c, reqs[0])
	}
	if c := byRow[3]; c.Status != "fix" || !strings.Contains(c.Problems[0].Msg, "did you mean Ugreen (Zabbix agent)?") {
		t.Fatalf("row 3: %+v", c)
	}
	if c := byRow[4]; c.Status != "fix" || c.Problems[0].Field != "probe" {
		t.Fatalf("row 4: %+v", c)
	}
	if c := byRow[5]; c.Status != "skip" || c.Note != "already monitored as web1" {
		t.Fatalf("row 5: %+v", c)
	}
	if c := byRow[6]; c.Status != "skip" || !strings.Contains(c.Note, "nas-old (same address and class)") {
		t.Fatalf("row 6: %+v", c)
	}
	if c := byRow[7]; c.Status != "ready" || c.Probe != "Server" || c.Note != "creates the group site4/Servers" || reqs[5].DNS != "dns-a.example.lan" || reqs[5].Macros["{$ADGUARD.URL}"] != "http://dns-a.example.lan" {
		t.Fatalf("row 7: %+v %+v", c, reqs[5])
	}
	if c := byRow[8]; c.Status != "fix" || len(c.Missing) != 1 || c.Missing[0].Macro != "{$NUT.UPS}" || c.Problems[0].Field != "macro:{$NUT.UPS}" {
		t.Fatalf("row 8: %+v", c)
	}
	if c := byRow[9]; c.Status != "ready" || reqs[7].Macros["{$NUT.UPS}"] != "ups" {
		t.Fatalf("row 9: %+v %+v", c, reqs[7].Macros)
	}
	if c := byRow[10]; c.Status != "fix" || c.Problems[0].Msg != "the same name as row 2" {
		t.Fatalf("row 10: %+v", c)
	}
	fields := map[string]bool{}
	for _, p := range byRow[11].Problems {
		fields[p.Field] = true
	}
	if !fields["address"] || !fields["site"] || !fields["tags"] {
		t.Fatalf("row 11: %+v", byRow[11])
	}
	if c := countImport(cks); c.Ready != 3 || c.Skip != 2 || c.Fix != 5 {
		t.Fatalf("counts: %+v", c)
	}
	if macroKey("{$nut.ups}") != "{$NUT.UPS}" || macroKey("$NUT.UPS") != "{$NUT.UPS}" || macroKey(" ") != "" {
		t.Fatal("macroKey")
	}
}

// PRTG's tree: devices with their group path below the probe, their tags and sensor types, and the
// class those suggest. A refused key says so without the key in the message.
func TestReadPRTG(t *testing.T) {
	tables := map[string]string{
		"devices":    `{"prtg-version":"24.1","devices":[{"objid":2041,"device":"SW-Core-01","host":"10.0.0.2","probe":"Site 1 probe","parentid":510,"tags":"critical customer-a"},{"objid":2057,"device":"Fileserver","host":"10.0.0.31","probe":"Site 1 probe","parentid":511,"tags":""},{"objid":2102,"device":"Printer","host":"10.0.0.77","probe":"Local Probe","parentid":1,"tags":""}]}`,
		"groups":     `{"groups":[{"objid":0,"name":"Root","parentid":-1},{"objid":1,"name":"Local Probe","parentid":0},{"objid":500,"name":"Site 1 probe","parentid":0},{"objid":510,"name":"Network","parentid":500},{"objid":511,"name":"Servers/Win","parentid":510}]}`,
		"probenodes": `{"probenodes":[{"objid":1,"name":"Local Probe"},{"objid":500,"name":"Site 1 probe"}]}`,
		"sensors":    `{"sensors":[{"objid":1,"parentid":2041,"type":"SNMP Traffic","type_raw":"snmptraffic"},{"objid":2,"parentid":2041,"type":"Ping","type_raw":"ping"},{"objid":3,"parentid":2057,"type":"WMI Free Disk Space (Multi Disk)","type_raw":"wmidiskspace"},{"objid":4,"parentid":2102,"type":"Ping","type_raw":"ping"}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apitoken") != "k3y" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(tables[r.URL.Query().Get("content")]))
	}))
	defer srv.Close()
	v, err := readPRTG(context.Background(), prtgClient{base: srv.URL, key: "k3y", http: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != "24.1" || len(v.Devices) != 3 || v.Groups != 2 || strings.Join(v.Tags, ",") != "critical,customer-a" || len(v.Probes) != 2 {
		t.Fatalf("tree: %+v", v)
	}
	by := map[string]prtgDevice{}
	for _, d := range v.Devices {
		by[d.Name] = d
	}
	if d := by["SW-Core-01"]; strings.Join(d.Groups, "/") != "Network" || d.Class != "base" || !strings.Contains(d.Hint, "SNMP Traffic") {
		t.Fatalf("switch: %+v", d)
	}
	if d := by["Fileserver"]; strings.Join(d.Groups, "/") != "Network/Servers-Win" || d.Class != "windows-snmp" {
		t.Fatalf("file server: %+v", d)
	}
	if d := by["Printer"]; len(d.Groups) != 0 || d.Class != "base" || d.Hint != "only a Ping sensor" {
		t.Fatalf("printer: %+v", d)
	}
	_, err = readPRTG(context.Background(), prtgClient{base: srv.URL, key: "wrong", http: srv.Client()})
	if err == nil || !strings.Contains(err.Error(), "refused the API key") || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("refused: %v", err)
	}
	if c, _ := guessPRTGClass("USW-Lite-16 UniFi", nil, nil); c != "unifi-switch" {
		t.Fatalf("unifi by name: %q", c)
	}
	if c, h := guessPRTGClass("esx01", []string{"VMware Host Hardware"}, nil); c != "" || !strings.Contains(h, "VMware") {
		t.Fatalf("vmware: %q %q", c, h)
	}
	if c, _ := guessPRTGClass("srv", []string{"SSH Disk Free"}, []string{"sshdiskfree"}); c != "linux-ssh" {
		t.Fatalf("ssh: %q", c)
	}
}
