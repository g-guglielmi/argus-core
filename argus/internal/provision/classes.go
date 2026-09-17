// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package provision owns the device-class catalog (DESIGN §5) and the Zabbix templates that back it:
// the registry the attach UI/discovery choose from, and the startup reconcile that imports the
// hand-authored class templates into Zabbix. It is a leaf on the store + zabbix client, like notify.
package provision

// Templates every host / add-on references by their Zabbix technical name. Base Ping is attached to
// every provisioned host automatically; the HTTP endpoint is an optional add-on.
const (
	TemplateBasePing = "Argus Base Ping"
	TemplateHTTP     = "Argus HTTP Endpoint"
)

// Pattern is how a class is monitored (drives which collector/template style it uses). See DESIGN §5.
type Pattern string

const (
	PatternBase      Pattern = "base"          // Ping only
	PatternSNMP      Pattern = "snmp"          // SNMP template + sysObjectID fingerprint + IF-MIB LLD
	PatternHTTPAPI   Pattern = "http-api"      // Zabbix HTTP-agent items against a vendor REST API
	PatternAgentless Pattern = "agentless"     // server/proxy-run checks (net.dns, ssh.run)
	PatternVMware    Pattern = "native-vmware" // Zabbix's built-in VMware collector
	PatternCollector Pattern = "collector"     // sidecar / script item (XCP-NG XAPI, NUT upsd)
	PatternAgent     Pattern = "agent"         // Zabbix agent (passive) - a device that runs the agent but has no SNMP (Ugreen UGOS)
)

// IfaceKind is the network interface a class's host needs in Zabbix.
type IfaceKind string

const (
	IfaceNone  IfaceKind = ""      // no interface (endpoint-source classes register elsewhere)
	IfaceAgent IfaceKind = "agent" // agent / simple checks (ping) — Zabbix type 1, default port 10050
	IfaceSNMP  IfaceKind = "snmp"  // SNMP — Zabbix type 2, default port 161
)

// PresetMacro is a host macro a class sets silently at creation - class-tuned defaults such as
// unRAID's filesystem skip list. Host macros override template macros deterministically (unlike a
// macro defined on two linked templates); a user-supplied macro of the same name wins over a preset.
type PresetMacro struct {
	Macro string `json:"-"`
	Value string `json:"-"`
}

// MacroSpec is a per-host macro the attach UI asks for when creating a host of this class - how
// API-source classes (UniFi, later Nutanix/XCP-NG/…) receive their endpoint and credentials.
type MacroSpec struct {
	Macro    string `json:"macro"`             // Zabbix macro name, e.g. "{$UNIFI.URL}"
	Label    string `json:"label"`             // form label
	Hint     string `json:"hint"`              // placeholder / example
	Required bool   `json:"required"`          // creation fails without it
	Secret   bool   `json:"secret"`            // stored as a Zabbix secret macro (write-only afterwards)
	Derive   string `json:"derive,omitempty"` // form auto-fills this from the host address ("{host}" -> the IP/DNS), overridable; e.g. "http://{host}". Only for host-addressed URLs, never a controller URL (UniFi) that differs from the device.
}

// ClassSetup is optional prerequisite guidance the Add-device form shows for a class that needs
// something set up ON the device first - e.g. the Ugreen agent class needs a Zabbix agent 2
// container running on the NAS before the proxy can poll it. Purely informational: the create call
// ignores it. In Command, "{host}" is replaced with the device name the user typed.
type ClassSetup struct {
	Title   string   `json:"title"`
	Intro   string   `json:"intro,omitempty"`
	Steps   []string `json:"steps,omitempty"`   // ordered prose steps
	Command string   `json:"command,omitempty"` // a copyable shell snippet ({host} -> the device name)
	Note    string   `json:"note,omitempty"`
}

// Class is a device class in the registry: the metadata that drives provisioning (which Zabbix
// templates to attach and what interface the host needs) and, later, discovery + the management UI.
type Class struct {
	ID         string      `json:"id"`         // stable Argus id, e.g. "linux-snmp"
	Label      string      `json:"label"`      // human name, e.g. "Generic Linux (SNMP)"
	Family     string      `json:"family"`     // UI grouping, e.g. "Linux", "UniFi", "Base"
	Pattern    Pattern     `json:"pattern"`    //
	Iface      IfaceKind   `json:"iface"`      // interface the host needs (agent/snmp/none)
	Templates  []string    `json:"templates"`  // Zabbix templates to attach (Base Ping is added on top)
	OffersHTTP bool        `json:"offers_http"` // the HTTP/HTTPS add-on may be attached to this class
	Icon       string      `json:"icon"`       // tree glyph name (server, switch, shield, …); see web devIcon map
	Macros     []MacroSpec `json:"macros,omitempty"` // per-host macros the attach UI collects
	HostMacros []PresetMacro `json:"-"`        // macros set silently on every host of this class
	Setup      *ClassSetup `json:"setup,omitempty"` // prerequisite steps shown in the attach form (agent classes)
}

// registry is the catalog. C0 shipped the universal "base" class (Ping only); C1 adds Generic Linux
// (SNMP) and UniFi Switch; C2 the rest (DESIGN §5). Adding a class here means shipping its template
// under templates/ and re-importing (the startup reconcile handles that when the file set changes).
var registry = []Class{
	{
		ID:         "base",
		Label:      "Ping only",
		Family:     "Base",
		Pattern:    PatternBase,
		Iface:      IfaceAgent, // ICMP simple checks resolve the target from the host's interface
		Templates:  nil,        // Base Ping is attached to every host automatically
		OffersHTTP: true,
		Icon:       "device",
	},
	{
		ID:         "linux-snmp",
		Label:      "Generic Linux (SNMP)",
		Family:     "Linux",
		Pattern:    PatternSNMP,
		Iface:      IfaceSNMP,
		Templates:  []string{"Argus Linux by SNMP"},
		OffersHTTP: true,
		Icon:       "server",
	},
	{
		// Windows Server over SNMP: HOST-RESOURCES (CPU/memory/disk) + IF-MIB + the LAN Manager
		// service table. Same item keys as Generic Linux SNMP, so curation is shared. SNMP creds
		// inherit the proxy default like every SNMP class.
		ID:         "windows-snmp",
		Label:      "Windows (SNMP)",
		Family:     "Windows",
		Pattern:    PatternSNMP,
		Iface:      IfaceSNMP,
		Templates:  []string{"Argus Windows by SNMP"},
		OffersHTTP: true,
		Icon:       "server",
	},
	{
		// unRAID = the Linux SNMP template plus the extend scripts from the Community Applications
		// "SNMP" plugin (per-disk temperatures, per-share free space). SNMP creds inherit the proxy
		// default like every SNMP class.
		ID:         "unraid",
		Label:      "unRAID (SNMP)",
		Family:     "NAS",
		Pattern:    PatternSNMP,
		Iface:      IfaceSNMP,
		Templates:  []string{"Argus Linux by SNMP", "Argus unRAID by SNMP"},
		OffersHTTP: true,
		Icon:       "nas",
		HostMacros: []PresetMacro{{
			// The Linux default plus unRAID's utility mount roots (addons/disks/remotes/rootshare,
			// memtester) and the children of /var/lib/docker (the btrfs/overlay subvolume mirrors
			// /var/lib/docker itself, which stays - that's the docker.img usage).
			Macro: "{$FS.NAME.SKIP}",
			Value: `^(/run|/dev|/sys|/proc|/mnt/addons|/mnt/disks|/mnt/remotes|/mnt/rootshare|/var/lib/memtester|/var/lib/docker/[^/]+)($|/)`,
		}},
	},
	{
		// Ugreen UGOS exposes no SNMP, so this is our first Zabbix-agent class: the agent runs in a
		// container ON the NAS (host network, /proc + /sys + / mounts, smartmontools for SMART) and the
		// site proxy polls it passively on :10050 - the agent interface IfaceAgent already provisions.
		// CPU/memory/filesystem/interface reuse the native agent item keys, so curation is shared with
		// the Linux/Windows SNMP classes; the SMART master adds per-disk temperatures. No credentials -
		// the agent's Server= IP allow-list is the access control (add a PSK to encrypt the link).
		ID:         "ugreen",
		Label:      "Ugreen (Zabbix agent)",
		Family:     "NAS",
		Pattern:    PatternAgent,
		Iface:      IfaceAgent,
		Templates:  []string{"Argus NAS by Zabbix agent"},
		OffersHTTP: true,
		Icon:       "nas",
		// The data-volume filter lives as the template's {$FS.NAME.MATCHES} default (not a host preset)
		// so it also reaches hosts added before it existed, on the next template re-import.
		Setup: &ClassSetup{
			Title: "Set up the Zabbix agent on the NAS",
			Intro: "UGOS has no SNMP, so this device is monitored by a Zabbix agent 2 container running on the NAS itself. Once it's up, the proxy (or core) that monitors this device polls the agent on port 10050 - nothing has to reach back out.",
			Steps: []string{
				"On the NAS, open a shell (the container needs host networking, so run it from a shell or a compose/run config, not the simple Docker-app form).",
				"Run the block below. Replace <PROXY-IP> with the IP of the proxy (or core) shown in \"Monitored by\" above - that address is the only one allowed to poll the agent.",
				"Mount each data volume you want disk-usage for (the -v /volume1:/volume1:ro line; add /volume2, ... if you have more). Only the data volumes are shown; the container's own filesystems are filtered out.",
				"The first lines write a small config with two readings - CPU temperature, and per-disk SMART temperature read with 'smartctl -n standby' so a spun-down disk is NOT woken (it just holds its last reading). The -v that mounts the config and --privileged --user root (for SMART) are already in the command; everything - CPU, memory, disks, temps - comes up together.",
			},
			Command: "mkdir -p /volume1/docker/argus-agent\n" +
				"cat > /volume1/docker/argus-agent/nas-agent.conf <<'EOF'\n" +
				"UserParameter=ugreen.cpu.temp,for h in /sys/class/hwmon/hwmon*; do case \"$(cat \"$h/name\" 2>/dev/null)\" in coretemp|k10temp) cat \"$h/temp1_input\"; exit 0;; esac; done; cat /sys/class/thermal/thermal_zone*/temp 2>/dev/null | sort -rn | head -1\n" +
				"UserParameter=ugreen.disk.temp[*],case \"$1\" in *nvme*) n= ;; *) n=\"-n standby\" ;; esac; o=$(smartctl $n -a -jc \"$1\" 2>/dev/null); t=$(printf '%s' \"$o\" | grep -oE '\"temperature\":\\{[^}]*\"current\":[0-9]+' | grep -oE '[0-9]+$'); if [ -n \"$t\" ]; then echo \"$t\"; elif [ -n \"$n\" ] && printf '%s' \"$o\" | grep -qiE 'in (STANDBY|SLEEP) mode'; then echo 20; fi\n" +
				"EOF\n" +
				"docker run -d --name argus-agent --restart unless-stopped \\\n" +
				"  --network host --pid host --privileged --user root \\\n" +
				"  -e ZBX_SERVER_HOST=\"<PROXY-IP>\" \\\n" +
				"  -e ZBX_HOSTNAME=\"{host}\" \\\n" +
				"  -v /volume1:/volume1:ro -v /proc:/proc:ro -v /sys:/sys:ro \\\n" +
				"  -v /volume1/docker/argus-agent/nas-agent.conf:/etc/zabbix/zabbix_agent2.d/plugins.d/nas-agent.conf:ro \\\n" +
				"  zabbix/zabbix-agent2:alpine-7.0-latest",
			Note: "The link is unencrypted; the ZBX_SERVER_HOST allow-list is what restricts polling to your proxy. To encrypt it, add a PSK on the agent and the matching TLS fields on the host - see deploy/README.md.",
		},
	},
	{
		// The host's own interface is the switch's IP (Base Ping runs against it); the metrics come
		// from the UniFi controller through the Integration API, addressed by the macros below - so
		// one template covers a cloud gateway (:443) and a self-hosted console on a custom port.
		ID:         "unifi-switch",
		Label:      "UniFi Switch",
		Family:     "UniFi",
		Pattern:    PatternHTTPAPI,
		Iface:      IfaceAgent,
		Templates:  []string{"Argus UniFi Switch by HTTP"},
		OffersHTTP: false,
		Icon:       "switch",
		Macros: []MacroSpec{
			{Macro: "{$UNIFI.URL}", Label: "Controller URL", Hint: "https://unifi.example.lan:11443", Required: true},
			{Macro: "{$UNIFI.KEY}", Label: "API key", Hint: "UniFi Network → Settings → Control Plane → Integrations", Required: true, Secret: true},
			{Macro: "{$UNIFI.MAC}", Label: "Switch MAC", Hint: "aa:bb:cc:dd:ee:ff", Required: true},
			{Macro: "{$UNIFI.SITE}", Label: "Site name", Hint: "default"},
		},
	},
	{
		// Same access pattern as the switch. On a UniFi OS gateway the controller usually runs on
		// the gateway itself, so {$UNIFI.URL} often points back at this same device.
		ID:         "unifi-gateway",
		Label:      "UniFi Gateway",
		Family:     "UniFi",
		Pattern:    PatternHTTPAPI,
		Iface:      IfaceAgent,
		Templates:  []string{"Argus UniFi Gateway by HTTP"},
		OffersHTTP: false,
		Icon:       "router",
		Macros: []MacroSpec{
			{Macro: "{$UNIFI.URL}", Label: "Controller URL", Hint: "https://unifi.example.lan:11443", Required: true},
			{Macro: "{$UNIFI.KEY}", Label: "API key", Hint: "UniFi Network → Settings → Control Plane → Integrations", Required: true, Secret: true},
			{Macro: "{$UNIFI.MAC}", Label: "Gateway MAC", Hint: "aa:bb:cc:dd:ee:ff", Required: true},
			{Macro: "{$UNIFI.SITE}", Label: "Site name", Hint: "default"},
		},
	},
	{
		ID:         "unifi-ap",
		Label:      "UniFi Access Point",
		Family:     "UniFi",
		Pattern:    PatternHTTPAPI,
		Iface:      IfaceAgent,
		Templates:  []string{"Argus UniFi AP by HTTP"},
		OffersHTTP: false,
		Icon:       "wifi",
		Macros: []MacroSpec{
			{Macro: "{$UNIFI.URL}", Label: "Controller URL", Hint: "https://unifi.example.lan:11443", Required: true},
			{Macro: "{$UNIFI.KEY}", Label: "API key", Hint: "UniFi Network → Settings → Control Plane → Integrations", Required: true, Secret: true},
			{Macro: "{$UNIFI.MAC}", Label: "Access point MAC", Hint: "aa:bb:cc:dd:ee:ff", Required: true},
			{Macro: "{$UNIFI.SITE}", Label: "Site name", Hint: "default"},
		},
	},
	{
		// The console host itself (Cloud Key / UNVR / a console-capable gateway) - its own health
		// plus internal storage. A gateway that also hosts the console is better served by the
		// UniFi Gateway class (WAN + ports); this is for a dedicated console.
		ID:         "unifi-console",
		Label:      "UniFi OS Console",
		Family:     "UniFi",
		Pattern:    PatternHTTPAPI,
		Iface:      IfaceAgent,
		Templates:  []string{"Argus UniFi OS Console by HTTP"},
		OffersHTTP: false,
		Icon:       "cloud",
		Macros: []MacroSpec{
			{Macro: "{$UNIFI.URL}", Label: "Controller URL", Hint: "https://unifi.example.lan:11443", Required: true},
			{Macro: "{$UNIFI.KEY}", Label: "API key", Hint: "UniFi Network → Settings → Control Plane → Integrations", Required: true, Secret: true},
			{Macro: "{$UNIFI.MAC}", Label: "Console MAC", Hint: "aa:bb:cc:dd:ee:ff", Required: true},
			{Macro: "{$UNIFI.SITE}", Label: "Site name", Hint: "default"},
		},
	},
	{
		// AdGuard Home polled from its admin API (DNS filtering stats + protection + version) with a
		// proxy-run net.dns resolve check alongside. The host's own interface is the AdGuard box's IP.
		ID:         "adguard",
		Label:      "AdGuard Home",
		Family:     "DNS",
		Pattern:    PatternHTTPAPI,
		Iface:      IfaceAgent,
		// The admin-API stats plus the shared DNS-resolution add-on (a real per-name resolve check
		// against this server), so AdGuard reads like any other DNS server on top of its own stats.
		Templates:  []string{"Argus AdGuard Home by HTTP", "Argus DNS resolution"},
		OffersHTTP: false,
		Icon:       "globe",
		Macros: []MacroSpec{
			{Macro: "{$ADGUARD.URL}", Label: "Admin URL", Hint: "http://adguard.example.lan:3000", Required: true, Derive: "http://{host}"},
			{Macro: "{$ADGUARD.USER}", Label: "Admin username", Hint: "blank if the admin UI has no login"},
			{Macro: "{$ADGUARD.PASSWORD}", Label: "Admin password", Hint: "blank if the admin UI has no login", Secret: true},
			{Macro: "{$DNS.RESOLVE.NAMES}", Label: "Names to resolve", Hint: "comma-separated, e.g. example.com,cloudflare.com"},
		},
	},
	{
		// Home Assistant polled from its REST API with a long-lived access token: platform health
		// (API up, version, integration/entity counts, unavailable entities) - not the entities
		// themselves. The host's own interface is the Home Assistant box's IP (Base Ping).
		ID:         "home-assistant",
		Label:      "Home Assistant",
		Family:     "Home",
		Pattern:    PatternHTTPAPI,
		Iface:      IfaceAgent,
		Templates:  []string{"Argus Home Assistant by HTTP"},
		OffersHTTP: false,
		Icon:       "home",
		Macros: []MacroSpec{
			{Macro: "{$HASS.URL}", Label: "Base URL", Hint: "http://homeassistant.example.lan:8123", Required: true, Derive: "http://{host}:8123"},
			{Macro: "{$HASS.TOKEN}", Label: "Long-lived access token", Hint: "Home Assistant → Profile → Security", Required: true, Secret: true},
		},
	},
	{
		// Any DNS server (Pi-hole, Microsoft DNS, Ubiquiti, Sophos, a plain resolver, or AdGuard) -
		// the proxy/core runs dns-resolver.py to actually resolve names against it. Reusable add-on:
		// AdGuard can attach this template too for a real resolve check on top of its API stats.
		ID:         "dns-server",
		Label:      "DNS server",
		Family:     "DNS",
		Pattern:    PatternCollector,
		Iface:      IfaceAgent,
		Templates:  []string{"Argus DNS resolution"},
		OffersHTTP: false,
		Icon:       "globe",
		Macros: []MacroSpec{
			{Macro: "{$DNS.RESOLVE.NAMES}", Label: "Names to resolve", Hint: "comma-separated, e.g. example.com,cloudflare.com"},
		},
	},
	{
		// A UPS monitored through NUT via PeaNUT (a container that fronts upsd over HTTP), so it's a
		// plain HTTP-API class - no proxy collector needed. The host is the PeaNUT box; {$PEANUT.URL}
		// defaults to its own IP on :8080.
		ID:         "nut-ups",
		Label:      "UPS (NUT via PeaNUT)",
		Family:     "Power",
		Pattern:    PatternHTTPAPI,
		Iface:      IfaceAgent,
		Templates:  []string{"Argus UPS by PeaNUT"},
		OffersHTTP: false,
		Icon:       "battery",
		Macros: []MacroSpec{
			{Macro: "{$PEANUT.URL}", Label: "PeaNUT URL", Hint: "http://peanut.example.lan:8080", Required: true, Derive: "http://{host}:8080"},
			{Macro: "{$PEANUT.UPS}", Label: "UPS name", Hint: "ups (see PeaNUT /api/v1/devices)", Required: true},
			{Macro: "{$PEANUT.USER}", Label: "PeaNUT username", Hint: "blank if PeaNUT runs with AUTH_DISABLED"},
			{Macro: "{$PEANUT.PASSWORD}", Label: "PeaNUT password", Hint: "blank if PeaNUT runs with AUTH_DISABLED", Secret: true},
		},
	},
	{
		// The direct NUT path: the proxy's argus_nut.py collector (an external check baked into the
		// probe image) speaks the NUT protocol to upsd - no extra app. Same curated sensors as the
		// PeaNUT class; adding a UPS is just this device + its macros. The host is the NUT server's IP.
		ID:         "nut-collector",
		Label:      "UPS (NUT)",
		Family:     "Power",
		Pattern:    PatternCollector,
		Iface:      IfaceAgent,
		Templates:  []string{"Argus UPS by NUT"},
		OffersHTTP: false,
		Icon:       "battery",
		Macros: []MacroSpec{
			{Macro: "{$NUT.UPS}", Label: "UPS name", Hint: "ups (see: upsc -l <host>)", Required: true},
			{Macro: "{$NUT.PORT}", Label: "upsd port", Hint: "3493"},
			{Macro: "{$NUT.USER}", Label: "upsd username", Hint: "only if upsd needs a login to read"},
			{Macro: "{$NUT.PASSWORD}", Label: "upsd password", Hint: "only if upsd needs a login to read", Secret: true},
		},
	},
}

// Classes returns the device-class catalog (stable order).
func Classes() []Class { return registry }

// ClassByID returns the class with this id, or (zero, false).
func ClassByID(id string) (Class, bool) {
	for _, c := range registry {
		if c.ID == id {
			return c, true
		}
	}
	return Class{}, false
}
