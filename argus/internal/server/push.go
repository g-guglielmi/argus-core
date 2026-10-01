// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"argus/internal/auth"
	"argus/internal/provision"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Push sensors (Uptime Kuma's push monitors): a job reports to Argus when it runs, at its own secret
// URL (/api/push/{token}), with ok or fail and an optional message. Argus keeps the last run
// (store.PushSensor); the host's "Argus Push" template reads the host's push sensors back once a
// minute, through the host's own proxy, from /api/push/host/{id} with the host's key. So each push
// sensor is a Zabbix sensor like any other: history, uptime, maintenance, held by its master, alerts.
// Zabbix takes a pushed (trapper) value for a host behind a proxy only through that proxy, which the
// core can't reach; the proxy fetching from Argus works for every host. DESIGN section 5.

const pushMacroURL, pushMacroKey = "{$PUSH.URL}", "{$PUSH.KEY}"

// Push sensor times: a warning after LateSecs without a run, an error after MissedSecs.
const (
	minPushSecs = 2 * 60      // the template reads once a minute
	maxPushSecs = 400 * 86400 // a yearly job, with room
	maxPushMsg  = 200         // runes kept of a run's message
	maxPushName = 64          // runes of a push sensor's name
	maxPerHost  = 50          // push sensors on one host
	pushBodyCap = 4096        // a run's report
)

type pushView struct {
	ID         int64  `json:"id"`
	HostID     string `json:"host_id"`
	Name       string `json:"name"`
	LateSecs   int64  `json:"late_secs"`
	MissedSecs int64  `json:"missed_secs"`
	CreatedAt  int64  `json:"created_at"`
	CreatedBy  string `json:"created_by,omitempty"`
	LastAt     int64  `json:"last_at,omitempty"` // 0 = no run yet
	LastOK     bool   `json:"last_ok"`
	LastMsg    string `json:"last_msg,omitempty"`
	Runs       int64  `json:"runs"`
	URL        string `json:"url,omitempty"` // the job's URL: admins and helpdesk only
}

func toPushView(p store.PushSensor) pushView {
	return pushView{ID: p.ID, HostID: p.HostID, Name: p.Name, LateSecs: p.LateSecs, MissedSecs: p.MissedSecs, CreatedAt: p.CreatedAt,
		CreatedBy: p.CreatedBy, LastAt: p.LastAt, LastOK: p.LastOK, LastMsg: p.LastMsg, Runs: p.Runs}
}

// pushURL is a push sensor's URL: absolute when Argus knows its address, else the path.
func (s *Server) pushURL(r *http.Request, token string) string {
	return strings.TrimRight(s.baseURL(r), "/") + "/api/push/" + token
}

type pushRequest struct {
	Name       string `json:"name"`
	LateSecs   int64  `json:"late_secs"`
	MissedSecs int64  `json:"missed_secs"`
}

// validate checks a push sensor's name and times; others are the host's other push sensors.
func (req pushRequest) validate(others []store.PushSensor, selfID int64) (pushRequest, string) {
	req.Name = strings.TrimSpace(req.Name)
	switch n := len([]rune(req.Name)); {
	case n == 0:
		return req, "a push sensor needs a name"
	case n > maxPushName:
		return req, fmt.Sprintf("the name can be up to %d characters", maxPushName)
	}
	for _, c := range req.Name {
		if unicode.IsControl(c) || c == '{' || c == '}' {
			return req, "the name can't contain braces or control characters"
		}
	}
	for _, o := range others {
		if o.ID != selfID && strings.EqualFold(o.Name, req.Name) {
			return req, "this host already has a push sensor named " + o.Name
		}
	}
	if req.LateSecs < minPushSecs || req.LateSecs > maxPushSecs || req.MissedSecs < minPushSecs || req.MissedSecs > maxPushSecs {
		return req, "the late and missed times must be between 2 minutes and 400 days"
	}
	if req.MissedSecs <= req.LateSecs {
		return req, "the missed time must be longer than the late time"
	}
	return req, ""
}

// handleListPush returns a host's push sensors with their last run; admins and helpdesk also get each
// one's URL.
func (s *Server) handleListPush(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListPushSensors(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	out := make([]pushView, 0, len(list))
	for _, p := range list {
		v := toPushView(p)
		if canEditHosts(r) {
			if tok, err := s.st.PushToken(r.Context(), p.ID); err == nil {
				v.URL = s.pushURL(r, tok)
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreatePush(w http.ResponseWriter, r *http.Request) {
	hostID := r.PathValue("id")
	var req pushRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	others, err := s.st.ListPushSensors(ctx, hostID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	if len(others) >= maxPerHost {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("a host can have up to %d push sensors", maxPerHost)})
		return
	}
	req, msg := req.validate(others, 0)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if msg := s.pushHostProblem(ctx, r, hostID); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	by := ""
	if u, ok := auth.UserFrom(r.Context()); ok {
		by = u.Email
	}
	token := newStatusToken()
	id, err := s.st.CreatePushSensor(ctx, store.PushSensor{HostID: hostID, Name: req.Name, LateSecs: req.LateSecs, MissedSecs: req.MissedSecs, CreatedBy: by}, token)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	// The host reads its push sensors through the template: link it and give the host its URL and key.
	// A failure takes the push sensor back out, so there's never one Zabbix can't see.
	if err := s.ensurePushHost(ctx, r, hostID); err != nil {
		_ = s.st.DeletePushSensor(ctx, id)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	p, err := s.st.GetPushSensor(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	v := toPushView(*p)
	v.URL = s.pushURL(r, token)
	s.logger.Info("push sensor created", "host", hostID, "id", id, "name", req.Name, "by", by)
	writeJSON(w, http.StatusOK, v)
}

// pushSensorInScope loads the path-id push sensor and confirms its host is the caller's to see (404
// otherwise, like a host out of scope).
func (s *Server) pushSensorInScope(w http.ResponseWriter, r *http.Request) (*store.PushSensor, bool) {
	p, err := s.st.GetPushSensor(r.Context(), atoi64(r.PathValue("id")))
	if err == nil {
		if sc := scopeFrom(r); !sc.all {
			ctx, cancel := scopeLookupCtx(r)
			if !s.hostInScope(ctx, sc, p.HostID) {
				err = store.ErrNotFound
			}
			cancel()
		}
	}
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "push sensor not found"})
		return nil, false
	}
	return p, true
}

func (s *Server) handleUpdatePush(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pushSensorInScope(w, r)
	if !ok {
		return
	}
	var req pushRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	others, err := s.st.ListPushSensors(r.Context(), p.HostID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	req, msg := req.validate(others, p.ID)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	p.Name, p.LateSecs, p.MissedSecs = req.Name, req.LateSecs, req.MissedSecs
	if err := s.st.UpdatePushSensor(r.Context(), *p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	// The new name and times reach Zabbix with the next read (the discovery carries them). Saving also
	// puts back the host's URL and key, for a host whose macros were lost.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.ensurePushHost(ctx, r, p.HostID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "saved, but Zabbix: " + err.Error()})
		return
	}
	v := toPushView(*p)
	if tok, err := s.st.PushToken(r.Context(), p.ID); err == nil {
		v.URL = s.pushURL(r, tok)
	}
	writeJSON(w, http.StatusOK, v)
}

// handleRotatePush gives a push sensor a new URL; the old one stops working at once.
func (s *Server) handleRotatePush(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pushSensorInScope(w, r)
	if !ok {
		return
	}
	token := newStatusToken()
	if err := s.st.RotatePushToken(r.Context(), p.ID, token); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	v := toPushView(*p)
	v.URL = s.pushURL(r, token)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleDeletePush(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pushSensorInScope(w, r)
	if !ok {
		return
	}
	if err := s.st.DeletePushSensor(r.Context(), p.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	// Its sensors go at the template's next read. With the host's last push sensor gone, the template
	// and its macros go too.
	rest, _ := s.st.ListPushSensors(r.Context(), p.HostID)
	if len(rest) == 0 {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.dropPushHost(ctx, p.HostID); err != nil {
			s.logger.Warn("push: could not remove the template from the host", "host", p.HostID, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// pushHostProblem says why a host can't have push sensors ("" when it can): Argus needs an address the
// host's proxy can reach it at, and the template must be imported.
func (s *Server) pushHostProblem(ctx context.Context, r *http.Request, hostID string) string {
	if !s.zbx.Authenticated() {
		return "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"
	}
	if s.baseURL(r) == "" {
		return "Argus doesn't know its own address yet: set the Public URL in Settings, so the host's probe can reach it"
	}
	if classID, ok, _ := s.st.GetDeviceClass(ctx, hostID); ok {
		if c, ok := provision.ClassByID(classID); ok && c.Internal {
			return "this host is managed by Argus; it can't have push sensors"
		}
	}
	return ""
}

// ensurePushHost links the push template to the host and sets the URL and key it reads its push
// sensors with. The key is made once and kept (only its hash is stored); a host whose key macro or
// stored hash went missing gets a new one.
func (s *Server) ensurePushHost(ctx context.Context, r *http.Request, hostID string) error {
	names, err := s.zbx.HostLinkedTemplateNames(ctx, hostID)
	if err != nil {
		return err
	}
	linked := false
	for _, n := range names {
		linked = linked || n == provision.TemplatePush
	}
	if !linked {
		tmpls, err := s.zbx.Templates(ctx, []string{provision.TemplatePush})
		if err != nil {
			return err
		}
		if len(tmpls) == 0 {
			return errors.New("the Argus Push template isn't imported yet (it is at the next start of Argus)")
		}
		if err := s.zbx.LinkHostTemplate(ctx, hostID, tmpls[0].TemplateID); err != nil {
			return err
		}
	}
	cur := map[string]zabbix.HostMacro{}
	hm, err := s.zbx.HostMacros(ctx, hostID)
	if err != nil {
		return err
	}
	for _, m := range hm {
		cur[m.Macro] = m
	}
	url := strings.TrimRight(s.baseURL(r), "/") + "/api/push/host/" + hostID
	if err := s.setHostMacro(ctx, hostID, cur, pushMacroURL, url, 0); err != nil {
		return err
	}
	_, hashErr := s.st.PushHostKeyHash(ctx, hostID)
	if _, has := cur[pushMacroKey]; has && hashErr == nil {
		return nil
	}
	key := newStatusToken()
	if err := s.setHostMacro(ctx, hostID, cur, pushMacroKey, key, 1); err != nil {
		return err
	}
	return s.st.SetPushHostKey(ctx, hostID, key)
}

// setHostMacro creates or updates one host macro (a secret one always, since it can't be compared).
func (s *Server) setHostMacro(ctx context.Context, hostID string, cur map[string]zabbix.HostMacro, macro, value string, typ int) error {
	if m, has := cur[macro]; has {
		if typ == 0 && m.Value == value {
			return nil
		}
		return s.zbx.UpdateHostMacro(ctx, m.MacroID, value, typ)
	}
	return s.zbx.CreateHostMacro(ctx, hostID, zabbix.Macro{Macro: macro, Value: value, Type: typ})
}

// dropPushHost unlinks the push template from a host (clearing its sensors) and removes its macros
// and key.
func (s *Server) dropPushHost(ctx context.Context, hostID string) error {
	if tmpls, err := s.zbx.Templates(ctx, []string{provision.TemplatePush}); err == nil && len(tmpls) > 0 {
		names, err := s.zbx.HostLinkedTemplateNames(ctx, hostID)
		if err != nil {
			return err
		}
		for _, n := range names {
			if n == provision.TemplatePush {
				if err := s.zbx.UnlinkHostTemplate(ctx, hostID, tmpls[0].TemplateID); err != nil {
					return err
				}
			}
		}
	}
	if hm, err := s.zbx.HostMacros(ctx, hostID); err == nil {
		var ids []string
		for _, m := range hm {
			if m.Macro == pushMacroURL || m.Macro == pushMacroKey {
				ids = append(ids, m.MacroID)
			}
		}
		if err := s.zbx.DeleteHostMacros(ctx, ids...); err != nil {
			return err
		}
	}
	return s.st.DeletePushHost(ctx, hostID)
}

// parsePushStatus reads a run's status: blank or ok/up/success/1/true is a success, fail/failed/
// failure/down/error/0/false a failure (Uptime Kuma's up/down are accepted, so its examples work).
func parsePushStatus(v string) (ok, valid bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "ok", "up", "success", "succeeded", "1", "true":
		return true, true
	case "fail", "failed", "failure", "down", "error", "0", "false":
		return false, true
	}
	return false, false
}

// cleanPushMsg makes a run's message one line of printable text, at most maxPushMsg runes.
func cleanPushMsg(msg string) string {
	msg = strings.Join(strings.FieldsFunc(msg, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }), " ")
	if r := []rune(msg); len(r) > maxPushMsg {
		msg = string(r[:maxPushMsg-1]) + "…"
	}
	return msg
}

// handlePushIngest records a run reported at a push sensor's URL (public: the token in the path is the
// credential). GET or POST; status and msg come from the query, a form, or a JSON body.
func (s *Server) handlePushIngest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status, msg := q.Get("status"), q.Get("msg")
	if msg == "" {
		msg = q.Get("message")
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, pushBodyCap)
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch ct {
		case "application/json":
			var b struct {
				Status  string `json:"status"`
				Msg     string `json:"msg"`
				Message string `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "the body isn't valid JSON"})
				return
			}
			status, msg = firstNonEmpty(b.Status, status), firstNonEmpty(b.Msg, b.Message, msg)
		case "application/x-www-form-urlencoded", "multipart/form-data":
			if err := r.ParseForm(); err == nil {
				status, msg = firstNonEmpty(r.PostForm.Get("status"), status), firstNonEmpty(r.PostForm.Get("msg"), r.PostForm.Get("message"), msg)
			}
		}
	}
	ok, valid := parsePushStatus(status)
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "status must be ok or fail"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p, err := s.st.RecordPush(ctx, r.PathValue("token"), ok, cleanPushMsg(msg), time.Now())
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "unknown push URL"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not record the run"})
		return
	}
	if !ok {
		s.logger.Info("push sensor reported a failure", "host", p.HostID, "id", p.ID, "name", p.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// pushPollEntry is one push sensor as the host's template reads it.
type pushPollEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OK      int    `json:"ok"`  // 1 = the last run reported success (or no run yet), 0 = a failure
	Age     int64  `json:"age"` // seconds since the last run (since it was made, before the first)
	Message string `json:"message"`
	Late    int64  `json:"late"`
	Missed  int64  `json:"missed"`
}

// pushPoll is what the host's template reads at now.
func pushPoll(list []store.PushSensor, now int64) []pushPollEntry {
	out := make([]pushPollEntry, 0, len(list))
	for _, p := range list {
		since, ok, msg := p.LastAt, 1, p.LastMsg
		if !p.LastOK {
			ok = 0
		}
		switch {
		case p.LastAt == 0:
			since, msg = p.CreatedAt, "No run reported yet"
		case msg == "" && p.LastOK:
			msg = "The last run reported success"
		case msg == "":
			msg = "The last run reported a failure, without a message"
		}
		out = append(out, pushPollEntry{ID: strconv.FormatInt(p.ID, 10), Name: p.Name, OK: ok, Age: max(0, now-since), Message: msg, Late: p.LateSecs, Missed: p.MissedSecs})
	}
	return out
}

// handlePushPoll is the host's push sensors for its template (public: the host's key is the
// credential, sent as a bearer token by the proxy).
func (s *Server) handlePushPoll(w http.ResponseWriter, r *http.Request) {
	hostID := r.PathValue("id")
	key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	want, err := s.st.PushHostKeyHash(r.Context(), hostID)
	if err != nil || key == "" || subtle.ConstantTimeCompare([]byte(want), []byte(store.HashStatusToken(key))) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown host or key"})
		return
	}
	list, err := s.st.ListPushSensors(r.Context(), hostID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the push sensors"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sensors": pushPoll(list, time.Now().Unix())})
}
