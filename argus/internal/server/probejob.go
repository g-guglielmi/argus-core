// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"strings"
	"time"
)

// probeJob is an update Argus asked a probe's sidecar for, as the Probes page shows it until it ends,
// so a page reload never hides one that is still going.
type probeJob struct {
	State string `json:"state"`        // queued | updating | failed
	Tag   string `json:"tag"`          // the image tag asked for
	At    int64  `json:"at,omitempty"` // when it was handed to the sidecar (updating) or given up on (failed)
}

// probeFailedShown is how long an update that didn't take stays on the Probes page (a new try, or
// the version changing, clears it sooner).
const probeFailedShown = 24 * time.Hour

// probeJobOf is where an update stands: queued until the sidecar's next check-in takes it, then
// updating until the probe reports the new version, or failed when it didn't within selfUpdateGrace
// (the system notices decide that; the updater rolls back a version that doesn't start healthy). A
// rolling tag is done once the probe runs the newest published version. handed and failed are the
// "tag|unix" records the check-in and the notices keep; nil when there is nothing to show.
func probeJobOf(queued, handed, failed, version, newest string, now int64) *probeJob {
	if queued != "" {
		return &probeJob{State: "queued", Tag: queued}
	}
	if tag, at, ok := strings.Cut(handed, "|"); ok && tag != "" {
		if versionMatchesTag(version, tag) || (rollingTag(tag) && versionMatchesTag(version, newest)) {
			return nil // done; the notices clear the record within a minute
		}
		return &probeJob{State: "updating", Tag: tag, At: atoi64(at)}
	}
	if tag, at, ok := strings.Cut(failed, "|"); ok && tag != "" && now-atoi64(at) < int64(probeFailedShown.Seconds()) {
		return &probeJob{State: "failed", Tag: tag, At: atoi64(at)}
	}
	return nil
}

// probeJob reads the records for one probe's proxy or sidecar update.
func (s *Server) probeJob(ctx context.Context, proxy, queued, pendPrefix, failPrefix, version, newest string, now int64) *probeJob {
	handed, _, _ := s.st.MetaGet(ctx, pendPrefix+proxy)
	failed, _, _ := s.st.MetaGet(ctx, failPrefix+proxy)
	return probeJobOf(queued, handed, failed, version, newest, now)
}
