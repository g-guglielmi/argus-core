// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package provision owns the device-class catalog (DESIGN §5) and the Zabbix templates that back it:
// the registry the attach UI/discovery choose from, and the startup reconcile that imports the
// hand-authored class templates into Zabbix. It is a leaf on the store + zabbix client, like notify.
package provision

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Templates every host / set references by their Zabbix technical name. Base Ping is attached to
// every provisioned host automatically; the HTTP endpoint is an optional set.
const (
	TemplateBasePing = "Argus Base Ping"
	// ClassProbe is the Argus-managed class of the per-site Probe health host.
	ClassProbe   = "probe"
	TemplateHTTP = "Argus HTTP Endpoint"
	TemplateTCP  = "Argus TCP ports"
	// TemplatePush reads a host's push sensors back from Argus (push.go); Argus links it itself.
	TemplatePush = "Argus Push"
	// TemplateWeb carries the thresholds of a host's web scenarios (webscenarios.go); Argus links it.
	TemplateWeb = "Argus Web Scenarios"
	// TemplateProbeHealth is the per-probe health template (internal checks run on the proxy).
	TemplateProbeHealth = "Argus Probe Health"
	// ClassCore is the Argus core itself: the Zabbix server's own host, adopted at startup.
	ClassCore = "core"
	// TemplateCoreHealth is the core's health template (internal checks the server runs on itself).
	TemplateCoreHealth = "Argus Core Health"
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
	IfaceAgent IfaceKind = "agent" // agent / simple checks (ping) - Zabbix type 1, default port 10050
	IfaceSNMP  IfaceKind = "snmp"  // SNMP - Zabbix type 2, default port 161
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
	Macro        string             `json:"macro"`                   // Zabbix macro name, e.g. "{$UNIFI.URL}"
	Label        string             `json:"label"`                   // form label
	Hint         string             `json:"hint"`                    // placeholder / example
	Required     bool               `json:"required"`                // creation fails without it
	Secret       bool               `json:"secret"`                  // stored as a Zabbix secret macro (write-only afterwards)
	Derive       string             `json:"derive,omitempty"`        // form auto-fills this from the host address ("{host}" -> the IP/DNS), overridable; e.g. "http://{host}". Only for host-addressed URLs, never a controller URL (UniFi) that differs from the device.
	Options      []string           `json:"options,omitempty"`       // fixed value set: the form renders a select instead of a text input (blank stays "template default"); e.g. XCP-NG's VM-monitoring mode off/state/full
	SettingsOnly bool               `json:"settings_only,omitempty"` // shown only in host settings, not the Add-device wizard - for values that need discovered data first (XCP-NG's ignored-VMs list)
	Pattern      string             `json:"-"`                       // optional regexp the value must match; for values that reach a command line on the probe
	Check        func(string) error `json:"-"`                       // optional check beyond the pattern (a URL list), saying what is wrong
	OptionLabels map[string]string  `json:"option_labels,omitempty"` // how the form names each option ("ookla" -> "Ookla Speedtest"); unlisted ones show as they are
	ShowIf       *ShowIf            `json:"show_if,omitempty"`       // the field only applies while another field of the same form has this value
	Terms        *Terms             `json:"terms,omitempty"`         // an option that runs under a third party's terms, accepted before it is set
}

// OptionLabel is how the form names one of the field's options.
func (ms MacroSpec) OptionLabel(v string) string {
	if l := ms.OptionLabels[v]; l != "" {
		return l
	}
	return v
}

// ShowIf ties a field to another field's value: an engine's own options are shown only with it.
type ShowIf struct {
	Macro string `json:"macro"`
	Value string `json:"value"`
}

// Terms are a third party's terms one option of a field runs under (Ookla's, for its speed test).
// Setting a host's field to that option needs them accepted in the same save; the change log says who
// accepted them and when. Keeping it set needs nothing more.
type Terms struct {
	Value string     `json:"value"` // the option they apply to
	Title string     `json:"title"` // "Ookla's terms"
	Text  string     `json:"text"`  // what accepting means, in plain words
	Links []TermLink `json:"links"` // the terms themselves
}

// TermLink is one document of a third party's terms.
type TermLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Value patterns for macros that end up as command-line arguments of a probe's external check.
// Zabbix passes each parameter as its own argv entry (no shell), but an argument that starts
// with "-" is an option to the program, and a path is opened as given.
const (
	patternLogin    = `^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`                  // a login name: never a leading "-", no spaces
	patternEngine   = `^(cloudflare|ookla)?$`                               // the speed test's engine, or blank (Cloudflare)
	patternServerID = `^([0-9]{1,9})?$`                                     // an Ookla server ID, or blank (Ookla picks)
	patternPort     = `^([1-9][0-9]{0,4})?$`                                // 1-65535 or blank (the template default)
	patternMAC      = `^[0-9A-Fa-f]{2}(:[0-9A-Fa-f]{2}){5}$`                // a MAC address, six hex pairs with colons
	patternKey      = `^(/var/lib/zabbix/ssh/[A-Za-z0-9][A-Za-z0-9._-]*)?$` // a key inside the proxy's ssh dir, or blank
	// systemd unit names, comma or space separated: systemd's own characters, never a leading "-"
	patternUnits = `^([A-Za-z0-9][A-Za-z0-9@._:-]*([ ,]+[A-Za-z0-9][A-Za-z0-9@._:-]*)*[ ,]*)?$`
	// a regular expression the collector applies itself (never a shell): one line, bounded
	patternRegexLine = `^[^\r\n\x00]{0,256}$`
	// TCP ports, comma or space separated, each "port" or "name:port" (argus_tcp.py checks them again)
	patternPorts = `^(([A-Za-z][A-Za-z0-9._-]{0,31}:)?[0-9]{1,5}([ ,]+([A-Za-z][A-Za-z0-9._-]{0,31}:)?[0-9]{1,5})*[ ,]*)?$`
	// host names to resolve, comma or space separated: never a leading "-"
	patternNames = `^([A-Za-z0-9_][A-Za-z0-9_.-]*([ ,]+[A-Za-z0-9_][A-Za-z0-9_.-]*)*[ ,]*)?$`
	// a timeout in seconds, blank for the template default
	patternSeconds = `^([0-9]{1,2}(\.[0-9]{1,2})?)?$`
	// accepted HTTP status codes: codes and ranges, comma separated ("200-299, 401")
	patternCodes = `^([1-5][0-9]{2}( *- *[1-5][0-9]{2})?( *, *[1-5][0-9]{2}( *- *[1-5][0-9]{2})?)*)?$`
)

// ValidateMacroValue checks an entered value against the spec's pattern (blank always passes: it
// means "template default"). A macro without a pattern only refuses control characters, which no
// Zabbix macro value needs and which break the files these values are written into.
func ValidateMacroValue(ms MacroSpec, v string) error {
	if v == "" {
		return nil
	}
	if strings.ContainsAny(v, "\r\n\x00") {
		return fmt.Errorf("%s can't contain line breaks", ms.Label)
	}
	if ms.Pattern != "" && !regexp.MustCompile(ms.Pattern).MatchString(v) {
		return fmt.Errorf("%s: %q isn't an accepted value", ms.Label, v)
	}
	// A fixed set of values is just that: a template's delay or mode can't be typed freely
	// (a speed test every second would saturate a site's line).
	if len(ms.Options) > 0 && !slices.Contains(ms.Options, v) {
		return fmt.Errorf("%s: %q isn't one of its options", ms.Label, v)
	}
	if ms.Check != nil {
		if err := ms.Check(v); err != nil {
			return fmt.Errorf("%s: %v", ms.Label, err)
		}
	}
	return nil
}

// maxHTTPURLs is how many URLs the HTTP set checks on one host (argus_http.py's MAX_URLS).
const maxHTTPURLs = 16

// urlEntry is the characters argus_http.py takes in a URL entry: what URLs use, without spaces,
// quotes, backslashes, backticks or "$".
var urlEntry = regexp.MustCompile(`^[A-Za-z0-9._~:/?#\[\]@!&'()*+,;=%-]+$`)

// checkURLList checks the HTTP set's URL list the way argus_http.py reads it, so a list it would
// refuse is caught where it's typed: full http(s) URLs, hosts without a scheme ("10.0.0.20:8443") or
// paths on the host ("/login"), comma or space separated, each with optional options after "#"
// ("tls=self-signed&text=Welcome", or the older "#text" / "#!text").
func checkURLList(v string) error {
	n := 0
	for _, e := range regexp.MustCompile(`[,\s]+`).Split(strings.TrimSpace(v), -1) {
		if e == "" {
			continue
		}
		if n++; n > maxHTTPURLs {
			return fmt.Errorf("at most %d URLs per host", maxHTTPURLs)
		}
		if len(e) > 2048 || !urlEntry.MatchString(e) {
			return fmt.Errorf("%q has characters a URL can't have", e)
		}
		target, frag, _ := strings.Cut(e, "#")
		if err := checkURLOptions(e, frag); err != nil {
			return err
		}
		if strings.HasPrefix(target, "/") {
			continue
		}
		if !strings.Contains(target, "://") {
			target = "https://" + target // a host without a scheme gets the set's
		}
		u, err := url.Parse(target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return fmt.Errorf("%q is not an http(s) URL or a path starting with /", e)
		}
		if u.User != nil {
			return fmt.Errorf("%q has a user name or password in it, which is not supported", e)
		}
		if p := u.Port(); p != "" {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("%q has a port outside 1-65535", e)
			}
		}
	}
	return nil
}

// checkURLOptions checks one URL's options after "#": "tls=verify|self-signed|ignore", "text=..." and
// "notext=...", joined by "&". A fragment without "=" is the older "#text" form, always fine.
func checkURLOptions(entry, frag string) error {
	if !strings.Contains(frag, "=") {
		return nil
	}
	for _, part := range strings.Split(frag, "&") {
		k, v, _ := strings.Cut(part, "=")
		val, err := url.PathUnescape(v)
		if err != nil {
			return fmt.Errorf("%q: %s has a bad %%-escape", entry, k)
		}
		switch k {
		case "tls":
			if val != "verify" && val != "self-signed" && val != "ignore" {
				return fmt.Errorf("%q: tls must be verify, self-signed or ignore", entry)
			}
		case "text", "notext":
			if strings.TrimSpace(val) == "" {
				return fmt.Errorf("%q: %s needs the text to look for", entry, k)
			}
		case "name":
			if strings.TrimSpace(val) == "" || len([]rune(val)) > 60 {
				return fmt.Errorf("%q: a name is 1 to 60 characters", entry)
			}
		case "":
		default:
			return fmt.Errorf("%q: %q is not an option (tls, text, notext or name)", entry, k)
		}
	}
	return nil
}

// ValidThresholdValue accepts a plain decimal number: what a trigger expression compares against.
// strconv.ParseFloat would also take NaN, Inf and hex, which would make every comparison
// "unknown" and silently stop the sensor alerting.
func ValidThresholdValue(v string) bool { return thresholdNumber.MatchString(v) }

var thresholdNumber = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

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

// Class is a device class in the registry: what a device is (its name and icon in the tree) and the
// sensor sets a device of that kind usually has (sets.go). Templates, Macros, HostMacros and Setup
// are filled in from those sets when the package loads; an Argus-managed class names its template
// itself.
type Class struct {
	ID         string        `json:"id"`               // stable Argus id, e.g. "linux-snmp"
	Label      string        `json:"label"`            // human name, e.g. "Linux (SNMP)"
	Family     string        `json:"family"`           // UI grouping, e.g. "Linux", "UniFi", "Base"
	Pattern    Pattern       `json:"pattern"`          //
	Iface      IfaceKind     `json:"iface"`            // interface the host needs (agent/snmp/none)
	Sets       []string      `json:"sets"`             // the sensor sets a device of this class usually has (ids, sets.go)
	Templates  []string      `json:"templates"`        // Zabbix templates to attach (Base Ping is added on top); from Sets
	OffersHTTP bool          `json:"offers_http"`      // the HTTP/HTTPS set may be attached; true for every class today (any device may expose a web UI worth watching - the admin decides), kept as a seam
	Icon       string        `json:"icon"`             // tree glyph name (server, switch, shield, …); see web devIcon map
	Macros     []MacroSpec   `json:"macros,omitempty"` // per-host macros the attach UI collects; from Sets
	HostMacros []PresetMacro `json:"-"`                // macros set silently on every host of this class; from Sets
	Setup      *ClassSetup   `json:"setup,omitempty"`  // prerequisite steps shown in the attach form; from Sets
	// Internal classes are managed by Argus itself (the per-site Probe host): never offered in the
	// Add-device / discovery / change-class pickers, and refused by the create and change-class APIs.
	Internal bool `json:"internal,omitempty"`
}

// registry is the catalog. C0 shipped the universal "base" class (Ping only); C1 adds Linux (SNMP)
// and UniFi Switch; C2 the rest (DESIGN §5). Adding a class here means shipping its template
// under templates/ and re-importing (the startup reconcile handles that when the file set changes).
var registry = []Class{
	{
		// One per probe, created by Argus (server.EnsureProbeHosts), monitored BY that proxy and with
		// no interface: its Zabbix internal checks measure the proxy's own health. No Base Ping.
		ID:        ClassProbe,
		Label:     "Argus probe",
		Family:    "Argus",
		Pattern:   PatternBase,
		Iface:     IfaceNone,
		Templates: []string{TemplateProbeHealth},
		Icon:      "probe",
		Internal:  true,
	},
	{
		// The core itself: the Zabbix server's own host ("Zabbix server", created by Zabbix), adopted
		// by Argus at startup (server.EnsureCoreHost). The stock server health template makes way for
		// Argus Core Health, whose internal checks the server runs on itself: the capacity gauges the
		// sizing pass (DESIGN 14b) is built on. Argus adds no interface and no Base Ping; whatever
		// agent template the host has stays.
		ID:        ClassCore,
		Label:     "Argus core",
		Family:    "Argus",
		Pattern:   PatternBase,
		Iface:     IfaceNone,
		Templates: []string{TemplateCoreHealth},
		Icon:      "core",
		Internal:  true,
	},
	{
		ID:         "base",
		Label:      "Ping only",
		Family:     "Base",
		Pattern:    PatternBase,
		Iface:      IfaceAgent, // ICMP simple checks resolve the target from the host's interface
		OffersHTTP: true,
		Icon:       "device",
	},
	{
		ID:         "linux-snmp",
		Label:      "Linux (SNMP)",
		Family:     "Linux",
		Pattern:    PatternSNMP,
		Iface:      IfaceSNMP,
		Sets:       []string{"linux-snmp"},
		OffersHTTP: true,
		Icon:       "server",
	},
	{
		// The agentless, no-SNMP fallback for Linux: the proxy/core runs argus_linux_ssh.py (an
		// external check) which opens ONE SSH session per poll, reads /proc + df + /proc/net/dev and
		// emits JSON. Item keys match the native agent/SNMP keys, so curation is shared with the other
		// Linux classes. Auth is a private key on the proxy (recommended) or a password secret macro;
		// either way a read-only login is enough. The host's address is the box's own IP.
		ID:         "linux-ssh",
		Label:      "Linux (SSH, agentless)",
		Family:     "Linux",
		Pattern:    PatternAgentless,
		Iface:      IfaceAgent,
		Sets:       []string{"linux-ssh"},
		OffersHTTP: true,
		Icon:       "server",
	},
	{
		// Windows Server over SNMP: HOST-RESOURCES (CPU/memory/disk) + IF-MIB + the LAN Manager
		// service table. Same item keys as the Linux (SNMP) class, so curation is shared. SNMP creds
		// inherit the proxy default like every SNMP class.
		ID:         "windows-snmp",
		Label:      "Windows (SNMP)",
		Family:     "Windows",
		Pattern:    PatternSNMP,
		Iface:      IfaceSNMP,
		Sets:       []string{"windows-snmp"},
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
		Sets:       []string{"linux-snmp", "unraid"},
		OffersHTTP: true,
		Icon:       "nas",
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
		Sets:       []string{"nas-agent"},
		OffersHTTP: true,
		Icon:       "nas",
		// The data-volume filter lives as the template's {$FS.NAME.MATCHES} default (not a host preset)
		// so it also reaches hosts added before it existed, on the next template re-import.
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
		Sets:       []string{"unifi-switch"},
		OffersHTTP: true,
		Icon:       "switch",
	},
	{
		// Same access pattern as the switch. On a UniFi OS gateway the controller usually runs on
		// the gateway itself, so {$UNIFI.URL} often points back at this same device.
		ID:         "unifi-gateway",
		Label:      "UniFi Gateway",
		Family:     "UniFi",
		Pattern:    PatternHTTPAPI,
		Iface:      IfaceAgent,
		Sets:       []string{"unifi-gateway"},
		OffersHTTP: true,
		Icon:       "router",
	},
	{
		ID:         "unifi-ap",
		Label:      "UniFi Access Point",
		Family:     "UniFi",
		Pattern:    PatternHTTPAPI,
		Iface:      IfaceAgent,
		Sets:       []string{"unifi-ap"},
		OffersHTTP: true,
		Icon:       "wifi",
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
		Sets:       []string{"unifi-console"},
		OffersHTTP: true,
		Icon:       "cloud",
	},
	{
		// AdGuard Home polled from its admin API (DNS filtering stats + protection + version) with a
		// proxy-run net.dns resolve check alongside. The host's own interface is the AdGuard box's IP.
		ID:      "adguard",
		Label:   "AdGuard Home",
		Family:  "DNS",
		Pattern: PatternHTTPAPI,
		Iface:   IfaceAgent,
		// The admin-API stats plus the shared DNS-resolution set (a real per-name resolve check
		// against this server), so AdGuard reads like any other DNS server on top of its own stats.
		Sets:       []string{"adguard", "dns-resolve"},
		OffersHTTP: true,
		Icon:       "globe",
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
		Sets:       []string{"home-assistant"},
		OffersHTTP: true,
		Icon:       "home",
	},
	{
		// Any DNS server (Pi-hole, Microsoft DNS, Ubiquiti, Sophos, a plain resolver, or AdGuard) -
		// the proxy/core runs dns-resolver.py to actually resolve names against it. Reusable set:
		// AdGuard can attach this template too for a real resolve check on top of its API stats.
		ID:         "dns-server",
		Label:      "DNS server",
		Family:     "DNS",
		Pattern:    PatternCollector,
		Iface:      IfaceAgent,
		Sets:       []string{"dns-resolve"},
		OffersHTTP: true,
		Icon:       "globe",
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
		Sets:       []string{"ups-peanut"},
		OffersHTTP: true,
		Icon:       "battery",
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
		Sets:       []string{"ups-nut"},
		OffersHTTP: true,
		Icon:       "battery",
	},
	{
		// An XCP-NG pool via XAPI: the proxy/core runs argus_xcpng.py (an external check) against
		// the pool master - one XML-RPC session per poll covers every hypervisor in the pool, so a
		// single-host pool reads like one machine and a real pool gets per-member sensors. Per-VM
		// monitoring is opt-in ({$XCP.VM.MODE}); CPU temperature needs the optional argus-temp dom0
		// plugin (docs/hosts/xcpng.md). The host's address is the pool master's IP.
		ID:         "xcpng",
		Label:      "XCP-NG (XAPI)",
		Family:     "Virtualization",
		Pattern:    PatternCollector,
		Iface:      IfaceAgent,
		Sets:       []string{"xcpng"},
		OffersHTTP: true,
		Icon:       "vm",
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

// HostTemplates is every template a host of this class carries: Base Ping plus the class's own, or
// just the class's own for a class with no interface (Ping needs an address to ping).
func (c Class) HostTemplates() []string {
	if c.Iface == IfaceNone {
		return append([]string(nil), c.Templates...)
	}
	return append([]string{TemplateBasePing}, c.Templates...)
}
