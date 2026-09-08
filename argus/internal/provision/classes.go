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
)

// IfaceKind is the network interface a class's host needs in Zabbix.
type IfaceKind string

const (
	IfaceNone  IfaceKind = ""      // no interface (endpoint-source classes register elsewhere)
	IfaceAgent IfaceKind = "agent" // agent / simple checks (ping) — Zabbix type 1, default port 10050
	IfaceSNMP  IfaceKind = "snmp"  // SNMP — Zabbix type 2, default port 161
)

// MacroSpec is a per-host macro the attach UI asks for when creating a host of this class - how
// API-source classes (UniFi, later Nutanix/XCP-NG/…) receive their endpoint and credentials.
type MacroSpec struct {
	Macro    string `json:"macro"`    // Zabbix macro name, e.g. "{$UNIFI.URL}"
	Label    string `json:"label"`    // form label
	Hint     string `json:"hint"`     // placeholder / example
	Required bool   `json:"required"` // creation fails without it
	Secret   bool   `json:"secret"`   // stored as a Zabbix secret macro (write-only afterwards)
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
