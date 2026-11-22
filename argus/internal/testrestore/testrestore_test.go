// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package testrestore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTestRestoreFollowsTheMark(t *testing.T) {
	dir := t.TempDir()
	Init(dir)
	t.Cleanup(func() { Init("") })
	if Active() {
		t.Fatal("no mark, yet a test restore")
	}
	mark := filepath.Join(dir, FileName)
	if err := os.WriteFile(mark, []byte(`{"since":1700000000,"archive":"argus-backup-core-20261121T100000Z.tar.gpg","from":"core"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	checked = time.Time{} // the next look reads the folder again
	mu.Unlock()
	in := Current()
	if in == nil || in.From != "core" || in.Since != 1700000000 {
		t.Fatalf("the mark was not read: %+v", in)
	}
	// A mark nobody can parse is still a test restore: the file being there is the signal.
	if err := os.WriteFile(mark, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	checked = time.Time{}
	mu.Unlock()
	if !Active() {
		t.Fatal("an unreadable mark must still mean a test restore")
	}
	if err := os.Remove(mark); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	checked = time.Time{}
	mu.Unlock()
	if Active() {
		t.Fatal("the mark is gone, the test restore is over")
	}
}

func TestNoDataFolderIsNeverATestRestore(t *testing.T) {
	Init("")
	if Active() {
		t.Fatal("a core without a data folder set can not be a test restore")
	}
}
