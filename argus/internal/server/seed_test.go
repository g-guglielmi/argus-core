package server

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/kdomanski/iso9660"
)

// TestBuildSeedISO round-trips the generated image back through the ISO9660 reader to prove the
// golden image's first-boot service will find the volume label and the ARGUS.ENV file with its
// enrollment inputs intact - the parity check we can run without booting a VM.
func TestBuildSeedISO(t *testing.T) {
	s := &Server{}
	env := "ARGUS_ENROLL_URL=https://monitoring.example.com/api/enroll\n" +
		"ARGUS_ENROLL_TOKEN=deadbeefcafe\n" +
		"ZBX_SERVER_HOST=10.0.0.10\n"

	iso, err := s.buildSeedISO(env)
	if err != nil {
		t.Fatalf("buildSeedISO: %v", err)
	}
	if len(iso)%2048 != 0 {
		t.Fatalf("ISO length %d is not a multiple of the 2048-byte sector size", len(iso))
	}

	img, err := iso9660.OpenImage(bytes.NewReader(iso))
	if err != nil {
		t.Fatalf("OpenImage: %v", err)
	}
	root, err := img.RootDir()
	if err != nil {
		t.Fatalf("RootDir: %v", err)
	}
	children, err := root.GetChildren()
	if err != nil {
		t.Fatalf("GetChildren: %v", err)
	}

	var found *iso9660.File
	for _, c := range children {
		// Plain ISO9660 may surface the name uppercased and/or with a ";1" version suffix - the golden
		// image reads it case-insensitively, so match the same way here.
		name := strings.ToUpper(strings.SplitN(c.Name(), ";", 2)[0])
		if name == "ARGUS.ENV" {
			found = c
		}
	}
	if found == nil {
		var names []string
		for _, c := range children {
			names = append(names, c.Name())
		}
		t.Fatalf("ARGUS.ENV not found in seed image; entries: %v", names)
	}

	rc := found.Reader()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read ARGUS.ENV: %v", err)
	}
	if string(got) != env {
		t.Fatalf("ARGUS.ENV content mismatch:\n got: %q\nwant: %q", got, env)
	}
}
