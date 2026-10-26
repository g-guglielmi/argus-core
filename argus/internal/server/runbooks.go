// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"argus/internal/auth"
	"argus/internal/provision"
	"argus/internal/store"
)

// Runbook notes (DESIGN section 7o): what someone should do when a sensor is in trouble, written for
// one sensor or for every sensor of a kind on hosts of a class (a sensor's own wins). The first line
// shows under its problem in the lists and on the host page; the whole note goes out in the alerts of
// channels with "Runbook notes" on, like "Who to call".

const runbookMax = 1000 // runes

// runbookView is a sensor's runbook as the views carry it.
type runbookView struct {
	Text  string `json:"text"`
	Scope string `json:"scope"` // item | kind
}

// keyKind is the kind of sensor an item key is: its first parameter, the instance (a mount, a NIC, a
// URL's id), as "*" ("vfs.fs.size[/data,pused]" -> "vfs.fs.size[*,pused]", "icmpping" stays).
func keyKind(key string) string {
	i := strings.IndexByte(key, '[')
	if i < 0 || !strings.HasSuffix(key, "]") {
		return key
	}
	params := key[i+1 : len(key)-1]
	rest := ""
	if j := strings.IndexByte(params, ','); j >= 0 {
		rest = params[j:]
	}
	return key[:i] + "[*" + rest + "]"
}

// runbookIndex finds a sensor's runbook: its own, else its kind's on its host's class.
type runbookIndex struct {
	items   map[string]store.Runbook
	kinds   map[string]store.Runbook // class|keyKind
	classes map[string]string        // host -> class
}

func (ix runbookIndex) find(hostID, itemID, key string) *store.Runbook {
	if r, ok := ix.items[itemID]; ok && itemID != "" {
		return &r
	}
	if c := ix.classes[hostID]; c != "" {
		if r, ok := ix.kinds[c+"|"+keyKind(key)]; ok {
			return &r
		}
	}
	return nil
}

func (ix runbookIndex) view(hostID, itemID, key string) *runbookView {
	if r := ix.find(hostID, itemID, key); r != nil {
		return &runbookView{Text: r.Text, Scope: r.Scope}
	}
	return nil
}

var runbookCache struct {
	sync.Mutex
	at time.Time
	ix runbookIndex
}

func forgetRunbooks() {
	runbookCache.Lock()
	runbookCache.at = time.Time{}
	runbookCache.Unlock()
}

// loadRunbooks reads the runbooks and the hosts' classes (kept 15 seconds: every list asks).
func loadRunbooks(ctx context.Context, st *store.Store) runbookIndex {
	runbookCache.Lock()
	defer runbookCache.Unlock()
	if !runbookCache.at.IsZero() && time.Since(runbookCache.at) < 15*time.Second {
		return runbookCache.ix
	}
	ix := runbookIndex{items: map[string]store.Runbook{}, kinds: map[string]store.Runbook{}, classes: map[string]string{}}
	if rbs, err := st.Runbooks(ctx); err == nil {
		for _, r := range rbs {
			if r.Scope == "item" {
				ix.items[r.ItemID] = r
			} else {
				ix.kinds[r.ClassID+"|"+r.KeyKind] = r
			}
		}
	}
	if len(ix.kinds) > 0 {
		if cs, err := st.DeviceClasses(ctx); err == nil {
			ix.classes = cs
		}
	}
	runbookCache.at, runbookCache.ix = time.Now(), ix
	return ix
}

// runbookFor is the target a request names: the sensor, its host, its class and what its kind reads as.
type runbookTarget struct {
	itemID, hostID, key, sensor, classID, classLabel string
}

func (s *Server) runbookTarget(ctx context.Context, itemID string) (runbookTarget, bool) {
	it, err := s.zbx.Item(ctx, itemID)
	if err != nil || it == nil {
		return runbookTarget{}, false
	}
	t := runbookTarget{itemID: itemID, hostID: it.HostID, key: it.Key, sensor: it.Name}
	if rows, err := s.sensorCensus(ctx); err == nil {
		for _, r := range rows {
			if r.ItemID == itemID {
				if r.Label != "" {
					t.sensor = r.Label
				}
				break
			}
		}
	}
	if cs, err := s.st.DeviceClasses(ctx); err == nil {
		t.classID = cs[it.HostID]
		if c, ok := provision.ClassByID(t.classID); ok {
			t.classLabel = c.Label
		}
	}
	return t, true
}

// GET /api/runbooks/item/{id}: the sensor's own runbook and its kind's, what the kind covers and how
// many sensors it reaches now.
func (s *Server) handleItemRunbook(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	t, ok := s.runbookTarget(ctx, r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sensor not found"})
		return
	}
	rbs, err := s.st.Runbooks(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	type rbView struct {
		Text      string `json:"text"`
		By        string `json:"by,omitempty"`
		UpdatedAt int64  `json:"updated_at"`
	}
	out := map[string]any{"sensor": t.sensor, "class_id": t.classID, "class_label": t.classLabel, "kind_count": 0}
	kind := keyKind(t.key)
	for _, rb := range rbs {
		v := rbView{Text: rb.Text, By: rb.ByName, UpdatedAt: rb.UpdatedAt}
		if rb.Scope == "item" && rb.ItemID == t.itemID {
			out["item"] = v
		}
		if rb.Scope == "kind" && t.classID != "" && rb.ClassID == t.classID && rb.KeyKind == kind {
			out["kind"] = v
		}
	}
	if t.classID != "" {
		cs, _ := s.st.DeviceClasses(ctx)
		n := 0
		if rows, err := s.sensorCensus(ctx); err == nil {
			for _, row := range rows {
				if cs[row.HostID] == t.classID && keyKind(row.key) == kind {
					n++
				}
			}
		}
		out["kind_count"] = n
	}
	writeJSON(w, http.StatusOK, out)
}

// PUT /api/runbooks/item/{id} (admin, helpdesk): {scope: item|kind, text}; an empty text removes it.
func (s *Server) handleSaveRunbook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scope string `json:"scope"`
		Text  string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil || (req.Scope != "item" && req.Scope != "kind") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	text := strings.TrimSpace(strings.ReplaceAll(req.Text, "\r\n", "\n"))
	if len([]rune(text)) > runbookMax {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a runbook can be up to 1000 characters"})
		return
	}
	for _, c := range text {
		if unicode.IsControl(c) && c != '\n' && c != '\t' {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the runbook has characters it can't have"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	t, ok := s.runbookTarget(ctx, r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sensor not found"})
		return
	}
	rb := store.Runbook{Scope: req.Scope, Text: text}
	object := ""
	if req.Scope == "item" {
		rb.ItemID = t.itemID
		object = s.hostLabel(ctx, t.hostID) + " · " + t.sensor
		rb.Label = object
	} else {
		if t.classID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this host has no class, so its runbook can be for this sensor only"})
			return
		}
		rb.ClassID, rb.KeyKind = t.classID, keyKind(t.key)
		object = "every " + t.sensor + " sensor on " + t.classLabel + " hosts"
		rb.Label = object
	}
	if u, ok := auth.UserFrom(r.Context()); ok {
		rb.ByName = userLabel(u)
	}
	var err error
	action := "Wrote a runbook"
	if text == "" {
		err = s.st.DeleteRunbook(ctx, rb.Scope, rb.ItemID, rb.ClassID, rb.KeyKind)
		action = "Removed a runbook"
	} else {
		err = s.st.SaveRunbook(ctx, rb)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	forgetRunbooks()
	c := store.Change{Category: "hosts", Action: action, Object: object, Detail: firstLine(text)}
	if req.Scope == "item" {
		c.HostIDs, c.ItemIDs = []string{t.hostID}, []string{t.itemID}
	}
	noteChange(r, c)
	s.handleItemRunbook(w, r)
}

// hostLabel is a host's name, or its id when Zabbix can't say.
func (s *Server) hostLabel(ctx context.Context, hostID string) string {
	if idx, err := s.hostIndex(ctx); err == nil {
		if h, ok := idx[hostID]; ok {
			return h.Name
		}
	}
	return "host " + hostID
}

// firstLine is a note's first line, as the lists show it.
func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}
