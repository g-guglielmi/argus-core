// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"argus/internal/auth"
	"argus/internal/provision"
	"argus/internal/store"
	"argus/internal/unifi"
	"argus/internal/zabbix"
)

// Import (DESIGN section 7h): hosts from a spreadsheet or from PRTG. The browser reads the file (or
// asks Argus to read PRTG, importprtg.go) into rows; Argus checks every row against what exists (its
// class, site and probe, whether it is already monitored, the inputs its class needs) before anything
// is created. The import then runs in the background, one host at a time, through the same path as
// Add device, and the change log gets one entry for it.

// importRow is one device to import, as the file or PRTG gave it.
type importRow struct {
	Row      int               `json:"row"`           // its line in the file
	Ref      string            `json:"ref,omitempty"` // its PRTG object id
	Name     string            `json:"name"`
	Address  string            `json:"address"` // an IP address or a DNS name
	Class    string            `json:"class"`   // a class id or its label
	Site     string            `json:"site"`    // the group it goes in
	Probe    string            `json:"probe"`   // a probe's name, "server", or "" for the site's probe
	Tags     string            `json:"tags"`    // comma- or semicolon-separated
	AssetTag string            `json:"asset_tag"`
	Location string            `json:"location"`
	MAC      string            `json:"mac"`    // a UniFi device's MAC ({$UNIFI.MAC})
	Macros   map[string]string `json:"macros"` // the class's inputs, by macro ({$NUT.UPS})
}

type importProblem struct {
	Field string `json:"field"` // name | address | class | site | probe | tags | macro:{$X} | macros
	Msg   string `json:"msg"`
}

// importCheck is what Argus makes of a row: ready to import, skipped (already monitored), or to fix.
type importCheck struct {
	Row       int                   `json:"row"`
	Status    string                `json:"status"` // ready | skip | fix
	Problems  []importProblem       `json:"problems,omitempty"`
	Note      string                `json:"note,omitempty"` // "already monitored as web1", "new group site4/Printers"
	ClassID   string                `json:"class_id,omitempty"`
	Class     string                `json:"class,omitempty"` // its label
	Probe     string                `json:"probe,omitempty"` // the probe it goes to ("Server" for the core)
	Missing   []provision.MacroSpec `json:"missing,omitempty"`
	FromUniFi bool                  `json:"from_unifi,omitempty"` // its UniFi inputs come from a saved controller
}

const maxImportRows = 5000

// importEnv is what a check compares rows with, read once per check.
type importEnv struct {
	techNames map[string]string // lowercased technical name -> visible name
	visible   map[string]string // lowercased visible name -> visible name
	atIP      map[string][]ipHost
	proxies   map[string]zabbix.Proxy // lowercased name
	groups    map[string]bool
	tags      map[string]string // lowercased -> as written
	unifi     *unifiLookup
}

type ipHost struct{ name, class string }

// loadImportEnv reads the hosts, probes, groups and tags a check needs.
func (s *Server) loadImportEnv(ctx context.Context, needUniFi bool) (*importEnv, error) {
	env := &importEnv{techNames: map[string]string{}, visible: map[string]string{}, atIP: map[string][]ipHost{}, proxies: map[string]zabbix.Proxy{}, groups: map[string]bool{}, tags: map[string]string{}}
	hosts, err := s.zbx.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, h := range hosts {
		env.visible[strings.ToLower(h.Name)] = h.Name
		names[h.HostID] = h.Name
	}
	tech, err := s.zbx.HostTechNames(ctx)
	if err != nil {
		return nil, err
	}
	for id, t := range tech {
		env.techNames[strings.ToLower(t)] = names[id]
	}
	classes, _ := s.st.DeviceClasses(ctx)
	if ips, err := s.zbx.HostIPs(ctx); err == nil {
		for id, ip := range ips {
			env.atIP[ip] = append(env.atIP[ip], ipHost{name: names[id], class: classes[id]})
		}
	}
	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range proxies {
		env.proxies[strings.ToLower(p.Name)] = p
	}
	if gs, err := s.zbx.HostGroups(ctx); err == nil {
		for _, g := range gs {
			env.groups[g.Name] = true
		}
	}
	if ts, err := s.st.ListTags(ctx); err == nil {
		for _, t := range ts {
			env.tags[strings.ToLower(t.Name)] = t.Name
		}
	}
	if needUniFi {
		env.unifi = s.unifiLookup(ctx)
	}
	return env, nil
}

// --- UniFi: the saved controllers fill a UniFi device's inputs, like a sweep's adoption ---

type unifiLookup struct {
	ctls  []store.UniFiController // with their keys
	byIP  map[string]unifiHit
	byMAC map[string]unifiHit
}

type unifiHit struct {
	ctl int // index into ctls
	dev unifi.Device
}

var unifiImportCache struct {
	mu  sync.Mutex
	at  time.Time
	val *unifiLookup
}

// unifiLookup reads the saved controllers' devices (cached for 5 minutes: a check runs on every
// edit). A controller the core can't reach just contributes nothing.
func (s *Server) unifiLookup(ctx context.Context) *unifiLookup {
	unifiImportCache.mu.Lock()
	defer unifiImportCache.mu.Unlock()
	if unifiImportCache.val != nil && time.Since(unifiImportCache.at) < 5*time.Minute {
		return unifiImportCache.val
	}
	l := &unifiLookup{byIP: map[string]unifiHit{}, byMAC: map[string]unifiHit{}}
	list, _ := s.st.ListUniFiControllers(ctx)
	for _, c := range list {
		ctl, err := s.st.UniFiControllerByID(ctx, c.ID)
		if err != nil || ctl.APIKey == "" {
			continue
		}
		l.ctls = append(l.ctls, *ctl)
		i := len(l.ctls) - 1
		c2, cancel := context.WithTimeout(ctx, 15*time.Second)
		devs, err := unifi.Sweep(c2, ctl.URL, ctl.APIKey, controllerOptions(ctl))
		cancel()
		if err != nil {
			continue
		}
		for _, d := range devs {
			if d.IP != "" {
				l.byIP[d.IP] = unifiHit{ctl: i, dev: d}
			}
			if m := normMAC(d.MAC); m != "" {
				l.byMAC[m] = unifiHit{ctl: i, dev: d}
			}
		}
	}
	unifiImportCache.val, unifiImportCache.at = l, time.Now()
	return l
}

// fill gives a UniFi row its controller inputs: the device the controller lists at its MAC (or its
// address) and that controller, else the one controller serving the site. It reports whether it did.
func (l *unifiLookup) fill(req *createHostRequest, mac, site string) bool {
	if l == nil {
		return false
	}
	set := func(macro, value string) {
		if strings.TrimSpace(req.Macros[macro]) == "" && value != "" {
			req.Macros[macro] = value
		}
	}
	hit, ok := l.byMAC[normMAC(mac)]
	if !ok || mac == "" {
		hit, ok = l.byIP[req.IP]
	}
	if ok {
		ctl := l.ctls[hit.ctl]
		set("{$UNIFI.URL}", ctl.URL)
		set("{$UNIFI.KEY}", ctl.APIKey)
		set("{$UNIFI.MAC}", hit.dev.MAC)
		set("{$UNIFI.SITE}", hit.dev.Site)
		return true
	}
	var in []store.UniFiController
	for _, c := range l.ctls {
		if c.InScope(siteOf([]string{site})) {
			in = append(in, c)
		}
	}
	if len(in) == 1 {
		set("{$UNIFI.URL}", in[0].URL)
		set("{$UNIFI.KEY}", in[0].APIKey)
		return true
	}
	return false
}

// classFor is the UniFi class the controller gives the device at this address ("" when it lists none).
func (l *unifiLookup) classFor(ip string) (string, string) {
	if l == nil {
		return "", ""
	}
	if hit, ok := l.byIP[ip]; ok {
		return provision.SuggestUniFiClass(hit.dev.Type, hit.dev.Model), hit.dev.Model
	}
	return "", ""
}

// --- the check ---

var dnsNameShape = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62})(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}))*\.?$`)

// importClass finds the class a row names, by id or label; with a suggestion when it names none.
func importClass(v string) (provision.Class, string) {
	v = strings.TrimSpace(v)
	low := strings.ToLower(v)
	var best provision.Class
	bestD := 1 << 30
	for _, c := range provision.Classes() {
		if c.Internal {
			continue
		}
		if low == strings.ToLower(c.ID) || low == strings.ToLower(c.Label) {
			return c, ""
		}
		for _, cand := range []string{strings.ToLower(c.ID), strings.ToLower(c.Label)} {
			if d := editDistance(low, cand); d < bestD {
				best, bestD = c, d
			}
			// One inside the other ("ugreen-nas" and "ugreen") is as good as a typo.
			if len(low) >= 4 && len(cand) >= 4 && (strings.Contains(cand, low) || strings.Contains(low, cand)) && bestD > 1 {
				best, bestD = c, 1
			}
		}
	}
	if bestD <= 4 || bestD*3 <= len(low) {
		return provision.Class{}, best.Label
	}
	return provision.Class{}, ""
}

// editDistance is the Levenshtein distance between two strings.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// splitTags reads a tags cell: comma- or semicolon-separated.
func splitTags(v string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' }) {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// macroKey reads a macro column's name: {$NUT.UPS}, $NUT.UPS and NUT.UPS are the same input.
func macroKey(k string) string {
	k = strings.TrimSpace(k)
	k = strings.TrimSuffix(strings.TrimPrefix(k, "{"), "}")
	k = strings.TrimPrefix(k, "$")
	if k == "" {
		return ""
	}
	return "{$" + strings.ToUpper(k) + "}"
}

// checkImportRow makes a host request of one row and says what is wrong with it, if anything.
func checkImportRow(env *importEnv, row importRow) (createHostRequest, importCheck) {
	ck := importCheck{Row: row.Row}
	var req createHostRequest
	bad := func(field, msg string) { ck.Problems = append(ck.Problems, importProblem{Field: field, Msg: msg}) }

	name := strings.TrimSpace(row.Name)
	req.Name = name
	if name == "" {
		bad("name", "a name is required")
	}
	addr := strings.TrimSpace(row.Address)
	if a, err := netip.ParseAddr(addr); err == nil {
		req.IP = a.String()
	} else if addr != "" && dnsNameShape.MatchString(addr) && len(addr) <= 253 {
		req.DNS = addr
	} else if addr == "" {
		bad("address", "an IP address or a DNS name is required")
	} else {
		bad("address", "not an IP address or a DNS name")
	}

	class, suggest := importClass(row.Class)
	switch {
	case class.ID != "":
		req.ClassID, ck.ClassID, ck.Class = class.ID, class.ID, class.Label
	case strings.TrimSpace(row.Class) == "":
		bad("class", "pick a class")
	case suggest != "":
		bad("class", fmt.Sprintf("unknown class %q: did you mean %s?", strings.TrimSpace(row.Class), suggest))
	default:
		bad("class", fmt.Sprintf("unknown class %q", strings.TrimSpace(row.Class)))
	}

	site, ok := cleanGroupName(row.Site)
	if !ok {
		bad("site", "a site (group) is required")
	} else {
		req.Site = site
		if !env.groups[site] {
			ck.Note = "creates the group " + site
		}
	}

	probe := strings.TrimSpace(row.Probe)
	switch low := strings.ToLower(probe); {
	case low == "server" || low == "core" || low == "argus" || low == "none" || low == "zabbix server":
		ck.Probe = "Server"
	case low == "":
		// The site's own probe (proxy-<site>) when there is one, else the core.
		ck.Probe = "Server"
		if p, ok := env.proxies["proxy-"+strings.ToLower(siteOf([]string{site}))]; ok && site != "" {
			req.ProxyID, ck.Probe = p.ProxyID, p.Name
		}
	default:
		if p, ok := env.proxies[low]; ok {
			req.ProxyID, ck.Probe = p.ProxyID, p.Name
		} else {
			bad("probe", fmt.Sprintf("no probe named %q", probe))
		}
	}

	for _, t := range splitTags(row.Tags) {
		if _, msg := cleanTag(t, "", ""); msg != "" {
			bad("tags", fmt.Sprintf("tag %q: %s", t, msg))
			break
		}
	}

	// The class's inputs: the row's macro columns, a UniFi device's MAC and its controller, and the
	// ones derived from the address.
	req.Macros = map[string]string{}
	if class.ID != "" {
		declared := map[string]bool{}
		for _, ms := range class.Macros {
			declared[ms.Macro] = true
		}
		for k, v := range row.Macros {
			if m := macroKey(k); declared[m] && strings.TrimSpace(v) != "" {
				req.Macros[m] = strings.TrimSpace(v)
			}
		}
		if strings.HasPrefix(class.ID, "unifi-") {
			if mac := strings.TrimSpace(row.MAC); mac != "" {
				req.Macros["{$UNIFI.MAC}"] = mac
			}
			ck.FromUniFi = env.unifi.fill(&req, row.MAC, site)
		}
		host := req.IP
		if host == "" {
			host = req.DNS
		}
		for _, ms := range class.Macros {
			if ms.Required && req.Macros[ms.Macro] == "" && ms.Derive != "" && host != "" {
				req.Macros[ms.Macro] = strings.ReplaceAll(ms.Derive, "{host}", host)
			}
			if ms.Required && req.Macros[ms.Macro] == "" {
				ck.Missing = append(ck.Missing, ms)
				bad("macro:"+ms.Macro, ms.Label+" is required")
			}
		}
		if len(ck.Missing) == 0 {
			if _, err := buildMacros(req, class); err != nil {
				bad("macros", err.Error())
			}
		}
	}

	// Already monitored: by its name, or at its address as the same class.
	if name != "" {
		if v, ok := env.techNames[strings.ToLower(zabbix.TechnicalName(name))]; ok {
			ck.Note = "already monitored as " + v
		} else if v, ok := env.visible[strings.ToLower(name)]; ok {
			ck.Note = "already monitored as " + v
		}
	}
	if !strings.HasPrefix(ck.Note, "already") && req.IP != "" && class.ID != "" {
		for _, h := range env.atIP[req.IP] {
			if h.class == class.ID {
				ck.Note = "already monitored as " + h.name + " (same address and class)"
				break
			}
		}
	}
	switch {
	case strings.HasPrefix(ck.Note, "already"):
		ck.Status, ck.Problems, ck.Missing = "skip", nil, nil
	case len(ck.Problems) > 0:
		ck.Status = "fix"
	default:
		ck.Status = "ready"
	}
	return req, ck
}

// checkImport checks every row; a name that repeats an earlier row's is one to fix.
func checkImport(env *importEnv, rows []importRow) ([]createHostRequest, []importCheck) {
	reqs := make([]createHostRequest, len(rows))
	out := make([]importCheck, len(rows))
	seen := map[string]int{}
	for i, row := range rows {
		reqs[i], out[i] = checkImportRow(env, row)
		key := strings.ToLower(zabbix.TechnicalName(strings.TrimSpace(row.Name)))
		if key == "" || out[i].Status == "skip" {
			continue
		}
		if first, dup := seen[key]; dup {
			out[i].Problems = append(out[i].Problems, importProblem{Field: "name", Msg: fmt.Sprintf("the same name as row %d", first)})
			out[i].Status = "fix"
			continue
		}
		seen[key] = row.Row
	}
	return reqs, out
}

// needsUniFi reports whether any row is a UniFi device, so the controllers are worth asking.
func needsUniFi(rows []importRow) bool {
	for _, r := range rows {
		if c, _ := importClass(r.Class); strings.HasPrefix(c.ID, "unifi-") {
			return true
		}
	}
	return false
}

type importCounts struct {
	Ready int `json:"ready"`
	Skip  int `json:"skip"`
	Fix   int `json:"fix"`
}

func countImport(cks []importCheck) importCounts {
	var c importCounts
	for _, ck := range cks {
		switch ck.Status {
		case "ready":
			c.Ready++
		case "skip":
			c.Skip++
		default:
			c.Fix++
		}
	}
	return c
}

func readImportRows(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return false
	}
	return true
}

// POST /api/import/check: what Argus makes of each row. Nothing is created.
func (s *Server) handleImportCheck(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	var req struct {
		Rows []importRow `json:"rows"`
	}
	if !readImportRows(w, r, &req) {
		return
	}
	if len(req.Rows) > maxImportRows {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("up to %d rows at a time", maxImportRows)})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	env, err := s.loadImportEnv(ctx, needsUniFi(req.Rows))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	_, cks := checkImport(env, req.Rows)
	writeJSON(w, http.StatusOK, map[string]any{"rows": cks, "counts": countImport(cks)})
}

// --- the run ---

type importFailure struct {
	Row   int    `json:"row"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

type importJob struct {
	mu      sync.Mutex
	ID      string          `json:"id"`
	Source  string          `json:"source"`
	Total   int             `json:"total"`
	Done    int             `json:"done"`
	Created int             `json:"created"`
	Skipped int             `json:"skipped"`
	Failed  []importFailure `json:"failed"`
	Running bool            `json:"running"`
	Started int64           `json:"started"`
	userID  int64
}

var importJobs = struct {
	mu   sync.Mutex
	byID map[string]*importJob
}{byID: map[string]*importJob{}}

func (j *importJob) view() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	failed := append([]importFailure{}, j.Failed...)
	return map[string]any{"id": j.ID, "source": j.Source, "total": j.Total, "done": j.Done, "created": j.Created, "skipped": j.Skipped, "failed": failed, "running": j.Running, "started": j.Started}
}

// POST /api/import/run: import the rows that are ready, in the background. Rows to fix and rows
// already monitored are left out; the answer is the job to follow.
func (s *Server) handleImportRun(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	var req struct {
		Rows   []importRow `json:"rows"`
		Source string      `json:"source"` // the file's name, or "PRTG"
	}
	if !readImportRows(w, r, &req) {
		return
	}
	if len(req.Rows) > maxImportRows {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("up to %d rows at a time", maxImportRows)})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	env, err := s.loadImportEnv(ctx, needsUniFi(req.Rows))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	reqs, cks := checkImport(env, req.Rows)
	type item struct {
		req createHostRequest
		row importRow
	}
	var todo []item
	skipped := 0
	for i, ck := range cks {
		switch ck.Status {
		case "ready":
			todo = append(todo, item{reqs[i], req.Rows[i]})
		default:
			skipped++
		}
	}
	if len(todo) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no row is ready to import"})
		return
	}
	importJobs.mu.Lock()
	for _, j := range importJobs.byID {
		j.mu.Lock()
		running := j.Running
		j.mu.Unlock()
		if running {
			importJobs.mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]string{"error": "an import is already running"})
			return
		}
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	source := strings.TrimSpace(req.Source)
	if len(source) > 120 {
		source = source[:120]
	}
	job := &importJob{ID: hex.EncodeToString(b), Source: source, Total: len(todo), Skipped: skipped, Running: true, Started: time.Now().Unix(), Failed: []importFailure{}}
	if u, ok := auth.UserFrom(r.Context()); ok && u != nil {
		job.userID = u.ID
	}
	importJobs.byID[job.ID] = job
	importJobs.mu.Unlock()

	actorKind, actorID, actor := changeActor(r)
	reason := changeReason(r)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
		defer cancel()
		if err := provision.Reconcile(ctx, s.zbx, s.st, s.logger); err != nil {
			s.logger.Warn("import: template reconcile failed, each host checks again", "err", err)
		}
		var created []string
		for _, it := range todo {
			r := it.req
			hctx, hcancel := context.WithTimeout(ctx, 60*time.Second)
			id, perr := s.provisionHost(hctx, &r, "imported", true)
			if perr == nil {
				s.importExtras(hctx, id, it.row)
			}
			hcancel()
			job.mu.Lock()
			job.Done++
			if perr != nil {
				job.Failed = append(job.Failed, importFailure{Row: it.row.Row, Name: it.row.Name, Error: perr.msg})
			} else {
				job.Created++
				created = append(created, id)
			}
			job.mu.Unlock()
		}
		job.mu.Lock()
		job.Running = false
		c := store.Change{
			At: time.Now().Unix(), ActorKind: actorKind, ActorID: actorID, Actor: actor, Reason: reason, RequestID: newRequestID(),
			Category: "hosts", Action: "Imported " + plural(job.Created, "host", "hosts"), Object: job.Source, HostIDs: created,
			Detail: importSummary(job.Created, job.Skipped, len(job.Failed)),
		}
		job.mu.Unlock()
		if err := s.st.AddChanges(ctx, []store.Change{c}); err != nil {
			s.logger.Warn("change log: could not write", "err", err)
		}
		s.forgetHostIndex()
		s.census.invalidate()
		// The job stays readable for a day.
		time.AfterFunc(24*time.Hour, func() {
			importJobs.mu.Lock()
			delete(importJobs.byID, job.ID)
			importJobs.mu.Unlock()
		})
	}()
	writeJSON(w, http.StatusOK, job.view())
}

// importSummary reads "12 created, 1 skipped, 2 failed".
func importSummary(created, skipped, failed int) string {
	out := fmt.Sprintf("%d created", created)
	if skipped > 0 {
		out += fmt.Sprintf(", %d skipped", skipped)
	}
	if failed > 0 {
		out += fmt.Sprintf(", %d failed", failed)
	}
	return out
}

// importExtras gives an imported host its tags (made when new), asset tag and location.
func (s *Server) importExtras(ctx context.Context, hostID string, row importRow) {
	if tags := splitTags(row.Tags); len(tags) > 0 {
		known := map[string]string{}
		if ts, err := s.st.ListTags(ctx); err == nil {
			for _, t := range ts {
				known[strings.ToLower(t.Name)] = t.Name
			}
		}
		var names []string
		for _, t := range tags {
			if n, ok := known[strings.ToLower(t)]; ok {
				names = append(names, n)
				continue
			}
			tag, msg := cleanTag(t, "", "")
			if msg != "" {
				continue
			}
			if err := s.st.CreateTag(ctx, tag); err != nil && err != store.ErrTagExists {
				continue
			}
			known[strings.ToLower(tag.Name)] = tag.Name
			names = append(names, tag.Name)
		}
		if err := s.st.SetHostTags(ctx, hostID, names); err != nil {
			s.logger.Warn("import: could not tag the host", "host", hostID, "err", err)
		}
	}
	at, loc := strings.TrimSpace(row.AssetTag), strings.TrimSpace(row.Location)
	if at != "" || loc != "" {
		if len(at) > 120 {
			at = at[:120]
		}
		if len(loc) > 200 {
			loc = loc[:200]
		}
		_ = s.st.SetHostFacts(ctx, hostID, store.HostOwnFacts{AssetTag: at, Location: loc})
	}
}

// GET /api/import/{id}: how an import is going.
func (s *Server) handleImportJob(w http.ResponseWriter, r *http.Request) {
	importJobs.mu.Lock()
	job, ok := importJobs.byID[r.PathValue("id")]
	importJobs.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such import"})
		return
	}
	writeJSON(w, http.StatusOK, job.view())
}
