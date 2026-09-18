// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package provision

import (
	"strings"
	"testing"
)

// The embedded templates load, hash stably, and cover the universal Base Ping + HTTP add-on that C0
// ships. (Zabbix-schema validity is confirmed by the lab import; this guards syntax + presence.)
func TestLoadTemplates(t *testing.T) {
	docs, h1, err := loadTemplates()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(docs) < 2 {
		t.Fatalf("expected at least the base + http templates, got %d", len(docs))
	}
	if h1 == "" {
		t.Fatal("empty hash")
	}
	_, h2, _ := loadTemplates()
	if h1 != h2 {
		t.Fatalf("hash not stable: %s vs %s", h1, h2)
	}

	all := ""
	for _, d := range docs {
		all += d.content
	}
	for _, want := range []string{TemplateBasePing, TemplateHTTP, "Argus Linux by SNMP", "icmpping", "{$HTTP.PORT}", "{$PING.LOSS.WARN}", "{$CPU.UTIL.WARN}", "vfs.fs.discovery", "net.if.discovery",
		"Argus UniFi Switch by HTTP", "Argus UniFi AP by HTTP", "Argus UniFi Gateway by HTTP", "Argus UniFi OS Console by HTTP", "unifi.radio.discovery", "unifi.wan.discovery", "unifi.storage.discovery", "{$UNIFI.WAN.AVAIL.MIN}",
		"Argus unRAID by SNMP", "unraid.disktemp.discovery", "unraid.share.discovery", "{$DISK.TEMP.WARN}",
		"snmp.cpu.core.discovery", "snmp.mem.shared", "DISABLE_NEVER",
		"unraid.pooltemp.discovery", "unraid.arraytemp.discovery", "{$POOL.TEMP.WARN}",
		"Argus Windows by SNMP", "win.service.discovery", "{$WIN.SERVICE.MATCHES}",
		"Argus AdGuard Home by HTTP", "adguard.raw", "adguard.block_pct", "{$ADGUARD.URL}",
		"Argus Home Assistant by HTTP", "hass.raw", "hass.version.core", "hass.version.os", "{$HASS.TOKEN}",
		"Argus UPS by PeaNUT", "nut.raw", "nut.battery.charge", "nut.on_battery", "{$PEANUT.URL}", "{$PEANUT.UPS}",
		"Argus UPS by NUT", "argus_nut.py[{HOST.CONN}", "{$NUT.UPS}", "{$NUT.PORT}", "nut.output.voltage",
		"Argus DNS resolution", "dns-resolver.py[discover", "dns.resolve.success", "dns.resolve.time", "{$DNS.RESOLVE.NAMES}",
		"Argus XCP-NG by XAPI", "argus_xcpng.py[{HOST.CONN}", "xcp.host.discovery", "xcp.vm.discovery", "xcp.vmperf.discovery", "{$XCP.VM.MODE}", "{$XCP.VM.IGNORE}", "{$XCP.TEMP.WARN}",
		"Argus Linux by SSH", "argus_linux_ssh.py[{HOST.CONN}", "linux.ssh.reachable", "system.cpu.util[ssh]", "vfs.fs.discovery[ssh]", "net.if.discovery[ssh]", "{$SSH.AUTH}", "{$SSH.KEYFILE}", "{$SSH.PASSWORD}"} {
		if !strings.Contains(all, want) {
			t.Errorf("templates missing %q", want)
		}
	}
}

// Every UniFi class asks for the same controller macros; the templates they attach must exist.
func TestUnifiClassFamily(t *testing.T) {
	for _, id := range []string{"unifi-switch", "unifi-gateway", "unifi-ap", "unifi-console"} {
		c, ok := ClassByID(id)
		if !ok {
			t.Fatalf("%s class missing", id)
		}
		if c.Pattern != PatternHTTPAPI || c.Iface != IfaceAgent || len(c.Templates) != 1 {
			t.Fatalf("unexpected %s class: %+v", id, c)
		}
		var key, mac bool
		for _, m := range c.Macros {
			if m.Macro == "{$UNIFI.KEY}" && m.Required && m.Secret {
				key = true
			}
			if m.Macro == "{$UNIFI.MAC}" && m.Required {
				mac = true
			}
		}
		if !key || !mac {
			t.Errorf("%s: macro specs incomplete (key=%v mac=%v)", id, key, mac)
		}
	}
}

// AdGuard Home and Home Assistant are HTTP-API classes like UniFi, each backed by one template and
// carrying a required secret credential macro (AdGuard's password is optional; HA's token is not).
func TestHTTPServiceClasses(t *testing.T) {
	ag, ok := ClassByID("adguard")
	if !ok {
		t.Fatal("adguard class missing")
	}
	// AdGuard attaches its own template plus the shared DNS-resolution add-on (multi-template).
	if ag.Pattern != PatternHTTPAPI || ag.Iface != IfaceAgent || len(ag.Templates) != 2 || ag.Icon != "globe" {
		t.Fatalf("unexpected adguard class: %+v", ag)
	}
	var agDNS bool
	for _, tmpl := range ag.Templates {
		if tmpl == "Argus DNS resolution" {
			agDNS = true
		}
	}
	if !agDNS {
		t.Error("adguard must attach the Argus DNS resolution add-on")
	}
	var agURL, agPass bool
	for _, m := range ag.Macros {
		if m.Macro == "{$ADGUARD.URL}" && m.Required {
			agURL = true
		}
		if m.Macro == "{$ADGUARD.PASSWORD}" && m.Secret && !m.Required {
			agPass = true
		}
	}
	if !agURL || !agPass {
		t.Errorf("adguard macro specs incomplete (url=%v secret-pass=%v)", agURL, agPass)
	}

	ha, ok := ClassByID("home-assistant")
	if !ok {
		t.Fatal("home-assistant class missing")
	}
	if ha.Pattern != PatternHTTPAPI || ha.Iface != IfaceAgent || len(ha.Templates) != 1 || ha.Icon != "home" {
		t.Fatalf("unexpected home-assistant class: %+v", ha)
	}
	var haToken bool
	for _, m := range ha.Macros {
		if m.Macro == "{$HASS.TOKEN}" && m.Required && m.Secret {
			haToken = true
		}
	}
	if !haToken {
		t.Error("home-assistant must require a secret {$HASS.TOKEN}")
	}

	// NUT via PeaNUT is also an HTTP-API class (no proxy collector); its URL derives from the host.
	nut, ok := ClassByID("nut-ups")
	if !ok {
		t.Fatal("nut-ups class missing")
	}
	if nut.Pattern != PatternHTTPAPI || nut.Iface != IfaceAgent || len(nut.Templates) != 1 || nut.Icon != "battery" {
		t.Fatalf("unexpected nut-ups class: %+v", nut)
	}
	var nutURL, nutUps bool
	for _, m := range nut.Macros {
		if m.Macro == "{$PEANUT.URL}" && m.Required && m.Derive == "http://{host}:8080" {
			nutURL = true
		}
		if m.Macro == "{$PEANUT.UPS}" && m.Required {
			nutUps = true
		}
	}
	if !nutURL || !nutUps {
		t.Errorf("nut-ups macro specs incomplete (url-derive=%v ups=%v)", nutURL, nutUps)
	}

	// The direct NUT class is a collector (external check on the proxy), not HTTP-API - the other
	// UPS transport, sharing the same curated nut.* sensors.
	nc, ok := ClassByID("nut-collector")
	if !ok {
		t.Fatal("nut-collector class missing")
	}
	if nc.Pattern != PatternCollector || nc.Iface != IfaceAgent || len(nc.Templates) != 1 || nc.Icon != "battery" {
		t.Fatalf("unexpected nut-collector class: %+v", nc)
	}
	var ncUps bool
	for _, m := range nc.Macros {
		if m.Macro == "{$NUT.UPS}" && m.Required {
			ncUps = true
		}
	}
	if !ncUps {
		t.Error("nut-collector must require {$NUT.UPS}")
	}

	// The DNS server class is a collector (external-check resolver) usable on any DNS server.
	dns, ok := ClassByID("dns-server")
	if !ok {
		t.Fatal("dns-server class missing")
	}
	if dns.Pattern != PatternCollector || dns.Iface != IfaceAgent || len(dns.Templates) != 1 || dns.Templates[0] != "Argus DNS resolution" {
		t.Fatalf("unexpected dns-server class: %+v", dns)
	}

	// XCP-NG is a collector class (argus_xcpng.py against the pool master): root credentials with a
	// secret password, and the VM-monitoring mode is a fixed-choice macro (rendered as a select).
	xcp, ok := ClassByID("xcpng")
	if !ok {
		t.Fatal("xcpng class missing")
	}
	if xcp.Pattern != PatternCollector || xcp.Iface != IfaceAgent || len(xcp.Templates) != 1 || xcp.Templates[0] != "Argus XCP-NG by XAPI" || xcp.Icon != "vm" {
		t.Fatalf("unexpected xcpng class: %+v", xcp)
	}
	var xcpUser, xcpPass, xcpMode bool
	for _, m := range xcp.Macros {
		if m.Macro == "{$XCP.USER}" && m.Required {
			xcpUser = true
		}
		if m.Macro == "{$XCP.PASS}" && m.Required && m.Secret {
			xcpPass = true
		}
		if m.Macro == "{$XCP.VM.MODE}" && len(m.Options) == 3 && m.Options[0] == "off" {
			xcpMode = true
		}
	}
	if !xcpUser || !xcpPass || !xcpMode {
		t.Errorf("xcpng macro specs incomplete (user=%v secret-pass=%v mode-options=%v)", xcpUser, xcpPass, xcpMode)
	}
	if xcp.Setup == nil {
		t.Error("xcpng should carry pool-master setup guidance")
	}
}

func TestRegistry(t *testing.T) {
	if len(Classes()) == 0 {
		t.Fatal("empty registry")
	}
	base, ok := ClassByID("base")
	if !ok {
		t.Fatal("base class missing")
	}
	if base.Pattern != PatternBase || base.Iface != IfaceAgent || !base.OffersHTTP {
		t.Fatalf("unexpected base class: %+v", base)
	}
	// The Generic Linux SNMP class (C1) drives the SNMP interface branch of the create path.
	lx, ok := ClassByID("linux-snmp")
	if !ok {
		t.Fatal("linux-snmp class missing")
	}
	if lx.Pattern != PatternSNMP || lx.Iface != IfaceSNMP || len(lx.Templates) != 1 {
		t.Fatalf("unexpected linux-snmp class: %+v", lx)
	}
	if _, ok := ClassByID("does-not-exist"); ok {
		t.Fatal("unexpected class")
	}
	// Linux (SSH, agentless): the collector class that reuses the native Linux item keys, so curation
	// is shared. Offers both auth modes via the {$SSH.AUTH} select and a secret password macro.
	lssh, ok := ClassByID("linux-ssh")
	if !ok {
		t.Fatal("linux-ssh class missing")
	}
	if lssh.Pattern != PatternAgentless || lssh.Iface != IfaceAgent || len(lssh.Templates) != 1 || lssh.Templates[0] != "Argus Linux by SSH" {
		t.Fatalf("unexpected linux-ssh class: %+v", lssh)
	}
	var sshAuthOpts, sshPassSecret bool
	for _, m := range lssh.Macros {
		if m.Macro == "{$SSH.AUTH}" && len(m.Options) == 2 && m.Options[0] == "key" && m.Options[1] == "password" {
			sshAuthOpts = true
		}
		if m.Macro == "{$SSH.PASSWORD}" && m.Secret {
			sshPassSecret = true
		}
	}
	if !sshAuthOpts || !sshPassSecret {
		t.Errorf("linux-ssh macro specs incomplete (auth-options=%v secret-pass=%v)", sshAuthOpts, sshPassSecret)
	}
	if lssh.Setup == nil {
		t.Error("linux-ssh should carry SSH-access setup guidance")
	}
	// Windows SNMP reuses the Linux item keys through its own template.
	win, ok := ClassByID("windows-snmp")
	if !ok {
		t.Fatal("windows-snmp class missing")
	}
	if win.Pattern != PatternSNMP || win.Iface != IfaceSNMP || len(win.Templates) != 1 {
		t.Fatalf("unexpected windows-snmp class: %+v", win)
	}
	// unRAID stacks the unRAID extras on top of the Linux SNMP template (multi-template attach).
	ur, ok := ClassByID("unraid")
	if !ok {
		t.Fatal("unraid class missing")
	}
	if ur.Pattern != PatternSNMP || ur.Iface != IfaceSNMP || len(ur.Templates) != 2 || len(ur.Macros) != 0 {
		t.Fatalf("unexpected unraid class: %+v", ur)
	}
	// The class presets a host-level FS skip list (unRAID utility mounts + docker.img subvolumes).
	if len(ur.HostMacros) != 1 || ur.HostMacros[0].Macro != "{$FS.NAME.SKIP}" {
		t.Fatalf("unraid preset macros: %+v", ur.HostMacros)
	}
	for _, frag := range []string{"/mnt/addons", "/mnt/disks", "/mnt/remotes", "/mnt/rootshare", "/var/lib/memtester", "/var/lib/docker/"} {
		if !strings.Contains(ur.HostMacros[0].Value, frag) {
			t.Errorf("unraid FS skip missing %q", frag)
		}
	}
	// Ugreen is our first Zabbix-agent class: passive agent over the agent interface (:10050), no
	// credentials. It reuses the native agent item keys, so the create path treats it like any other
	// agent-interface class.
	ug, ok := ClassByID("ugreen")
	if !ok {
		t.Fatal("ugreen class missing")
	}
	if ug.Pattern != PatternAgent || ug.Iface != IfaceAgent || len(ug.Templates) != 1 || ug.Templates[0] != "Argus NAS by Zabbix agent" {
		t.Fatalf("unexpected ugreen class: %+v", ug)
	}
	if len(ug.Macros) != 0 {
		t.Fatalf("ugreen needs no credential macros: %+v", ug.Macros)
	}
}
