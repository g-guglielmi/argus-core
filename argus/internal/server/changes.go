// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"argus/internal/auth"
	"argus/internal/store"
)

// The change log (DESIGN section 7c): who changed what and when. Every signed-in write passes the
// changeLog middleware: a handler may describe its change in its own words (noteChange) or add to the
// entry its route gets from changeRoutes (changeObject, changeDetail, changeDiff); a route the table
// doesn't list and no handler noted isn't logged (sign-ins, tests, checks, reads). Only a write that
// succeeded is logged, and never a request body: just the action, the names, the values a handler
// chose to show and the reason the person gave.

// changeReasonHeader carries the reason someone typed for a change (optional, up to 200 characters).
const changeReasonHeader = "X-Argus-Reason"

// changeCategories are the change log's kinds, as its filter offers them.
var changeCategories = []string{"hosts", "states", "thresholds", "groups", "maintenance", "discovery", "probes", "channels", "users", "statuspages", "updates", "settings"}

type changeCtxKey struct{}

// changeRec is one request's change: the entries its handler noted, or what it added to the generic
// entry of its route.
type changeRec struct {
	mu      sync.Mutex
	entries []store.Change
	object  string
	hostIDs []string
	detail  []string
	diff    []store.ChangeDiff
	skip    bool
}

func changeRecFrom(ctx context.Context) *changeRec {
	rec, _ := ctx.Value(changeCtxKey{}).(*changeRec)
	return rec
}

// noteChange records this request's change in the handler's own words; it replaces the route's
// generic entry. Who, when and the reason are filled in.
func noteChange(r *http.Request, c store.Change) {
	if rec := changeRecFrom(r.Context()); rec != nil {
		rec.mu.Lock()
		rec.entries = append(rec.entries, c)
		rec.mu.Unlock()
	}
}

// changeObject names what the request changed (and the hosts it touched) for its route's entry.
func changeObject(r *http.Request, object string, hostIDs ...string) {
	if rec := changeRecFrom(r.Context()); rec != nil {
		rec.mu.Lock()
		rec.object = object
		rec.hostIDs = append(rec.hostIDs, hostIDs...)
		rec.mu.Unlock()
	}
}

// changeDetail adds a line of detail to the route's entry.
func changeDetail(r *http.Request, detail string) {
	if rec := changeRecFrom(r.Context()); rec != nil && detail != "" {
		rec.mu.Lock()
		rec.detail = append(rec.detail, detail)
		rec.mu.Unlock()
	}
}

// changeDiff adds a value the request moved from old to new; an unchanged value is dropped.
func changeDiff(r *http.Request, field, old, new string) {
	if old == new {
		return
	}
	if rec := changeRecFrom(r.Context()); rec != nil {
		rec.mu.Lock()
		rec.diff = append(rec.diff, store.ChangeDiff{Field: field, Old: old, New: new})
		rec.mu.Unlock()
	}
}

// skipChange keeps this request out of the log (a write that turned out to change nothing).
func skipChange(r *http.Request) {
	if rec := changeRecFrom(r.Context()); rec != nil {
		rec.mu.Lock()
		rec.skip = true
		rec.mu.Unlock()
	}
}

// routeChange is how a route's change reads when its handler doesn't say more. obj names what the
// path's id is, so the entry can name it; detail reads the request's own fields (never secrets).
type routeChange struct {
	cat, action, obj string
	pre              bool // name the object before the handler runs (it deletes or renames it)
	detail           func(b map[string]any) string
}

var changeRoutes = map[string]routeChange{
	// alert states
	"POST /api/events/{id}/ack":      {cat: "states", action: "Acknowledged", obj: "event", detail: durDetail("until fixed")},
	"DELETE /api/events/{id}/ack":    {cat: "states", action: "Removed an acknowledgement", obj: "event", pre: true},
	"POST /api/hosts/{id}/pause":     {cat: "states", action: "Paused a host", obj: "host", detail: durDetail("until resumed")},
	"DELETE /api/hosts/{id}/pause":   {cat: "states", action: "Resumed a host", obj: "host"},
	"POST /api/items/{id}/pause":     {cat: "states", action: "Paused a sensor", obj: "item", detail: durDetail("until resumed")},
	"DELETE /api/items/{id}/pause":   {cat: "states", action: "Resumed a sensor", obj: "item"},
	"POST /api/items/{id}/mute":      {cat: "states", action: "Turned a sensor's alerts off", obj: "item"},
	"DELETE /api/items/{id}/mute":    {cat: "states", action: "Turned a sensor's alerts on", obj: "item"},
	"POST /api/hosts/{id}/hide":      {cat: "states", action: "Hid a host", obj: "host", detail: durDetail("until shown again")},
	"DELETE /api/hosts/{id}/hide":    {cat: "states", action: "Showed a host again", obj: "host"},
	"POST /api/items/{id}/hide":      {cat: "states", action: "Hid a sensor", obj: "item", detail: durDetail("until shown again")},
	"DELETE /api/items/{id}/hide":    {cat: "states", action: "Showed a sensor again", obj: "item"},
	"PUT /api/sensors/{key}/note":    {cat: "states", action: "Left a note", obj: "sensorkey", detail: fieldDetail("text")},
	"DELETE /api/sensors/{key}/note": {cat: "states", action: "Removed a note", obj: "sensorkey", pre: true},
	"POST /api/items/{id}/priority":  {cat: "hosts", action: "Changed a sensor's priority", obj: "item"},
	"POST /api/alert/ack":            {cat: "states", action: "Acknowledged from an alert", obj: ""},

	// hosts
	"POST /api/hosts":                    {cat: "hosts", action: "Added a host"},
	"PATCH /api/hosts/{id}/config":       {cat: "hosts", action: "Changed host settings", obj: "host"},
	"POST /api/hosts/{id}/class":         {cat: "hosts", action: "Changed a host's class", obj: "host"},
	"POST /api/hosts/{id}/proxy":         {cat: "hosts", action: "Moved a host to another probe", obj: "host"},
	"POST /api/hosts/{id}/groups":        {cat: "hosts", action: "Changed a host's groups", obj: "host"},
	"POST /api/hosts/{id}/discover":      {cat: "hosts", action: "Ran discovery on a host", obj: "host"},
	"POST /api/hosts/{id}/push":          {cat: "hosts", action: "Added a push sensor", obj: "host", detail: fieldDetail("name")},
	"PATCH /api/push-sensors/{id}":       {cat: "hosts", action: "Changed a push sensor", obj: "push"},
	"POST /api/push-sensors/{id}/rotate": {cat: "hosts", action: "Made a new push URL", obj: "push"},
	"DELETE /api/push-sensors/{id}":      {cat: "hosts", action: "Removed a push sensor", obj: "push", pre: true},

	// groups and the tree
	"POST /api/groups":        {cat: "groups", action: "Created a group", detail: fieldDetail("name")},
	"PATCH /api/groups/{id}":  {cat: "groups", action: "Renamed a group", obj: "group", pre: true},
	"DELETE /api/groups/{id}": {cat: "groups", action: "Deleted a group", obj: "group", pre: true},
	"PUT /api/tree/order":     {cat: "groups", action: "Reordered the tree"},
	"PUT /api/tree/hidden":    {cat: "groups", action: "Changed the hidden groups"},

	// maintenance
	"POST /api/maintenance":        {cat: "maintenance", action: "Created a maintenance window", detail: fieldDetail("name")},
	"PATCH /api/maintenance/{id}":  {cat: "maintenance", action: "Changed a maintenance window", obj: "maint"},
	"DELETE /api/maintenance/{id}": {cat: "maintenance", action: "Deleted a maintenance window", obj: "maint", pre: true},

	// discovery
	"POST /api/discovery/jobs":               {cat: "discovery", action: "Started a scan"},
	"DELETE /api/discovery/jobs/{id}":        {cat: "discovery", action: "Deleted a scan", obj: "job", pre: true},
	"POST /api/discovery/results/state":      {cat: "discovery", action: "Changed discovery results"},
	"POST /api/discovery/controllers":        {cat: "discovery", action: "Saved a UniFi controller", detail: fieldDetail("name")},
	"DELETE /api/discovery/controllers/{id}": {cat: "discovery", action: "Removed a UniFi controller", obj: "controller", pre: true},

	// thresholds
	"PUT /api/thresholds/default": {cat: "thresholds", action: "Changed a default threshold"},

	// probes
	"PUT /api/proxies/{id}/snmp":             {cat: "probes", action: "Changed a probe's SNMP defaults", obj: "proxy"},
	"POST /api/proxies/{id}/snmp/adopt":      {cat: "probes", action: "Applied a probe's SNMP defaults to its hosts", obj: "proxy"},
	"POST /api/proxies/reconcile":            {cat: "probes", action: "Synced the probes with Zabbix"},
	"DELETE /api/proxies/{id}":               {cat: "probes", action: "Removed a probe", obj: "proxy", pre: true},
	"POST /api/probes/tokens":                {cat: "probes", action: "Created a probe enrollment", detail: fieldDetail("site")},
	"DELETE /api/probes/tokens/{id}":         {cat: "probes", action: "Deleted a probe enrollment", obj: "token", pre: true},
	"PUT /api/probes/target":                 {cat: "probes", action: "Changed the probes' target version", detail: fieldDetail("target")},
	"POST /api/probes/{name}/procs/release":  {cat: "probes", action: "Released a probe's process counts", obj: "probe"},
	"POST /api/probes/{name}/update":         {cat: "updates", action: "Updated a probe", obj: "probe"},
	"POST /api/probes/{name}/updater-update": {cat: "updates", action: "Updated a probe's sidecar", obj: "probe"},
	"POST /api/probes/{name}/checkin-token":  {cat: "probes", action: "Issued a probe check-in credential", obj: "probe"},
	"GET /api/probes/{name}/break-glass":     {cat: "probes", action: "Revealed a probe VM's console password", obj: "probe"},

	// updates
	"POST /api/update/start":    {cat: "updates", action: "Updated Argus", detail: fieldDetail("target")},
	"POST /api/update/updater":  {cat: "updates", action: "Updated Argus's sidecar"},
	"PUT /api/os/reboot-window": {cat: "updates", action: "Changed the core reboot window"},
	"PUT /api/os/zbx-window":    {cat: "updates", action: "Changed the core Zabbix update window"},

	// shared alert channels
	"POST /api/notify/channels":              {cat: "channels", action: "Created a channel", detail: fieldDetail("name")},
	"PATCH /api/notify/channels/{id}":        {cat: "channels", action: "Changed a channel", obj: "channel"},
	"POST /api/notify/channels/{id}/enabled": {cat: "channels", action: "Switched a channel", obj: "channel", detail: onOffDetail},
	"DELETE /api/notify/channels/{id}":       {cat: "channels", action: "Deleted a channel", obj: "channel", pre: true},

	// users and sign-in security
	"POST /api/users":                       {cat: "users", action: "Created a user", detail: fieldDetail("email")},
	"PATCH /api/users/{id}":                 {cat: "users", action: "Changed a user", obj: "user"},
	"POST /api/users/{id}/disabled":         {cat: "users", action: "Switched a user's sign-in", obj: "user", detail: disabledDetail},
	"DELETE /api/users/{id}":                {cat: "users", action: "Deleted a user", obj: "user", pre: true},
	"POST /api/users/{id}/password":         {cat: "users", action: "Reset a user's password", obj: "user"},
	"POST /api/users/{id}/mfa/reset":        {cat: "users", action: "Reset a user's two-factor sign-in", obj: "user"},
	"POST /api/users/{id}/passkeys/reset":   {cat: "users", action: "Removed a user's passkeys", obj: "user"},
	"POST /api/me/password":                 {cat: "users", action: "Changed their password"},
	"POST /api/me/mfa/enable":               {cat: "users", action: "Turned on two-factor sign-in"},
	"POST /api/me/mfa/disable":              {cat: "users", action: "Turned off two-factor sign-in"},
	"POST /api/me/mfa/recovery-codes":       {cat: "users", action: "Made new recovery codes"},
	"POST /api/me/passkeys/register/finish": {cat: "users", action: "Added a passkey"},
	"DELETE /api/me/passkeys/{id}":          {cat: "users", action: "Removed a passkey"},
	"POST /api/me/notify/channels":          {cat: "channels", action: "Added a personal channel", detail: fieldDetail("type")},
	"PATCH /api/me/notify/channels/{id}":    {cat: "channels", action: "Changed a personal channel"},
	"DELETE /api/me/notify/channels/{id}":   {cat: "channels", action: "Removed a personal channel"},

	// status pages
	"POST /api/status-pages":             {cat: "statuspages", action: "Created a status page", detail: fieldDetail("name")},
	"PATCH /api/status-pages/{id}":       {cat: "statuspages", action: "Changed a status page", obj: "page"},
	"PUT /api/status-pages/{id}/note":    {cat: "statuspages", action: "Put a note on a status page", obj: "page", detail: fieldDetail("text")},
	"DELETE /api/status-pages/{id}/note": {cat: "statuspages", action: "Took a status page's note down", obj: "page"},
	"POST /api/status-pages/{id}/rotate": {cat: "statuspages", action: "Made a new status page link", obj: "page"},
	"DELETE /api/status-pages/{id}":      {cat: "statuspages", action: "Deleted a status page", obj: "page", pre: true},

	// tags
	"POST /api/tags":             {cat: "settings", action: "Created a tag"},
	"PATCH /api/tags/{name}":     {cat: "settings", action: "Changed a tag"},
	"DELETE /api/tags/{name}":    {cat: "settings", action: "Deleted a tag"},
	"PUT /api/proxies/{id}/tags": {cat: "probes", action: "Changed a probe's tags"},

	// settings
	"PATCH /api/settings":         {cat: "settings", action: "Changed settings"},
	"PUT /api/settings/retention": {cat: "settings", action: "Changed data retention"},
	"PUT /api/backup":             {cat: "settings", action: "Changed the backup settings"},
	"POST /api/backup/run":        {cat: "settings", action: "Started a backup"},
	"POST /api/backup/ssh-key":    {cat: "settings", action: "Made a new backup SSH key"},
}

// durDetail reads a state's length: "for 2 h", or the given words when it has none.
func durDetail(open string) func(map[string]any) string {
	return func(b map[string]any) string {
		if d, ok := b["duration_seconds"].(float64); ok && d > 0 {
			return "for " + humanDur(int64(d))
		}
		return open
	}
}

// fieldDetail shows one plain field of the request (a name, a note's text), never a secret.
func fieldDetail(field string) func(map[string]any) string {
	return func(b map[string]any) string {
		if v, ok := b[field].(string); ok {
			v = strings.TrimSpace(v)
			if len(v) > 200 {
				v = v[:200] + "..."
			}
			return v
		}
		return ""
	}
}

func onOffDetail(b map[string]any) string {
	if on, ok := b["enabled"].(bool); ok {
		if on {
			return "on"
		}
		return "off"
	}
	return ""
}

func disabledDetail(b map[string]any) string {
	if off, ok := b["disabled"].(bool); ok {
		if off {
			return "can't sign in"
		}
		return "can sign in again"
	}
	return ""
}

// humanDur is a length of time the way the log reads it: 45 min, 2 h, 1 h 30 min, 3 d.
func humanDur(secs int64) string {
	switch {
	case secs < 3600:
		m := (secs + 30) / 60
		if m < 1 {
			m = 1
		}
		return fmt.Sprintf("%d min", m)
	case secs < 86400:
		h, m := secs/3600, (secs%3600)/60
		if m == 0 {
			return fmt.Sprintf("%d h", h)
		}
		return fmt.Sprintf("%d h %d min", h, m)
	default:
		d, h := secs/86400, (secs%86400)/3600
		if h == 0 {
			return fmt.Sprintf("%d d", d)
		}
		return fmt.Sprintf("%d d %d h", d, h)
	}
}

// statusRecorder remembers the status code a handler answered with.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// changeBodyMax is how much of a write's JSON body the log reads for its generic detail.
const changeBodyMax = 64 << 10

// changeLog is the middleware that logs every signed-in change that succeeded.
func (s *Server) changeLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || s.st == nil {
			next.ServeHTTP(w, r)
			return
		}
		rec := &changeRec{}
		r = r.WithContext(context.WithValue(r.Context(), changeCtxKey{}, rec))
		write := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		pattern := s.routePattern(r)
		rc, listed := changeRoutes[pattern]
		vals := patternValues(pattern, r.URL.Path)
		// The body, for the route's generic detail; the handler reads the same bytes. Only a listed
		// write's: check-ins and scan uploads pass untouched.
		var body map[string]any
		ct := r.Header.Get("Content-Type")
		if write && listed && r.Body != nil && r.Body != http.NoBody && (ct == "" || strings.HasPrefix(ct, "application/json")) {
			buf, err := io.ReadAll(io.LimitReader(r.Body, changeBodyMax+1))
			if err == nil && len(buf) <= changeBodyMax {
				_ = json.Unmarshal(buf, &body)
			}
			rest := io.Reader(bytes.NewReader(buf))
			if err == nil && len(buf) > changeBodyMax {
				rest = io.MultiReader(rest, r.Body)
			}
			r.Body = struct {
				io.Reader
				io.Closer
			}{rest, r.Body}
		}
		// A delete or rename takes the object's name with it: read it first.
		var pre resolvedObject
		if listed && rc.pre {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			pre = s.resolveChangeObject(ctx, rc.obj, vals)
			cancel()
		}
		sw := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.code >= 400 {
			return
		}
		rec.mu.Lock()
		entries, skip := rec.entries, rec.skip
		object, hosts, detail, diff := rec.object, rec.hostIDs, rec.detail, rec.diff
		rec.mu.Unlock()
		if skip || (len(entries) == 0 && (!listed || !write && pattern != "GET /api/probes/{name}/break-glass")) {
			return
		}
		actorKind, actorID, actor := changeActor(r)
		reason := changeReason(r)
		reqID := newRequestID()
		now := time.Now().Unix()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if len(entries) == 0 {
				c := store.Change{Category: rc.cat, Action: rc.action, Object: object, HostIDs: hosts, Diff: diff}
				if c.Object == "" {
					o := pre
					if !rc.pre {
						o = s.resolveChangeObject(ctx, rc.obj, vals)
					}
					c.Object = o.name
					c.HostIDs = append(c.HostIDs, o.hostIDs...)
				}
				if rc.detail != nil && body != nil {
					if d := rc.detail(body); d != "" {
						detail = append([]string{d}, detail...)
					}
				}
				c.Detail = strings.Join(detail, " · ")
				// An acknowledgement's or a hide's own note is the reason when none was typed apart.
				if reason == "" && body != nil {
					for _, f := range []string{"message", "note"} {
						if v, ok := body[f].(string); ok && strings.TrimSpace(v) != "" && (rc.cat == "states") && pattern != "PUT /api/sensors/{key}/note" {
							reason = strings.TrimSpace(v)
							break
						}
					}
				}
				entries = []store.Change{c}
			}
			for i := range entries {
				e := &entries[i]
				e.At, e.ActorKind, e.ActorID, e.Actor, e.RequestID = now, actorKind, actorID, actor, reqID
				if e.Reason == "" {
					e.Reason = reason
				}
			}
			if err := s.st.AddChanges(ctx, entries); err != nil {
				s.logger.Warn("change log: could not write", "err", err)
			}
			s.pruneChangesDaily(ctx)
		}()
	})
}

// logArgusChange writes a change Argus made by itself (an upstream device read from the controller,
// an import's hosts).
func (s *Server) logArgusChange(ctx context.Context, c store.Change) {
	c.At, c.ActorKind, c.Actor = time.Now().Unix(), "argus", "Argus"
	if err := s.st.AddChanges(ctx, []store.Change{c}); err != nil {
		s.logger.Warn("change log: could not write", "err", err)
	}
}

var changePrune struct {
	mu   sync.Mutex
	last time.Time
}

// pruneChangesDaily drops what's past the change log's retention, at most once a day.
func (s *Server) pruneChangesDaily(ctx context.Context) {
	changePrune.mu.Lock()
	due := time.Since(changePrune.last) > 24*time.Hour
	if due {
		changePrune.last = time.Now()
	}
	changePrune.mu.Unlock()
	if due {
		keep := 365 * 24 * time.Hour
		if s.mgr != nil {
			keep = s.mgr.ChangesKeep()
		}
		_ = s.st.PruneChanges(ctx, time.Now().Add(-keep).Unix())
	}
}

// routePattern is the mux pattern the request matches ("POST /api/hosts/{id}/pause"), found
// before the handler runs so the middleware can name a deleted object.
func (s *Server) routePattern(r *http.Request) string {
	if s.mux == nil {
		return ""
	}
	_, p := s.mux.Handler(r)
	return p
}

// patternValues reads a pattern's {name} segments off a path ("POST /api/hosts/{id}/pause" and
// /api/hosts/17/pause give id=17): the mux sets them only once it hands the request on.
func patternValues(pattern, path string) map[string]string {
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		pattern = pattern[i+1:]
	}
	ps, us := strings.Split(pattern, "/"), strings.Split(path, "/")
	out := map[string]string{}
	if len(ps) != len(us) {
		return out
	}
	for i, seg := range ps {
		if len(seg) > 2 && seg[0] == '{' && seg[len(seg)-1] == '}' {
			v, err := url.PathUnescape(us[i])
			if err != nil {
				v = us[i]
			}
			out[strings.TrimSuffix(seg[1:len(seg)-1], "...")] = v
		}
	}
	return out
}

func changeActor(r *http.Request) (kind string, id int64, name string) {
	if u, ok := auth.UserFrom(r.Context()); ok && u != nil {
		return "user", u.ID, userLabel(u)
	}
	if r.URL.Path == "/api/alert/ack" {
		return "link", 0, "an alert's acknowledge link"
	}
	return "user", 0, ""
}

// changeReason is the reason typed for a change: the header, URL-encoded so any text fits.
func changeReason(r *http.Request) string {
	v := r.Header.Get(changeReasonHeader)
	if v == "" {
		return ""
	}
	if d, err := url.QueryUnescape(v); err == nil {
		v = d
	}
	v = strings.TrimSpace(v)
	if len([]rune(v)) > 200 {
		v = string([]rune(v)[:200])
	}
	return v
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// resolvedObject is a changed object's name and the hosts it belongs to.
type resolvedObject struct {
	name    string
	hostIDs []string
}

// resolveChangeObject names the object a route's path points at. Best-effort: a name it can't find
// leaves the entry without one rather than failing it.
func (s *Server) resolveChangeObject(ctx context.Context, kind string, vals map[string]string) resolvedObject {
	id := vals["id"]
	switch kind {
	case "host":
		if idx, err := s.hostIndex(ctx); err == nil {
			if h, ok := idx[id]; ok {
				return resolvedObject{h.Name, []string{id}}
			}
		}
		return resolvedObject{"host " + id, []string{id}}
	case "item":
		return s.resolveSensor(ctx, func(row sensorRow) bool { return row.ItemID == id })
	case "sensorkey":
		key := vals["key"]
		return s.resolveSensor(ctx, func(row sensorRow) bool {
			if row.ItemID == key {
				return true
			}
			for _, e := range row.EventIDs {
				if row.ItemID == "" && e == key {
					return true
				}
			}
			return false
		})
	case "event":
		return s.resolveSensor(ctx, func(row sensorRow) bool {
			for _, e := range row.EventIDs {
				if e == id {
					return true
				}
			}
			return false
		})
	case "user":
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			if u, err := s.st.UserByID(ctx, n); err == nil {
				return resolvedObject{name: u.Email}
			}
		}
	case "channel":
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			if c, err := s.st.GetNotifyChannel(ctx, n); err == nil && c != nil {
				return resolvedObject{name: c.Name}
			}
		}
	case "group":
		if gs, err := s.zbx.HostGroups(ctx); err == nil {
			for _, g := range gs {
				if g.GroupID == id {
					return resolvedObject{name: g.Name}
				}
			}
		}
	case "maint":
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			if m, err := s.st.MaintenanceWindow(ctx, n); err == nil {
				return resolvedObject{name: m.Name, hostIDs: m.HostIDs}
			}
		}
	case "page":
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			if p, err := s.st.GetStatusPage(ctx, n); err == nil && p != nil {
				return resolvedObject{name: p.Name}
			}
		}
	case "proxy":
		if ps, err := s.zbx.Proxies(ctx); err == nil {
			for _, p := range ps {
				if p.ProxyID == id {
					return resolvedObject{name: p.Name}
				}
			}
		}
	case "probe":
		return resolvedObject{name: vals["name"]}
	case "push":
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			if p, err := s.st.GetPushSensor(ctx, n); err == nil && p != nil {
				name := p.Name
				if idx, err := s.hostIndex(ctx); err == nil {
					if h, ok := idx[p.HostID]; ok {
						name = h.Name + " · " + p.Name
					}
				}
				return resolvedObject{name, []string{p.HostID}}
			}
		}
	case "controller":
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			if c, err := s.st.UniFiControllerByID(ctx, n); err == nil && c != nil {
				return resolvedObject{name: c.Name}
			}
		}
	case "job":
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			if j, err := s.st.DiscoveryJobByID(ctx, n); err == nil && j != nil {
				if j.ControllerName != "" {
					return resolvedObject{name: j.ControllerName}
				}
				return resolvedObject{name: j.CIDR}
			}
		}
	case "token":
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			if ts, err := s.st.ListEnrollTokens(ctx); err == nil {
				for _, t := range ts {
					if t.ID == n {
						return resolvedObject{name: t.ProxyName}
					}
				}
			}
		}
	}
	return resolvedObject{}
}

// resolveSensor names a sensor "host · sensor" from the census.
func (s *Server) resolveSensor(ctx context.Context, match func(sensorRow) bool) resolvedObject {
	rows, err := s.sensorCensus(ctx)
	if err != nil {
		return resolvedObject{}
	}
	for _, row := range rows {
		if match(row) {
			name := row.Label
			if name == "" {
				name = row.Name
			}
			return resolvedObject{row.HostName + " · " + name, []string{row.HostID}}
		}
	}
	return resolvedObject{}
}

// --- reading the log ---

type changeView struct {
	ID      int64              `json:"id"`
	At      int64              `json:"at"`
	Actor   string             `json:"actor"`
	ByArgus bool               `json:"by_argus,omitempty"`
	Cat     string             `json:"category"`
	Action  string             `json:"action"`
	Object  string             `json:"object,omitempty"`
	Detail  string             `json:"detail,omitempty"`
	Diff    []store.ChangeDiff `json:"diff,omitempty"`
	Reason  string             `json:"reason,omitempty"`
	Hosts   []changeHostView   `json:"hosts,omitempty"`
}

type changeHostView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GET /api/changes?from=&q=&cat=&probe=&group=&before= (admin): the change log, newest first.
func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cq := store.ChangeQuery{Text: q.Get("q"), Category: q.Get("cat"), Limit: 300}
	if v, err := strconv.ParseInt(q.Get("from"), 10, 64); err == nil {
		cq.From = v
	}
	if v, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil {
		cq.Before = v
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if f := parseHostFilter(r); f.active() {
		set, err := s.filterHostIDs(ctx, f)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": s.errText(r, err)})
			return
		}
		cq.HostIDs = setKeys(set)
	}
	s.writeChanges(ctx, w, cq)
}

// GET /api/hosts/{id}/changes: one host's changes, newest first.
func (s *Server) handleHostChanges(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	cq := store.ChangeQuery{HostIDs: []string{r.PathValue("id")}, Limit: 200}
	if v, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil {
		cq.Before = v
	}
	s.writeChanges(ctx, w, cq)
}

func (s *Server) writeChanges(ctx context.Context, w http.ResponseWriter, cq store.ChangeQuery) {
	limit := cq.Limit
	cq.Limit = limit + 1
	cs, err := s.st.ListChanges(ctx, cq)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the change log"})
		return
	}
	more := len(cs) > limit
	if more {
		cs = cs[:limit]
	}
	idx, _ := s.hostIndex(ctx)
	out := make([]changeView, 0, len(cs))
	for _, c := range cs {
		v := changeView{ID: c.ID, At: c.At, Actor: c.Actor, ByArgus: c.ActorKind == "argus", Cat: c.Category, Action: c.Action,
			Object: c.Object, Detail: c.Detail, Diff: c.Diff, Reason: c.Reason}
		for _, h := range c.HostIDs {
			name := ""
			if hi, ok := idx[h]; ok {
				name = hi.Name
			}
			v.Hosts = append(v.Hosts, changeHostView{ID: h, Name: name})
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": out, "more": more, "keep_days": int(s.changesKeep().Hours() / 24)})
}

func (s *Server) changesKeep() time.Duration {
	if s.mgr == nil {
		return 365 * 24 * time.Hour
	}
	return s.mgr.ChangesKeep()
}

func setKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
