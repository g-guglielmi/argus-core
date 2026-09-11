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
		"Argus AdGuard Home by HTTP", "adguard.raw", "adguard.block_pct", "{$ADGUARD.URL}", "net.dns[{HOST.CONN}",
		"Argus Home Assistant by HTTP", "hass.raw", "hass.unavailable", "{$HASS.TOKEN}"} {
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
	if ag.Pattern != PatternHTTPAPI || ag.Iface != IfaceAgent || len(ag.Templates) != 1 || ag.Icon != "globe" {
		t.Fatalf("unexpected adguard class: %+v", ag)
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
}
