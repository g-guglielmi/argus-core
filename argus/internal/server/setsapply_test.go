// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"argus/internal/provision"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// setsZabbix is a Zabbix that keeps one host: its linked templates, macros, interfaces and items,
// and the order of the calls that changed them.
type setsZabbix struct {
	linked []string
	macros map[string]string // name -> value
	ifaces []int             // interface types
	items  map[string]string // item id -> key
	calls  []string
}

func (z *setsZabbix) serve(t *testing.T) *httptest.Server {
	tid := func(name string) string { return "t:" + name }
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     any             `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var p map[string]json.RawMessage
		_ = json.Unmarshal(req.Params, &p)
		var result any = []any{}
		switch req.Method {
		case "host.get":
			var parents []map[string]string
			for _, n := range z.linked {
				parents = append(parents, map[string]string{"host": n})
			}
			var ifs []map[string]any
			for i, typ := range z.ifaces {
				ifs = append(ifs, map[string]any{"interfaceid": fmt.Sprint(900 + i), "type": fmt.Sprint(typ), "main": "1", "useip": "1", "ip": "10.0.0.53", "dns": "", "port": "10050", "details": []any{}})
			}
			result = []map[string]any{{"hostid": "50", "host": "adguard1", "name": "adguard1", "proxyid": "7", "monitored_by": "1", "parentTemplates": parents, "interfaces": ifs}}
		case "usermacro.get":
			var out []map[string]string
			for n, v := range z.macros {
				out = append(out, map[string]string{"hostmacroid": "m:" + n, "macro": n, "value": v, "type": "0"})
			}
			result = out
		case "usermacro.create":
			var m struct{ Macro, Value string }
			_ = json.Unmarshal(req.Params, &m)
			z.macros[m.Macro] = m.Value
			z.calls = append(z.calls, "macro+ "+m.Macro)
			result = map[string]any{}
		case "usermacro.update":
			var m struct{ HostMacroID, Value string }
			_ = json.Unmarshal(req.Params, &m)
			z.macros[strings.TrimPrefix(m.HostMacroID, "m:")] = m.Value
			result = map[string]any{}
		case "usermacro.delete":
			var ids []string
			_ = json.Unmarshal(req.Params, &ids)
			for _, id := range ids {
				delete(z.macros, strings.TrimPrefix(id, "m:"))
				z.calls = append(z.calls, "macro- "+strings.TrimPrefix(id, "m:"))
			}
			result = map[string]any{}
		case "template.get":
			var f struct {
				Filter struct{ Host []string } `json:"filter"`
			}
			_ = json.Unmarshal(req.Params, &f)
			var out []map[string]string
			for _, n := range f.Filter.Host {
				out = append(out, map[string]string{"templateid": tid(n), "host": n, "name": n})
			}
			result = out
		case "host.massadd":
			var m struct {
				Templates []struct{ TemplateID string } `json:"templates"`
			}
			_ = json.Unmarshal(req.Params, &m)
			for _, tp := range m.Templates {
				n := strings.TrimPrefix(tp.TemplateID, "t:")
				z.linked = append(z.linked, n)
				z.calls = append(z.calls, "link "+n)
			}
			result = map[string]any{}
		case "host.update":
			var m struct {
				Clear []struct{ TemplateID string } `json:"templates_clear"`
			}
			_ = json.Unmarshal(req.Params, &m)
			for _, tp := range m.Clear {
				n := strings.TrimPrefix(tp.TemplateID, "t:")
				z.linked = slices.DeleteFunc(z.linked, func(x string) bool { return x == n })
				z.calls = append(z.calls, "unlink "+n)
			}
			result = map[string]any{}
		case "hostinterface.create":
			var m struct {
				Type any `json:"type"`
			}
			_ = json.Unmarshal(req.Params, &m)
			typ := 1
			if fmt.Sprint(m.Type) == "2" {
				typ = 2
			}
			z.ifaces = append(z.ifaces, typ)
			z.calls = append(z.calls, fmt.Sprintf("iface+ %d", typ))
			result = map[string]any{"interfaceids": []string{"950"}}
		case "item.get":
			var out []map[string]string
			for id, k := range z.items {
				out = append(out, map[string]string{"itemid": id, "hostid": "50", "name": k, "key_": k})
			}
			result = out
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
	}))
	t.Cleanup(mock.Close)
	return mock
}

// A host's sensor sets change in one save: an AdGuard host gains Linux by SNMP (with the SNMP
// interface it reads through, its settings set before the link), a second system is refused before
// anything is written, one system swaps for another, and a set that goes takes its settings and its
// sensors' open alerts with it, without a RESOLVED.
func TestApplySets(t *testing.T) {
	const adguard, dns, linux, ssh = "Argus AdGuard Home by HTTP", "Argus DNS resolution", "Argus Linux by SNMP", "Argus Linux by SSH"
	z := &setsZabbix{
		linked: []string{provision.TemplateBasePing, adguard, dns},
		macros: map[string]string{"{$ADGUARD.URL}": "http://10.0.0.53:3000", "{$ADGUARD.USER}": "admin", "{$DNS.RESOLVE.NAMES}": "example.com", "{$PING.LOSS.WARN}": "30"},
		ifaces: []int{1},
		items:  map[string]string{"501": "adguard.running", "502": "icmpping", "503": "net.dns.time[example.com]", "504": "adguard.protection"},
	}
	mock := z.serve(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	s := &Server{st: st, zbx: zabbix.New(mock.URL, "test-token"), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_ = st.SetDeviceClass(ctx, "50", "adguard", "manual")
	refused := func(err error, want string) {
		t.Helper()
		if _, ok := err.(setsRefusal); !ok || !strings.Contains(err.Error(), want) {
			t.Fatalf("want a refusal saying %q, got %v", want, err)
		}
		if len(z.calls) != 0 {
			t.Fatalf("a refused save wrote to Zabbix: %v", z.calls)
		}
	}

	// No SNMP interface and no default to inherit: refused, the host untouched.
	refused(s.applySets(ctx, "50", map[string]setDesired{"linux-snmp": {On: true}}), "reads over SNMP")
	// A set its template can't do without a setting.
	refused(s.applySets(ctx, "50", map[string]setDesired{"tcp": {On: true, Macros: map[string]string{"{$TCP.PORTS}": " "}}}), "Ports is required for TCP ports")
	// A value that would reach the collector's command line as something else.
	refused(s.applySets(ctx, "50", map[string]setDesired{"tcp": {On: true, Macros: map[string]string{"{$TCP.PORTS}": "22;reboot"}}}), "isn't an accepted value")
	// A probe's own set on a device.
	refused(s.applySets(ctx, "50", map[string]setDesired{"speedtest": {On: true}}), "probe's own host only")

	// With its probe's SNMP default: the interface, then the link.
	_ = st.SetSNMPDefault(ctx, "7", store.SNMPDefault{Version: 2, Community: "public", Bulk: 1})
	if err := s.applySets(ctx, "50", map[string]setDesired{"linux-snmp": {On: true}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(z.calls, "; "); got != "iface+ 2; link "+linux {
		t.Fatalf("adding Linux by SNMP: %s", got)
	}
	z.calls = nil

	// A second system: refused by the rule, in its words.
	refused(s.applySets(ctx, "50", map[string]setDesired{"linux-ssh": {On: true}}), "one system per host: Linux by SNMP and Linux by SSH can't both be on")

	// One system for another in the same save: the old one goes first, the new one's settings are
	// on the host before its template is.
	if err := s.applySets(ctx, "50", map[string]setDesired{"linux-snmp": {On: false},
		"linux-ssh": {On: true, Macros: map[string]string{"{$SSH.USER}": "monitor", "{$SSH.PORT}": ""}}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(z.calls, "; "); got != "unlink "+linux+"; macro+ {$SSH.USER}; link "+ssh {
		t.Fatalf("swapping the system: %s", got)
	}
	z.calls = nil

	// A setting of a set that is on: changed, cleared back to the default, the others left alone.
	if err := s.applySets(ctx, "50", map[string]setDesired{"dns-resolve": {On: true, Macros: map[string]string{"{$DNS.RESOLVE.NAMES}": "example.com, cloudflare.com"}},
		"linux-ssh": {On: true, Macros: map[string]string{"{$SSH.USER}": ""}}}); err != nil {
		t.Fatal(err)
	}
	if z.macros["{$DNS.RESOLVE.NAMES}"] != "example.com, cloudflare.com" || z.macros["{$ADGUARD.USER}"] != "admin" {
		t.Fatalf("settings: %v", z.macros)
	}
	if _, has := z.macros["{$SSH.USER}"]; has {
		t.Fatal("a cleared setting stayed on the host")
	}
	z.calls = nil

	// A required setting cleared on a set that stays.
	refused(s.applySets(ctx, "50", map[string]setDesired{"adguard": {On: true, Macros: map[string]string{"{$ADGUARD.URL}": ""}}}), "Admin URL is required for AdGuard Home")

	// AdGuard Home goes: its settings with it, the DNS check's and a threshold stay, and its
	// sensors are marked so their alerts close without a RESOLVED.
	t.Cleanup(func() { unmarkItems([]string{"501", "502", "503", "504"}) })
	if err := s.applySets(ctx, "50", map[string]setDesired{"adguard": {On: false}}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(z.linked, adguard) || !slices.Contains(z.linked, dns) || !slices.Contains(z.linked, ssh) {
		t.Fatalf("linked after removing AdGuard Home: %v", z.linked)
	}
	for _, gone := range []string{"{$ADGUARD.URL}", "{$ADGUARD.USER}"} {
		if _, has := z.macros[gone]; has {
			t.Errorf("%s outlived its set", gone)
		}
	}
	if z.macros["{$DNS.RESOLVE.NAMES}"] == "" || z.macros["{$PING.LOSS.WARN}"] != "30" {
		t.Fatalf("another set's setting or a threshold went: %v", z.macros)
	}
	if !itemRemoved("501") || !itemRemoved("504") || itemRemoved("502") || itemRemoved("503") {
		t.Fatal("the removed set's sensors, and only those, are marked removed")
	}
}

// A set that others read on top of can't go alone, and the sets a save doesn't name stay as they are.
func TestApplySetsNeedsAndUntouched(t *testing.T) {
	z := &setsZabbix{linked: []string{provision.TemplateBasePing, "Argus Linux by SNMP", "Argus unRAID by SNMP"}, macros: map[string]string{"{$FS.NAME.SKIP}": "x"}, ifaces: []int{2}, items: map[string]string{}}
	mock := z.serve(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	s := &Server{st: st, zbx: zabbix.New(mock.URL, "test-token"), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_ = st.SetDeviceClass(ctx, "50", "unraid", "manual")
	err = s.applySets(ctx, "50", map[string]setDesired{"linux-snmp": {On: false}})
	if _, ok := err.(setsRefusal); !ok || err.Error() != "unRAID needs Linux by SNMP" {
		t.Fatalf("removing what unRAID reads on top of: %v", err)
	}
	if err := s.applySets(ctx, "50", map[string]setDesired{}); err != nil || len(z.calls) != 0 {
		t.Fatalf("an empty save: %v, %v", err, z.calls)
	}
	// Both go together; unRAID's preset goes with it.
	if err := s.applySets(ctx, "50", map[string]setDesired{"linux-snmp": {On: false}, "unraid": {On: false}}); err != nil {
		t.Fatal(err)
	}
	if len(z.linked) != 1 || len(z.macros) != 0 {
		t.Fatalf("after removing both: %v, %v", z.linked, z.macros)
	}
}

// A new host carries its class's usual sets, or exactly the ones the form listed: an AdGuard box
// that is also a Linux server gets both, with one interface per kind its sets read through.
func TestHostSetsFor(t *testing.T) {
	ids := func(sets []provision.Set) string {
		var out []string
		for _, st := range sets {
			out = append(out, st.ID)
		}
		return strings.Join(out, ",")
	}
	adguard, _ := provision.ClassByID("adguard")
	usual, err := hostSetsFor(createHostRequest{}, adguard)
	if err != nil || ids(usual) != "adguard,dns-resolve" {
		t.Fatalf("the class's usual sets: %s, %v", ids(usual), err)
	}
	if k := setIfaces(usual); len(k) != 1 || k[0] != provision.IfaceAgent {
		t.Fatalf("interfaces: %v", k)
	}
	picked := []string{"adguard", "dns-resolve", "linux-snmp", "http", "http"}
	both, err := hostSetsFor(createHostRequest{Sets: &picked}, adguard)
	if err != nil || ids(both) != "linux-snmp,adguard,http,dns-resolve" {
		t.Fatalf("the sets picked: %s, %v", ids(both), err)
	}
	if k := setIfaces(both); len(k) != 1 || k[0] != provision.IfaceSNMP {
		t.Fatalf("interfaces with an SNMP set: %v", k)
	}
	none := []string{}
	if only, err := hostSetsFor(createHostRequest{Sets: &none, HTTP: true}, adguard); err != nil || ids(only) != "http" {
		t.Fatalf("an empty list plus the older HTTP flag: %s, %v", ids(only), err)
	}
	two := []string{"linux-snmp", "windows-snmp"}
	if _, err := hostSetsFor(createHostRequest{Sets: &two}, adguard); err == nil {
		t.Fatal("two systems on a new host passed")
	}
	// Its settings are its sets': one of a set it doesn't carry is refused.
	ms, err := buildMacros(createHostRequest{Sets: &picked, Macros: map[string]string{"{$ADGUARD.URL}": "http://10.0.0.53", "{$HTTP.SCHEME}": "http", "{$CPU.UTIL.WARN}": "85"}}, adguard)
	if err != nil || len(ms) != 3 {
		t.Fatalf("settings of the sets picked: %v, %v", ms, err)
	}
	if _, err := buildMacros(createHostRequest{Macros: map[string]string{"{$SSH.USER}": "root"}}, adguard); err == nil {
		t.Fatal("a setting of a set the host doesn't carry passed")
	}
}
