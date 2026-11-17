// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// Why a probe update didn't take. A sidecar that refuses an image (the pull failed, its digest or
// signature didn't check) or rolls one back (the new version didn't start healthy) knows why, and
// from updater 0.2.27 on says so with its check-in (update_result). Before that Argus could only
// see the old version still running twenty minutes after the hand-out, and the reason stayed in
// the sidecar's own log on the probe's host.
//
// The check-in keeps the newest report per probe and kind; the system notices (selfUpdateEvents)
// turn a new one into a failed update at once, with the reason in the notice and on the Updates
// page (probeJob.Why).

// updateResultReport is the record as the sidecar sends it.
type updateResultReport struct {
	What string `json:"what"` // probe | updater
	Tag  string `json:"tag"`  // the image tag it was asked for
	At   int64  `json:"at"`   // the sidecar's own time for the record: what tells one from the next
	Why  string `json:"why"`
}

// updateResult is a report as Argus keeps it.
type updateResult struct {
	SAt  int64  `json:"sat"` // the sidecar's At
	At   int64  `json:"at"`  // when Argus first heard of it
	Tag  string `json:"tag"`
	Why  string `json:"why"`
	Told bool   `json:"told"` // the notices have dealt with it
}

const (
	updateResultProbe    = "update_result_probe:" // app_meta: the newest report, JSON updateResult
	updateResultUpdater  = "update_result_updater:"
	noticeProbeFailedWhy = "notice_failed_why_probe:" // app_meta: the reason that goes with notice_failed_*
	noticeUpdFailedWhy   = "notice_failed_why_updater:"
	updateWhyMax         = 300
)

// updateResultKeys are the app_meta prefixes of one kind of update: its newest report, its failed
// record and the reason shown with it.
func updateResultKeys(what string) (result, failed, why string) {
	if what == "updater" {
		return updateResultUpdater, noticeUpdFailed, noticeUpdFailedWhy
	}
	return updateResultProbe, noticeProbeFailed, noticeProbeFailedWhy
}

// cleanUpdateResult takes a sidecar's report as data: a known kind, a tag's shape, a time, and one
// line of printable text of at most updateWhyMax characters. Anything else is dropped.
func cleanUpdateResult(in *updateResultReport) (what string, res updateResult, ok bool) {
	if in == nil || (in.What != "probe" && in.What != "updater") || in.At <= 0 {
		return "", updateResult{}, false
	}
	tag := reportedVersion(in.Tag)
	// Line breaks and tabs become spaces before the other control characters are dropped, so two
	// lines don't run into one word.
	why := strings.Join(strings.Fields(printable(strings.Join(strings.FieldsFunc(in.Why, func(r rune) bool { return r == 10 || r == 13 || r == 9 }), " "))), " ")
	if tag == "" || why == "" {
		return "", updateResult{}, false
	}
	if utf8.RuneCountInString(why) > updateWhyMax {
		why = string([]rune(why)[:updateWhyMax-3]) + "..."
	}
	return in.What, updateResult{SAt: in.At, Tag: tag, Why: why}, true
}

// recordUpdateResult keeps a sidecar's report when it is a new one. The sidecar sends its record
// with every check-in until an update goes through, so the same one comes again and again.
func (s *Server) recordUpdateResult(ctx context.Context, proxy string, in *updateResultReport) {
	what, res, ok := cleanUpdateResult(in)
	if !ok {
		return
	}
	key, _, _ := updateResultKeys(what)
	if cur, ok := s.updateResult(ctx, key+proxy); ok && cur.SAt == res.SAt && cur.Tag == res.Tag && cur.Why == res.Why {
		return
	}
	res.At = time.Now().Unix()
	if b, err := json.Marshal(res); err == nil {
		_ = s.st.MetaSet(ctx, key+proxy, string(b))
		if s.logger != nil {
			s.logger.Info("probe update didn't take", "proxy", proxy, "what", what, "tag", res.Tag, "why", res.Why)
		}
	}
}

// updateResult reads a kept report.
func (s *Server) updateResult(ctx context.Context, key string) (updateResult, bool) {
	raw, ok, _ := s.st.MetaGet(ctx, key)
	var res updateResult
	if !ok || json.Unmarshal([]byte(raw), &res) != nil || res.Tag == "" || res.Why == "" {
		return updateResult{}, false
	}
	return res, true
}

// reportedUpdateFailure deals with a report the notices haven't seen: the update of what (probe |
// updater) failed now, for the reason the sidecar gave. It marks the report told, drops the pending
// hand-out, keeps the failed record with its reason for the Updates page, and returns the notice.
// No notice comes back when there is nothing new to tell: no fresh report, the version asked for
// runs after all, or the grace time already told this failure and the reason is all that's new.
func (s *Server) reportedUpdateFailure(ctx context.Context, proxy, what, subject, version, pendPrefix, host, site string) (notice, bool) {
	resKey, failPrefix, whyPrefix := updateResultKeys(what)
	res, ok := s.updateResult(ctx, resKey+proxy)
	if !ok || res.Told {
		return notice{}, false
	}
	res.Told = true
	if b, err := json.Marshal(res); err == nil {
		_ = s.st.MetaSet(ctx, resKey+proxy, string(b))
	}
	if versionMatchesTag(version, res.Tag) {
		return notice{}, false
	}
	now := time.Now().Unix()
	told := false
	if prev, ok, _ := s.st.MetaGet(ctx, failPrefix+proxy); ok {
		tag, at, _ := strings.Cut(prev, "|")
		told = tag == res.Tag && now-atoi64(at) < int64(selfUpdateGrace.Seconds())
	}
	_ = s.st.MetaDelete(ctx, pendPrefix+proxy)
	_ = s.st.MetaSet(ctx, whyPrefix+proxy, res.Why)
	if told {
		return notice{}, false
	}
	_ = s.st.MetaSet(ctx, failPrefix+proxy, res.Tag+"|"+itoa64(now))
	detail := "Its updater says: " + res.Why
	if version != "" {
		detail = "It's still on " + version + ". " + detail
	}
	return notice{level: noticeError, key: what + "-update-failed:" + proxy + ":" + res.Tag + ":r" + itoa64(res.SAt),
		title: subject + " failed to update to " + res.Tag, detail: detail, host: host, site: site, view: "updates"}, true
}
