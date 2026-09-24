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
// DNS resolution layers per-name resolve checks onto a host that isn't already a DNS class.
var addOns = []AddOn{
	{
		ID:          "http",
		Label:       "HTTP/HTTPS endpoint",
		Template:    TemplateHTTP,
		Description: "Reachability + response time on a web port, run from the host's proxy.",
		Macros: []MacroSpec{
			{Macro: "{$HTTP.SCHEME}", Label: "Scheme", Hint: "https", Options: []string{"https", "http"}},
			{Macro: "{$HTTP.PORT}", Label: "Port", Hint: "443"},
		},
	},
	{
		ID:          "dns-resolve",
		Label:       "DNS resolution",
		Template:    "Argus DNS resolution",
		Description: "Resolve one or more names against this host and alert on failures or slow answers.",
		Macros: []MacroSpec{
			{Macro: "{$DNS.RESOLVE.NAMES}", Label: "Names to resolve", Hint: "example.com,cloudflare.com", Required: true},
			{Macro: "{$DNS.PORT}", Label: "DNS port", Hint: "53"},
		},
	},
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
