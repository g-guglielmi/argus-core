// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"argus/internal/buildinfo"
)

// imageCollectors is where the image carries the core's collectors (the Dockerfile copies
// deploy/core/externalscripts/*.py there).
const imageCollectors = "/collectors"

// collectorsSummary is what `argus install-collectors` prints, for the argus-updater to report.
type collectorsSummary struct {
	Version   string   `json:"version"`   // the Argus build whose collectors these are
	Installed []string `json:"installed"` // written this time
	Unchanged []string `json:"unchanged"` // already current
}

// runInstallCollectors is `argus install-collectors [folder]`. The core's Zabbix server is a host
// package, not a container, so the collectors it runs for the hosts it monitors can't ride the image
// by themselves: after every core update the argus-updater runs this image once, as root with only
// the host's Zabbix ExternalScripts folder bound in (at /dst unless named), so that folder always
// holds the running version's collectors.
func runInstallCollectors(args []string) int {
	dst := "/dst"
	if len(args) > 1 {
		dst = args[1]
	}
	if err := installCollectors(imageCollectors, dst, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "argus install-collectors:", err)
		return 1
	}
	return 0
}

// installCollectors copies every *.py in src into dst, writing a file only when its content or mode
// differs (to a temporary name, then renamed over, so Zabbix never runs half a script) and touching
// nothing else in dst. It writes a collectorsSummary to w.
func installCollectors(src, dst string, w io.Writer) error {
	ents, err := os.ReadDir(src)
	if err != nil {
		return errors.New("this image carries no collectors")
	}
	if fi, err := os.Stat(dst); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", dst)
	}
	sum := collectorsSummary{Version: buildinfo.Version, Installed: []string{}, Unchanged: []string{}}
	for _, e := range ents {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".py") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			return err
		}
		target := filepath.Join(dst, name)
		if cur, err := os.ReadFile(target); err == nil && bytes.Equal(cur, b) {
			if fi, err := os.Stat(target); err == nil && fi.Mode().Perm() == 0o755 {
				sum.Unchanged = append(sum.Unchanged, name)
				continue
			}
		}
		tmp := filepath.Join(dst, "."+name+".argus-new")
		if err := os.WriteFile(tmp, b, 0o755); err != nil {
			return fmt.Errorf("could not write %s in %s: %w", name, dst, err)
		}
		// WriteFile's mode passes through the umask; the collectors must be executable by Zabbix.
		if err := os.Chmod(tmp, 0o755); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("could not make %s executable: %w", name, err)
		}
		if err := os.Rename(tmp, target); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("could not replace %s: %w", name, err)
		}
		sum.Installed = append(sum.Installed, name)
	}
	if len(sum.Installed)+len(sum.Unchanged) == 0 {
		return errors.New("this image carries no collectors")
	}
	sort.Strings(sum.Installed)
	sort.Strings(sum.Unchanged)
	return json.NewEncoder(w).Encode(sum)
}
