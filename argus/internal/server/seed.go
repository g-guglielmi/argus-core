package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
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
		StaticIP  string `json:"static_ip"` // static networking (no DHCP); blank = DHCP
		Prefix    string `json:"prefix"`    // "24" or a dotted mask "255.255.255.0"
		Gateway   string `json:"gateway"`
		DNS       string `json:"dns"` // comma/space separated
		Name      string `json:"name"` // proxy name, for the download filename only
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
	// Static networking (for sites with no DHCP). Omitted -> the VM DHCPs as usual. Validated here so a
	// bad address never reaches the VM's network config; the first-boot service writes a static
	// systemd-networkd file from these before enrollment.
	if cidr, err := staticCIDR(req.StaticIP, req.Prefix); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	} else if cidr != "" {
		env.WriteString("ARGUS_IP=" + cidr + "\n")
		if gw := validIP(req.Gateway); gw != "" {
			env.WriteString("ARGUS_GATEWAY=" + gw + "\n")
		}
		if dns := cleanDNS(req.DNS); dns != "" {
			env.WriteString("ARGUS_DNS=" + dns + "\n")
		}
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

// validIP returns a trimmed IP if it parses, else "".
func validIP(s string) string {
	s = strings.TrimSpace(s)
	if net.ParseIP(s) == nil {
		return ""
	}
	return s
}

// staticCIDR combines a static IP and a prefix/netmask into "ip/prefix" for systemd-networkd's
// Address=. An empty ip means DHCP (returns "" with no error). prefix accepts a CIDR length ("24") or
// a dotted mask ("255.255.255.0"), defaulting to /24 when blank.
func staticCIDR(ip, prefix string) (string, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "", nil // DHCP
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "", errors.New("static IP is not a valid address")
	}
	v4 := parsed.To4() != nil
	prefix = strings.TrimSpace(prefix)
	var ones int
	switch {
	case prefix == "":
		ones = 24
	case strings.Contains(prefix, "."):
		m := net.ParseIP(prefix).To4()
		if m == nil {
			return "", errors.New("subnet mask is not valid")
		}
		var bits int
		ones, bits = net.IPMask(m).Size()
		if bits == 0 {
			return "", errors.New("subnet mask is not a valid contiguous mask")
		}
	default:
		n, err := strconv.Atoi(prefix)
		max := 32
		if !v4 {
			max = 128
		}
		if err != nil || n < 0 || n > max {
			return "", errors.New("subnet prefix must be a number (0-32) or a netmask")
		}
		ones = n
	}
	return ip + "/" + strconv.Itoa(ones), nil
}

// cleanDNS keeps the valid IPs from a comma/space separated list and rejoins them with commas.
func cleanDNS(s string) string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if ip := validIP(f); ip != "" {
			out = append(out, ip)
		}
	}
	return strings.Join(out, ",")
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
