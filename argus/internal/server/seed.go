package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/kdomanski/iso9660"
)

// handleSeedISO builds a small "seed" ISO the probe golden image reads on first boot to self-enroll
// with no interaction - the "attach a CD" alternative to pasting cloud-init user-data, for
// hypervisors without a cloud-init field.
//
// It is deliberately NOT a cloud-init NoCloud seed: NoCloud needs the files named exactly
// `user-data`/`meta-data`, which plain ISO9660 mangles (8.3, uppercase, no hyphen) and only Joliet or
// Rock Ridge preserve. Instead this is an Argus-owned image - volume label ARGUSSEED, one 8.3-safe
// file ARGUS.ENV holding the probe.env inputs - read by our own first-boot service, which sidesteps
// cloud-init's NoCloud datasource detection (fiddly on XCP-NG, per DESIGN §14a) entirely. The image
// carries the single-use enrollment token, so this is admin-only like token minting.
func (s *Server) handleSeedISO(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token     string `json:"token"`
		EnrollURL string `json:"enroll_url"`
		CoreHost  string `json:"core_host"`
		Keymap    string `json:"keymap"` // console keyboard layout, e.g. "it" (default "us" on the VM)
		Name      string `json:"name"`   // proxy name, for the download filename only
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	token := strings.TrimSpace(req.Token)
	enrollURL := strings.TrimSpace(req.EnrollURL)
	if token == "" || enrollURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token and enroll_url are required"})
		return
	}

	// The probe.env contract (see deploy/probe-vm/files/probe.env.example). The first-boot reader
	// parses these KEY=VALUE lines and starts the proxy + updater once a token is present.
	var env strings.Builder
	env.WriteString("ARGUS_ENROLL_URL=" + enrollURL + "\n")
	env.WriteString("ARGUS_ENROLL_TOKEN=" + token + "\n")
	if h := strings.TrimSpace(req.CoreHost); h != "" {
		env.WriteString("ZBX_SERVER_HOST=" + h + "\n")
	}
	if km := validKeymap(req.Keymap); km != "" {
		env.WriteString("ARGUS_KEYMAP=" + km + "\n")
	}

	iso, err := s.buildSeedISO(env.String())
	if err != nil {
		s.logger.Warn("seed-iso: build failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not build the seed image"})
		return
	}

	fname := "argus-seed.iso"
	if n := slugify(req.Name); n != "" {
		fname = "argus-seed-" + n + ".iso"
	}
	w.Header().Set("Content-Type", "application/x-iso9660-image")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(iso)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(iso)
}

// validKeymap returns a sanitized console keymap code (e.g. "it", "de", "gb"), or "" if the input
// isn't a plausible layout name. The probe VM's first-boot service feeds this to console tooling, so
// constrain it to the characters a keymap name uses.
func validKeymap(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 16 {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return ""
		}
	}
	return s
}

// buildSeedISO packages the probe.env content into a plain ISO9660 image (label ARGUSSEED, file
// ARGUS.ENV). Both names are ISO9660 d-characters (uppercase, <=8.3, no hyphen), so no Joliet/Rock
// Ridge is needed and the golden image's first-boot reader finds them case-insensitively.
func (s *Server) buildSeedISO(envContent string) ([]byte, error) {
	iw, err := iso9660.NewWriter()
	if err != nil {
		return nil, err
	}
	defer iw.Cleanup()
	if err := iw.AddFile(strings.NewReader(envContent), "ARGUS.ENV"); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := iw.WriteTo(&buf, "ARGUSSEED"); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
