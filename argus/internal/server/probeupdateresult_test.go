// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus/internal/auth"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// A sidecar's report is data: a known kind, a tag's shape, a time and one printable line.
func TestCleanUpdateResult(t *testing.T) {
	what, res, ok := cleanUpdateResult(&updateResultReport{What: "updater", Tag: "latest", At: 1_800_000_000,
		Why: "  the image is not signed by its build\n(timeout: can't execute 'cosign')\x1b[0m;\trefusing to run it  "})
	if !ok || what != "updater" || res.Tag != "latest" || res.SAt != 1_800_000_000 {
		t.Fatalf("a good report is kept: %q %+v %v", what, res, ok)
	}
	if want := "the image is not signed by its build (timeout: can't execute 'cosign')[0m; refusing to run it"; res.Why != want {
		t.Errorf("why is one printable line:\n got %q\nwant %q", res.Why, want)
	}
	_, res, ok = cleanUpdateResult(&updateResultReport{What: "probe", Tag: "7.0.32-r27", At: 5, Why: strings.Repeat("x", 500)})
	if !ok || len([]rune(res.Why)) != updateWhyMax || !strings.HasSuffix(res.Why, "...") {
		t.Errorf("a long reason is cut to %d characters: %d", updateWhyMax, len([]rune(res.Why)))
	}
	for name, in := range map[string]*updateResultReport{
		"nothing":         nil,
		"an unknown kind": {What: "core", Tag: "latest", At: 5, Why: "x"},
		"no time":         {What: "probe", Tag: "latest", At: 0, Why: "x"},
		"a bad tag":       {What: "probe", Tag: "la test; rm", At: 5, Why: "x"},
		"no reason":       {What: "probe", Tag: "latest", At: 5, Why: " \n\t "},
	} {
		if _, _, ok := cleanUpdateResult(in); ok {
			t.Errorf("%s is dropped", name)
		}
	}
}

// What the sidecar says about an update that didn't take fails it at once, with the reason in the
// notice and on the Updates page, and is told once.
func TestReportedUpdateFailure(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr, err := settings.New(ctx, st, zabbix.New("", ""))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, mgr: mgr, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const raw, proxy = "probe-token", "proxy-site1"
	if err := st.UpsertProbeCredential(ctx, proxy, auth.HashToken(raw)); err != nil {
		t.Fatal(err)
	}
	checkin := func(body string) {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/probes/checkin", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		s.handleProbeCheckin(w, r)
		if w.Code != 200 {
			t.Fatalf("check-in: %d %s", w.Code, w.Body.String())
		}
	}
	events := func() []notice {
		return s.selfUpdateEvents(ctx, proxy, "updater", "0.2.24", noticeUpdVerPrefix, noticeUpdPending, "Probe site1", "site1")
	}
	job := func() *probeJob {
		return s.probeJob(ctx, proxy, "", noticeUpdPending, noticeUpdFailed, noticeUpdFailedWhy, "0.2.24", "0.2.26", time.Now().Unix())
	}
	const why = "the image is not signed by its build (timeout: can't execute 'cosign': Exec format error); refusing to run it - the sidecar was left untouched"
	report := func(at string) string {
		return `{"selfupdate":true,"updater_version":"0.2.24","update_result":{"what":"updater","tag":"latest","at":` + at + `,"why":"` + why + `"}}`
	}

	// A report from a caller that isn't the sidecar is not taken.
	checkin(`{"version":"7.0.32-r26","update_result":{"what":"updater","tag":"latest","at":1,"why":"x"}}`)
	if _, ok := s.updateResult(ctx, updateResultUpdater+proxy); ok {
		t.Fatal("only the sidecar reports an update's outcome")
	}

	// Handed out a minute ago; the sidecar says it refused the image.
	_ = st.MetaSet(ctx, noticeUpdPending+proxy, "latest|"+itoa64(time.Now().Unix()-60))
	events() // remembers the version
	if j := job(); j == nil || j.State != "updating" {
		t.Fatalf("handed out: %+v", j)
	}
	checkin(report("1800000000"))
	ev := events()
	if len(ev) != 1 || ev[0].level != noticeError || ev[0].title != "The updater of probe site1 failed to update to latest" {
		t.Fatalf("one failure notice: %+v", ev)
	}
	if want := "It's still on 0.2.24. Its updater says: " + why; ev[0].detail != want {
		t.Errorf("the notice carries the reason:\n got %q\nwant %q", ev[0].detail, want)
	}
	if _, ok, _ := st.MetaGet(ctx, noticeUpdPending+proxy); ok {
		t.Error("the hand-out is over")
	}
	if j := job(); j == nil || j.State != "failed" || j.Tag != "latest" || j.Why != why {
		t.Fatalf("failed, with its reason: %+v", j)
	}

	// The sidecar sends the same record with every check-in: told once.
	checkin(report("1800000000"))
	if ev := events(); len(ev) != 0 {
		t.Fatalf("the same report is not told twice: %+v", ev)
	}

	// A new try that fails the same way is a new record, and told again.
	_ = st.MetaDelete(ctx, noticeUpdFailed+proxy)
	_ = st.MetaDelete(ctx, noticeUpdFailedWhy+proxy)
	if j := job(); j != nil {
		t.Fatalf("a new try clears the old failure: %+v", j)
	}
	_ = st.MetaSet(ctx, noticeUpdPending+proxy, "latest|"+itoa64(time.Now().Unix()-30))
	checkin(report("1800000000")) // still the old record: nothing new yet
	if ev := events(); len(ev) != 0 {
		t.Fatalf("the old record doesn't fail the new try: %+v", ev)
	}
	if j := job(); j == nil || j.State != "updating" {
		t.Fatalf("the new try is still going: %+v", j)
	}
	checkin(report("1800000600"))
	if ev := events(); len(ev) != 1 {
		t.Fatalf("the new failure is told: %+v", ev)
	}

	// The grace time told a failure with no word from the sidecar; the reason comes late: it joins
	// the failure on the page, with no second notice.
	_ = st.MetaSet(ctx, noticeUpdFailed+proxy, "latest|"+itoa64(time.Now().Unix()-120))
	_ = st.MetaDelete(ctx, noticeUpdFailedWhy+proxy)
	if j := job(); j == nil || j.State != "failed" || j.Why != "" {
		t.Fatalf("failed, reason unknown: %+v", j)
	}
	checkin(report("1800001200"))
	if ev := events(); len(ev) != 0 {
		t.Fatalf("a late reason is no second notice: %+v", ev)
	}
	if j := job(); j == nil || j.Why != why {
		t.Fatalf("the late reason shows: %+v", j)
	}

	// The fleet target failing with nothing handed out is a failure too.
	pev := func() []notice {
		return s.selfUpdateEvents(ctx, proxy, "probe", "7.0.32-r26", noticeProbeVerPrefix, noticeProbePending, "Probe site1", "site1")
	}
	pev()
	checkin(`{"selfupdate":true,"updater_version":"0.2.24","update_result":{"what":"probe","tag":"7.0.32-r27","at":1800002000,"why":"pull of the image failed after 3 attempts - the proxy was left untouched"}}`)
	ev = pev()
	if len(ev) != 1 || ev[0].title != "Probe site1 failed to update to 7.0.32-r27" || !strings.Contains(ev[0].detail, "pull of the image failed after 3 attempts") {
		t.Fatalf("the probe's own failure: %+v", ev)
	}
	pj := s.probeJob(ctx, proxy, "", noticeProbePending, noticeProbeFailed, noticeProbeFailedWhy, "7.0.32-r26", "7.0.32-r27", time.Now().Unix())
	if pj == nil || pj.State != "failed" || !strings.HasPrefix(pj.Why, "pull of the image failed") {
		t.Fatalf("the probe's failure shows with its reason: %+v", pj)
	}
	// The sidecar's own failure is untouched by the probe's.
	if j := job(); j == nil || j.Why != why {
		t.Fatalf("one kind's reason doesn't replace the other's: %+v", j)
	}

	// A report for a version that runs after all is not a failure.
	checkin(`{"selfupdate":true,"updater_version":"0.2.24","update_result":{"what":"probe","tag":"7.0.32-r26","at":1800003000,"why":"stale"}}`)
	if ev := pev(); len(ev) != 0 {
		t.Fatalf("the version asked for runs: %+v", ev)
	}
}
