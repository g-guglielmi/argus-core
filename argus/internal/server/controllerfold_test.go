// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strings"
	"testing"

	"argus/internal/zabbix"
)

func TestUniFiGroupsByControllerURL(t *testing.T) {
	macros := map[string]string{
		"1": "https://UniFi.example.lan:11443/", "2": "https://unifi.example.lan:11443", "3": "https://unifi.example.lan:11443/network",
		"4": "https://10.0.0.1", "5": "https://10.0.0.1/", "6": "", "7": "https://other.example.lan",
	}
	classes := map[string]string{"1": "unifi-ap", "2": "unifi-gateway", "3": "unifi-console", "4": "unifi-gateway", "5": "unifi-ap", "7": "unifi-switch"}
	groups := groupUniFi(macros, classes)
	if len(groups) != 3 {
		t.Fatalf("groups: %+v", groups)
	}
	// Written three ways, one controller; its console is the controller's host even though a gateway shares the URL.
	g := groups[2]
	if g.url != "https://unifi.example.lan:11443" || strings.Join(g.devices, ",") != "1,2,3" || g.host != "3" {
		t.Fatalf("console group: %+v", g)
	}
	// No console: the gateway stands for the controller.
	if g := groups[0]; g.url != "https://10.0.0.1" || g.host != "4" || len(g.devices) != 2 {
		t.Fatalf("gateway group: %+v", g)
	}
	// Neither: nobody, the first device will carry the problem.
	if g := groups[1]; g.url != "https://other.example.lan" || g.host != "" || len(g.devices) != 1 {
		t.Fatalf("lone group: %+v", g)
	}
	if controllerLabel("https://unifi.example.lan:11443") != "unifi.example.lan:11443" || controllerID("a") == controllerID("b") || !strings.HasPrefix(controllerID("a"), "argus-controller-") {
		t.Fatal("label or id")
	}
}

func TestControllerOutagesNeedAQuorum(t *testing.T) {
	g := unifiGroup{url: "https://c", host: "9", devices: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}}
	sig := func(offline ...string) map[string]deviceSignal {
		s := map[string]deviceSignal{}
		for _, d := range g.devices {
			s[d] = deviceSignal{connected: 1, pingUp: true}
		}
		for _, d := range offline {
			s[d] = deviceSignal{connected: 0, pingUp: true}
		}
		return s
	}
	// Two of ten offline while pinging: two problems of their own, not the controller's.
	if out := controllerOutages([]unifiGroup{g}, sig("1", "2")); len(out) != 0 {
		t.Fatalf("two of ten folded: %+v", out)
	}
	// Half of them: the controller.
	out := controllerOutages([]unifiGroup{g}, sig("1", "2", "3", "4", "5"))
	if len(out) != 1 || strings.Join(out[0].affected, ",") != "1,2,3,4,5" {
		t.Fatalf("half: %+v", out)
	}
	// Devices that stopped pinging are their own outage (the ping holds them), so they don't count...
	s := sig("1", "2", "3", "4", "5")
	for _, d := range []string{"1", "2", "3"} {
		s[d] = deviceSignal{connected: 0, pingUp: false}
	}
	if out := controllerOutages([]unifiGroup{g}, s); len(out) != 0 {
		t.Fatalf("unpingable devices counted: %+v", out)
	}
	// ...while a device whose controller call itself stopped does (the API down).
	s = sig()
	for _, d := range []string{"1", "2", "3", "4", "5", "6"} {
		s[d] = deviceSignal{connected: 1, pingUp: true, rawBroken: true}
	}
	if out := controllerOutages([]unifiGroup{g}, s); len(out) != 1 || len(out[0].affected) != 6 {
		t.Fatalf("broken calls: %+v", out)
	}
	// A controller with one device never folds; one with two needs both.
	small := unifiGroup{url: "https://s", devices: []string{"1", "2"}}
	if out := controllerOutages([]unifiGroup{small}, map[string]deviceSignal{"1": {connected: 0, pingUp: true}, "2": {connected: 1, pingUp: true}}); len(out) != 0 {
		t.Fatalf("one of two: %+v", out)
	}
	if out := controllerOutages([]unifiGroup{small}, map[string]deviceSignal{"1": {connected: 0, pingUp: true}, "2": {connected: 0, pingUp: true}}); len(out) != 1 {
		t.Fatalf("two of two: %+v", out)
	}
	if out := controllerOutages([]unifiGroup{{url: "https://l", devices: []string{"1"}}}, map[string]deviceSignal{"1": {connected: 0, pingUp: true}}); len(out) != 0 {
		t.Fatalf("a lone device folded: %+v", out)
	}
}

func TestControllerProblemsKeepTheirStartAndEnd(t *testing.T) {
	controllerClock.since, controllerClock.recovered = nil, nil
	g := unifiGroup{url: "https://unifi.example.lan", host: "9", devices: []string{"1", "2", "9"}}
	names := map[string]string{"1": "ap-hall", "2": "ap-lab", "9": "udm"}
	hostOf := func(id string) (zabbix.TargetHost, bool) {
		n, ok := names[id]
		return zabbix.TargetHost{HostID: id, Name: n, Status: "0"}, ok
	}
	out := []controllerOutage{{group: g, affected: []string{"1", "2"}}}
	probs, targets, readings := controllerProblems(out, names, hostOf, 1000)
	if len(probs) != 1 || probs[0].Name != "UniFi controller unifi.example.lan: 2 of 3 devices offline on it" || probs[0].Clock != "1000" || probs[0].Severity != "4" {
		t.Fatalf("problem: %+v", probs)
	}
	id := probs[0].EventID
	if tg := targets[id]; len(tg.Hosts) != 1 || tg.Hosts[0].HostID != "9" || len(tg.Items) != 0 {
		t.Fatalf("target: %+v", tg)
	}
	if readings[id] != "The controller is restarting or unreachable; offline on it: ap-hall, ap-lab" {
		t.Fatalf("reading: %q", readings[id])
	}
	// Seen again later: the start stays.
	probs, _, _ = controllerProblems(out, names, hostOf, 1300)
	if probs[0].Clock != "1000" {
		t.Fatalf("the start moved: %+v", probs)
	}
	// Gone: the end is noted for the settle window, and a fresh outage later starts afresh.
	controllerProblems(nil, names, hostOf, 1600)
	if controllerRecoveredAt(g.url) != 1600 || controllerOpen() {
		t.Fatalf("recovery: %d open=%v", controllerRecoveredAt(g.url), controllerOpen())
	}
	probs, _, _ = controllerProblems(out, names, hostOf, 2000)
	if probs[0].Clock != "2000" {
		t.Fatalf("a new outage kept the old start: %+v", probs)
	}
	// No monitored controller host: the first device carries it.
	lone := unifiGroup{url: "https://x", devices: []string{"1", "2"}}
	probs, targets, _ = controllerProblems([]controllerOutage{{group: lone, affected: []string{"1", "2"}}}, names, hostOf, 2100)
	if len(probs) != 1 || targets[probs[0].EventID].Hosts[0].HostID != "1" {
		t.Fatalf("owner: %+v", targets)
	}
	controllerClock.since, controllerClock.recovered = nil, nil
}

func TestControllerHoldsItsDevices(t *testing.T) {
	m := testMasters()
	id := controllerID("https://unifi.example.lan")
	pm := masterItem{itemID: id, key: controllerKey, lastClock: 1100, rank: -1}
	// Hosts 10 (an AP, ping 100) and 50 (the console) read through the same controller.
	m.controller = map[string][]masterItem{"10": {pm}, "50": {pm}}
	m.controllerHost = map[string]string{id: "50"}
	m.byHost["50"] = []masterItem{{itemID: "500", key: "icmpping", lastValue: "1", lastClock: 1000}}
	state := []masterRef{{id: "105", key: "unifi.device.state"}}
	raw := []masterRef{{id: "501", key: "unifi.console.raw"}}
	// No outage: a device's own alert goes out as always.
	if v := m.hold("10", state, 990, 1100); v.held {
		t.Fatalf("held without an outage: %+v", v)
	}
	// The controller's outage is open: the device's alert, and the console's own, wait on it.
	m.down[id] = true
	if v := m.hold("10", state, 990, 1100); !v.held || !v.down || v.by != "controller" || v.host != "50" || v.master.itemID != id {
		t.Fatalf("device under the controller: %+v", v)
	}
	if v := m.hold("50", raw, 990, 1100); !v.held || v.by != "controller" {
		t.Fatalf("the console's call under the controller: %+v", v)
	}
	// The outage problem itself has no sensor and is never held by its own pseudo master.
	if v := m.hold("50", nil, 990, 1100); v.held {
		t.Fatalf("the outage held itself: %+v", v)
	}
	// The ping still holds a device that stopped answering, before the controller gets a say.
	m.down["100"] = true
	if v := m.hold("10", state, 990, 1100); !v.held || v.by != "host" {
		t.Fatalf("ping first: %+v", v)
	}
	delete(m.down, "100")
	// Just back: the devices wait out the settle window, then alert on their own.
	delete(m.down, id)
	m.recovered, m.settle = map[string]int64{id: 1090}, 300
	if v := m.hold("10", state, 990, 1100); !v.held || !v.settling || v.by != "controller" {
		t.Fatalf("settling: %+v", v)
	}
	if v := m.hold("10", state, 990, 1500); v.held {
		t.Fatalf("held past the window: %+v", v)
	}
	// masterDown knows the outage problem by its id, sensor or not.
	down := masterDown([]zabbix.Problem{{EventID: id, ObjectID: id, Severity: "4"}}, map[string]zabbix.TriggerTarget{id: {}})
	if !down[id] {
		t.Fatal("the outage problem is not a down master")
	}
}

func TestControllerWording(t *testing.T) {
	if controllerUpName("UniFi controller unifi.example.lan:11443: 3 of 5 devices offline on it") != "UniFi controller unifi.example.lan:11443: its devices are back on it" {
		t.Fatal("recovery sentence")
	}
	if controllerUpName("Access point offline on the UniFi controller") != "" {
		t.Fatal("a device's own problem is not the controller's")
	}
	if interfaceRowLabel("UniFi controller x: 2 of 3 devices offline on it") != "UniFi controller" {
		t.Fatal("row label")
	}
	off, unpingable, broken, unknown := deviceSignal{connected: 0, pingUp: true}, deviceSignal{connected: 0, pingUp: false}, deviceSignal{connected: 1, pingUp: true, rawBroken: true}, deviceSignal{connected: -1, pingUp: true}
	if !off.affected() || unpingable.affected() || !broken.affected() || unknown.affected() {
		t.Fatal("signature")
	}
}
