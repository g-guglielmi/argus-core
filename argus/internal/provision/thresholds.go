// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package provision

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// Thresholds (ROADMAP §D). A threshold is a Zabbix user macro a trigger compares against
// (e.g. {$CPU.UTIL.WARN}=80). The values live in the class template YAML under templates/*.yaml and
// are single-sourced there; this file only declares WHICH macros are editable thresholds and how to
// label them, and reads the factory values back out of the embedded YAML. Grouping is BY TEMPLATE,
// not by class: a template like "Argus Linux by SNMP" backs several classes, and changing its macro
// changes it for all of them - grouping by template makes that scope honest instead of surprising.

// ThresholdSpec is one editable threshold macro on a template, with UI presentation metadata. The
// factory value + current override are filled in by the server from the YAML scan and the store.
type ThresholdSpec struct {
	Macro string `json:"macro"` // full Zabbix macro incl. any context, e.g. "{$DISK.TEMP.WARN:ssd}"
	Label string `json:"label"`
	Unit  string `json:"unit"` // display suffix: "%", "s", "°C", or "" for none
}

// TemplateThresholds is a template's ordered set of editable thresholds.
type TemplateThresholds struct {
	Template string          `json:"template"` // Zabbix technical name, e.g. "Argus Linux by SNMP"
	Specs    []ThresholdSpec `json:"specs"`
}

// Shared spec groups, since most compute classes carry the identical CPU/memory/disk bands.
func cpuUtilSpecs() []ThresholdSpec {
	return []ThresholdSpec{
		{Macro: "{$CPU.UTIL.WARN}", Label: "CPU utilization - warning", Unit: "%"},
		{Macro: "{$CPU.UTIL.HIGH}", Label: "CPU utilization - high", Unit: "%"},
	}
}
func memUsedSpecs() []ThresholdSpec {
	return []ThresholdSpec{
		{Macro: "{$MEM.USED.WARN}", Label: "Memory used - warning", Unit: "%"},
		{Macro: "{$MEM.USED.HIGH}", Label: "Memory used - high", Unit: "%"},
	}
}
func diskUsedSpecs() []ThresholdSpec {
	return []ThresholdSpec{
		{Macro: "{$DISK.PUSED.WARN}", Label: "Disk usage - warning", Unit: "%"},
		{Macro: "{$DISK.PUSED.HIGH}", Label: "Disk usage - high", Unit: "%"},
	}
}

// thresholdCatalog is the master list. Ordered for display: every-host base first, compute classes,
// network gear, DNS, virtualization, then the optional HTTP add-on. Each Macro must exist in its
// template's YAML macros block (guarded by TestThresholdCatalogMatchesTemplates).
var thresholdCatalog = []TemplateThresholds{
	{Template: TemplateBasePing, Specs: []ThresholdSpec{
		{Macro: "{$PING.LOSS.WARN}", Label: "Packet loss - warning", Unit: "%"},
		{Macro: "{$PING.RESP.WARN}", Label: "Response time - warning", Unit: "s"},
	}},
	{Template: "Argus Linux by SNMP", Specs: concat(cpuUtilSpecs(), memUsedSpecs(), diskUsedSpecs())},
	{Template: "Argus Linux by SSH", Specs: concat(cpuUtilSpecs(), memUsedSpecs(), diskUsedSpecs())},
	{Template: "Argus Windows by SNMP", Specs: concat(cpuUtilSpecs(), memUsedSpecs(), diskUsedSpecs())},
	{Template: "Argus unRAID by SNMP", Specs: []ThresholdSpec{
		{Macro: "{$DISK.TEMP.WARN}", Label: "Array drive temp (HDD) - warning", Unit: "°C"},
		{Macro: "{$DISK.TEMP.HIGH}", Label: "Array drive temp (HDD) - high", Unit: "°C"},
		{Macro: "{$POOL.TEMP.WARN}", Label: "Pool/cache temp (SSD) - warning", Unit: "°C"},
		{Macro: "{$POOL.TEMP.HIGH}", Label: "Pool/cache temp (SSD) - high", Unit: "°C"},
		{Macro: "{$CPU.TEMP.WARN}", Label: "CPU temp - warning", Unit: "°C"},
		{Macro: "{$CPU.TEMP.HIGH}", Label: "CPU temp - high", Unit: "°C"},
	}},
	{Template: "Argus NAS by Zabbix agent", Specs: concat(cpuUtilSpecs(), memUsedSpecs(), diskUsedSpecs(), []ThresholdSpec{
		{Macro: "{$DISK.TEMP.WARN}", Label: "Disk temp (HDD) - warning", Unit: "°C"},
		{Macro: "{$DISK.TEMP.HIGH}", Label: "Disk temp (HDD) - high", Unit: "°C"},
		{Macro: "{$DISK.TEMP.WARN:ssd}", Label: "Disk temp (SSD) - warning", Unit: "°C"},
		{Macro: "{$DISK.TEMP.HIGH:ssd}", Label: "Disk temp (SSD) - high", Unit: "°C"},
		{Macro: "{$DISK.TEMP.WARN:nvme}", Label: "Disk temp (NVMe) - warning", Unit: "°C"},
		{Macro: "{$DISK.TEMP.HIGH:nvme}", Label: "Disk temp (NVMe) - high", Unit: "°C"},
		{Macro: "{$CPU.TEMP.WARN}", Label: "CPU temp - warning", Unit: "°C"},
		{Macro: "{$CPU.TEMP.HIGH}", Label: "CPU temp - high", Unit: "°C"},
	})},
	{Template: "Argus UniFi Switch by HTTP", Specs: concat(cpuUtilSpecs(), memUsedSpecs())},
	{Template: "Argus UniFi Gateway by HTTP", Specs: concat(cpuUtilSpecs(), memUsedSpecs(), []ThresholdSpec{
		{Macro: "{$UNIFI.WAN.AVAIL.MIN}", Label: "WAN availability - minimum", Unit: "%"},
	})},
	{Template: "Argus UniFi AP by HTTP", Specs: concat(cpuUtilSpecs(), memUsedSpecs())},
	{Template: "Argus UniFi OS Console by HTTP", Specs: concat(cpuUtilSpecs(), memUsedSpecs(), diskUsedSpecs())},
	{Template: "Argus DNS resolution", Specs: []ThresholdSpec{
		{Macro: "{$DNS.RTT.HIGH}", Label: "Resolve time - warning", Unit: "s"},
	}},
	{Template: "Argus XCP-NG by XAPI", Specs: []ThresholdSpec{
		{Macro: "{$XCP.CPU.UTIL.WARN}", Label: "Hypervisor CPU - warning", Unit: "%"},
		{Macro: "{$XCP.MEM.WARN}", Label: "Hypervisor memory - warning", Unit: "%"},
		{Macro: "{$XCP.TEMP.WARN}", Label: "Hypervisor CPU temp - warning", Unit: "°C"},
		{Macro: "{$XCP.TEMP.HIGH}", Label: "Hypervisor CPU temp - high", Unit: "°C"},
	}},
	{Template: TemplateHTTP, Specs: []ThresholdSpec{
		{Macro: "{$HTTP.RESPONSE.WARN}", Label: "Response time - warning", Unit: "s"},
	}},
}

func concat(groups ...[]ThresholdSpec) []ThresholdSpec {
	var out []ThresholdSpec
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// ThresholdCatalog returns the editable-threshold catalog (stable order), keyed by template.
func ThresholdCatalog() []TemplateThresholds { return thresholdCatalog }

// ThresholdsForTemplates returns the catalog entries whose template is in the given set, in catalog
// order. Used to gather the thresholds that apply to one host (its class templates + Base Ping).
func ThresholdsForTemplates(templates []string) []TemplateThresholds {
	want := map[string]bool{}
	for _, t := range templates {
		want[t] = true
	}
	var out []TemplateThresholds
	for _, tt := range thresholdCatalog {
		if want[tt.Template] {
			out = append(out, tt)
		}
	}
	return out
}

// TemplateClassLabels returns the human labels of the classes that link this template, so the UI can
// show a template group's scope ("Applies to: ..."). Base Ping / HTTP are handled by the caller.
func TemplateClassLabels(template string) []string {
	var out []string
	for _, c := range registry {
		for _, t := range c.Templates {
			if t == template {
				out = append(out, c.Label)
				break
			}
		}
	}
	return out
}

// --- Factory-default scanner --------------------------------------------------------------------

var (
	reTemplateLine = regexp.MustCompile(`^\s+template:\s+'([^']*)'`)
	reMacroLine    = regexp.MustCompile(`^-\s*macro:\s*'([^']*)'`)
	reValueLine    = regexp.MustCompile(`^value:\s*'?(.*?)'?\s*$`)
)

// TemplateFactoryDefaults scans the embedded template YAML and returns template -> macro -> factory
// value (the value shipped in the file). It reads the regular `- macro:` / `value:` lines in each
// template's `macros:` block - the format is ours and consistent, so this stays dependency-free (no
// YAML library). A format drift is caught by TestThresholdCatalogMatchesTemplates.
func TemplateFactoryDefaults() (map[string]map[string]string, error) {
	docs, _, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, d := range docs {
		currentTpl := ""
		macroIndent := -1
		pending := ""
		for _, line := range strings.Split(d.content, "\n") {
			if m := reTemplateLine.FindStringSubmatch(line); m != nil {
				currentTpl = m[1]
				if out[currentTpl] == nil {
					out[currentTpl] = map[string]string{}
				}
				macroIndent = -1
				pending = ""
				continue
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			indent := len(line) - len(strings.TrimLeft(line, " "))
			if trimmed == "macros:" {
				macroIndent = indent
				pending = ""
				continue
			}
			if macroIndent < 0 || currentTpl == "" {
				continue
			}
			if indent <= macroIndent {
				macroIndent = -1 // dedented out of the macros block
				continue
			}
			if m := reMacroLine.FindStringSubmatch(trimmed); m != nil {
				pending = m[1]
				continue
			}
			if pending != "" {
				if m := reValueLine.FindStringSubmatch(trimmed); m != nil {
					out[currentTpl][pending] = m[1]
					pending = ""
				}
			}
		}
	}
	return out, nil
}

// --- Global apply -------------------------------------------------------------------------------

// ApplyGlobalThresholds re-applies the admin's stored fleet-wide threshold defaults onto the live
// Zabbix templates. Argus is the source of truth: this runs at startup AFTER Reconcile (so a template
// re-import that resets macros to their factory values is immediately overlaid again) and after each
// save. Idempotent; a soft no-op when Zabbix has no token yet, like Reconcile.
func ApplyGlobalThresholds(ctx context.Context, zbx *zabbix.Client, st *store.Store, logger *slog.Logger) error {
	if !zbx.Authenticated() {
		return nil
	}
	defaults, err := st.ThresholdDefaults(ctx)
	if err != nil {
		return err
	}
	if len(defaults) == 0 {
		return nil
	}
	names := make([]string, 0, len(defaults))
	for tpl := range defaults {
		names = append(names, tpl)
	}
	tmpls, err := zbx.Templates(ctx, names) // returns only templates that exist
	if err != nil {
		return err
	}
	idByName := map[string]string{}
	for _, t := range tmpls {
		idByName[t.Host] = t.TemplateID
	}
	applied := 0
	for tpl, macros := range defaults {
		tid, ok := idByName[tpl]
		if !ok {
			logger.Warn("thresholds: stored default references an unknown template, skipping", "template", tpl)
			continue
		}
		cur, err := zbx.TemplateMacros(ctx, tid)
		if err != nil {
			return err
		}
		byMacro := map[string]zabbix.HostMacro{}
		for _, m := range cur {
			byMacro[m.Macro] = m
		}
		for macro, value := range macros {
			if existing, has := byMacro[macro]; has {
				if existing.Value != value {
					if err := zbx.UpdateHostMacro(ctx, existing.MacroID, value, 0); err != nil {
						return err
					}
					applied++
				}
			} else {
				if err := zbx.CreateHostMacro(ctx, tid, zabbix.Macro{Macro: macro, Value: value, Type: 0}); err != nil {
					return err
				}
				applied++
			}
		}
	}
	if applied > 0 {
		logger.Info("thresholds: applied global defaults to templates", "changed", applied)
	}
	return nil
}
