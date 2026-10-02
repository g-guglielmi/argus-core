// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func runInstall(t *testing.T, src, dst string) collectorsSummary {
	t.Helper()
	var out bytes.Buffer
	if err := installCollectors(src, dst, &out); err != nil {
		t.Fatalf("install: %v", err)
	}
	var sum collectorsSummary
	if err := json.Unmarshal(out.Bytes(), &sum); err != nil {
		t.Fatalf("summary %q: %v", out.String(), err)
	}
	return sum
}

// The collectors land in the folder executable, only when they changed, and nothing else is touched.
func TestInstallCollectors(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "argus_http.py"), "print('http')\n", 0o644)
	writeFile(t, filepath.Join(src, "argus_tcp.py"), "print('tcp')\n", 0o644)
	writeFile(t, filepath.Join(src, "README.txt"), "not a collector", 0o644)
	if err := os.Mkdir(filepath.Join(src, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dst, "someone_elses.sh"), "#!/bin/sh\n", 0o700)
	writeFile(t, filepath.Join(dst, "argus_tcp.py"), "print('old tcp')\n", 0o755)

	sum := runInstall(t, src, dst)
	if want := []string{"argus_http.py", "argus_tcp.py"}; !reflect.DeepEqual(sum.Installed, want) {
		t.Fatalf("first run installed %v, want %v", sum.Installed, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "argus_tcp.py")); string(b) != "print('tcp')\n" {
		t.Fatalf("the old collector wasn't replaced: %q", b)
	}
	if runtime.GOOS != "windows" { // Windows has no POSIX modes to check
		if fi, _ := os.Stat(filepath.Join(dst, "argus_http.py")); fi.Mode().Perm() != 0o755 {
			t.Fatalf("mode %v, want 0755", fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "README.txt")); err == nil {
		t.Fatal("a file that isn't a collector was copied")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "someone_elses.sh")); string(b) != "#!/bin/sh\n" {
		t.Fatal("another file in the folder was touched")
	}
	ents, _ := os.ReadDir(dst)
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".argus-new" {
			t.Fatalf("a temporary file was left behind: %s", e.Name())
		}
	}

	if runtime.GOOS == "windows" {
		return // no POSIX modes: every run rewrites; the CI runs this on Linux
	}
	again := runInstall(t, src, dst)
	if len(again.Installed) != 0 || len(again.Unchanged) != 2 {
		t.Fatalf("second run: installed %v unchanged %v, want nothing written", again.Installed, again.Unchanged)
	}
}

func TestInstallCollectorsRefuses(t *testing.T) {
	var out bytes.Buffer
	if err := installCollectors(filepath.Join(t.TempDir(), "none"), t.TempDir(), &out); err == nil || err.Error() != "this image carries no collectors" {
		t.Fatalf("no collectors folder: err = %v", err)
	}
	if err := installCollectors(t.TempDir(), t.TempDir(), &out); err == nil || err.Error() != "this image carries no collectors" {
		t.Fatalf("an empty collectors folder: err = %v", err)
	}
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "argus_http.py"), "x", 0o644)
	if err := installCollectors(src, filepath.Join(t.TempDir(), "missing"), &out); err == nil {
		t.Fatal("a missing target folder was accepted")
	}
}
