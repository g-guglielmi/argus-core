// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"argus/internal/auth"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Renamed sensors (DESIGN section 7s). A device that renames what it reports (Windows lists a service
// under a new display name after an upgrade) makes Zabbix's discovery lose the sensor under its old
// name, switch it off and delete it a week later with its history, and make a new sensor under the
// new name. Argus finds the likely pairs (lost and new in the same discovery round, on the same host
// and rule, their names alike) and asks: someone links each pair, or keeps it apart. Linking keeps
// the old sensor's readings in Argus's own database (hourly for a year, raw as far as Zabbix has them)
// and its closed incidents, and hands its priority, notes, runbook, actions and report entries to the
// new sensor; every chart, uptime and report reads across both names (zabbix/past.go).

const (
	renameLookback   = 2 * 24 * time.Hour // a sensor with readings this long before the loss isn't new
	renameKeepTrends = 400 * 24 * time.Hour
	renameKeepRaw    = 31 * 24 * time.Hour
	renameMinScore   = 0.3 // weaker pairs aren't suggested
	renameSureScore  = 0.6 // under this, the popup says to check the pair
	renameAction     = "Linked a renamed sensor"
)

// renameIndex is every rename decision, in memory: which sensors were renamed from which.
type renameIndex struct {
	mu      sync.RWMutex
	decided map[string]store.SensorRename // old id -> its decision (linked or kept apart)
	was     map[string]store.SensorRename // new id -> its newest link
	prev    map[string][]string           // new id -> the old sensors it was, nearest first
}

func (x *renameIndex) load(ctx context.Context, st *store.Store) error {
	rows, err := st.SensorRenames(ctx)
	if err != nil {
		return err
	}
	decided := map[string]store.SensorRename{}
	was := map[string]store.SensorRename{}
	direct := map[string][]string{}
	for _, r := range rows {
		decided[r.OldID] = r
		if r.NewID == "" {
			continue
		}
		direct[r.NewID] = append(direct[r.NewID], r.OldID)
		if w, ok := was[r.NewID]; !ok || r.At >= w.At {
			was[r.NewID] = r
		}
	}
	prev := map[string][]string{}
	for id := range direct {
		seen := map[string]bool{id: true}
		var chain []string
		queue := append([]string(nil), direct[id]...)
		for len(queue) > 0 {
			o := queue[0]
			queue = queue[1:]
			if seen[o] {
				continue
			}
			seen[o] = true
			chain = append(chain, o)
			queue = append(queue, direct[o]...)
		}
		prev[id] = chain
	}
	x.mu.Lock()
	x.decided, x.was, x.prev = decided, was, prev
	x.mu.Unlock()
	return nil
}

// linkedOld reports an old sensor that was linked to its new name (its row goes).
func (x *renameIndex) linkedOld(id string) bool {
	if x == nil {
		return false
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	r, ok := x.decided[id]
	return ok && r.NewID != ""
}

func (x *renameIndex) isDecided(id string) bool {
	if x == nil {
		return false
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	_, ok := x.decided[id]
	return ok
}

func (x *renameIndex) wasOf(id string) (store.SensorRename, bool) {
	if x == nil {
		return store.SensorRename{}, false
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	r, ok := x.was[id]
	return r, ok
}

func (x *renameIndex) chain(id string) []string {
	if x == nil {
		return nil
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.prev[id]
}

// renamePast hands the kept readings to the Zabbix client (zabbix.Past).
type renamePast struct {
	idx *renameIndex
	st  *store.Store
}

func (p renamePast) PastHistory(ctx context.Context, itemID string, from, to int64) []zabbix.PastPoint {
	olds := p.idx.chain(itemID)
	if len(olds) == 0 {
		return nil
	}
	rows, err := p.st.ArchivedHistory(ctx, olds, from, to)
	if err != nil {
		return nil
	}
	out := make([]zabbix.PastPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, zabbix.PastPoint{Clock: r.Clock, V: r.V})
	}
	return out
}

func (p renamePast) PastTrends(ctx context.Context, itemID string, from, to int64) []zabbix.PastTrend {
	olds := p.idx.chain(itemID)
	if len(olds) == 0 {
		return nil
	}
	rows, err := p.st.ArchivedTrends(ctx, olds, from, to)
	if err != nil {
		return nil
	}
	out := make([]zabbix.PastTrend, 0, len(rows))
	for _, r := range rows {
		out = append(out, zabbix.PastTrend{Clock: r.Clock, Min: r.Min, Avg: r.Avg, Max: r.Max, Num: r.Num})
	}
	return out
}

// startRenames loads the decisions, hands the kept readings to the Zabbix client and prunes them
// once a day.
func (s *Server) startRenames(ctx context.Context) {
	s.ren = &renameIndex{}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := s.ren.load(lctx, s.st); err != nil {
		s.logger.Warn("renamed sensors: load failed", "err", err)
	}
	cancel()
	s.zbx.SetPast(renamePast{idx: s.ren, st: s.st})
	go func() {
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			now := time.Now()
			pctx, cancel := context.WithTimeout(ctx, time.Minute)
			_ = s.st.PruneSensorArchive(pctx, now.Add(-renameKeepRaw).Unix(), now.Add(-renameKeepTrends).Unix())
			_ = s.ren.load(pctx, s.st)
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// renameWords are a name's words, lower case.
func renameWords(name string) []string {
	f := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	seen := map[string]bool{}
	var out []string
	for _, w := range f {
		lw := strings.ToLower(w)
		if !seen[lw] {
			seen[lw] = true
			out = append(out, lw)
		}
	}
	return out
}

// renameScore is how alike two names are, from 0 to 1: the weight of the words they share over the
// weight of all their words, a word weighing less the more names of the rule have it (every Citrix
// service says "Citrix", so that word says little). distinct are the shared words that weigh.
func renameScore(oldName, newName string, weight func(string) float64) (score float64, distinct []string) {
	a, b := renameWords(oldName), renameWords(newName)
	inA := map[string]bool{}
	for _, w := range a {
		inA[w] = true
	}
	var shared, union float64
	inB := map[string]bool{}
	for _, w := range b {
		inB[w] = true
		union += weight(w)
		if inA[w] {
			shared += weight(w)
			if weight(w) >= 0.5 {
				distinct = append(distinct, w)
			}
		}
	}
	for _, w := range a {
		if !inB[w] {
			union += weight(w)
		}
	}
	if union == 0 {
		return 0, nil
	}
	return shared / union, distinct
}

// renameWhy says why a pair looks like one sensor renamed, and whether to check it.
func renameWhy(oldName, newName string, score float64, distinct []string) (string, bool) {
	a, b := renameWords(oldName), renameWords(newName)
	inA, inB := map[string]bool{}, map[string]bool{}
	for _, w := range a {
		inA[w] = true
	}
	for _, w := range b {
		inB[w] = true
	}
	added, dropped := 0, 0
	for _, w := range b {
		if !inA[w] {
			added++
		}
	}
	for _, w := range a {
		if !inB[w] {
			dropped++
		}
	}
	words := func(n int) string {
		if n == 1 {
			return "a word"
		}
		return strconv.Itoa(n) + " words"
	}
	switch {
	case added == 0 && dropped == 0:
		return "the same words", false
	case dropped == 0:
		return "the new name adds " + words(added), score < renameSureScore
	case added == 0:
		return "the new name drops " + words(dropped), score < renameSureScore
	}
	if score < renameSureScore {
		// The shared words as the new name writes them.
		orig := map[string]string{}
		for _, w := range strings.FieldsFunc(newName, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			if _, ok := orig[strings.ToLower(w)]; !ok {
				orig[strings.ToLower(w)] = w
			}
		}
		q := make([]string, 0, len(distinct))
		for _, w := range distinct {
			q = append(q, `"`+orig[w]+`"`)
		}
		if len(q) == 0 {
			return "few words in common: check it", true
		}
		return "only " + strings.Join(q, " and ") + " in common: check it", true
	}
	return "most of the words in common", false
}

type renameSide struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Last int64  `json:"last,omitempty"` // an old sensor: when discovery last found it
}

type renamePair struct {
	Old     renameSide   `json:"old"`
	New     string       `json:"new"` // the suggested new sensor
	Why     string       `json:"why"`
	Check   bool         `json:"check,omitempty"`
	Removed int64        `json:"removed,omitempty"` // when Zabbix deletes the old one
	Options []renameSide `json:"options"`           // the new sensors of the same rule
}

// renameCand is a sensor of a host as the pairing sees it.
type renameCand struct {
	id, name, label, vt, parent string
	lost                        bool
	last, removed               int64
}

// renamePairs pairs a host's lost sensors with its new ones: within each discovery rule, the new
// sensors are those with no readings before the earliest loss; every pair is scored, the strongest
// taken first, each sensor once; pairs under renameMinScore aren't suggested. hasData says which of
// the given sensors had readings in [from, to].
func renamePairs(cands []renameCand, decided func(string) bool, taken func(string) bool,
	hasData func(ids []string, from, to int64) map[string]bool) []renamePair {
	byParent := map[string][]renameCand{}
	var parents []string
	for _, c := range cands {
		if c.parent == "" || c.parent == "0" {
			continue
		}
		if _, ok := byParent[c.parent]; !ok {
			parents = append(parents, c.parent)
		}
		byParent[c.parent] = append(byParent[c.parent], c)
	}
	sort.Strings(parents)
	var out []renamePair
	for _, p := range parents {
		group := byParent[p]
		var lost, others []renameCand
		var since int64
		for _, c := range group {
			if c.lost {
				if decided(c.id) {
					continue
				}
				lost = append(lost, c)
				if since == 0 || (c.last > 0 && c.last < since) {
					since = c.last
				}
			} else if !taken(c.id) {
				others = append(others, c)
			}
		}
		if len(lost) == 0 || len(others) == 0 || since == 0 {
			continue
		}
		ids := make([]string, 0, len(others))
		for _, c := range others {
			ids = append(ids, c.id)
		}
		had := hasData(ids, since-int64(renameLookback.Seconds()), since-3600)
		var fresh []renameCand
		for _, c := range others {
			if !had[c.id] {
				fresh = append(fresh, c)
			}
		}
		if len(fresh) == 0 {
			continue
		}
		// A word's weight: the fewer of the rule's names have it, the more it says.
		df := map[string]int{}
		for _, c := range group {
			for _, w := range renameWords(c.name) {
				df[w]++
			}
		}
		n := float64(len(group))
		weight := func(w string) float64 { return math.Log((n + 1) / float64(max(df[w], 1))) }
		type scored struct {
			o, f     int
			score    float64
			distinct []string
		}
		var all []scored
		for i, o := range lost {
			for j, f := range fresh {
				if o.vt != f.vt {
					continue
				}
				sc, d := renameScore(o.name, f.name, weight)
				if sc >= renameMinScore && len(d) > 0 {
					all = append(all, scored{i, j, sc, d})
				}
			}
		}
		sort.SliceStable(all, func(a, b int) bool { return all[a].score > all[b].score })
		usedO, usedF := map[int]bool{}, map[int]bool{}
		for _, sc := range all {
			if usedO[sc.o] || usedF[sc.f] {
				continue
			}
			usedO[sc.o], usedF[sc.f] = true, true
			o, f := lost[sc.o], fresh[sc.f]
			why, check := renameWhy(o.name, f.name, sc.score, sc.distinct)
			pair := renamePair{Old: renameSide{ID: o.id, Name: o.label, Last: o.last}, New: f.id, Why: why, Check: check, Removed: o.removed}
			for _, c := range fresh {
				if c.vt == o.vt {
					pair.Options = append(pair.Options, renameSide{ID: c.id, Name: c.label})
				}
			}
			out = append(out, pair)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Old.Name < out[j].Old.Name })
	return out
}

func renameCands(items []zabbix.Item) []renameCand {
	out := make([]renameCand, 0, len(items))
	for _, it := range items {
		c := renameCand{id: it.ItemID, name: it.Name, label: sensorLabel(it.Key, it.Name), vt: it.ValueType,
			parent: it.Discovery.Parent, lost: it.Discovery.Lost()}
		if _, l, _, _, ok := classifyItem(it.Key, it.Name); ok && l != "" {
			c.label = l
		}
		c.last, _ = strconv.ParseInt(it.Discovery.LastCheck, 10, 64)
		c.removed, _ = strconv.ParseInt(it.Discovery.TSDelete, 10, 64)
		out = append(out, c)
	}
	return out
}

// GET /api/hosts/{id}/renames: the host's sensors that look renamed, each with its likely new name.
func (s *Server) handleHostRenames(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	items, err := s.zbx.Items(ctx, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	pairs := s.hostRenamePairs(ctx, items)
	if pairs == nil {
		pairs = []renamePair{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"host": s.hostLabel(ctx, r.PathValue("id")), "pairs": pairs})
}

func (s *Server) hostRenamePairs(ctx context.Context, items []zabbix.Item) []renamePair {
	anyLost := false
	for _, it := range items {
		if it.Discovery.Lost() && !s.ren.isDecided(it.ItemID) {
			anyLost = true
			break
		}
	}
	if !anyLost {
		return nil
	}
	raw := zabbix.WithoutPast(ctx)
	hasData := func(ids []string, from, to int64) map[string]bool {
		had := map[string]bool{}
		rows, err := s.zbx.TrendRows(raw, ids, from, to)
		if err != nil {
			for _, id := range ids { // can't tell: nothing is new
				had[id] = true
			}
			return had
		}
		for _, r := range rows {
			had[r.ItemID] = true
		}
		return had
	}
	taken := func(id string) bool { _, ok := s.ren.wasOf(id); return ok }
	return renamePairs(renameCands(items), s.ren.isDecided, taken, hasData)
}

// POST /api/hosts/{id}/renames (admin, helpdesk): {links: [{old, new}], apart: [old]}.
func (s *Server) handleSaveRenames(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Links []struct {
			Old string `json:"old"`
			New string `json:"new"`
		} `json:"links"`
		Apart []string `json:"apart"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || len(req.Links)+len(req.Apart) == 0 || len(req.Links)+len(req.Apart) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	hostID := r.PathValue("id")
	items, err := s.zbx.Items(ctx, hostID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	byID := map[string]zabbix.Item{}
	for _, it := range items {
		byID[it.ItemID] = it
	}
	label := func(it zabbix.Item) string {
		if _, l, _, _, ok := classifyItem(it.Key, it.Name); ok && l != "" {
			return l
		}
		return sensorLabel(it.Key, it.Name)
	}
	// Every old sensor must be a lost one of this host, every new one a live one, each used once.
	usedNew := map[string]bool{}
	usedOld := map[string]bool{}
	bad := func(msg string) { writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg}) }
	for _, l := range req.Links {
		o, ok1 := byID[l.Old]
		n, ok2 := byID[l.New]
		if !ok1 || !ok2 || !o.Discovery.Lost() || n.Discovery.Lost() || usedOld[l.Old] || usedNew[l.New] {
			bad("a pair isn't a lost sensor and a new one of this host")
			return
		}
		if o.ValueType != n.ValueType {
			bad(label(o) + " and " + label(n) + " read different kinds of values")
			return
		}
		if _, linked := s.ren.wasOf(l.New); linked {
			bad(label(n) + " is linked to another sensor already")
			return
		}
		usedOld[l.Old], usedNew[l.New] = true, true
	}
	for _, id := range req.Apart {
		o, ok := byID[id]
		if !ok || !o.Discovery.Lost() || usedOld[id] {
			bad("a sensor to keep apart isn't a lost sensor of this host")
			return
		}
		usedOld[id] = true
	}
	by := ""
	if u, ok := auth.UserFrom(r.Context()); ok {
		by = userLabel(u)
	}
	host := s.hostLabel(ctx, hostID)
	now := time.Now().Unix()
	linked := 0
	for _, l := range req.Links {
		o, n := byID[l.Old], byID[l.New]
		if err := s.keepRenamed(ctx, hostID, host, o, n); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Couldn't keep " + label(o) + "'s history: " + s.errText(r, err)})
			_ = s.ren.load(context.WithoutCancel(ctx), s.st)
			return
		}
		until, _ := strconv.ParseInt(o.Discovery.LastCheck, 10, 64)
		if err := s.st.PutSensorRename(ctx, store.SensorRename{OldID: o.ItemID, NewID: n.ItemID, HostID: hostID, OldName: label(o),
			OldKey: o.Key, Until: until, ByName: by, At: now}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
			return
		}
		linked++
		noteChange(r, store.Change{Category: "hosts", Action: renameAction, Object: host + " · " + label(n),
			Detail: "was " + label(o), HostIDs: []string{hostID}, ItemIDs: []string{n.ItemID}})
	}
	for _, id := range req.Apart {
		o := byID[id]
		until, _ := strconv.ParseInt(o.Discovery.LastCheck, 10, 64)
		if err := s.st.PutSensorRename(ctx, store.SensorRename{OldID: id, HostID: hostID, OldName: label(o), OldKey: o.Key,
			Until: until, ByName: by, At: now}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
			return
		}
		noteChange(r, store.Change{Category: "hosts", Action: "Kept a lost sensor apart", Object: host + " · " + label(o),
			Detail: "not renamed: it goes with its history", HostIDs: []string{hostID}, ItemIDs: []string{id}})
	}
	if err := s.ren.load(ctx, s.st); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	// What was read before the link is read again with the old readings.
	availCache.mu.Lock()
	uptimeDaysCache.mu.Lock()
	for _, l := range req.Links {
		delete(availCache.m, l.New)
		delete(uptimeDaysCache.m, l.New)
	}
	uptimeDaysCache.mu.Unlock()
	availCache.mu.Unlock()
	forgetRunbooks()
	forgetActions()
	if s.census != nil {
		s.census.invalidate()
	}
	writeJSON(w, http.StatusOK, map[string]any{"linked": linked, "apart": len(req.Apart)})
}

// keepRenamed keeps an old sensor's readings and closed incidents for its new one, and hands it what
// Argus keeps about the old one.
func (s *Server) keepRenamed(ctx context.Context, hostID, hostName string, o, n zabbix.Item) error {
	raw := zabbix.WithoutPast(ctx)
	now := time.Now()
	if numericValueType(o.ValueType) {
		trends, err := s.zbx.Trends(raw, o.ItemID, now.Add(-renameKeepTrends).Unix(), now.Unix())
		if err != nil {
			return err
		}
		rows := make([]zabbix.TrendRow, 0, len(trends))
		for _, t := range trends {
			rows = append(rows, zabbix.TrendRow{ItemID: o.ItemID, Clock: t.Clock, Num: t.Num, ValueAvg: t.ValueAvg})
		}
		// Whole-number up/down hours are read again from the raw checks while Zabbix still has them.
		rows, _ = s.exactUptime(raw, rows, map[string]string{o.ItemID: o.ValueType})
		at := make([]store.ArchTrend, 0, len(trends))
		for i, t := range trends {
			at = append(at, store.ArchTrend{ItemID: o.ItemID, Clock: atoi64(t.Clock), Min: fz(t.ValueMin), Avg: fz(rows[i].ValueAvg),
				Max: fz(t.ValueMax), Num: atoi64(rows[i].Num)})
		}
		hist, err := s.zbx.History(raw, o.ItemID, atoi(o.ValueType), now.Add(-renameKeepRaw).Unix(), now.Unix())
		if err != nil {
			return err
		}
		ah := make([]store.ArchPoint, 0, len(hist))
		for _, h := range hist {
			ah = append(ah, store.ArchPoint{ItemID: o.ItemID, Clock: atoi64(h.Clock), V: fz(h.Value)})
		}
		if err := s.st.PutSensorArchive(ctx, ah, at); err != nil {
			return err
		}
	}
	// Its closed incidents: Zabbix deletes them with its triggers.
	if incs, err := s.collectIncidents(raw, []string{hostID}, []string{o.ItemID}, now.Add(-argusIncidentKeep).Unix(), incidentsMaxRows); err == nil {
		var keep []store.ArgusIncident
		for _, v := range incs {
			if v.Argus || v.End == 0 {
				continue
			}
			keep = append(keep, store.ArgusIncident{EventID: v.EventID, HostID: hostID, HostName: hostName, ItemID: n.ItemID,
				Name: v.Name, Severity: v.Severity, Reason: v.Reason, StartedAt: v.Start, EndedAt: v.End})
		}
		if err := s.st.PutArchivedIncidents(ctx, keep); err != nil {
			return err
		}
	}
	return s.st.MoveSensor(ctx, o.ItemID, n.ItemID)
}

func fz(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

// renamedView is the name a sensor had before it was renamed, for its row.
type renamedView struct {
	Name  string `json:"name"`
	Until int64  `json:"until"`
	By    string `json:"by,omitempty"`
	At    int64  `json:"at"`
}

func (s *Server) renamedOf(itemID string) *renamedView {
	r, ok := s.ren.wasOf(itemID)
	if !ok {
		return nil
	}
	return &renamedView{Name: r.OldName, Until: r.Until, By: r.ByName, At: r.At}
}

// renameMarks are the chart markers of the renames of the given sensors: when the old name was last
// found.
func (s *Server) renameMarks(items map[string]bool, from, to int64) []chartMark {
	var out []chartMark
	for id := range items {
		for _, old := range s.ren.chain(id) {
			s.ren.mu.RLock()
			r := s.ren.decided[old]
			s.ren.mu.RUnlock()
			if r.Until >= from && r.Until <= to {
				out = append(out, chartMark{T: r.Until, Kind: "change", What: "Renamed: was " + r.OldName, Who: r.ByName})
			}
		}
	}
	return out
}
