// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"argus/internal/provision"
	"argus/internal/zabbix"
)

// Thresholds screen (ROADMAP §D). Global (fleet-wide) threshold defaults are edited here (a template
// list, each opening an edit dialog) and per-host overrides in the host settings dialog. Global
// defaults are stored in Argus (source of truth) and applied to the Zabbix templates, so linked hosts
// inherit them and a template re-import can't clobber them. Sensor-category order is fixed by default
// (the built-in per-shape profiles) and overridable only per host, in that host's settings.

type thresholdRowView struct {
	Macro   string `json:"macro"`
	Label   string `json:"label"`
	Unit    string `json:"unit"`
	Default string `json:"default"`         // factory value shipped in the template (the reset target)
	Value   string `json:"value,omitempty"` // current global override ("" = using the factory default)
}

type thresholdTemplateView struct {
	Template  string             `json:"template"`             // Zabbix technical name (what a save targets)
	Label     string             `json:"label"`                // display name (Argus- prefix stripped)
	EveryHost bool               `json:"every_host,omitempty"` // Base Ping - applies to every device
	Optional  bool               `json:"optional,omitempty"`   // an opt-in add-on (HTTP endpoint)
	Classes   []string           `json:"classes,omitempty"`    // class labels that link this template
	Rows      []thresholdRowView `json:"thresholds"`
}

type thresholdsResponse struct {
	Templates []thresholdTemplateView `json:"templates"`
}

// displayTemplateName drops the "Argus " prefix templates carry, for a cleaner section heading.
func displayTemplateName(name string) string { return strings.TrimPrefix(name, "Argus ") }

// handleThresholds returns the global-default catalog for the thresholds screen, grouped by template
// (one row per template; the UI opens an edit dialog per template). Admin-only.
func (s *Server) handleThresholds(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	factory, err := provision.TemplateFactoryDefaults()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read template defaults: " + err.Error()})
		return
	}
	overrides, err := s.st.ThresholdDefaults(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	var out thresholdsResponse
	for _, tt := range provision.ThresholdCatalog() {
		tv := thresholdTemplateView{
			Template:  tt.Template,
			Label:     displayTemplateName(tt.Template),
			EveryHost: tt.Template == provision.TemplateBasePing,
			Optional:  tt.Template == provision.TemplateHTTP,
			Classes:   provision.TemplateClassLabels(tt.Template),
		}
		for _, sp := range tt.Specs {
			tv.Rows = append(tv.Rows, thresholdRowView{
				Macro:   sp.Macro,
				Label:   sp.Label,
				Unit:    sp.Unit,
				Default: factory[tt.Template][sp.Macro],
				Value:   overrides[tt.Template][sp.Macro],
			})
		}
		out.Templates = append(out.Templates, tv)
	}
	writeJSON(w, http.StatusOK, out)
}

// thresholdMacroKind returns the unit-less spec for a (template, macro) pair if it is a known,
// editable threshold - the allow-list a save is validated against.
func thresholdSpecFor(template, macro string) (provision.ThresholdSpec, bool) {
	for _, tt := range provision.ThresholdCatalog() {
		if tt.Template != template {
			continue
		}
		for _, sp := range tt.Specs {
			if sp.Macro == macro {
				return sp, true
			}
		}
	}
	return provision.ThresholdSpec{}, false
}

// handleSetThresholdDefault sets (or, with a blank value, resets) one template's global threshold
// default. Admin-only. Argus stores the override and applies it to the live template so linked hosts
// inherit it; a reset deletes the override and restores the template's factory value.
func (s *Server) handleSetThresholdDefault(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	var req struct {
		Template string `json:"template"`
		Macro    string `json:"macro"`
		Value    string `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	req.Value = strings.TrimSpace(req.Value)
	if _, ok := thresholdSpecFor(req.Template, req.Macro); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown threshold macro for this template"})
		return
	}
	if req.Value != "" {
		if _, err := strconv.ParseFloat(req.Value, 64); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a threshold must be a number"})
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// Resolve the target value to write onto the template: the override, or the factory default on a
	// reset. Argus is the source of truth, so the store is updated first; the live-template write
	// follows (a failure there is reported, but the stored value still applies on the next reconcile).
	target := req.Value
	if req.Value == "" {
		if err := s.st.DeleteThresholdDefault(ctx, req.Template, req.Macro); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		factory, _ := provision.TemplateFactoryDefaults()
		target = factory[req.Template][req.Macro]
	} else {
		if err := s.st.SetThresholdDefault(ctx, req.Template, req.Macro, req.Value); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	if target != "" {
		if err := s.setTemplateMacro(ctx, req.Template, req.Macro, target); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "saved, but applying to Zabbix failed (will retry on restart): " + err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// setTemplateMacro writes one user macro onto a Zabbix template (create or update), used to push a
// global threshold default onto the template so linked hosts inherit it.
func (s *Server) setTemplateMacro(ctx context.Context, template, macro, value string) error {
	tmpls, err := s.zbx.Templates(ctx, []string{template})
	if err != nil {
		return err
	}
	if len(tmpls) == 0 {
		return nil // template not imported yet; the startup reconcile will apply the stored default
	}
	tid := tmpls[0].TemplateID
	cur, err := s.zbx.TemplateMacros(ctx, tid)
	if err != nil {
		return err
	}
	for _, m := range cur {
		if m.Macro == macro {
			if m.Value == value {
				return nil
			}
			return s.zbx.UpdateHostMacro(ctx, m.MacroID, value, 0)
		}
	}
	return s.zbx.CreateHostMacro(ctx, tid, zabbix.Macro{Macro: macro, Value: value, Type: 0})
}

// validCategories reports whether every name is a known sensor category (the canonical set). Used to
// validate a per-host sensor-order override (saved through the host-config PATCH).
func validCategories(cats []string) bool {
	for _, c := range cats {
		if _, ok := categoryOrderServer[c]; !ok {
			return false
		}
	}
	return true
}
