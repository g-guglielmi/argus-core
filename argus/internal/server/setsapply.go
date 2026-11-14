// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"argus/internal/auth"
	"argus/internal/provision"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Sensor sets on a host (DESIGN 5b): a host carries any number of the catalog's sets (provision
// sets.go), and its linked templates are the record of which. This file applies a save's changes:
// sets that come, sets that go, and the settings of the ones that are on.

// setDesired is the client's desired state for one sensor set in a host settings save.
type setDesired struct {
	On bool `json:"on"`
	// Enabled is On's older name, from a tab still running the bundle that knew sets as add-ons.
	Enabled bool              `json:"enabled,omitempty"`
	Macros  map[string]string `json:"macros"`
	// Accepted lists the fields whose option's terms (provision.Terms) the user accepted in this save.
	Accepted []string `json:"accepted,omitempty"`
}

// accepted reports whether this save accepted the terms of the field's option.
func (d setDesired) accepted(macro string) bool {
	return slices.Contains(d.Accepted, macro)
}

// setsRefusal is a change to a host's sets that the rules refuse (a second system, a required
// setting left blank): the user's to fix, so it answers 400 where a Zabbix failure answers 502.
type setsRefusal struct{ msg string }

func (e setsRefusal) Error() string { return e.msg }

// setsStatus is the HTTP status a failed sets change answers with.
func setsStatus(err error) int {
	if _, ok := err.(setsRefusal); ok {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

// hostClass is a host's device class, the zero Class for a host Argus didn't create.
func (s *Server) hostClass(ctx context.Context, hostID string) provision.Class {
	if id, ok, _ := s.st.GetDeviceClass(ctx, hostID); ok {
		if c, ok := provision.ClassByID(id); ok {
			return c
		}
	}
	return provision.Class{}
}

// applySets brings a host's sensor sets to the desired state: it links the sets that come (with the
// interface they read through, their presets and their settings), unlinks the ones that go (their
// sensors and history go with them, and their settings unless a set that stays reads the same) and
// sets what was sent for the ones that are on. A set not in desired is left as it is. Everything is
// checked before anything is written: the catalog's rules on the sets the host ends with, and each
// setting's value. A blank setting reverts to the template's default; a blank secret is unchanged.
func (s *Server) applySets(ctx context.Context, hostID string, desired map[string]setDesired) error {
	names, err := s.zbx.HostLinkedTemplateNames(ctx, hostID)
	if err != nil {
		return err
	}
	class := s.hostClass(ctx, hostID)
	cur := map[string]zabbix.HostMacro{}
	hm, err := s.zbx.HostMacros(ctx, hostID)
	if err != nil {
		return err
	}
	for _, m := range hm {
		cur[m.Macro] = m
	}

	var final []string
	var added, removed, edited []provision.Set
	for _, st := range provision.Sets() {
		on := slices.Contains(names, st.Template)
		d, sent := desired[st.ID]
		switch {
		case sent && d.On && !on:
			added = append(added, st)
			final = append(final, st.ID)
		case sent && !d.On && on:
			removed = append(removed, st)
		case on:
			final = append(final, st.ID)
			if sent {
				edited = append(edited, st)
			}
		}
	}
	if len(added)+len(removed) > 0 {
		if err := provision.CheckSets(final, class.ID, class.Internal); err != nil {
			return setsRefusal{err.Error()}
		}
	}
	live := append(append([]provision.Set{}, added...), edited...)
	for _, st := range live {
		d := desired[st.ID]
		isNew := !slices.Contains(names, st.Template)
		for _, ms := range st.Macros {
			v, sent := d.Macros[ms.Macro]
			v = strings.TrimSpace(v)
			c, has := cur[ms.Macro]
			has = has && (ms.Secret || strings.TrimSpace(c.Value) != "")
			// A required setting is given, or already on the host and left alone (a secret sent
			// blank is left alone: it can't be read back to show).
			if ms.Required && v == "" && (isNew || sent) && !(has && (ms.Secret || !sent)) {
				return setsRefusal{ms.Label + " is required for " + st.Label}
			}
			if !sent {
				continue
			}
			// The values reach an external check's command line on the probe.
			if err := provision.ValidateMacroValue(ms, v); err != nil {
				return setsRefusal{err.Error()}
			}
			if err := termsRefusal(st.Label, ms, v, d, cur); err != nil {
				return setsRefusal{err.Error()}
			}
		}
	}

	ids := map[string]string{}
	if len(added)+len(removed) > 0 {
		var tnames []string
		for _, st := range append(append([]provision.Set{}, added...), removed...) {
			tnames = append(tnames, st.Template)
		}
		if ids, err = s.zbx.TemplateIDsByName(ctx, tnames); err != nil {
			// A set's template isn't in Zabbix yet (a fresh install, an import that failed): import
			// the templates, then look again.
			if rerr := provision.Reconcile(ctx, s.zbx, s.st, s.logger); rerr != nil {
				s.logger.Warn("sets: template import failed", "err", rerr)
			}
			if ids, err = s.zbx.TemplateIDsByName(ctx, tnames); err != nil {
				return err
			}
		}
	}

	// The interface each new set reads through, before anything is taken away: a set that can't be
	// read (no SNMP credentials to use) stops the save while the host is still whole.
	for _, st := range added {
		if st.Iface == provision.IfaceNone {
			continue
		}
		hd, err := s.zbx.HostDetail(ctx, hostID)
		if err != nil {
			return err
		}
		if msg := s.ensureIface(ctx, hd, st.Iface, st.Label, nil); msg != "" {
			return setsRefusal{msg}
		}
	}

	// Sets that go, before the ones that come: a host swaps one system set for another in one save.
	if len(removed) > 0 {
		gone := s.itemsOfSets(ctx, hostID, removed, names)
		markItemsRemoved(gone, time.Now())
		for _, st := range removed {
			if err := s.zbx.UnlinkHostTemplate(ctx, hostID, ids[st.Template]); err != nil {
				unmarkItems(gone)
				return err
			}
		}
		// Their settings go with them (a credential doesn't outlive what it was for), unless a set
		// the host keeps reads the same one: two UniFi kinds share their controller's.
		kept := map[string]bool{}
		for _, id := range final {
			st, _ := provision.SetByID(id)
			for _, ms := range st.Macros {
				kept[ms.Macro] = true
			}
			for _, pm := range st.HostMacros {
				kept[pm.Macro] = true
			}
		}
		for _, st := range removed {
			var own []string
			for _, ms := range st.Macros {
				own = append(own, ms.Macro)
			}
			for _, pm := range st.HostMacros {
				own = append(own, pm.Macro)
			}
			for _, name := range own {
				if m, has := cur[name]; has && !kept[name] {
					if err := s.zbx.DeleteHostMacros(ctx, m.MacroID); err != nil {
						return err
					}
					delete(cur, name)
				}
			}
		}
	}

	for _, st := range added {
		for _, pm := range st.HostMacros {
			if _, has := cur[pm.Macro]; has {
				continue
			}
			if err := s.zbx.CreateHostMacro(ctx, hostID, zabbix.Macro{Macro: pm.Macro, Value: pm.Value}); err != nil {
				return err
			}
			cur[pm.Macro] = zabbix.HostMacro{Macro: pm.Macro, Value: pm.Value}
		}
	}
	// Settings before the link, so a new set's first poll and discovery already read them.
	for _, st := range live {
		d := desired[st.ID]
		for _, ms := range st.Macros {
			v, sent := d.Macros[ms.Macro]
			if !sent {
				continue
			}
			v = strings.TrimSpace(v)
			existing, has := cur[ms.Macro]
			mType := 0
			if ms.Secret {
				mType = 1
			}
			switch {
			case ms.Secret && v == "":
				// Blank secret = unchanged (its stored value can't be read back to compare).
			case v == "":
				if has {
					if err := s.zbx.DeleteHostMacros(ctx, existing.MacroID); err != nil {
						return err
					}
					delete(cur, ms.Macro)
				}
			case has:
				if ms.Secret || existing.Value != v {
					if err := s.zbx.UpdateHostMacro(ctx, existing.MacroID, v, mType); err != nil {
						return err
					}
				}
			default:
				if err := s.zbx.CreateHostMacro(ctx, hostID, zabbix.Macro{Macro: ms.Macro, Value: v, Type: mType}); err != nil {
					return err
				}
			}
		}
		// An ignored VM's "not running" problem can never recover on its own: the next poll drops
		// the VM from the blob, discovery disables its state item, and no recovery value ever
		// arrives. The triggers allow manual close, so close those problems here (best-effort).
		if v, sent := d.Macros["{$XCP.VM.IGNORE}"]; sent && st.ID == "xcpng" {
			s.closeIgnoredVMProblems(ctx, hostID, v)
		}
	}
	for _, st := range added {
		if err := s.zbx.LinkHostTemplate(ctx, hostID, ids[st.Template]); err != nil {
			return err
		}
	}
	if len(added) > 0 {
		// Kick the new sets' discovery rules, so per-instance sensors appear in seconds.
		s.scheduleDiscovery(hostID)
	}
	if len(added)+len(removed) > 0 && s.census != nil {
		s.census.invalidate()
	}
	return nil
}

// termsRefusal says why a field can't be set to v: an option under a third party's terms is chosen
// only with them accepted in the same save. One the host already has stays as it is.
func termsRefusal(set string, ms provision.MacroSpec, v string, d setDesired, cur map[string]zabbix.HostMacro) error {
	if ms.Terms == nil || v != ms.Terms.Value || d.accepted(ms.Macro) {
		return nil
	}
	if c, has := cur[ms.Macro]; has && c.Value == v {
		return nil
	}
	return fmt.Errorf("%s: accept %s to choose %s", set, ms.Terms.Title, ms.OptionLabel(v))
}

// itemsOfSets is the host's sensors that belong to these sets: those whose key is one of the sets'
// templates' and of no other template the host has (linked is its templates, by name).
func (s *Server) itemsOfSets(ctx context.Context, hostID string, sets []provision.Set, linked []string) []string {
	items, err := s.zbx.Items(ctx, hostID)
	if err != nil {
		return nil
	}
	going := map[string]bool{}
	for _, st := range sets {
		going[st.Template] = true
	}
	stays, theirs := map[string]bool{}, map[string]bool{}
	for _, t := range linked {
		for b := range provision.TemplateKeyBases(t) {
			if going[t] {
				theirs[b] = true
			} else {
				stays[b] = true
			}
		}
	}
	var out []string
	for _, it := range items {
		if b, _ := splitKey(it.Key); theirs[b] && !stays[b] {
			out = append(out, it.ItemID)
		}
	}
	return out
}

// removedItems marks the sensors removed with their set lately (item id -> when). Zabbix drops a
// removed sensor's problems with it, which would read as recoveries: the notifier closes their
// alerts without a RESOLVED (notifyTick), like a deleted host's.
var removedItems sync.Map

func markItemsRemoved(ids []string, now time.Time) {
	removedItems.Range(func(k, v any) bool {
		if now.Sub(v.(time.Time)) > deletedMarkKeep {
			removedItems.Delete(k)
		}
		return true
	})
	for _, id := range ids {
		removedItems.Store(id, now)
	}
}

func unmarkItems(ids []string) {
	for _, id := range ids {
		removedItems.Delete(id)
	}
}

func itemRemoved(id string) bool {
	if id == "" {
		return false
	}
	v, ok := removedItems.Load(id)
	return ok && time.Since(v.(time.Time)) <= deletedMarkKeep
}

// closeRemovedAlert notes on an alert's timeline that it closed with its sensor, nothing sent.
func closeRemovedAlert(ctx context.Context, st *store.Store, eventID string) {
	tlog(ctx, st, eventID, "removed", "", "", "nothing was sent", 0)
	forgetTimelineState(eventID)
}

// setHostClass changes what a host is shown as: its class's name and icon in the tree, and what goes
// by class (its links, the runbooks and actions of its kind). Its sensors stay as they are: they
// are its sets. Admin only; an Argus-managed host keeps its class. Returns a status and a message,
// or 0 when done (or nothing to do).
func (s *Server) setHostClass(ctx context.Context, r *http.Request, hostID, classID string) (int, string) {
	old := s.hostClass(ctx, hostID)
	if classID == old.ID {
		return 0, ""
	}
	if u, ok := auth.UserFrom(r.Context()); !ok || u.Role != "admin" {
		return http.StatusForbidden, "only an admin changes a host's class"
	}
	if old.Internal {
		return http.StatusBadRequest, "this host is managed by Argus; its class can't be changed"
	}
	nc, ok := provision.ClassByID(classID)
	if !ok || nc.Internal {
		return http.StatusBadRequest, "unknown device class"
	}
	if err := s.st.SetDeviceClass(ctx, hostID, nc.ID, "manual"); err != nil {
		return http.StatusInternalServerError, "could not save the device class"
	}
	// Keep the Zabbix-side argus.class tag aligned (preserving argus.source). Argus's own record
	// above is the one it reads, so this is best-effort, for the raw Zabbix view.
	if err := s.zbx.SetHostTag(ctx, hostID, "argus.class", nc.ID); err != nil {
		s.logger.Warn("sets: could not update the argus.class tag", "host", hostID, "err", err)
	}
	return 0, ""
}
