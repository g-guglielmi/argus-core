// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"argus/internal/auth"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// One Argus serving several customers: an account limited to site1 sees, makes, emails, changes and
// deletes only the saved reports whose every site, host and sensor is inside site1. A report that
// also covers site2, or a host it can't place (gone since), is as if it didn't exist: 404, nothing
// about it in the list, and a report it saves can't reach outside site1 either.
func TestReportsStayInTheirSites(t *testing.T) {
	mock := reportZabbix(t, time.Now().AddDate(0, -1, 0))
	defer mock.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	mgr, err := settings.New(ctx, st, zabbix.New("", ""))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, zbx: zabbix.New(mock.URL, "test-token"), mgr: mgr, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// Site1 holds hosts 10 and 11, site2 host 12 (reportZabbix); host 99 is gone.
	reports := map[string]store.ReportSpec{
		"site1":              {Sites: []string{"site1"}, Period: "7d"},
		"site1 subgroup":     {Sites: []string{"site1/Servers"}, Period: "7d"},
		"site1 host":         {Hosts: []string{"11"}, Period: "7d"},
		"site1 sensor":       {Sensors: []store.ReportSensor{{HostID: "11", Items: []string{"111"}, Label: "HTTPS response time"}}, Period: "7d"},
		"site2":              {Sites: []string{"site2"}, Period: "7d"},
		"both sites":         {Sites: []string{"site1", "site2"}, Period: "7d"},
		"site1 + site2 host": {Sites: []string{"site1"}, Hosts: []string{"12"}, Period: "7d"},
		"site2 sensor":       {Sites: []string{"site1"}, Sensors: []store.ReportSensor{{HostID: "12", Items: []string{"120"}, Label: "ICMP"}}, Period: "7d"},
		"a gone host":        {Hosts: []string{"11", "99"}, Period: "7d"},
	}
	ids := map[string]int64{}
	for name, spec := range reports {
		id, err := st.CreateReport(ctx, store.Report{Name: name, Spec: spec, Schedule: store.ReportSchedule{To: []string{"it@example.com"}}, OwnerID: 1})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	admin := &store.User{ID: 1, Email: "admin@example.com", Role: "admin"}
	site1 := &store.User{ID: 2, Email: "customer-a@example.com", Role: "helpdesk", Sites: []string{"site1"}}
	viewer1 := &store.User{ID: 3, Email: "viewer-a@example.com", Role: "viewer", Sites: []string{"site1"}}
	anySite := &store.User{ID: 4, Email: "noc@example.com", Role: "helpdesk"} // no sites set: every site, as everywhere in Argus

	call := func(u *store.User, method, path string, h http.HandlerFunc, id int64, body any) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rd)
		if id != 0 {
			req.SetPathValue("id", strconv.FormatInt(id, 10))
		}
		req = req.WithContext(auth.WithUser(req.Context(), u))
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}
	listed := func(u *store.User) []string {
		rec := call(u, "GET", "/api/reports", s.handleListReports, 0, nil)
		var got []reportView
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, r := range got {
			names = append(names, r.Name)
		}
		sort.Strings(names)
		return names
	}
	same := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	mine := []string{"site1", "site1 host", "site1 sensor", "site1 subgroup"}
	if got := listed(site1); !same(got, mine) {
		t.Fatalf("site1 account lists %v, want only %v", got, mine)
	}
	if got := listed(viewer1); !same(got, mine) {
		t.Fatalf("site1 viewer lists %v, want only %v", got, mine)
	}
	if got := listed(admin); len(got) != len(reports) {
		t.Fatalf("admin lists %v, want all %d", got, len(reports))
	}
	if got := listed(anySite); len(got) != len(reports) {
		t.Fatalf("an account with no sites set lists %v, want all %d", got, len(reports))
	}

	for name, id := range ids {
		inside := false
		for _, m := range mine {
			inside = inside || m == name
		}
		if inside {
			continue
		}
		for _, c := range []struct {
			what string
			rec  *httptest.ResponseRecorder
		}{
			{"PDF", call(site1, "GET", "/api/reports/x/pdf", s.handleReportPDF, id, nil)},
			{"email", call(site1, "POST", "/api/reports/x/email", s.handleEmailReport, id, map[string]any{"to": []string{"x@example.com"}})},
			{"edit", call(site1, "PUT", "/api/reports/x", s.handleUpdateReport, id, map[string]any{"name": "mine now", "spec": store.ReportSpec{Sites: []string{"site1"}, Period: "7d"}})},
			{"delete", call(site1, "DELETE", "/api/reports/x", s.handleDeleteReport, id, nil)},
		} {
			if c.rec.Code != http.StatusNotFound {
				t.Errorf("%q: site1 account's %s got %d, want 404", name, c.what, c.rec.Code)
			}
		}
		if r, err := st.Report(ctx, id); err != nil || r.Name != name || r.LastError != "" || r.LastSentAt != 0 {
			t.Errorf("%q was touched by the site1 account: %+v %v", name, r, err)
		}
	}

	// Its own sites' reports work, and the PDF is made of its own hosts.
	if rec := call(site1, "GET", "/api/reports/x/pdf", s.handleReportPDF, ids["site1"], nil); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("site1 report PDF for the site1 account: %d %s", rec.Code, rec.Body.String())
	}

	// Nothing it saves or makes can reach outside site1.
	for _, spec := range []store.ReportSpec{
		{Sites: []string{"site2"}, Period: "7d"},
		{Sites: []string{"site1"}, Hosts: []string{"12"}, Period: "7d"},
		{Sensors: []store.ReportSensor{{HostID: "12", Items: []string{"120"}}}, Period: "7d"},
	} {
		body := map[string]any{"name": "sneaky", "spec": spec}
		if rec := call(site1, "POST", "/api/reports", s.handleCreateReport, 0, body); rec.Code != http.StatusBadRequest {
			t.Errorf("site1 account saved %+v: %d", spec, rec.Code)
		}
		if rec := call(site1, "POST", "/api/reports/pdf", s.handleDraftPDF, 0, body); rec.Code != http.StatusBadRequest {
			t.Errorf("site1 account made a PDF of %+v: %d", spec, rec.Code)
		}
		if rec := call(site1, "PUT", "/api/reports/x", s.handleUpdateReport, ids["site1"], body); rec.Code != http.StatusBadRequest {
			t.Errorf("site1 account widened its report to %+v: %d", spec, rec.Code)
		}
	}
}
