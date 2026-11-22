// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package testrestore says whether this core is a test restore: a backup restored on a spare machine to
// see that it works, next to the real core that is still running (argus-restore restore --test,
// DESIGN section 14e).
//
// A restored core is a complete copy: the same channels, the same heartbeat URL, the same scheduled
// reports, the same syslog target, the same backup target. Left alone it would alert everyone about
// probes that only it cannot hear, vouch for the real core's health, and export its own archives.
// So a test restore sends nothing by itself and exports nothing, until `argus-restore live` ends it.
//
// The mark is one file in the data folder, written by argus-restore before Argus starts. The file
// being there is the whole signal (a file that cannot be read still means a test restore), and it is looked
// at again every few seconds, so ending the test needs no restart.
package testrestore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileName is the mark's name inside the data folder.
const FileName = "restore-test.json"

// ErrNoSend is what a send answers on a test restore.
var ErrNoSend = errors.New("this core is a test restore: it sends nothing (sudo argus-restore live ends the test)")

// Info is what argus-restore wrote down about the test restore.
type Info struct {
	Since   int64  `json:"since"`             // when the restore finished, unix s
	Archive string `json:"archive,omitempty"` // the archive's file name
	From    string `json:"from,omitempty"`    // the host the archive was made on
	MadeAt  int64  `json:"made_at,omitempty"` // when the archive was made, unix s
}

const recheck = 5 * time.Second

var (
	mu      sync.Mutex
	path    string
	checked time.Time
	cur     *Info
)

// Init names the data folder the mark lives in. Without it no core is ever a test restore.
func Init(dataDir string) {
	mu.Lock()
	defer mu.Unlock()
	path, checked, cur = "", time.Time{}, nil
	if dataDir != "" {
		path = filepath.Join(dataDir, FileName)
	}
}

// Current is the test restore this core is, or nil.
func Current() *Info {
	mu.Lock()
	defer mu.Unlock()
	if path == "" {
		return nil
	}
	if !checked.IsZero() && time.Since(checked) < recheck {
		return cur
	}
	checked = time.Now()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cur = nil
		} else {
			cur = &Info{} // it is there but unreadable: still a test restore
		}
		return cur
	}
	in := &Info{}
	_ = json.Unmarshal(b, in)
	cur = in
	return cur
}

// Active reports whether this core is a test restore.
func Active() bool { return Current() != nil }
