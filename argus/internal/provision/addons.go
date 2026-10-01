// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package provision

// AddOn is an optional Argus template that can be layered onto (or removed from) any host from the
// host-settings dialog, staying inside the curated model. Each add-on is one registry entry: the
// Zabbix template it links plus the per-host macros the form collects. An add-on is only offered on a
// host whose device class doesn't already include its template (those are managed as class options).
type AddOn struct {
	ID          string      `json:"id"`
	Label       string      `json:"label"`
	Template    string      `json:"template"`
	Description string      `json:"description"`
	Macros      []MacroSpec `json:"macros,omitempty"`
}

// addOns is the catalog. HTTP is the universal add-on (any device may expose a web UI worth watching);
// DNS resolution layers per-name resolve checks onto a host that isn't already a DNS class; TCP ports
// checks any listed port.
var addOns = []AddOn{
	{
		ID:       "http",
		Label:    "HTTP/HTTPS endpoint",
		Template: TemplateHTTP,
		Description: "A real request to each URL, from the host's proxy: an accepted status code, the response time and the certificate's days left. " +
			"Add full URLs, hosts (10.0.0.20:8443) or paths on this host (/login), each with its own certificate check and text its page must or must not contain; with none, the host itself is checked on the scheme and port below. " +
			"Certificate: verify wants one a known CA issued; self-signed also takes the device's own, still not expired and, for a URL by name, for that name; ignore takes any.",
		Macros: []MacroSpec{
			{Macro: "{$HTTP.URLS}", Label: "URLs", Hint: "https://portal.example.com/app, /login", Check: checkURLList},
			{Macro: "{$HTTP.SCHEME}", Label: "Scheme (for paths and hosts)", Hint: "https", Options: []string{"https", "http"}},
			{Macro: "{$HTTP.PORT}", Label: "Port (for paths and a blank list)", Hint: "443", Pattern: patternPort},
			{Macro: "{$HTTP.EXPECT}", Label: "Accepted status codes", Hint: "200-299", Pattern: patternCodes},
			{Macro: "{$HTTP.TLS.VERIFY}", Label: "Certificate (unless a URL sets its own)", Hint: "verify", Options: []string{"verify", "self-signed", "ignore"}},
			{Macro: "{$HTTP.TIMEOUT}", Label: "Timeout (seconds)", Hint: "10", Pattern: patternSeconds},
		},
	},
	{
		ID:          "dns-resolve",
		Label:       "DNS resolution",
		Template:    "Argus DNS resolution",
		Description: "Resolve one or more names against this host and alert on failures or slow answers.",
		Macros: []MacroSpec{
			{Macro: "{$DNS.RESOLVE.NAMES}", Label: "Names to resolve", Hint: "example.com,cloudflare.com", Required: true, Pattern: patternNames},
			{Macro: "{$DNS.PORT}", Label: "DNS port", Hint: "53", Pattern: patternPort},
		},
	},
	{
		ID:          "tcp",
		Label:       "TCP ports",
		Template:    TemplateTCP,
		Description: "Check that TCP ports accept a connection (SMTP, RDP, a database, an admin port), with the connect time and why a port doesn't answer, run from the host's proxy.",
		Macros: []MacroSpec{
			{Macro: "{$TCP.PORTS}", Label: "Ports", Hint: "SMTP:25, RDP:3389, 8443", Required: true, Pattern: patternPorts},
			{Macro: "{$TCP.TIMEOUT}", Label: "Timeout (seconds)", Hint: "3", Pattern: patternSeconds},
		},
	},
}

// IsAddOnTemplate reports whether template is an add-on's (an opt-in template, not a class's own).
func IsAddOnTemplate(template string) bool {
	for _, a := range addOns {
		if a.Template == template {
			return true
		}
	}
	return false
}

// AddOns returns the add-on catalog (stable order).
func AddOns() []AddOn { return addOns }

// AddOnByID returns the add-on with this id, or (zero, false).
func AddOnByID(id string) (AddOn, bool) {
	for _, a := range addOns {
		if a.ID == id {
			return a, true
		}
	}
	return AddOn{}, false
}
