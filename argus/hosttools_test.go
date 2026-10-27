// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeTool(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstallHostToolsWritesWhatChangedAndAsksForAReloadOnlyForUnits(t *testing.T) {
	src, root, upd := t.TempDir(), t.TempDir(), t.TempDir()
	writeTool(t, src, "manifest", `# the core's host files
argus-os-report          /usr/local/sbin/argus-os-report            0755
argus-os-report.service  /etc/systemd/system/argus-os-report.service 0644 render
argus-os-report.timer    /etc/systemd/system/argus-os-report.timer   0644 enable
argus-host-tools.path    /etc/systemd/system/argus-host-tools.path   0644 render enable
`)
	writeTool(t, src, "argus-os-report", "#!/usr/bin/env bash\necho report\n")
	writeTool(t, src, "argus-os-report.service", "[Service]\nEnvironment=ARGUS_STATE_DIR=@STATE_DIR@\n")
	writeTool(t, src, "argus-os-report.timer", "[Timer]\nOnBootSec=2min\n")
	writeTool(t, src, "argus-host-tools.path", "[Path]\nPathChanged=@STATE_DIR@/host-tools-apply.json\n")

	var out bytes.Buffer
	if err := installHostTools(src, root, "/docker/argus-update", upd, &out); err != nil {
		t.Fatal(err)
	}
	var sum hostToolsSummary
	if err := json.Unmarshal(out.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	if len(sum.Installed) != 4 || len(sum.Unchanged) != 0 || !sum.Apply {
		t.Fatalf("first run: %+v", sum)
	}
	if got := strings.Join(sum.UnitsChanged, " "); got != "argus-host-tools.path argus-os-report.service argus-os-report.timer" {
		t.Errorf("units changed: %q", got)
	}
	svc, _ := os.ReadFile(filepath.Join(root, "etc", "systemd", "system", "argus-os-report.service"))
	if !strings.Contains(string(svc), "ARGUS_STATE_DIR=/docker/argus-update") {
		t.Errorf("the state dir was not rendered: %q", svc)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(root, "usr", "local", "sbin", "argus-os-report")); fi.Mode().Perm() != 0o755 {
			t.Errorf("script mode = %o", fi.Mode().Perm())
		}
	}
	mb, err := os.ReadFile(filepath.Join(upd, hostToolsApplyFile))
	if err != nil {
		t.Fatal("no reload marker:", err)
	}
	var marker map[string]any
	_ = json.Unmarshal(mb, &marker)
	if marker["enable"] != "argus-os-report.timer argus-host-tools.path" {
		t.Errorf("marker enable = %v", marker["enable"])
	}
	if _, err := os.Stat(filepath.Join(upd, hostToolsApplyFile+".tmp")); err == nil {
		t.Error("the marker's temporary file was left behind")
	}

	// A second run finds everything current: nothing written, no marker.
	_ = os.Remove(filepath.Join(upd, hostToolsApplyFile))
	out.Reset()
	if err := installHostTools(src, root, "/docker/argus-update", upd, &out); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(out.Bytes(), &sum)
	if len(sum.Installed) != 0 || len(sum.Unchanged) != 4 || sum.Apply {
		t.Fatalf("second run: %+v", sum)
	}
	if _, err := os.Stat(filepath.Join(upd, hostToolsApplyFile)); err == nil {
		t.Error("a run that changed no unit must not ask for a reload")
	}

	// A script change alone is written without a reload; a unit change asks for one.
	writeTool(t, src, "argus-os-report", "#!/usr/bin/env bash\necho report v2\n")
	out.Reset()
	_ = installHostTools(src, root, "/docker/argus-update", upd, &out)
	_ = json.Unmarshal(out.Bytes(), &sum)
	if strings.Join(sum.Installed, " ") != "/usr/local/sbin/argus-os-report" || sum.Apply {
		t.Errorf("script-only change: %+v", sum)
	}
	if _, err := os.Stat(filepath.Join(upd, hostToolsApplyFile)); err == nil {
		t.Error("a script change with nothing to run again must not drop the marker")
	}
	writeTool(t, src, "argus-os-report.timer", "[Timer]\nOnBootSec=5min\n")
	out.Reset()
	_ = installHostTools(src, root, "/docker/argus-update", upd, &out)
	_ = json.Unmarshal(out.Bytes(), &sum)
	if !sum.Apply || strings.Join(sum.UnitsChanged, " ") != "argus-os-report.timer" {
		t.Errorf("unit change: %+v", sum)
	}
}

func TestInstallHostToolsRunsAgainWhatTheManifestSaysAfterAnyChange(t *testing.T) {
	// A oneshot with run starts again after a round that wrote only a script, so a new version of it
	// acts now rather than at its timer's next tick.
	src, root, upd := t.TempDir(), t.TempDir(), t.TempDir()
	writeTool(t, src, "manifest", `argus-web-guard          /usr/local/sbin/argus-web-guard            0755
argus-web-guard.service  /etc/systemd/system/argus-web-guard.service 0644 enable run
argus-web-guard.timer    /etc/systemd/system/argus-web-guard.timer   0644 enable
`)
	writeTool(t, src, "argus-web-guard", "#!/usr/bin/env bash\necho v1\n")
	writeTool(t, src, "argus-web-guard.service", "[Service]\nType=oneshot\n")
	writeTool(t, src, "argus-web-guard.timer", "[Timer]\nOnUnitActiveSec=1h\n")
	if err := installHostTools(src, root, "/docker/argus-update", upd, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(upd, hostToolsApplyFile))

	writeTool(t, src, "argus-web-guard", "#!/usr/bin/env bash\necho v2\n")
	var out bytes.Buffer
	_ = installHostTools(src, root, "/docker/argus-update", upd, &out)
	var sum hostToolsSummary
	_ = json.Unmarshal(out.Bytes(), &sum)
	if !sum.Apply || len(sum.UnitsChanged) != 0 {
		t.Fatalf("script-only change with a run entry: %+v", sum)
	}
	mb, err := os.ReadFile(filepath.Join(upd, hostToolsApplyFile))
	if err != nil {
		t.Fatal("no marker:", err)
	}
	var marker map[string]any
	_ = json.Unmarshal(mb, &marker)
	if marker["run"] != "argus-web-guard.service" || marker["changed"] != "" {
		t.Errorf("marker = %v", marker)
	}

	// Nothing written: nothing to run.
	_ = os.Remove(filepath.Join(upd, hostToolsApplyFile))
	_ = installHostTools(src, root, "/docker/argus-update", upd, &bytes.Buffer{})
	if _, err := os.Stat(filepath.Join(upd, hostToolsApplyFile)); err == nil {
		t.Error("an unchanged round must not drop the marker")
	}
}

func TestInstallHostToolsRefusesABadManifestAndAMissingFile(t *testing.T) {
	src, root, upd := t.TempDir(), t.TempDir(), t.TempDir()
	if err := installHostTools(src, root, "/x", upd, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "no host tools") {
		t.Errorf("no manifest: %v", err)
	}
	writeTool(t, src, "manifest", "argus-os-report /usr/local/sbin/argus-os-report 0755\n")
	if err := installHostTools(src, root, "/x", upd, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "has no argus-os-report") {
		t.Errorf("missing source: %v", err)
	}
	for _, bad := range []string{"a relative/path 0755\n", "a /etc/x 0755 explode\n", "../a /etc/x 0755\n", "a /etc/../x 0755\n", "a /etc/x notamode\n"} {
		if _, err := parseHostToolsManifest(strings.NewReader(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := parseHostToolsManifest(strings.NewReader("# only comments\n\n")); err == nil {
		t.Error("an empty manifest must be refused")
	}
}

func TestRepoManifestMatchesTheHostFolder(t *testing.T) {
	// The manifest in deploy/core/host lists files that exist, every timer and path unit there is
	// listed with enable, and nothing listed points outside the two folders the updater binds.
	host := filepath.Join("..", "deploy", "core", "host")
	mf, err := os.Open(filepath.Join(host, "manifest"))
	if err != nil {
		t.Skip("no repo checkout around the binary")
	}
	defer mf.Close()
	entries, err := parseHostToolsManifest(mf)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]hostToolEntry{}
	for _, e := range entries {
		listed[e.Source] = e
		if _, err := os.Stat(filepath.Join(host, e.Source)); err != nil {
			t.Errorf("manifest lists %s, which is not in deploy/core/host", e.Source)
		}
		if !strings.HasPrefix(e.Dest, "/usr/local/sbin/") && !strings.HasPrefix(e.Dest, "/etc/systemd/system/") {
			t.Errorf("%s goes to %s, outside the folders the updater binds", e.Source, e.Dest)
		}
		if strings.HasSuffix(e.Dest, ".timer") || strings.HasSuffix(e.Dest, ".path") {
			if !e.Enable {
				t.Errorf("%s is a timer or path unit without enable", e.Source)
			}
		}
		if e.Run && !strings.HasSuffix(e.Dest, ".service") {
			t.Errorf("%s has run, which only a service takes", e.Source)
		}
		if strings.HasPrefix(e.Dest, "/usr/local/sbin/") && e.Mode != 0o755 {
			t.Errorf("%s is a script with mode %o", e.Source, e.Mode)
		}
	}
	ents, _ := os.ReadDir(host)
	for _, f := range ents {
		n := f.Name()
		if strings.HasSuffix(n, ".timer") || strings.HasSuffix(n, ".path") || strings.HasSuffix(n, ".service") {
			if _, ok := listed[n]; !ok {
				t.Errorf("%s is in deploy/core/host but not in the manifest", n)
			}
		}
	}
	for _, must := range []string{"argus-host-tools-apply", "argus-host-tools.path", "argus-os-report", "argus-size-zabbix", "argus-backup"} {
		if _, ok := listed[must]; !ok {
			t.Errorf("the manifest must carry %s", must)
		}
	}
}
