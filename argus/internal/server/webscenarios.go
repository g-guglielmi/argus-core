// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"argus/internal/provision"
	"argus/internal/zabbix"
)

// Multi-step web checks (DESIGN section 7p): Zabbix web scenarios on a host, written in its settings.
// The host's proxy runs the steps in order, keeping cookies, so a login is checked end to end (open the
// page, post the form, check the page after it). Zabbix makes the items (web.test.fail is the step
// that failed, web.test.error why, web.test.time each step's time); Argus makes the triggers: a failed
// step is an error, a slow step a warning past {$WEB.TIME.WARN} and an error past {$WEB.TIME.HIGH}
// (the "Argus Web Scenarios" template's macros, so Thresholds edits them). A password goes in a secret
// host macro ({$WEB.PASSWORD}) that a step's field names, never in the scenario.

const (
	wsMaxSteps   = 10
	wsMaxPerHost = 20
	wsTagKey     = "argus.web"
)

var (
	reWSName  = regexp.MustCompile(`^[\p{L}\p{N} _.:/()-]{1,64}$`)
	reWSCodes = regexp.MustCompile(`^\s*([1-5]\d\d(\s*-\s*[1-5]\d\d)?)(\s*,\s*[1-5]\d\d(\s*-\s*[1-5]\d\d)?)*\s*$`)
	reWSMacro = regexp.MustCompile(`\{\$[A-Z0-9_.]{1,64}\}`)
	wsEveryOK = map[string]bool{"1m": true, "2m": true, "5m": true, "15m": true, "30m": true, "1h": true}
)

type wsStepReq struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Posts    string `json:"posts"` // name=value a line
	Required string `json:"required"`
	Status   string `json:"status"`
}

type wsReq struct {
	Name    string            `json:"name"`
	Every   string            `json:"every"`
	Steps   []wsStepReq       `json:"steps"`
	Secrets map[string]string `json:"secrets"` // {$MACRO} -> value; blank keeps it
}

// validate checks a scenario and turns it into Zabbix's, or says what is wrong.
func (req wsReq) validate(hostID string) (zabbix.HTTPTest, string) {
	t := zabbix.HTTPTest{HostID: hostID, Name: strings.TrimSpace(req.Name), Delay: req.Every, Agent: "Argus web check", Retries: 1}
	if !reWSName.MatchString(t.Name) {
		return t, "the name is up to 64 letters, digits, spaces and . _ : / ( ) -"
	}
	if !wsEveryOK[t.Delay] {
		return t, "pick how often it runs"
	}
	if len(req.Steps) == 0 || len(req.Steps) > wsMaxSteps {
		return t, fmt.Sprintf("a scenario has 1 to %d steps", wsMaxSteps)
	}
	seen := map[string]bool{}
	for i, s := range req.Steps {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = "Step " + strconv.Itoa(i+1)
		}
		if !reWSName.MatchString(name) {
			return t, fmt.Sprintf("step %d: the name is up to 64 letters, digits, spaces and . _ : / ( ) -", i+1)
		}
		if seen[strings.ToLower(name)] {
			return t, fmt.Sprintf("step %d: two steps can't share a name", i+1)
		}
		seen[strings.ToLower(name)] = true
		u, err := url.Parse(strings.TrimSpace(s.URL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(s.URL) > 2048 {
			return t, fmt.Sprintf("step %d: the URL must be a full http(s) address", i+1)
		}
		codes := strings.TrimSpace(s.Status)
		if codes != "" && !reWSCodes.MatchString(codes) {
			return t, fmt.Sprintf("step %d: status codes are like 200 or 200,302 or 200-299", i+1)
		}
		if len(s.Required) > 255 || !noControl(s.Required) {
			return t, fmt.Sprintf("step %d: the text to find is up to 255 characters", i+1)
		}
		var posts []zabbix.HTTPField
		for _, line := range strings.Split(s.Posts, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok || strings.TrimSpace(k) == "" || len(line) > 1000 {
				return t, fmt.Sprintf("step %d: a form field is name=value, one a line", i+1)
			}
			posts = append(posts, zabbix.HTTPField{Name: strings.TrimSpace(k), Value: strings.TrimSpace(v)})
		}
		if len(posts) > 20 {
			return t, fmt.Sprintf("step %d: up to 20 form fields", i+1)
		}
		t.Steps = append(t.Steps, zabbix.HTTPTestStep{No: i + 1, Name: name, URL: strings.TrimSpace(s.URL), Posts: posts, Required: s.Required, StatusCodes: codes, FollowRedirects: 1, Timeout: "15s"})
	}
	for m, v := range req.Secrets {
		if !reWSMacro.MatchString(m) || len(v) > 1000 {
			return t, "a secret's name is like {$WEB.PASSWORD}"
		}
	}
	return t, ""
}

// wsTriggers are a scenario's triggers: a failed step, and each step slow (warning, then error).
func wsTriggers(host string, t zabbix.HTTPTest) []zabbix.NewTrigger {
	tags := func() map[string]string { return map[string]string{wsTagKey: t.HTTPTestID} }
	fail := zabbix.NewTrigger{
		Description: t.Name + " failed",
		Expression:  fmt.Sprintf("last(/%s/web.test.fail[%s])<>0", host, t.Name),
		Priority:    4,
		Comments:    "A step of the web scenario " + t.Name + " failed: its result says which, its error why.",
		Tags:        tags(),
	}
	fail.Tags["argus.recovery"] = t.Name + " works again"
	out := []zabbix.NewTrigger{fail}
	for _, s := range t.Steps {
		key := fmt.Sprintf("web.test.time[%s,%s,resp]", t.Name, s.Name)
		warn := zabbix.NewTrigger{
			Description: t.Name + ": " + s.Name + " is slow",
			Expression:  fmt.Sprintf("last(/%s/%s)>{$WEB.TIME.WARN} and last(/%s/%s)<={$WEB.TIME.HIGH}", host, key, host, key),
			Priority:    2, Comments: "The step took longer than {$WEB.TIME.WARN} seconds.", Tags: tags(),
		}
		warn.Tags["argus.recovery"] = t.Name + ": " + s.Name + " is quick again"
		high := zabbix.NewTrigger{
			Description: t.Name + ": " + s.Name + " is very slow",
			Expression:  fmt.Sprintf("last(/%s/%s)>{$WEB.TIME.HIGH}", host, key),
			Priority:    4, Comments: "The step took longer than {$WEB.TIME.HIGH} seconds.", Tags: tags(),
		}
		high.Tags["argus.recovery"] = t.Name + ": " + s.Name + " is quick again"
		out = append(out, warn, high)
	}
	return out
}

// wsView is a scenario as the host settings show it: its steps and how its last run went.
type wsView struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Every    string      `json:"every"`
	Steps    []wsStepReq `json:"steps"`
	LastAt   int64       `json:"last_at,omitempty"`
	Failed   int         `json:"failed"` // the step that failed, 0 none
	Error    string      `json:"error,omitempty"`
	TotalSec float64     `json:"total_s,omitempty"`
	Secrets  []string    `json:"secrets"` // the {$MACROS} its steps name
}

func (s *Server) wsViews(ctx context.Context, hostID string) ([]wsView, error) {
	tests, err := s.zbx.HTTPTests(ctx, hostID)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, t := range tests {
		keys = append(keys, "web.test.fail["+t.Name+"]", "web.test.error["+t.Name+"]")
		for _, st := range t.Steps {
			keys = append(keys, fmt.Sprintf("web.test.time[%s,%s,resp]", t.Name, st.Name))
		}
	}
	vals := map[string]zabbix.Item{}
	if len(keys) > 0 {
		if items, err := s.zbx.HostItemsByKeys(ctx, []string{hostID}, keys); err == nil {
			for _, it := range items {
				vals[it.Key] = it
			}
		}
	}
	out := []wsView{}
	for _, t := range tests {
		v := wsView{ID: t.HTTPTestID, Name: t.Name, Every: t.Delay, Steps: []wsStepReq{}, Secrets: []string{}}
		seen := map[string]bool{}
		sort.Slice(t.Steps, func(i, j int) bool { return t.Steps[i].No < t.Steps[j].No })
		for _, st := range t.Steps {
			var posts []string
			for _, p := range st.Posts {
				posts = append(posts, p.Name+"="+p.Value)
			}
			v.Steps = append(v.Steps, wsStepReq{Name: st.Name, URL: st.URL, Posts: strings.Join(posts, "\n"), Required: st.Required, Status: st.StatusCodes})
			for _, m := range reWSMacro.FindAllString(st.URL+" "+strings.Join(posts, " ")+" "+st.Required, -1) {
				if !seen[m] {
					seen[m] = true
					v.Secrets = append(v.Secrets, m)
				}
			}
			if it, ok := vals[fmt.Sprintf("web.test.time[%s,%s,resp]", t.Name, st.Name)]; ok {
				f, _ := strconv.ParseFloat(it.LastValue, 64)
				v.TotalSec += f
			}
		}
		if it, ok := vals["web.test.fail["+t.Name+"]"]; ok {
			v.LastAt = atoi64(it.LastClock)
			v.Failed = atoi(it.LastValue)
		}
		if v.Failed > 0 {
			v.Error = itemError(vals["web.test.error["+t.Name+"]"].LastValue)
		}
		v.TotalSec = float64(int(v.TotalSec*1000)) / 1000
		out = append(out, v)
	}
	return out, nil
}

// GET /api/hosts/{id}/web-scenarios.
func (s *Server) handleListWebScenarios(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	vs, err := s.wsViews(ctx, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	writeJSON(w, http.StatusOK, vs)
}

// wsSecrets stores the secret macros typed with a scenario (a blank value keeps the one there is).
func (s *Server) wsSecrets(ctx context.Context, hostID string, secrets map[string]string) error {
	if len(secrets) == 0 {
		return nil
	}
	hm, err := s.zbx.HostMacros(ctx, hostID)
	if err != nil {
		return err
	}
	cur := map[string]zabbix.HostMacro{}
	for _, m := range hm {
		cur[m.Macro] = m
	}
	for m, v := range secrets {
		if v == "" {
			continue
		}
		if err := s.setHostMacro(ctx, hostID, cur, m, v, 1); err != nil {
			return err
		}
	}
	return nil
}

// wsLink keeps the thresholds template on a host while it has scenarios.
func (s *Server) wsLink(ctx context.Context, hostID string, on bool) error {
	tmpls, err := s.zbx.Templates(ctx, []string{provision.TemplateWeb})
	if err != nil {
		return err
	}
	if len(tmpls) == 0 {
		return errors.New("the Argus Web Scenarios template isn't imported yet (it is at the next start of Argus)")
	}
	names, err := s.zbx.HostLinkedTemplateNames(ctx, hostID)
	if err != nil {
		return err
	}
	linked := false
	for _, n := range names {
		linked = linked || n == provision.TemplateWeb
	}
	switch {
	case on && !linked:
		return s.zbx.LinkHostTemplate(ctx, hostID, tmpls[0].TemplateID)
	case !on && linked:
		return s.zbx.UnlinkHostTemplate(ctx, hostID, tmpls[0].TemplateID)
	}
	return nil
}

// saveScenario creates or updates a scenario and remakes its triggers.
func (s *Server) saveScenario(w http.ResponseWriter, r *http.Request, hostID, id string) {
	var req wsReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	t, msg := req.validate(hostID)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	existing, err := s.zbx.HTTPTests(ctx, hostID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	for _, e := range existing {
		if e.HTTPTestID != id && strings.EqualFold(e.Name, t.Name) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this host already has a scenario named " + e.Name})
			return
		}
	}
	if id == "" && len(existing) >= wsMaxPerHost {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("a host can have up to %d scenarios", wsMaxPerHost)})
		return
	}
	host, err := s.zbx.HostTechnicalName(ctx, hostID)
	if err == nil {
		err = s.wsLink(ctx, hostID, true)
	}
	if err == nil {
		err = s.wsSecrets(ctx, hostID, req.Secrets)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	if id == "" {
		id, err = s.zbx.CreateHTTPTest(ctx, t)
	} else {
		t.HTTPTestID = id
		err = s.zbx.UpdateHTTPTest(ctx, t)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	t.HTTPTestID = id
	if old, err := s.zbx.TriggerIDsByTag(ctx, hostID, wsTagKey, id); err == nil {
		_ = s.zbx.DeleteTriggers(ctx, old)
	}
	if err := s.zbx.CreateTriggers(ctx, wsTriggers(host, t)); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "saved, but its alerts weren't made: Zabbix: " + s.errText(r, err)})
		return
	}
	changeObject(r, s.hostLabel(ctx, hostID)+" · "+t.Name, hostID)
	vs, _ := s.wsViews(ctx, hostID)
	writeJSON(w, http.StatusOK, vs)
}

// POST /api/hosts/{id}/web-scenarios (admin, helpdesk).
func (s *Server) handleCreateWebScenario(w http.ResponseWriter, r *http.Request) {
	s.saveScenario(w, r, r.PathValue("id"), "")
}

// wsInScope loads the path's scenario and checks its host is the caller's to see.
func (s *Server) wsInScope(w http.ResponseWriter, r *http.Request) (*zabbix.HTTPTest, bool) {
	t, err := s.zbx.HTTPTestByID(r.Context(), r.PathValue("id"))
	if err == nil && t != nil {
		if sc := scopeFrom(r); !sc.all {
			ctx, cancel := scopeLookupCtx(r)
			ok := s.hostInScope(ctx, sc, t.HostID)
			cancel()
			if !ok {
				t = nil
			}
		}
	}
	if err != nil || t == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "web scenario not found"})
		return nil, false
	}
	return t, true
}

// PUT /api/web-scenarios/{id} (admin, helpdesk).
func (s *Server) handleUpdateWebScenario(w http.ResponseWriter, r *http.Request) {
	t, ok := s.wsInScope(w, r)
	if !ok {
		return
	}
	s.saveScenario(w, r, t.HostID, t.HTTPTestID)
}

// DELETE /api/web-scenarios/{id} (admin, helpdesk): its items and triggers go with it.
func (s *Server) handleDeleteWebScenario(w http.ResponseWriter, r *http.Request) {
	t, ok := s.wsInScope(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.zbx.DeleteHTTPTest(ctx, t.HTTPTestID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	if rest, err := s.zbx.HTTPTests(ctx, t.HostID); err == nil && len(rest) == 0 {
		_ = s.wsLink(ctx, t.HostID, false)
	}
	changeObject(r, s.hostLabel(ctx, t.HostID)+" · "+t.Name, t.HostID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
