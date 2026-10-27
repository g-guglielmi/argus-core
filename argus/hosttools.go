// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"argus/internal/buildinfo"
)

// imageHostTools is where the image carries the core's host scripts and units (the Dockerfile copies
// deploy/core/host there, manifest included).
const imageHostTools = "/host-tools"

// hostToolsApplyFile is the marker dropped in the shared update dir when a unit changed, or when
// anything changed and the manifest has services to run again: a container cannot reach systemd, so
// argus-host-tools.path on the host starts argus-host-tools-apply, which reloads, enables and starts
// what the marker lists.
const hostToolsApplyFile = "host-tools-apply.json"

// hostToolsSummary is what `argus install-host-tools` prints, for the argus-updater to report.
type hostToolsSummary struct {
	Version      string   `json:"version"`       // the Argus build whose host tools these are
	Installed    []string `json:"installed"`     // written this time
	Unchanged    []string `json:"unchanged"`     // already current
	UnitsChanged []string `json:"units_changed"` // systemd files written this time (the host reloads)
	Apply        bool     `json:"apply"`         // the marker for the host's reload was dropped
}

// hostToolEntry is one manifest line: a file in the image, its destination on the host, its mode and
// flags (render = substitute @STATE_DIR@, enable = the host enables it after a reload, run = the host
// starts it again after any round that wrote a file, for a oneshot that must act on a new script
// right away rather than at its timer's next tick).
type hostToolEntry struct {
	Source, Dest string
	Mode         os.FileMode
	Render       bool
	Enable       bool
	Run          bool
}

// runInstallHostTools is `argus install-host-tools [--root /host] [--state-dir DIR] [--update-dir /update]`.
// The core's host scripts (the OS reporter, the reboot, patch and Zabbix watchers, the sizing tool,
// the backup tools) and their systemd units live on the host, which no container update reaches by
// itself: after every core update the argus-updater runs this image once, as root with the host's
// /usr/local/sbin and /etc/systemd/system bound under --root and the shared dir at --update-dir, and
// this writes the files that changed. --state-dir is the host path of the shared dir, which the
// units carry.
func runInstallHostTools(args []string) int {
	fs := flag.NewFlagSet("install-host-tools", flag.ContinueOnError)
	root := fs.String("root", "/host", "where the host's filesystem is bound")
	stateDir := fs.String("state-dir", "/docker/argus-update", "the host path of the folder shared with the core")
	updateDir := fs.String("update-dir", "/update", "that folder, as bound here")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if err := installHostTools(imageHostTools, *root, *stateDir, *updateDir, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "argus install-host-tools:", err)
		return 1
	}
	return 0
}

// parseHostToolsManifest reads the manifest: one entry per line, "source destination mode [flags]",
// comments and blank lines skipped.
func parseHostToolsManifest(r io.Reader) ([]hostToolEntry, error) {
	var out []hostToolEntry
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	for n, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			return nil, fmt.Errorf("manifest line %d: want source, destination and mode", n+1)
		}
		mode, err := strconv.ParseUint(f[2], 8, 32)
		if err != nil || !strings.HasPrefix(f[1], "/") || strings.Contains(f[0], "/") || strings.Contains(f[1], "..") {
			return nil, fmt.Errorf("manifest line %d: bad entry %q", n+1, line)
		}
		e := hostToolEntry{Source: f[0], Dest: f[1], Mode: os.FileMode(mode)}
		for _, fl := range f[3:] {
			switch fl {
			case "render":
				e.Render = true
			case "enable":
				e.Enable = true
			case "run":
				e.Run = true
			default:
				return nil, fmt.Errorf("manifest line %d: unknown flag %q", n+1, fl)
			}
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, errors.New("the manifest lists nothing")
	}
	return out, nil
}

// installHostTools writes every manifest entry under root, a file only when its content or mode
// differs (to a temporary name, then renamed over, so nothing on the host ever reads half a script),
// and drops the reload marker in updateDir when a systemd file changed, or when any file changed and
// some entry is to run again. It writes a hostToolsSummary to w.
func installHostTools(src, root, stateDir, updateDir string, w io.Writer) error {
	mf, err := os.Open(filepath.Join(src, "manifest"))
	if err != nil {
		return errors.New("this image carries no host tools")
	}
	entries, err := parseHostToolsManifest(mf)
	mf.Close()
	if err != nil {
		return err
	}
	sum := hostToolsSummary{Version: buildinfo.Version, Installed: []string{}, Unchanged: []string{}, UnitsChanged: []string{}}
	var enable, rerun []string
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Source))
		if err != nil {
			return fmt.Errorf("the image has no %s", e.Source)
		}
		if e.Render {
			b = bytes.ReplaceAll(b, []byte("@STATE_DIR@"), []byte(stateDir))
		}
		if e.Enable {
			enable = append(enable, filepath.Base(e.Dest))
		}
		if e.Run {
			rerun = append(rerun, filepath.Base(e.Dest))
		}
		target := filepath.Join(root, filepath.FromSlash(e.Dest))
		if cur, err := os.ReadFile(target); err == nil && bytes.Equal(cur, b) {
			if fi, err := os.Stat(target); err == nil && (runtime.GOOS == "windows" || fi.Mode().Perm() == e.Mode) {
				sum.Unchanged = append(sum.Unchanged, e.Dest)
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("could not create the folder for %s: %w", e.Dest, err)
		}
		tmp := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".argus-new")
		if err := os.WriteFile(tmp, b, e.Mode); err != nil {
			return fmt.Errorf("could not write %s: %w", e.Dest, err)
		}
		// WriteFile's mode passes through the umask; the scripts must stay executable.
		if err := os.Chmod(tmp, e.Mode); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("could not set the mode of %s: %w", e.Dest, err)
		}
		if err := os.Rename(tmp, target); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("could not replace %s: %w", e.Dest, err)
		}
		sum.Installed = append(sum.Installed, e.Dest)
		if strings.HasPrefix(e.Dest, "/etc/systemd/") {
			sum.UnitsChanged = append(sum.UnitsChanged, filepath.Base(e.Dest))
		}
	}
	sort.Strings(sum.Installed)
	sort.Strings(sum.Unchanged)
	sort.Strings(sum.UnitsChanged)
	if len(sum.UnitsChanged) > 0 || (len(sum.Installed) > 0 && len(rerun) > 0) {
		marker := map[string]any{"at": time.Now().Unix(), "version": buildinfo.Version, "enable": strings.Join(enable, " "),
			"run": strings.Join(rerun, " "), "changed": strings.Join(sum.UnitsChanged, " ")}
		mb, _ := json.Marshal(marker)
		final := filepath.Join(updateDir, hostToolsApplyFile)
		tmp := final + ".tmp"
		if err := os.WriteFile(tmp, mb, 0o644); err != nil {
			return fmt.Errorf("could not drop the reload marker in %s: %w", updateDir, err)
		}
		if err := os.Rename(tmp, final); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("could not drop the reload marker: %w", err)
		}
		sum.Apply = true
	}
	return json.NewEncoder(w).Encode(sum)
}
