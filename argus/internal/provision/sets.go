// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package provision

import (
	"fmt"
	"slices"
)

// Set is one block of sensors Argus can read from a host (DESIGN 5b): one Zabbix template plus the
// settings its form collects. A host carries any number of them, within the rules below, and its
// linked templates are the record of which. A device class is what the device is (its name and icon
// in the tree) and the sets a device of that kind usually has; nothing ties a host to them, so an
// AdGuard box that is also a Linux server carries AdGuard Home, DNS resolution and Linux by SNMP.
type Set struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Group       string `json:"group"`    // the catalog heading it sits under (SetGroups)
	Template    string `json:"template"` // the Zabbix template it links
	Description string `json:"description"`
	// Iface is the interface its checks need on the host: an SNMP one for an SNMP set, the agent's
	// for an agent set. Blank = whichever the host has (checks the probe runs against its address).
	Iface      IfaceKind     `json:"iface,omitempty"`
	Macros     []MacroSpec   `json:"macros,omitempty"` // the settings its form collects
	HostMacros []PresetMacro `json:"-"`                // macros set silently with it
	Setup      *ClassSetup   `json:"setup,omitempty"`  // what to prepare on the device first
	// Slot: a host carries one set of a slot. Two sets share a slot when they read the same sensors
	// another way (two system sets both define the interface and filesystem sensors), which Zabbix
	// refuses on one host.
	Slot string `json:"slot,omitempty"`
	// Needs lists sets that must be on the host too (unRAID reads on top of Linux by SNMP).
	Needs []string `json:"needs,omitempty"`
	// Classes limits the set to hosts of these classes (the probe's own: a speed test, the cloud
	// services seen from the site). Empty = any host but Argus's own.
	Classes []string `json:"classes,omitempty"`
	// Prefill: its form shows each setting's value in force (the template's default when the host
	// has none) and saves it as the host's own. The others show a blank field for "the default".
	Prefill bool `json:"prefill,omitempty"`
	// Zabbix runs a new item within 60 seconds of its creation, so a set that runs every few hours
	// (the speed test) still shows its first reading in a minute: Argus doesn't run it again.
}

// SetGroup is a heading of the set catalog.
type SetGroup struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
}

// Set slots, and what each means to a reader.
const (
	SlotSystem = "system"
	SlotUPS    = "ups"
)

var slotNotes = map[string]string{
	SlotSystem: "one system per host",
	SlotUPS:    "one UPS source per host",
}

// SlotNote is a slot's rule in plain words ("one system per host").
func SlotNote(slot string) string { return slotNotes[slot] }

var setGroups = []SetGroup{
	{ID: "system", Label: "System", Note: "one per host"},
	{ID: "app", Label: "Applications and devices"},
	{ID: "check", Label: "Checks"},
	{ID: "probe", Label: "From this probe"},
}

// SetGroups returns the catalog's headings, in order.
func SetGroups() []SetGroup { return setGroups }

// ooklaTerms are what choosing Ookla's speed test means: Ookla's own program under Ookla's terms,
// which Argus doesn't ship; the probe downloads it from Ookla once they are accepted.
var ooklaTerms = Terms{
	Value: "ookla",
	Title: "Ookla's terms",
	Text: "Ookla's speed test runs Ookla's own program, the Speedtest CLI, under Ookla's terms: its licence is for personal, non-commercial use, and Ookla keeps the results under its privacy policy. " +
		"Argus doesn't ship it: once you accept, this site's probe downloads it from Ookla (version 1.2.0, checked against its checksum) and runs it with Ookla's terms accepted. " +
		"Whether your use fits Ookla's terms is yours to judge.",
	Links: []TermLink{
		{Label: "Licence (EULA)", URL: "https://www.speedtest.net/about/eula"},
		{Label: "Terms of use", URL: "https://www.speedtest.net/about/terms"},
		{Label: "Privacy policy", URL: "https://www.speedtest.net/about/privacy"},
	},
}

// Template names of the probe's own sets.
const (
	TemplateSpeedtest = "Argus Speedtest"
	TemplateSaaS      = "Argus Common SaaS"
)

// unifiMacros are the settings every UniFi device set collects: its controller and its own MAC.
func unifiMacros(macLabel string) []MacroSpec {
	return []MacroSpec{
		{Macro: "{$UNIFI.URL}", Label: "Controller URL", Hint: "https://unifi.example.lan:11443", Required: true},
		{Macro: "{$UNIFI.KEY}", Label: "API key", Hint: "UniFi Network → Settings → Control Plane → Integrations", Required: true, Secret: true},
		{Macro: "{$UNIFI.MAC}", Label: macLabel, Hint: "aa:bb:cc:dd:ee:ff", Required: true, Pattern: patternMAC},
		{Macro: "{$UNIFI.SITE}", Label: "Site name", Hint: "default"},
	}
}

// sets is the catalog, in the order the "Add sensors" list shows it. Adding a set means shipping its
// template under templates/; TestCombinableSetsShareNothing then says whether it needs a slot.
//
// The system slot holds every set that reads a machine's own CPU, memory, disks or interfaces: the
// Linux, Windows and NAS sets define the same interface and filesystem sensors, and a UniFi device
// reads its CPU and memory against the same threshold macros.
var sets = []Set{
	{
		ID:          "linux-snmp",
		Label:       "Linux by SNMP",
		Group:       "system",
		Template:    "Argus Linux by SNMP",
		Description: "CPU, memory, filesystems and network interfaces, read over SNMP with the host's SNMP credentials (its probe's default, unless the host has its own).",
		Iface:       IfaceSNMP,
		Slot:        SlotSystem,
	},
	{
		// The agentless, no-SNMP way into a Linux box: the proxy/core runs argus_linux_ssh.py (an
		// external check) which opens ONE SSH session per poll, reads /proc + df + /proc/net/dev and
		// emits JSON. Item keys match the native agent/SNMP keys, so curation is shared with the other
		// system sets. Auth is a private key on the proxy (recommended) or a password secret macro;
		// either way a read-only login is enough.
		ID:          "linux-ssh",
		Label:       "Linux by SSH",
		Group:       "system",
		Template:    "Argus Linux by SSH",
		Description: "CPU, memory, filesystems and network interfaces without an agent or SNMP, plus the systemd services and containers you list: the probe logs in over SSH once a minute.",
		Slot:        SlotSystem,
		Macros: []MacroSpec{
			{Macro: "{$SSH.USER}", Label: "SSH user", Hint: "root (a read-only login is enough)", Pattern: patternLogin},
			{Macro: "{$SSH.PORT}", Label: "SSH port", Hint: "22", Pattern: patternPort},
			{Macro: "{$SSH.AUTH}", Label: "Authentication", Hint: "key", Options: []string{"key", "password"}},
			{Macro: "{$SSH.PASSWORD}", Label: "SSH password", Hint: "only for password auth", Secret: true},
			{Macro: "{$SSH.KEYFILE}", Label: "Private key path (on the proxy)", Hint: "/var/lib/zabbix/ssh/argus_id", Pattern: patternKey},
			{Macro: "{$SSH.UNITS}", Label: "Services to watch (systemd units)", Hint: "nginx, jellyfin, docker", Pattern: patternUnits},
			{Macro: "{$SSH.CONTAINERS}", Label: "Containers to watch (regex)", Hint: "^(jellyfin|immich.*)$ - needs docker ps rights", Pattern: patternRegexLine},
		},
		Setup: &ClassSetup{
			Title: "Give the proxy read-only SSH access",
			Intro: "This class has no agent and needs no SNMP - the proxy (or core) that monitors this device logs in over SSH once per minute and reads /proc, df and /proc/net/dev. A read-only account is all it needs.",
			Steps: []string{
				"Pick an SSH login on the target (root works; a plain read-only user is enough - the collector never writes).",
				"Key auth (recommended): generate a monitoring keypair, add the public key to the target's ~/.ssh/authorized_keys, and place the private key ON the proxy at the path in \"Private key path\" (default /var/lib/zabbix/ssh/argus_id) - mount it into the argus-probe container there. Set Authentication to \"key\".",
				"Password auth: set Authentication to \"password\" and fill in the SSH password (stored as a secret). The proxy image ships sshpass for this.",
				"The first connection is trust-on-first-use (accept-new); later host-key changes are rejected.",
				"Optional: list the systemd units to watch (no extra rights needed) and a name pattern for the Docker containers to watch. Containers need the login to run docker ps, which on a standard install means the docker group: root-equivalent on that host, so decide per host.",
			},
			Note: "The same key/credential is reused for every host on a given proxy, so one monitoring key per site is enough. To lock it down further, restrict the key in authorized_keys (from=, no-pty).",
		},
	},
	{
		// Windows Server over SNMP: HOST-RESOURCES (CPU/memory/disk) + IF-MIB + the LAN Manager
		// service table. Same item keys as Linux by SNMP, so curation is shared.
		ID:          "windows-snmp",
		Label:       "Windows by SNMP",
		Group:       "system",
		Template:    "Argus Windows by SNMP",
		Description: "CPU, memory, disks and network interfaces of a Windows machine over SNMP, plus the Windows services you name.",
		Iface:       IfaceSNMP,
		Slot:        SlotSystem,
		Macros: []MacroSpec{{
			// Opt-in Windows service monitoring: a regex of service display names (LAN Manager
			// svSvcTable). Empty = monitor no services. Only running services appear in the table,
			// so the "Service not running" trigger fires when a matched one stops or drops out.
			Macro: "{$WIN.SERVICE.MATCHES}",
			Label: "Monitored services (regex)",
			Hint:  "DNS Server|Print Spooler|SQL Server .*",
		}},
	},
	{
		// Ugreen UGOS exposes no SNMP, so this is the Zabbix-agent set: the agent runs in a container
		// ON the NAS (host network, /proc + /sys + / mounts, smartmontools for SMART) and the site
		// proxy polls it passively on :10050. CPU/memory/filesystem/interface reuse the native agent
		// item keys, so curation is shared with the other system sets; the SMART master adds per-disk
		// temperatures. No credentials: the agent's Server= IP allow-list is the access control.
		// The data-volume filter lives as the template's {$FS.NAME.MATCHES} default (not a host
		// preset) so it also reaches hosts added before it existed, on the next template re-import.
		ID:          "nas-agent",
		Label:       "NAS by Zabbix agent",
		Group:       "system",
		Template:    "Argus NAS by Zabbix agent",
		Description: "A NAS without SNMP (Ugreen UGOS): CPU, memory, data volumes, interfaces and per-disk temperatures, from a Zabbix agent container on the NAS.",
		Iface:       IfaceAgent,
		Slot:        SlotSystem,
		Setup: &ClassSetup{
			Title: "Set up the Zabbix agent on the NAS",
			Intro: "UGOS has no SNMP, so this device is monitored by a Zabbix agent 2 container running on the NAS itself. Once it's up, the proxy (or core) that monitors this device polls the agent on port 10050 - nothing has to reach back out.",
			Steps: []string{
				"On the NAS, open a shell (the container needs host networking, so run it from a shell or a compose/run config, not the simple Docker-app form).",
				"Run the block below. Replace <PROXY-IP> with the IP of the proxy (or core) shown in \"Monitored by\" above - that address is the only one allowed to poll the agent.",
				"Mount each data volume you want disk-usage for (the -v /volume1:/volume1:ro line; add /volume2, ... if you have more). Only the data volumes are shown; the container's own filesystems are filtered out.",
				"The first lines write a small config with three readings - CPU temperature; a disk list built purely from /sys (device name, model and HDD/SSD/NVMe type), so discovery never opens a disk; and per-disk SMART temperature read with 'smartctl -n standby' so a spun-down disk is NOT woken (it just holds its last reading). The -v that mounts the config and --privileged --user root (for SMART) are already in the command; everything - CPU, memory, disks, temps - comes up together.",
			},
			Command: "mkdir -p /volume1/docker/argus-agent\n" +
				"cat > /volume1/docker/argus-agent/nas-agent.conf <<'EOF'\n" +
				"UserParameter=ugreen.cpu.temp,for h in /sys/class/hwmon/hwmon*; do case \"$(cat \"$h/name\" 2>/dev/null)\" in coretemp|k10temp) cat \"$h/temp1_input\"; exit 0;; esac; done; cat /sys/class/thermal/thermal_zone*/temp 2>/dev/null | sort -rn | head -1\n" +
				"UserParameter=ugreen.disk.temp[*],case \"$1\" in *nvme*) n= ;; *) n=\"-n standby\" ;; esac; o=$(smartctl $n -a -jc \"$1\" 2>/dev/null); t=$(printf '%s' \"$o\" | grep -oE '\"current\": *[0-9]+' | head -1 | grep -oE '[0-9]+'); if [ -n \"$t\" ]; then echo \"$t\"; elif [ -n \"$n\" ] && printf '%s' \"$o\" | grep -qiE 'standby|sleep'; then echo 20; fi\n" +
				"UserParameter=ugreen.disk.discovery,emit(){ printf '%s{\"{#PATH}\":\"/dev/%s\",\"{#NAME}\":\"%s\",\"{#DISKTYPE}\":\"%s\",\"{#MODEL}\":\"%s\"}' \"$c\" \"$1\" \"$1\" \"$2\" \"$3\"; c=,; }; { printf '['; c=; for x in /sys/class/nvme/nvme*; do [ -e \"$x\" ] || continue; n=${x##*/}; m=$(sed 's/\"//g;s/^ *//;s/ *$//' \"$x/model\" 2>/dev/null); emit \"$n\" nvme \"$m\"; done; for d in /sys/block/sd*; do [ -e \"$d\" ] || continue; n=${d##*/}; [ \"$(cat \"$d/queue/rotational\" 2>/dev/null)\" = 1 ] && t=hdd || t=ssd; m=$(sed 's/\"//g;s/^ *//;s/ *$//' \"$d/device/model\" 2>/dev/null); emit \"$n\" \"$t\" \"$m\"; done; printf ']'; }\n" +
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
		// The metrics come from the UniFi controller through the Integration API, addressed by the
		// macros below, so one template covers a cloud gateway (:443) and a self-hosted console on a
		// custom port. The host's own address is the device's (Base Ping runs against it).
		ID:          "unifi-switch",
		Label:       "UniFi Switch",
		Group:       "system",
		Template:    "Argus UniFi Switch by HTTP",
		Description: "A UniFi switch as its controller sees it: state, CPU, memory, firmware, uplink and every port.",
		Slot:        SlotSystem,
		Macros:      unifiMacros("Switch MAC"),
	},
	{
		// On a UniFi OS gateway the controller usually runs on the gateway itself, so {$UNIFI.URL}
		// often points back at this same device.
		ID:          "unifi-gateway",
		Label:       "UniFi Gateway",
		Group:       "system",
		Template:    "Argus UniFi Gateway by HTTP",
		Description: "A UniFi gateway as its controller sees it: state, CPU, memory, firmware, WAN and every port.",
		Slot:        SlotSystem,
		Macros:      unifiMacros("Gateway MAC"),
	},
	{
		ID:          "unifi-ap",
		Label:       "UniFi Access Point",
		Group:       "system",
		Template:    "Argus UniFi AP by HTTP",
		Description: "A UniFi access point as its controller sees it: state, CPU, memory, firmware, clients and radios.",
		Slot:        SlotSystem,
		Macros:      unifiMacros("Access point MAC"),
	},
	{
		// The console host itself (Cloud Key / UNVR / a console-capable gateway): its own health plus
		// internal storage. A gateway that also hosts the console is better served by UniFi Gateway
		// (WAN + ports); this is for a dedicated console.
		ID:          "unifi-console",
		Label:       "UniFi OS Console",
		Group:       "system",
		Template:    "Argus UniFi OS Console by HTTP",
		Description: "A UniFi OS console (Cloud Key, UNVR): state, CPU, memory, firmware and its internal storage.",
		Slot:        SlotSystem,
		Macros:      unifiMacros("Console MAC"),
	},
	{
		// AdGuard Home polled from its admin API (DNS filtering stats + protection + version).
		ID:          "adguard",
		Label:       "AdGuard Home",
		Group:       "app",
		Template:    "Argus AdGuard Home by HTTP",
		Description: "AdGuard Home from its admin API: queries, the blocked share, whether protection is on, its version.",
		Macros: []MacroSpec{
			{Macro: "{$ADGUARD.URL}", Label: "Admin URL", Hint: "http://adguard.example.lan:3000", Required: true, Derive: "http://{host}"},
			{Macro: "{$ADGUARD.USER}", Label: "Admin username", Hint: "blank if the admin UI has no login"},
			{Macro: "{$ADGUARD.PASSWORD}", Label: "Admin password", Hint: "blank if the admin UI has no login", Secret: true},
		},
	},
	{
		// Home Assistant polled from its REST API with a long-lived access token: platform health
		// (API up, version, integration/entity counts, unavailable entities), not the entities.
		ID:          "home-assistant",
		Label:       "Home Assistant",
		Group:       "app",
		Template:    "Argus Home Assistant by HTTP",
		Description: "Home Assistant's own health from its API: whether it answers, its version, integrations and entities, and how many entities are unavailable.",
		Macros: []MacroSpec{
			{Macro: "{$HASS.URL}", Label: "Base URL", Hint: "http://homeassistant.example.lan:8123", Required: true, Derive: "http://{host}:8123"},
			{Macro: "{$HASS.TOKEN}", Label: "Long-lived access token", Hint: "Home Assistant → Profile → Security", Required: true, Secret: true},
		},
	},
	{
		// The extend scripts of the Community Applications "SNMP" plugin (per-disk temperatures,
		// per-share free space), on top of Linux by SNMP.
		ID:          "unraid",
		Label:       "unRAID",
		Group:       "app",
		Template:    "Argus unRAID by SNMP",
		Description: "unRAID's disks and shares on top of Linux by SNMP: per-disk temperatures and per-share free space, from the SNMP plugin of Community Applications.",
		Iface:       IfaceSNMP,
		Needs:       []string{"linux-snmp"},
		HostMacros: []PresetMacro{{
			// The Linux default plus unRAID's utility mount roots (addons/disks/remotes/rootshare,
			// memtester) and the children of /var/lib/docker (the btrfs/overlay subvolume mirrors
			// /var/lib/docker itself, which stays - that's the docker.img usage).
			Macro: "{$FS.NAME.SKIP}",
			Value: `^(/run|/dev|/sys|/proc|/mnt/addons|/mnt/disks|/mnt/remotes|/mnt/rootshare|/var/lib/memtester|/var/lib/docker/[^/]+)($|/)`,
		}},
	},
	{
		// An XCP-NG pool via XAPI: the proxy/core runs argus_xcpng.py (an external check) against
		// the pool master, and one XML-RPC session per poll covers every hypervisor in the pool, so a
		// single-host pool reads like one machine and a real pool gets per-member sensors. Per-VM
		// monitoring is opt-in ({$XCP.VM.MODE}); CPU temperature needs the optional argus-temp dom0
		// plugin (docs/hosts/xcpng.md). The host's address is the pool master's.
		ID:          "xcpng",
		Label:       "XCP-NG",
		Group:       "app",
		Template:    "Argus XCP-NG by XAPI",
		Description: "An XCP-NG pool through XAPI: every hypervisor's load, memory and storage, and, when you turn it on, each VM.",
		Macros: []MacroSpec{
			{Macro: "{$XCP.USER}", Label: "XAPI username", Hint: "root", Required: true},
			{Macro: "{$XCP.PASS}", Label: "XAPI password", Hint: "the host root password", Required: true, Secret: true},
			{Macro: "{$XCP.VM.MODE}", Label: "VM monitoring", Hint: "off", Options: []string{"off", "state", "full"}},
			{Macro: "{$XCP.TLS}", Label: "Certificate check", Hint: "pin", Options: []string{"pin", "verify", "ignore"}},
			// Rendered as a checklist of the discovered VMs in host settings (there is nothing to
			// pick from at add time). Ignored VMs vanish from the per-VM sensors AND the counts.
			{Macro: "{$XCP.VM.IGNORE}", Label: "Monitored VMs", Hint: "comma-separated VM names to ignore", SettingsOnly: true},
		},
		Setup: &ClassSetup{
			Title: "Point Argus at the pool master",
			Steps: []string{
				"Use the pool master's IP as the device address (a single host is its own master; a slave address is followed to the master automatically).",
				"Credentials are the XAPI login - root and the host root password on a stock XCP-NG.",
				"VM monitoring is optional: \"state\" adds each VM's power state, \"full\" adds per-VM CPU, memory and I/O.",
			},
			Note: "CPU temperature per hypervisor needs the optional argus-temp plugin on dom0 - see the XCP-NG guide in docs/hosts/.",
		},
	},
	{
		// The direct NUT path: the proxy's argus_nut.py collector (an external check baked into the
		// probe image) speaks the NUT protocol to upsd, no extra app. The host is the NUT server.
		ID:          "ups-nut",
		Label:       "UPS by NUT",
		Group:       "app",
		Template:    "Argus UPS by NUT",
		Description: "A UPS on a NUT server (upsd): battery charge and runtime, load, input voltage, and whether it runs on battery.",
		Slot:        SlotUPS,
		Macros: []MacroSpec{
			{Macro: "{$NUT.UPS}", Label: "UPS name", Hint: "ups (see: upsc -l <host>)", Required: true},
			{Macro: "{$NUT.PORT}", Label: "upsd port", Hint: "3493"},
			{Macro: "{$NUT.USER}", Label: "upsd username", Hint: "only if upsd needs a login to read"},
			{Macro: "{$NUT.PASSWORD}", Label: "upsd password", Hint: "only if upsd needs a login to read", Secret: true},
		},
	},
	{
		// A UPS monitored through NUT via PeaNUT (a container that fronts upsd over HTTP). The host
		// is the PeaNUT box; {$PEANUT.URL} defaults to its own address on :8080.
		ID:          "ups-peanut",
		Label:       "UPS by PeaNUT",
		Group:       "app",
		Template:    "Argus UPS by PeaNUT",
		Description: "The same UPS sensors, through a PeaNUT server's API.",
		Slot:        SlotUPS,
		Macros: []MacroSpec{
			{Macro: "{$PEANUT.URL}", Label: "PeaNUT URL", Hint: "http://peanut.example.lan:8080", Required: true, Derive: "http://{host}:8080"},
			{Macro: "{$PEANUT.UPS}", Label: "UPS name", Hint: "ups (see PeaNUT /api/v1/devices)", Required: true},
			{Macro: "{$PEANUT.USER}", Label: "PeaNUT username", Hint: "blank if PeaNUT runs with AUTH_DISABLED"},
			{Macro: "{$PEANUT.PASSWORD}", Label: "PeaNUT password", Hint: "blank if PeaNUT runs with AUTH_DISABLED", Secret: true},
		},
	},
	{
		// HTTP is the universal check (any device may expose a web UI worth watching).
		ID:       "http",
		Label:    "HTTP/HTTPS endpoint",
		Group:    "check",
		Template: TemplateHTTP,
		Description: "A real request to each URL, from the host's proxy: an accepted status code, the response time and the certificate's days left. " +
			"Add full URLs, hosts (10.0.0.20:8443) or paths on this host (/login), each with its own certificate check and text its page must or must not contain; with none, the host itself is checked on the scheme, port and certificate check below. " +
			"Certificate: verify wants one a known CA issued; self-signed also takes the device's own, still not expired and, for a URL by name, for that name; ignore takes any and doesn't track its expiry.",
		Macros: []MacroSpec{
			{Macro: "{$HTTP.URLS}", Label: "URLs", Hint: "https://portal.example.com/app, /login", Check: checkURLList},
			{Macro: "{$HTTP.SCHEME}", Label: "Scheme (for paths and hosts)", Hint: "https", Options: []string{"https", "http"}},
			{Macro: "{$HTTP.PORT}", Label: "Port (for paths and a blank list)", Hint: "443 for https, 80 for http", Pattern: patternPort},
			{Macro: "{$HTTP.EXPECT}", Label: "Accepted status codes", Hint: "200-299", Pattern: patternCodes},
			{Macro: "{$HTTP.TLS.VERIFY}", Label: "Certificate", Hint: "verify", Options: []string{"verify", "self-signed", "ignore"}},
			{Macro: "{$HTTP.TIMEOUT}", Label: "Timeout (seconds)", Hint: "10", Pattern: patternSeconds},
		},
		Prefill: true,
	},
	{
		// TCP ports checks any listed port.
		ID:          "tcp",
		Label:       "TCP ports",
		Group:       "check",
		Template:    TemplateTCP,
		Description: "Check that TCP ports accept a connection (SMTP, RDP, a database, an admin port), with the connect time and why a port doesn't answer, run from the host's proxy.",
		Macros: []MacroSpec{
			{Macro: "{$TCP.PORTS}", Label: "Ports", Hint: "SMTP:25, RDP:3389, 8443", Required: true, Pattern: patternPorts},
			{Macro: "{$TCP.TIMEOUT}", Label: "Timeout (seconds)", Hint: "3", Pattern: patternSeconds},
		},
		Prefill: true,
	},
	{
		// Any DNS server (Pi-hole, Microsoft DNS, Ubiquiti, Sophos, a plain resolver, or AdGuard):
		// the proxy/core runs dns-resolver.py to actually resolve names against it.
		ID:          "dns-resolve",
		Label:       "DNS resolution",
		Group:       "check",
		Template:    "Argus DNS resolution",
		Description: "Resolve one or more names against this host, each its own sensor, and alert on failures or slow answers.",
		Macros: []MacroSpec{
			{Macro: "{$DNS.RESOLVE.NAMES}", Label: "Names to resolve", Hint: "example.com,cloudflare.com", Pattern: patternNames},
			{Macro: "{$DNS.PORT}", Label: "DNS port", Hint: "53", Pattern: patternPort},
		},
	},
	{
		ID:       "speedtest",
		Label:    "Speedtest",
		Group:    "probe",
		Template: TemplateSpeedtest,
		Description: "The site's internet as its probe sees it: download and upload speed, latency, jitter, latency while busy (bufferbloat), the public address and the provider. " +
			"Cloudflare's speed test moves at most about 720 MB down and 300 MB up a run, no more than one test on Cloudflare's own page, and Cloudflare limits how much one address tests in an hour; a line over a gigabit may want more connections. " +
			"Ookla's (Speedtest by Ookla) picks a nearby server and fills a fast line, adds packet loss, and runs under Ookla's terms, which you accept to use it.",
		Macros: []MacroSpec{
			{Macro: "{$SPEEDTEST.INTERVAL}", Label: "How often", Hint: "6h", Options: []string{"1h", "3h", "4h", "6h", "8h", "12h", "24h"}},
			{Macro: "{$SPEEDTEST.ENGINE}", Label: "Engine", Hint: "cloudflare", Options: []string{"cloudflare", "ookla"}, Pattern: patternEngine,
				OptionLabels: map[string]string{"cloudflare": "Cloudflare", "ookla": "Ookla Speedtest"}, Terms: &ooklaTerms},
			{Macro: "{$SPEEDTEST.SECONDS}", Label: "Seconds per direction, at most", Hint: "8", Options: []string{"5", "8", "12"}, ShowIf: &ShowIf{Macro: "{$SPEEDTEST.ENGINE}", Value: "cloudflare"}},
			{Macro: "{$SPEEDTEST.STREAMS}", Label: "Connections", Hint: "8", Options: []string{"4", "8", "12", "16"}, ShowIf: &ShowIf{Macro: "{$SPEEDTEST.ENGINE}", Value: "cloudflare"}},
			{Macro: "{$SPEEDTEST.SERVER}", Label: "Ookla server ID (blank = nearest)", Hint: "nearest", Pattern: patternServerID, ShowIf: &ShowIf{Macro: "{$SPEEDTEST.ENGINE}", Value: "ookla"}},
		},
		Classes: []string{ClassProbe},
		Prefill: true,
	},
	{
		ID:       "saas",
		Label:    "Common SaaS",
		Group:    "probe",
		Template: TemplateSaaS,
		Description: "Whether the cloud services the site works with answer from here, and how fast: Microsoft 365, Outlook, Google, Amazon Web Services, Zoom and the others picked below, every 2 minutes. " +
			"Each is its own sensor, with why when it doesn't answer.",
		Macros: []MacroSpec{
			{Macro: "{$SAAS.URLS}", Label: "Services", Required: true, Check: checkURLList},
		},
		Classes: []string{ClassProbe},
		Prefill: true,
	},
}

// Sets returns the set catalog (stable order).
func Sets() []Set { return sets }

// SetByID returns the set with this id, or (zero, false).
func SetByID(id string) (Set, bool) {
	for _, s := range sets {
		if s.ID == id {
			return s, true
		}
	}
	return Set{}, false
}

// SetByTemplate returns the set that links this template, or (zero, false).
func SetByTemplate(template string) (Set, bool) {
	for _, s := range sets {
		if s.Template == template {
			return s, true
		}
	}
	return Set{}, false
}

// SetsOfTemplates is the sets a host carries, read from its linked templates, in catalog order.
func SetsOfTemplates(templates []string) []Set {
	var out []Set
	for _, s := range sets {
		if slices.Contains(templates, s.Template) {
			out = append(out, s)
		}
	}
	return out
}

// OffersOn reports whether the set is offered on a host of this class (internal = Argus's own).
func (s Set) OffersOn(classID string, internal bool) bool {
	if len(s.Classes) == 0 {
		return !internal
	}
	return slices.Contains(s.Classes, classID)
}

// SetBlock says why a set can't join the sets a host carries ("" = it can). on is their ids.
func SetBlock(s Set, on []string) string {
	if slices.Contains(on, s.ID) {
		return ""
	}
	if s.Slot != "" {
		for _, id := range on {
			if o, ok := SetByID(id); ok && o.Slot == s.Slot {
				return SlotNote(s.Slot) + ": " + o.Label + " is on"
			}
		}
	}
	for _, n := range s.Needs {
		if !slices.Contains(on, n) {
			if o, ok := SetByID(n); ok {
				return "needs " + o.Label
			}
		}
	}
	return ""
}

// CheckSets says what is wrong with a host of this class carrying exactly these sets (nil =
// nothing): one it isn't offered, two of one slot, or one without a set it needs.
func CheckSets(ids []string, classID string, internal bool) error {
	bySlot := map[string]Set{}
	for _, id := range ids {
		s, ok := SetByID(id)
		if !ok {
			return fmt.Errorf("%q is not a sensor set", id)
		}
		if !s.OffersOn(classID, internal) {
			if len(s.Classes) > 0 {
				return fmt.Errorf("%s is read from a probe's own host only", s.Label)
			}
			return fmt.Errorf("%s isn't offered on Argus's own hosts", s.Label)
		}
		if s.Slot != "" {
			if o, dup := bySlot[s.Slot]; dup && o.ID != s.ID {
				return fmt.Errorf("%s: %s and %s can't both be on", SlotNote(s.Slot), o.Label, s.Label)
			}
			bySlot[s.Slot] = s
		}
	}
	for _, id := range ids {
		s, _ := SetByID(id)
		for _, n := range s.Needs {
			if !slices.Contains(ids, n) {
				o, _ := SetByID(n)
				return fmt.Errorf("%s needs %s", s.Label, o.Label)
			}
		}
	}
	return nil
}

// IsOptionalTemplate reports whether template is a set no class comes with (HTTP, TCP ports, the
// probe's own): the thresholds screen marks those as optional.
func IsOptionalTemplate(template string) bool {
	s, ok := SetByTemplate(template)
	if !ok {
		return false
	}
	for _, c := range registry {
		if slices.Contains(c.Sets, s.ID) {
			return false
		}
	}
	return true
}

// init gives each class what its usual sets bring: their templates, the settings their forms
// collect, their preset macros and the first one's setup steps. The sets are the single source.
func init() {
	for i := range registry {
		c := &registry[i]
		seen := map[string]bool{}
		for _, id := range c.Sets {
			s, ok := SetByID(id)
			if !ok {
				panic("provision: class " + c.ID + " lists an unknown sensor set " + id)
			}
			c.Templates = append(c.Templates, s.Template)
			for _, ms := range s.Macros {
				if !seen[ms.Macro] {
					seen[ms.Macro] = true
					c.Macros = append(c.Macros, ms)
				}
			}
			c.HostMacros = append(c.HostMacros, s.HostMacros...)
			if c.Setup == nil {
				c.Setup = s.Setup
			}
		}
	}
}
