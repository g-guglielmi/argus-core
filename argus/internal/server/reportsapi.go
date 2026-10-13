// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"argus/internal/auth"
	"argus/internal/notify"
	"argus/internal/report"
	"argus/internal/store"
)

// Reports: anyone can make a report's PDF of what they can see; admins and helpdesk save them,
// email them and schedule them. A saved report is listed to those who can see everything it covers.

const (
	reportLogoMax      = 512 << 10
	reportMaxRecipient = 20
	reportRenderTime   = 2 * time.Minute
	reportRetryAfter   = 30 * time.Minute
	reportGiveUpAfter  = 6 * time.Hour // a scheduled send still failing this long after its time waits for the next one
)

// reportSlots keeps Zabbix from being asked for more than two reports at once.
var reportSlots = make(chan struct{}, 2)

// reportView is a saved report as the Reports page lists it.
type reportView struct {
	store.Report
	Covers      string `json:"covers"`       // "site1 (14 hosts); 6 sensors"
	PeriodLabel string `json:"period_label"` // the period it would cover if made now
	Next        int64  `json:"next,omitempty"`
	OwnerName   string `json:"owner_name,omitempty"`
}

type reportBody struct {
	Name     string               `json:"name"`
	Spec     store.ReportSpec     `json:"spec"`
	Schedule store.ReportSchedule `json:"schedule"`
	To       []string             `json:"to"` // email now: who to send it to
}

// reportVisible reports whether a user's scope covers everything a report covers.
func reportVisible(spec store.ReportSpec, sc siteScope, idx map[string]hostInfo) bool {
	if sc.all {
		return true
	}
	for _, site := range spec.Sites {
		if !sc.coversGroup(site) {
			return false
		}
	}
	seen := func(id string) bool {
		h, ok := idx[id]
		return ok && sc.sees(h.Groups)
	}
	for _, id := range spec.Hosts {
		if !seen(id) {
			return false
		}
	}
	for _, sn := range spec.Sensors {
		if !seen(sn.HostID) {
			return false
		}
	}
	return true
}

// checkReport validates a report's name, contents and schedule for a user who may save it.
func checkReport(b *reportBody, sc siteScope, idx map[string]hostInfo, now time.Time, loc *time.Location, withSchedule bool) error {
	// The name heads the PDF and the email's subject: one line, no control characters.
	b.Name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, b.Name))
	if len([]rune(b.Name)) > 120 {
		return errors.New("the name is too long (120 characters at most)")
	}
	sp := &b.Spec
	sp.Sites, sp.Hosts = cleanList(sp.Sites), cleanList(sp.Hosts)
	var sensors []store.ReportSensor
	for _, sn := range sp.Sensors {
		sn.Items = cleanList(sn.Items)
		if sn.HostID != "" && len(sn.Items) > 0 {
			sn.Label = strings.TrimSpace(sn.Label)
			sensors = append(sensors, sn)
		}
	}
	sp.Sensors = sensors
	if len(sp.Sites)+len(sp.Hosts)+len(sp.Sensors) == 0 {
		return errors.New("pick a site, a host or a sensor")
	}
	if len(sp.Sensors) > 200 {
		return errors.New("a report charts 200 sensors at most")
	}
	if !reportVisible(*sp, sc, idx) {
		return errors.New("the report covers a site or a host outside yours")
	}
	if sp.Target < 0 || sp.Target > 100 {
		return errors.New("the uptime target is a percentage, 0 to 100")
	}
	if _, _, _, err := reportPeriod(*sp, now, loc); err != nil {
		return err
	}
	if b.Name == "" {
		b.Name = reportAutoName(*sp, idx, now, loc)
	}
	if !withSchedule {
		return nil
	}
	s := &b.Schedule
	switch s.Every {
	case "", "day", "week", "month":
	default:
		return errors.New("send it every day, week or month, or only on request")
	}
	if s.Weekday < 0 || s.Weekday > 6 || s.Minute < 0 || s.Minute >= 24*60 || (s.Every == "month" && s.Day != -1 && (s.Day < 1 || s.Day > 28)) {
		return errors.New("the day or the time to send it is out of range")
	}
	to, err := cleanRecipients(s.To)
	if err != nil {
		return err
	}
	s.To = to
	if s.Every != "" && s.Enabled && len(s.To) == 0 {
		return errors.New("say who to email it to")
	}
	return nil
}

// reportAutoName names a report left unnamed, from what it covers and its period: "Firewall-BZ, last
// month", "mybz and myng, last 7 days", "mybz and 2 hosts, this month", "web1, 3 sensors, 1 to 15
// October 2026". The period is said the way it was picked, so a saved report's name stays right
// every time it is made. The UI shows the same as the name field's placeholder.
func reportAutoName(spec store.ReportSpec, idx map[string]hostInfo, now time.Time, loc *time.Location) string {
	hostName := func(id string) string { return idx[id].Name }
	var items []string
	if n := len(spec.Sites); n > 3 {
		items = append(items, spec.Sites[0], spec.Sites[1], plural(n-2, "more site", "more sites"))
	} else {
		items = append(items, spec.Sites...)
	}
	switch n := len(spec.Hosts); {
	case n == 1 && len(items) == 0 && hostName(spec.Hosts[0]) != "":
		items = append(items, hostName(spec.Hosts[0]))
	case n > 0:
		items = append(items, plural(n, "host", "hosts"))
	}
	if n := len(spec.Sensors); n > 0 && len(items) == 0 {
		one := true
		for _, sn := range spec.Sensors {
			one = one && sn.HostID == spec.Sensors[0].HostID
		}
		switch {
		case n == 1:
			items = append(items, strings.TrimSpace(hostName(spec.Sensors[0].HostID)+" "+spec.Sensors[0].Label))
		case one && hostName(spec.Sensors[0].HostID) != "":
			items = append(items, hostName(spec.Sensors[0].HostID)+", "+plural(n, "sensor", "sensors"))
		default:
			items = append(items, plural(n, "sensor", "sensors"))
		}
	}
	what := "Report"
	switch len(items) {
	case 0:
	case 1:
		what = items[0]
	default:
		what = strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
	period := map[string]string{"": "last 7 days", "7d": "last 7 days", "30d": "last 30 days", "last_month": "last month", "this_month": "this month"}[spec.Period]
	if spec.Period == "custom" {
		if _, _, label, err := reportPeriod(spec, now, loc); err == nil {
			period = label
		}
	}
	if period == "" {
		return what
	}
	return what + ", " + period
}

func cleanList(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// cleanRecipients checks a list of email addresses (each one bare, no names).
func cleanRecipients(in []string) ([]string, error) {
	out := cleanList(in)
	if len(out) > reportMaxRecipient {
		return nil, fmt.Errorf("%d addresses at most", reportMaxRecipient)
	}
	for i, a := range out {
		p, err := mail.ParseAddress(a)
		if err != nil || p.Name != "" || strings.ContainsAny(a, "\r\n<>") {
			return nil, fmt.Errorf("%q isn't an email address", a)
		}
		out[i] = p.Address
	}
	return out, nil
}

// reportCovers reads what a report covers, for the list: "site1 (14 hosts), web1; 6 sensors".
func reportCovers(spec store.ReportSpec, sc siteScope, idx map[string]hostInfo) string {
	var parts []string
	for _, site := range spec.Sites {
		n := 0
		for _, h := range idx {
			if !sc.sees(h.Groups) {
				continue
			}
			for _, g := range h.Groups {
				if siteCovers(site, g) {
					n++
					break
				}
			}
		}
		parts = append(parts, site+" ("+plural(n, "host", "hosts")+")")
	}
	for _, id := range spec.Hosts {
		if h, ok := idx[id]; ok {
			parts = append(parts, h.Name)
		}
	}
	out := strings.Join(parts, ", ")
	if n := len(spec.Sensors); n > 0 {
		if out != "" {
			out += "; "
		}
		out += plural(n, "sensor", "sensors")
	}
	return out
}

// reportNext is a schedule's first send after t (zero: never).
func reportNext(sc store.ReportSchedule, t time.Time, loc *time.Location) time.Time {
	lt := t.In(loc)
	at := func(d time.Time) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), sc.Minute/60, sc.Minute%60, 0, 0, loc)
	}
	day := time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, loc)
	switch sc.Every {
	case "day":
		for i := 0; i < 3; i++ {
			if s := at(day.AddDate(0, 0, i)); s.After(t) {
				return s
			}
		}
	case "week":
		for i := 0; i < 15; i++ {
			d := day.AddDate(0, 0, i)
			if int(d.Weekday()) == sc.Weekday {
				if s := at(d); s.After(t) {
					return s
				}
			}
		}
	case "month":
		m := time.Date(lt.Year(), lt.Month(), 1, 0, 0, 0, 0, loc)
		for i := 0; i < 3; i++ {
			first := m.AddDate(0, i, 0)
			d := sc.Day
			if d == -1 {
				d = first.AddDate(0, 1, -1).Day()
			}
			if s := at(time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, loc)); s.After(t) {
				return s
			}
		}
	}
	return time.Time{}
}

func (s *Server) reportIndex(r *http.Request) (map[string]hostInfo, bool) {
	idx, err := s.hostIndex(r.Context())
	return idx, err == nil
}

// handleListReports serves GET /api/reports: the saved reports the user can see.
func (s *Server) handleListReports(w http.ResponseWriter, r *http.Request) {
	idx, _ := s.reportIndex(r)
	sc := scopeFrom(r)
	all, err := s.st.Reports(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the reports"})
		return
	}
	users := map[int64]string{}
	if list, err := s.st.ListUsers(r.Context()); err == nil {
		for _, u := range list {
			if n := strings.TrimSpace(u.Name + " " + u.Surname); n != "" {
				users[u.ID] = n
			} else {
				users[u.ID] = u.Email
			}
		}
	}
	now, loc := time.Now(), s.mgr.Location()
	out := []reportView{}
	for _, rep := range all {
		if !reportVisible(rep.Spec, sc, idx) {
			continue
		}
		v := reportView{Report: rep, Covers: reportCovers(rep.Spec, sc, idx), OwnerName: users[rep.OwnerID]}
		if v.Spec.Sites == nil {
			v.Spec.Sites = []string{}
		}
		if v.Spec.Hosts == nil {
			v.Spec.Hosts = []string{}
		}
		if v.Spec.Sensors == nil {
			v.Spec.Sensors = []store.ReportSensor{}
		}
		if v.Schedule.To == nil {
			v.Schedule.To = []string{}
		}
		if _, _, label, err := reportPeriod(rep.Spec, now, loc); err == nil {
			v.PeriodLabel = label
		}
		if sch := rep.Schedule; sch.Enabled && sch.Every != "" {
			base := max(rep.UpdatedAt, rep.LastRunAt)
			if n := reportNext(sch, time.Unix(base, 0), loc); !n.IsZero() {
				v.Next = n.Unix()
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func readReportBody(w http.ResponseWriter, r *http.Request) (reportBody, bool) {
	var b reportBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return b, false
	}
	return b, true
}

// handleCreateReport serves POST /api/reports.
func (s *Server) handleCreateReport(w http.ResponseWriter, r *http.Request) {
	b, ok := readReportBody(w, r)
	if !ok {
		return
	}
	idx, _ := s.reportIndex(r)
	if err := checkReport(&b, scopeFrom(r), idx, time.Now(), s.mgr.Location(), true); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	u, _ := auth.UserFrom(r.Context())
	id, err := s.st.CreateReport(r.Context(), store.Report{Name: b.Name, Spec: b.Spec, Schedule: b.Schedule, OwnerID: u.ID})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the report"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

// savedReport reads the report in the path, if the user can see it.
func (s *Server) savedReport(w http.ResponseWriter, r *http.Request) (store.Report, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such report"})
		return store.Report{}, false
	}
	rep, err := s.st.Report(r.Context(), id)
	idx, _ := s.reportIndex(r)
	if err != nil || !reportVisible(rep.Spec, scopeFrom(r), idx) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such report"})
		return store.Report{}, false
	}
	return rep, true
}

// handleUpdateReport serves PUT /api/reports/{id}: the one saving it becomes its owner.
func (s *Server) handleUpdateReport(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.savedReport(w, r)
	if !ok {
		return
	}
	b, ok := readReportBody(w, r)
	if !ok {
		return
	}
	idx, _ := s.reportIndex(r)
	if err := checkReport(&b, scopeFrom(r), idx, time.Now(), s.mgr.Location(), true); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	u, _ := auth.UserFrom(r.Context())
	rep.Name, rep.Spec, rep.Schedule, rep.OwnerID = b.Name, b.Spec, b.Schedule, u.ID
	if err := s.st.UpdateReport(r.Context(), rep); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the report"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": rep.ID})
}

// handleDeleteReport serves DELETE /api/reports/{id}.
func (s *Server) handleDeleteReport(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.savedReport(w, r)
	if !ok {
		return
	}
	if err := s.st.DeleteReport(r.Context(), rep.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete the report"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// makeReport works a report out and draws its PDF, two at a time at most.
func (s *Server) makeReport(ctx context.Context, run reportRun) (report.Doc, []byte, error) {
	select {
	case reportSlots <- struct{}{}:
		defer func() { <-reportSlots }()
	case <-ctx.Done():
		return report.Doc{}, nil, errors.New("Argus is busy making other reports: try again in a minute")
	}
	ctx, cancel := context.WithTimeout(ctx, reportRenderTime)
	defer cancel()
	if !s.zbx.Authenticated() {
		return report.Doc{}, nil, errors.New("the Zabbix API token isn't set")
	}
	doc, err := s.buildReport(ctx, run)
	if err != nil {
		return doc, nil, err
	}
	pdf, err := report.Render(doc)
	if err != nil {
		return doc, nil, fmt.Errorf("drawing the PDF: %w", err)
	}
	return doc, pdf, nil
}

// reportFile names a report's PDF: its name and the first day it covers.
func reportFile(name string, spec store.ReportSpec, now time.Time, loc *time.Location) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "report"
	}
	if from, _, _, err := reportPeriod(spec, now, loc); err == nil {
		slug += "-" + from.In(loc).Format("2006-01-02")
	}
	return slug + ".pdf"
}

func writePDF(w http.ResponseWriter, filename string, pdf []byte) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"; filename*=UTF-8''`+url.PathEscape(filename))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

// handleReportPDF serves GET /api/reports/{id}/pdf: a saved report, made now for the one asking.
func (s *Server) handleReportPDF(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.savedReport(w, r)
	if !ok {
		return
	}
	now := time.Now()
	_, pdf, err := s.makeReport(r.Context(), reportRun{name: rep.Name, spec: rep.Spec, scope: scopeFrom(r), now: now})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	_ = s.st.MarkReportMade(r.Context(), rep.ID, now.Unix())
	writePDF(w, reportFile(rep.Name, rep.Spec, now, s.mgr.Location()), pdf)
}

// handleDraftPDF serves POST /api/reports/pdf: a report not saved (yet), from the builder.
func (s *Server) handleDraftPDF(w http.ResponseWriter, r *http.Request) {
	b, ok := readReportBody(w, r)
	if !ok {
		return
	}
	idx, _ := s.reportIndex(r)
	now := time.Now()
	if err := checkReport(&b, scopeFrom(r), idx, now, s.mgr.Location(), false); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	_, pdf, err := s.makeReport(r.Context(), reportRun{name: b.Name, spec: b.Spec, scope: scopeFrom(r), now: now})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writePDF(w, reportFile(b.Name, b.Spec, now, s.mgr.Location()), pdf)
}

// emailReport makes a report and emails it through the first email channel.
func (s *Server) emailReport(ctx context.Context, run reportRun) error {
	ch := s.firstEmailChannel(ctx)
	if ch == nil {
		return errors.New("no email channel to send it with: add one under Notifications")
	}
	from := strings.TrimSpace(ch.Config["from"])
	if from == "" {
		return errors.New("the email channel has no From address")
	}
	doc, pdf, err := s.makeReport(ctx, run)
	if err != nil {
		return err
	}
	subject := run.name + ", " + doc.Period
	link := ""
	if p := s.mgr.PublicURL(); p != "" {
		link = strings.TrimRight(p, "/") + "/?view=reports"
	}
	text := run.name + "\r\n" + doc.Period + "\r\n\r\nThe report is attached as a PDF."
	body := "<p style=\"font:15px sans-serif;margin:0 0 4px\"><b>" + html.EscapeString(run.name) + "</b></p>" +
		"<p style=\"font:14px sans-serif;color:#555;margin:0 0 14px\">" + html.EscapeString(doc.Period) + "</p>" +
		"<p style=\"font:14px sans-serif;margin:0\">The report is attached as a PDF.</p>"
	if link != "" {
		text += "\r\nReports in Argus: " + link
		body += "<p style=\"font:14px sans-serif;margin:10px 0 0\"><a href=\"" + html.EscapeString(link) + "\">Reports in Argus</a></p>"
	}
	file := reportFile(run.name, run.spec, run.now, s.mgr.Location())
	msg := notify.AttachmentMessage(from, run.to, subject, text, body, file, "application/pdf", pdf)
	sendCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := notify.SMTPFromConfig(ch.Config).Send(sendCtx, run.to, msg); err != nil {
		return fmt.Errorf("the mail server refused it: %v", err)
	}
	return nil
}

// handleEmailReport serves POST /api/reports/{id}/email: a saved report, now, to the addresses given
// (else its own).
func (s *Server) handleEmailReport(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.savedReport(w, r)
	if !ok {
		return
	}
	var b reportBody
	_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&b)
	to := b.To
	if len(to) == 0 {
		to = rep.Schedule.To
	}
	to, err := cleanRecipients(to)
	if err == nil && len(to) == 0 {
		err = errors.New("say who to email it to")
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	now := time.Now()
	err = s.emailReport(r.Context(), reportRun{name: rep.Name, spec: rep.Spec, scope: scopeFrom(r), now: now, to: to})
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	_ = s.st.MarkReportSent(r.Context(), rep.ID, now.Unix(), 0, errText)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent_to": to})
}

// handleEmailDraft serves POST /api/reports/email: a report from the builder, emailed now.
func (s *Server) handleEmailDraft(w http.ResponseWriter, r *http.Request) {
	b, ok := readReportBody(w, r)
	if !ok {
		return
	}
	idx, _ := s.reportIndex(r)
	now := time.Now()
	if err := checkReport(&b, scopeFrom(r), idx, now, s.mgr.Location(), false); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	to, err := cleanRecipients(b.To)
	if err == nil && len(to) == 0 {
		err = errors.New("say who to email it to")
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.emailReport(r.Context(), reportRun{name: b.Name, spec: b.Spec, scope: scopeFrom(r), now: now, to: to}); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent_to": to})
}

// handleReportLogo serves GET /api/reports/logo: the logo on the reports' first page.
func (s *Server) handleReportLogo(w http.ResponseWriter, r *http.Request) {
	v, ok, err := s.st.MetaGet(r.Context(), reportLogoKey)
	b, derr := base64.StdEncoding.DecodeString(v)
	if err != nil || !ok || v == "" || derr != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no logo"})
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(b))
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

// handleSetReportLogo serves PUT /api/reports/logo: a PNG or JPEG, 512 KB at most.
func (s *Server) handleSetReportLogo(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, reportLogoMax+1))
	if err != nil || len(b) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no image"})
		return
	}
	if len(b) > reportLogoMax {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the logo is too big: 512 KB at most"})
		return
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || (format != "png" && format != "jpeg") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the logo must be a PNG or a JPEG image"})
		return
	}
	if cfg.Width > 4000 || cfg.Height > 4000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the logo is too large: 4000 pixels a side at most"})
		return
	}
	if _, _, err := image.Decode(bytes.NewReader(b)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the image can't be read"})
		return
	}
	if err := s.st.MetaSet(r.Context(), reportLogoKey, base64.StdEncoding.EncodeToString(b)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the logo"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"width": cfg.Width, "height": cfg.Height})
}

// handleDeleteReportLogo serves DELETE /api/reports/logo.
func (s *Server) handleDeleteReportLogo(w http.ResponseWriter, r *http.Request) {
	if err := s.st.MetaDelete(r.Context(), reportLogoKey); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not remove the logo"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// startReportSchedule emails the scheduled reports when they are due, checked every minute.
func (s *Server) startReportSchedule(ctx context.Context) {
	go func() {
		first := time.NewTimer(2 * time.Minute)
		defer first.Stop()
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-first.C:
			case <-t.C:
			}
			s.reportTick(ctx, time.Now())
		}
	}()
}

// reportTick sends each scheduled report whose time has come, made with what its owner can see. A
// send that fails is tried again every half hour, until six hours past its time.
func (s *Server) reportTick(ctx context.Context, now time.Time) {
	reps, err := s.st.Reports(ctx)
	if err != nil {
		return
	}
	loc := s.mgr.Location()
	for _, rep := range reps {
		sch := rep.Schedule
		if !sch.Enabled || sch.Every == "" || len(sch.To) == 0 {
			continue
		}
		next := reportNext(sch, time.Unix(max(rep.UpdatedAt, rep.LastRunAt), 0), loc)
		if next.IsZero() || now.Before(next) {
			continue
		}
		if rep.LastTryAt >= next.Unix() && now.Sub(time.Unix(rep.LastTryAt, 0)) < reportRetryAfter {
			continue
		}
		var err error
		owner, uerr := s.st.UserByID(ctx, rep.OwnerID)
		if uerr != nil || owner == nil || owner.Disabled {
			err = errors.New("the account that saved it is gone or disabled: open it and save it again")
		} else {
			err = s.emailReport(ctx, reportRun{name: rep.Name, spec: rep.Spec, scope: scopeOf(owner), now: now, to: sch.To})
		}
		run, errText := now.Unix(), ""
		if err != nil {
			errText = err.Error()
			if now.Sub(next) < reportGiveUpAfter {
				run = 0 // try again
			}
			s.logger.Warn("report: scheduled send failed", "report", rep.Name, "err", err)
		}
		_ = s.st.MarkReportSent(ctx, rep.ID, now.Unix(), run, errText)
	}
}
