// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"argus/internal/buildinfo"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Heartbeat: nothing inside Argus can report that Argus itself has stopped, so once a minute, while
// it is healthy end to end, it pings an outside monitor (a healthchecks.io check, an Uptime Kuma
// push monitor, ...). When the pings stop, because the VM is down, Zabbix stopped taking data, the
// alert loop stalled or no channel can deliver, that monitor raises the alarm instead.

const (
	heartbeatInterval  = time.Minute
	heartbeatDataStale = 5 * time.Minute // no probe delivered data for this long: Zabbix isn't taking any
	heartbeatLoopStale = 3 * notifyPollInterval
	heartbeatMetaKey   = "heartbeat:checked_at"
)

// notifierRanAt is when the alert loop last read the problem list (unix s); the heartbeat holds when
// it goes stale.
var notifierRanAt atomic.Int64

// heartbeatStatus is what Settings shows under the Heartbeat URL.
type heartbeatStatus struct {
	At     int64  `json:"at,omitempty"`     // the last check
	OKAt   int64  `json:"ok_at,omitempty"`  // the last ping the monitor accepted
	Held   string `json:"held,omitempty"`   // why the last check sent no ping (Argus isn't healthy)
	Error  string `json:"error,omitempty"`  // why the last ping failed
	Status int    `json:"status,omitempty"` // the monitor's HTTP status for the last ping
}

type heartbeat struct {
	mu   sync.Mutex
	last heartbeatStatus
	run  sync.Mutex // one check at a time (the loop and a "Send now")
}

var heartbeatClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return http.ErrUseLastResponse
		}
		return nil
	},
}

func (s *Server) startHeartbeat(ctx context.Context) {
	go func() {
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if s.mgr.HeartbeatURL() != "" {
					s.beat(ctx)
				}
			}
		}
	}()
}

// beat checks Argus's health and, when it is healthy, pings the monitor.
func (s *Server) beat(ctx context.Context) heartbeatStatus {
	s.hb.run.Lock()
	defer s.hb.run.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := time.Now()
	st := heartbeatStatus{At: now.Unix()}
	s.hb.mu.Lock()
	st.OKAt = s.hb.last.OKAt
	s.hb.mu.Unlock()
	if why := heartbeatHealth(ctx, s.st, s.zbx, now); why != "" {
		st.Held = why
	} else if code, err := sendHeartbeat(ctx, s.mgr.HeartbeatURL()); err != nil {
		st.Error, st.Status = err.Error(), code
	} else {
		st.OKAt, st.Status = now.Unix(), code
	}
	s.hb.mu.Lock()
	changed := s.hb.last.Held != st.Held || s.hb.last.Error != st.Error
	s.hb.last = st
	s.hb.mu.Unlock()
	if changed {
		switch {
		case st.Held != "":
			s.logger.Warn("heartbeat held: Argus isn't healthy", "reason", st.Held)
		case st.Error != "":
			s.logger.Warn("heartbeat ping failed", "err", st.Error)
		default:
			s.logger.Info("heartbeat pinging")
		}
	}
	return st
}

// heartbeatHealth is "" when Argus is healthy end to end, else why not: the Zabbix API answers with
// the token, some probe delivered data lately (so zabbix_server is taking it), the alert loop is
// running, the database takes writes, and not every alert channel is failing.
func heartbeatHealth(ctx context.Context, st *store.Store, zbx *zabbix.Client, now time.Time) string {
	if !zbx.Authenticated() {
		return "the Zabbix API token isn't set"
	}
	proxies, err := zbx.Proxies(ctx)
	if err != nil {
		return "the Zabbix API doesn't answer: " + err.Error()
	}
	if why := heartbeatDataFlow(proxies, now); why != "" {
		return why
	}
	if ran := notifierRanAt.Load(); ran == 0 || now.Sub(time.Unix(ran, 0)) > heartbeatLoopStale {
		return "the alert loop hasn't read the problem list lately"
	}
	if err := st.MetaSet(ctx, heartbeatMetaKey, strconv.FormatInt(now.Unix(), 10)); err != nil {
		return "the database doesn't take writes: " + err.Error()
	}
	return heartbeatChannels(ctx, st)
}

// heartbeatDataFlow is "" while some probe has delivered data within heartbeatDataStale. Probes that
// never connected don't count, and with none that ever did there is nothing to judge by.
func heartbeatDataFlow(proxies []zabbix.Proxy, now time.Time) string {
	var newest int64
	for _, p := range proxies {
		if n := atoi64(p.LastAccess); n > newest {
			newest = n
		}
	}
	if newest == 0 {
		return ""
	}
	if age := now.Sub(time.Unix(newest, 0)); age > heartbeatDataStale {
		return fmt.Sprintf("no probe has delivered data for %d minutes (is zabbix_server running?)", int(age.Minutes()))
	}
	return ""
}

// heartbeatChannels is "" unless every enabled alert channel, shared or personal, failed its last
// send: then nobody would hear about a problem. A channel that never sent yet counts as fine.
func heartbeatChannels(ctx context.Context, st *store.Store) string {
	type health struct{ sent, failed int64 }
	var all []health
	if cs, err := st.EnabledNotifyChannels(ctx); err == nil {
		for _, c := range cs {
			if c.Alerts {
				all = append(all, health{c.LastSentAt, c.LastErrorAt})
			}
		}
	}
	if cs, err := st.EnabledUserNotifyChannels(ctx); err == nil {
		for _, c := range cs {
			if c.Alerts {
				all = append(all, health{c.LastSentAt, c.LastErrorAt})
			}
		}
	}
	if len(all) == 0 {
		return ""
	}
	for _, h := range all {
		if h.failed == 0 || h.sent >= h.failed {
			return ""
		}
	}
	return "every alert channel failed its last send"
}

// sendHeartbeat pings the monitor and returns its HTTP status. The URL is never logged or echoed:
// it usually carries the check's secret.
func sendHeartbeat(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("the heartbeat URL is invalid")
	}
	req.Header.Set("User-Agent", "Argus/"+buildinfo.Version+" heartbeat")
	resp, err := heartbeatClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("the monitor didn't answer (%s)", heartbeatErrText(err))
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("the monitor answered HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// handleHeartbeat serves GET /api/settings/heartbeat: the last check.
func (s *Server) handleHeartbeat(w http.ResponseWriter, _ *http.Request) {
	s.hb.mu.Lock()
	st := s.hb.last
	s.hb.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"configured": s.mgr.HeartbeatURL() != "", "status": st})
}

// handleHeartbeatNow serves POST /api/settings/heartbeat: check and ping now (the Send-now button).
func (s *Server) handleHeartbeatNow(w http.ResponseWriter, r *http.Request) {
	if s.mgr.HeartbeatURL() == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "set a Heartbeat URL first"})
		return
	}
	st := s.beat(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "status": st})
}

// heartbeatErrText is a transport error without the request URL (which carries the check's secret).
func heartbeatErrText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "timed out"
		}
		err = ue.Err
	}
	return err.Error()
}
