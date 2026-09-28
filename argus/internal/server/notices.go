// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"argus/internal/buildinfo"
	"argus/internal/notify"
	"argus/internal/store"
)

// System notices: Argus's own news, as neutral [INFO] messages sent once each to the channels that
// have "System notices" switched on (off by default). Two shapes:
//
//   - a condition that holds for a while - an update available, a probe behind, pending OS updates,
//     a reboot needed, a channel that keeps failing - is told once it has held for its own minimum
//     time (so routine churn, like security updates the VMs apply by themselves within a day, stays
//     quiet), and again only after it cleared and came back;
//   - an event - a self-update finished or failed, a discovery scan finished - is told once.
//
// Most of these states are only read on request elsewhere (caches, the update-dir files, the probes'
// check-in rows), so one loop compares them against the ledger once a minute.

const (
	noticeInterval = time.Minute
	// Pending handed-out probe/updater updates: one not running this long after its hand-out failed
	// (the updater rolls back when the new container doesn't come up healthy).
	selfUpdateGrace = 20 * time.Minute

	noticeWatermarkScans = "notice_scan_watermark" // app_meta: newest discovery job completion already told
	noticeProbeVerPrefix = "notice_ver_probe:"     // app_meta: last seen probe version, per proxy
	noticeUpdVerPrefix   = "notice_ver_updater:"   // app_meta: last seen updater version, per proxy
	noticeProbePending   = "notice_pending_probe:" // app_meta: update handed out to a probe, "tag|unix"
	noticeUpdPending     = "notice_pending_updater:"
)

// notice is one system notice: a stable key (the ledger dedupes on it), what to say, and where it
// belongs - a site (a probe's) or the whole install ("").
type notice struct {
	key, title, detail string
	host, site, view   string // host label, site group, and the Argus view to link to
	minAge             time.Duration
	// skip leaves a destination out (a failing channel isn't told about itself; a personal channel's
	// failure only goes to its owner).
	skip func(notifyDest) bool
}

// startNotices runs the system-notice loop in the background.
func (s *Server) startNotices(ctx context.Context) {
	go func() {
		first := time.NewTimer(90 * time.Second) // let the release/probe caches load first
		defer first.Stop()
		t := time.NewTicker(noticeInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-first.C:
			case <-t.C:
			}
			c, cancel := context.WithTimeout(ctx, 45*time.Second)
			s.noticeTick(c)
			cancel()
		}
	}()
}

func (s *Server) noticeTick(ctx context.Context) {
	channels, _ := s.st.EnabledNotifyChannels(ctx)
	userChannels, _ := s.st.EnabledUserNotifyChannels(ctx)
	var userEmails []string
	if anyEmailToUsers(channels) {
		userEmails, _ = s.st.NotifyUserEmails(ctx)
	}
	var dests []notifyDest
	for _, d := range notifyDests(s.st, channels, userChannels, userEmails, s.logger) {
		if d.notices {
			dests = append(dests, d)
		}
	}

	var conds []notice
	conds = append(conds, s.releaseNotices()...)
	conds = append(conds, s.coreOSNotices()...)
	probeConds, probeEvents := s.probeNotices(ctx)
	conds = append(conds, probeConds...)
	conds = append(conds, channelFailingNotices(channels, userChannels)...)

	// Conditions: track since when each holds; tell the ones old enough; forget the ones that ended.
	keys := make([]string, 0, len(conds))
	for _, n := range conds {
		keys = append(keys, n.key)
	}
	since, err := s.st.SyncNoticeConditions(ctx, keys)
	if err == nil {
		now := time.Now()
		for _, n := range conds {
			if now.Sub(time.Unix(since[n.key], 0)) >= n.minAge {
				s.tellOnce(ctx, n, store.NoticeFact, dests)
			}
		}
		if sent, err := s.st.SentFacts(ctx); err == nil {
			for _, k := range sent {
				if _, holds := since[k]; !holds {
					_ = s.st.ReleaseNotice(ctx, k)
				}
			}
		}
	}

	// Events.
	events := append(probeEvents, s.coreUpdateNotices()...)
	events = append(events, s.scanNotices(ctx)...)
	for _, n := range events {
		s.tellOnce(ctx, n, store.NoticeEvent, dests)
	}
	_ = s.st.PruneNoticeEvents(ctx)
}

// tellOnce sends a notice to the destinations it belongs to, unless it was told already. With no
// destination at all it isn't claimed, so it still goes out once someone turns notices on.
func (s *Server) tellOnce(ctx context.Context, n notice, kind string, dests []notifyDest) {
	to := noticeTargets(n, dests)
	if len(to) == 0 {
		return
	}
	if fresh, err := s.st.ClaimNotice(ctx, n.key, kind); err != nil || !fresh {
		return
	}
	ev := notify.Event{
		Kind: "info", State: "ok", Name: n.title, Detail: n.detail, Host: n.host, Site: n.site,
		When: time.Now().In(s.mgr.Location()), OpenURL: viewLink(s.mgr.PublicURL(), n.view),
	}
	s.logger.Info("notices: sending", "key", n.key, "channels", len(to))
	for _, d := range to {
		d.send(ctx, ev)
	}
}

// noticeTargets picks the notice-enabled destinations a notice belongs to: all of them for news about
// the whole install, those serving the site for a probe's, minus any the notice leaves out.
func noticeTargets(n notice, dests []notifyDest) []notifyDest {
	var to []notifyDest
	for _, d := range dests {
		if !d.notices || (n.skip != nil && n.skip(d)) {
			continue
		}
		if n.site == "" || channelMatches(d.sites, 0, []string{n.site}, 0) {
			to = append(to, d)
		}
	}
	return to
}

// plural renders "1 device" / "3 devices".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// viewLink deep-links to an Argus view ("" without a public URL).
func viewLink(publicURL, view string) string {
	if publicURL == "" || view == "" {
		return ""
	}
	return strings.TrimRight(publicURL, "/") + "/?view=" + view
}

// releaseNotices: a newer Argus release than the one running.
func (s *Server) releaseNotices() []notice {
	latest := s.appLatest.get()
	if _, avail := appUpdateStatus(buildinfo.Version, latest); !avail || latest == "" {
		return nil
	}
	detail := "This instance runs " + buildinfo.Version + "."
	if s.cfg.SelfUpdateEnabled() {
		detail += "\nUpdate from Settings -> About."
	}
	return []notice{{key: "argus-release:" + latest, title: "Argus " + latest + " is available", detail: detail, view: "settings"}}
}

// coreUpdateNotices: the outcome of a core self-update (the sidecar reports it in the update dir;
// a failure has already been rolled back).
func (s *Server) coreUpdateNotices() []notice {
	st, err := s.currentUpdateState()
	if err != nil || (st.State != "success" && st.State != "failed") {
		return nil
	}
	id := st.Target + "|" + st.FinishedAt
	if st.State == "success" {
		return []notice{{key: "core-update:" + id, title: "Argus updated to " + st.Target, detail: "The update from " + st.From + " finished and Argus restarted on the new version.", view: "settings"}}
	}
	detail := "Argus is still on " + st.From + "."
	if st.Message != "" {
		detail = st.Message + "\n" + detail
	}
	return []notice{{key: "core-update:" + id, title: "Argus update to " + st.Target + " failed", detail: detail, view: "settings"}}
}

// coreOSNotices: the core VM's pending security updates, a reboot it needs, and a Zabbix server minor
// update. Security updates apply by themselves, so only ones still pending after two days are news.
func (s *Server) coreOSNotices() []notice {
	v := s.coreOSStatus()
	if !v.Available {
		return nil
	}
	var out []notice
	if v.SecUpdates > 0 {
		out = append(out, notice{key: "core-os-updates", title: "Security updates pending on the core VM",
			detail: plural(v.SecUpdates, "security update has", "security updates have") + " been waiting for over two days; automatic patching may be stuck.",
			view:   "settings", minAge: 48 * time.Hour})
	}
	if v.RebootRequired {
		out = append(out, notice{key: "core-reboot", title: "The core VM needs a reboot",
			detail: "Updates installed on the core VM need a restart to take effect. It reboots in its reboot window, or restart it yourself.",
			view:   "settings", minAge: 24 * time.Hour})
	}
	if v.ZbxCandidate != "" && v.ZbxServer != "" && v.ZbxCandidate != v.ZbxServer {
		out = append(out, notice{key: "core-zabbix:" + v.ZbxCandidate, title: "Zabbix " + v.ZbxCandidate + " is available for the core",
			detail: "The core runs Zabbix server " + v.ZbxServer + ". It updates in its Zabbix update window, or from Settings.",
			view:   "settings", minAge: 24 * time.Hour})
	}
	return out
}

// probeNotices: per probe, a probe or updater behind the newest release (for six hours: the fleet
// normally updates itself sooner), pending OS updates or a reboot on a probe VM, and the outcome of
// self-updates - a new version reported (success) or a handed-out update not running in time (failed).
func (s *Server) probeNotices(ctx context.Context) (conds, events []notice) {
	agents, err := s.st.ProbeAgents(ctx)
	if err != nil {
		return nil, nil
	}
	target, err := s.st.ProbeTargetVersion(ctx)
	if err != nil {
		target = "latest"
	}
	latest, updLatest := s.probeLatest.get(), s.updaterLatest.get()
	for name, ag := range agents {
		site := probeSite(name)
		host := "Probe " + site
		if ag.Version != "" && updateStatus(ag.Version, target, latest) == "outdated" {
			conds = append(conds, notice{key: "probe-behind:" + name + ":" + latest, title: "Probe " + site + " is behind",
				detail: "It runs " + ag.Version + "; " + latest + " is available.", host: host, site: site, view: "probes", minAge: 6 * time.Hour})
		}
		if ag.SelfUpdate && ag.UpdaterVersion != "" && updaterStatus(ag.UpdaterVersion, updLatest) == "outdated" {
			conds = append(conds, notice{key: "updater-behind:" + name + ":" + updLatest, title: "The updater of probe " + site + " is behind",
				detail: "It runs " + ag.UpdaterVersion + "; " + updLatest + " is available.", host: host, site: site, view: "probes", minAge: 6 * time.Hour})
		}
		if ag.OSReportedAt > 0 && ag.SecUpdates > 0 {
			conds = append(conds, notice{key: "probe-os-updates:" + name, title: "Security updates pending on probe " + site,
				detail: plural(ag.SecUpdates, "security update has", "security updates have") + " been waiting for over two days; automatic patching may be stuck.",
				host:   host, site: site, view: "probes", minAge: 48 * time.Hour})
		}
		if ag.OSReportedAt > 0 && ag.RebootRequired {
			conds = append(conds, notice{key: "probe-reboot:" + name, title: "Probe " + site + " needs a reboot",
				detail: "Updates installed on the probe VM need a restart to take effect.", host: host, site: site, view: "probes", minAge: 24 * time.Hour})
		}
		events = append(events, s.selfUpdateEvents(ctx, name, "probe", ag.Version, noticeProbeVerPrefix, noticeProbePending, host, site)...)
		if ag.UpdaterVersion != "" {
			events = append(events, s.selfUpdateEvents(ctx, name, "updater", ag.UpdaterVersion, noticeUpdVerPrefix, noticeUpdPending, host, site)...)
		}
	}
	return conds, events
}

// selfUpdateEvents turns a probe's (or its updater's) version history into events: a changed version
// is an update that went through; an update Argus handed out that isn't running after the grace time
// failed (the updater rolled it back).
func (s *Server) selfUpdateEvents(ctx context.Context, proxy, what, version, verPrefix, pendPrefix, host, site string) []notice {
	var out []notice
	subject := "Probe " + site
	if what == "updater" {
		subject = "The updater of probe " + site
	}
	if version != "" {
		prev, seen, _ := s.st.MetaGet(ctx, verPrefix+proxy)
		if seen && prev != "" && prev != version {
			out = append(out, notice{key: what + "-updated:" + proxy + ":" + version, title: subject + " updated to " + version,
				detail: "It was on " + prev + ".", host: host, site: site, view: "probes"})
		}
		if !seen || prev != version {
			_ = s.st.MetaSet(ctx, verPrefix+proxy, version)
		}
	}
	if pend, ok, _ := s.st.MetaGet(ctx, pendPrefix+proxy); ok {
		tag, at, _ := strings.Cut(pend, "|")
		switch {
		case versionMatchesTag(version, tag):
			_ = s.st.MetaDelete(ctx, pendPrefix+proxy)
		case time.Since(time.Unix(atoi64(at), 0)) > selfUpdateGrace:
			out = append(out, notice{key: what + "-update-failed:" + proxy + ":" + tag + ":" + at, title: subject + " failed to update to " + tag,
				detail: "It's still on " + version + " " + fmt.Sprint(int(selfUpdateGrace.Minutes())) + " minutes after the update was handed out; the updater rolls back a version that doesn't start healthy.",
				host:   host, site: site, view: "probes"})
			_ = s.st.MetaDelete(ctx, pendPrefix+proxy)
		}
	}
	return out
}

// versionMatchesTag reports whether a reported version is the handed-out tag ("7.0.31-r3" vs a tag
// "7.0.31-r3" or "v0.2.5" vs "0.2.5").
func versionMatchesTag(version, tag string) bool {
	v, t := strings.TrimPrefix(version, "v"), strings.TrimPrefix(tag, "v")
	return v != "" && (v == t || strings.HasPrefix(v, t+"+"))
}

// scanNotices: discovery jobs that finished since the last look. The first run only sets the mark.
func (s *Server) scanNotices(ctx context.Context) []notice {
	jobs, err := s.st.ListDiscoveryJobs(ctx, 50)
	if err != nil {
		return nil
	}
	markRaw, seen, _ := s.st.MetaGet(ctx, noticeWatermarkScans)
	mark := atoi64(markRaw)
	newest := mark
	var finished []store.DiscoveryJob
	for _, j := range jobs {
		if (j.State == "done" || j.State == "failed") && j.CompletedAt > mark {
			finished = append(finished, j)
		}
	}
	jobs = finished
	s.adjustNewCounts(ctx, jobs) // devices already monitored aren't new, as on the Discovery page
	var out []notice
	for _, j := range jobs {
		if (j.State != "done" && j.State != "failed") || j.CompletedAt <= mark {
			continue
		}
		if j.CompletedAt > newest {
			newest = j.CompletedAt
		}
		if !seen {
			continue
		}
		site := probeSite(j.ProxyName)
		what, target := "Discovery scan", j.CIDR
		if j.Kind == "unifi" {
			what, target = "UniFi sweep", j.ControllerName
		}
		n := notice{key: fmt.Sprintf("scan-done:%d", j.ID), site: site, view: "discovery"}
		if j.State == "failed" {
			n.title = what + " failed"
			n.detail = strings.TrimSpace(target + " on probe " + site + ": " + j.Error)
		} else {
			n.title = what + " finished"
			n.detail = fmt.Sprintf("%s on probe %s: %s found, %d new.", target, site, plural(j.Found, "device", "devices"), j.NewCount)
			if j.NewCount > 0 {
				n.detail += " Review them in Discovery."
			}
		}
		out = append(out, n)
	}
	if newest > mark || !seen {
		if newest == 0 {
			newest = time.Now().Unix()
		}
		_ = s.st.MetaSet(ctx, noticeWatermarkScans, itoa64(newest))
	}
	return out
}

// channelFailingNotices: a channel whose deliveries have kept failing for half an hour (its last
// attempt failed, and nothing has gone through since). A shared channel is reported on the other
// channels; a personal one only on its owner's other personal channels.
func channelFailingNotices(channels []store.NotifyChannel, userChannels []store.UserNotifyChannel) []notice {
	var out []notice
	for _, c := range channels {
		if c.LastErrorAt == 0 || c.LastErrorAt <= c.LastSentAt {
			continue
		}
		key := store.DeliveryKey(store.DeliveryGlobal, c.ID)
		out = append(out, notice{key: "channel-failing:" + key, title: "Notification channel failing: " + c.Name,
			detail: "Its deliveries keep failing: " + c.LastError, view: "notifications", minAge: 30 * time.Minute,
			skip: func(d notifyDest) bool { return d.key == key }})
	}
	for _, c := range userChannels {
		if c.LastErrorAt == 0 || c.LastErrorAt <= c.LastSentAt {
			continue
		}
		key, owner := store.DeliveryKey(store.DeliveryUser, c.ID), c.UserID
		label := map[string]string{"telegram": "Telegram", "discord": "Discord"}[c.Type]
		if label == "" {
			label = c.Type
		}
		out = append(out, notice{key: "channel-failing:" + key, title: "Your " + label + " channel is failing",
			detail: "Its deliveries keep failing: " + c.LastError, view: "account", minAge: 30 * time.Minute,
			skip: func(d notifyDest) bool { return d.key == key || d.kind != store.DeliveryUser || d.userID != owner }})
	}
	return out
}
