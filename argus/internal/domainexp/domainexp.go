// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package domainexp reads when a domain's registration expires: from the registry's RDAP service
// (found through IANA's bootstrap file), else from its WHOIS server (found through IANA's WHOIS),
// for the registries that have no RDAP yet (.it among them). Some registries publish no expiry at
// all (.de, .eu): that is said, not guessed.
package domainexp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

// Where the lookups go (tests point them at local servers).
var (
	bootstrapURL = "https://data.iana.org/rdap/dns.json"
	ianaWhois    = "whois.iana.org:43"
	whoisPort    = "43"
	httpClient   = &http.Client{Timeout: 20 * time.Second}
)

// ErrNoExpiry says the registry publishes no expiry date for its domains.
var ErrNoExpiry = errors.New("its registry doesn't publish when a domain expires")

// Registrable is the domain a host name belongs to, as a registry sells it (shop.example.co.uk ->
// example.co.uk), or "" for an address, an internal name (example.lan) or a bare public suffix.
func Registrable(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || net.ParseIP(strings.Trim(host, "[]")) != nil {
		return ""
	}
	suffix, icann := publicsuffix.PublicSuffix(host)
	if !icann || suffix == host {
		return ""
	}
	d, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return ""
	}
	return d
}

// Lookup reads a domain's expiry, and how (rdap or whois).
func Lookup(ctx context.Context, domain string) (time.Time, string, error) {
	tld := domain[strings.LastIndexByte(domain, '.')+1:]
	if base, err := rdapBase(ctx, tld); err == nil && base != "" {
		t, err := lookupRDAP(ctx, base, domain)
		if err == nil || !errors.Is(err, ErrNoExpiry) {
			return t, "rdap", err
		}
	}
	server, err := whoisServer(ctx, tld)
	if err != nil {
		return time.Time{}, "whois", err
	}
	if server == "" {
		return time.Time{}, "", fmt.Errorf("the .%s registry has neither RDAP nor WHOIS", tld)
	}
	t, err := lookupWhois(ctx, server, domain)
	return t, "whois", err
}

// The bootstrap file and the WHOIS servers change rarely: kept a day.
var cache = struct {
	sync.Mutex
	rdap    map[string]string
	rdapAt  time.Time
	whois   map[string]string
	whoisAt map[string]time.Time
}{whois: map[string]string{}, whoisAt: map[string]time.Time{}}

func rdapBase(ctx context.Context, tld string) (string, error) {
	cache.Lock()
	fresh := cache.rdap != nil && time.Since(cache.rdapAt) < 24*time.Hour
	if fresh {
		b := cache.rdap[tld]
		cache.Unlock()
		return b, nil
	}
	cache.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bootstrapURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("IANA's RDAP list answered HTTP %d", resp.StatusCode)
	}
	var d struct {
		Services [][][]string `json:"services"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&d); err != nil {
		return "", err
	}
	m := map[string]string{}
	for _, svc := range d.Services {
		if len(svc) < 2 || len(svc[1]) == 0 {
			continue
		}
		base := svc[1][0]
		for _, u := range svc[1] {
			if strings.HasPrefix(u, "https://") {
				base = u
				break
			}
		}
		for _, t := range svc[0] {
			m[strings.ToLower(t)] = base
		}
	}
	cache.Lock()
	cache.rdap, cache.rdapAt = m, time.Now()
	cache.Unlock()
	return m[tld], nil
}

func lookupRDAP(ctx context.Context, base, domain string) (time.Time, error) {
	u := strings.TrimRight(base, "/") + "/domain/" + domain
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("the registry's RDAP didn't answer: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return time.Time{}, errors.New("the registry doesn't know this domain (is it registered?)")
	case resp.StatusCode == http.StatusTooManyRequests:
		return time.Time{}, errors.New("the registry's RDAP asks to slow down (HTTP 429); Argus tries again later")
	case resp.StatusCode != http.StatusOK:
		return time.Time{}, fmt.Errorf("the registry's RDAP answered HTTP %d", resp.StatusCode)
	}
	var d struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&d); err != nil {
		return time.Time{}, fmt.Errorf("the registry's RDAP answer isn't JSON: %w", err)
	}
	for _, e := range d.Events {
		if strings.EqualFold(e.Action, "expiration") {
			if t, ok := parseDate(e.Date); ok {
				return t, nil
			}
		}
	}
	return time.Time{}, ErrNoExpiry
}

// whoisQuery sends one WHOIS query and reads the answer (up to 64 KB).
func whoisQuery(ctx context.Context, addr, q string) (string, error) {
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := conn.Write([]byte(q + "\r\n")); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(conn, 64<<10))
	if err != nil && len(b) == 0 {
		return "", err
	}
	return string(b), nil
}

func whoisServer(ctx context.Context, tld string) (string, error) {
	cache.Lock()
	if at, ok := cache.whoisAt[tld]; ok && time.Since(at) < 24*time.Hour {
		s := cache.whois[tld]
		cache.Unlock()
		return s, nil
	}
	cache.Unlock()
	ans, err := whoisQuery(ctx, ianaWhois, tld)
	if err != nil {
		return "", fmt.Errorf("IANA's WHOIS didn't answer: %w", err)
	}
	server := ""
	sc := bufio.NewScanner(strings.NewReader(ans))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), ":"); ok && strings.EqualFold(strings.TrimSpace(k), "whois") {
			server = strings.TrimSpace(v)
			break
		}
	}
	cache.Lock()
	cache.whois[tld], cache.whoisAt[tld] = server, time.Now()
	cache.Unlock()
	return server, nil
}

var (
	reExpiry  = regexp.MustCompile(`(?im)^\s*(?:registry expiry date|registrar registration expiration date|expir(?:y|ation|e|es)(?: date| on)?|paid-till|renewal date|valid until)\s*[:.]+\s*(\S.*?)\s*$`)
	reUnknown = regexp.MustCompile(`(?i)no match|not found|no entries found|no data found|status:\s*available|is free|object does not exist`)
)

func lookupWhois(ctx context.Context, server, domain string) (time.Time, error) {
	ans, err := whoisQuery(ctx, net.JoinHostPort(server, whoisPort), domain)
	if err != nil {
		return time.Time{}, fmt.Errorf("the registry's WHOIS (%s) didn't answer: %w", server, err)
	}
	return parseWhois(ans)
}

// parseWhois finds the expiry in a WHOIS answer.
func parseWhois(ans string) (time.Time, error) {
	for _, m := range reExpiry.FindAllStringSubmatch(ans, -1) {
		if t, ok := parseDate(m[1]); ok {
			return t, nil
		}
	}
	if reUnknown.MatchString(ans) {
		return time.Time{}, errors.New("the registry doesn't know this domain (is it registered?)")
	}
	return time.Time{}, ErrNoExpiry
}

var dateLayouts = []string{
	time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05", "2006-01-02 15:04:05 MST", "2006-01-02 15:04:05",
	"2006-01-02", "2006.01.02", "2006/01/02", "02/01/2006", "02.01.2006", "02-01-2006", "02-Jan-2006", "2-Jan-2006", "January 2 2006", "Jan 2 2006",
}

func parseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, " ("); i > 0 { // "2027-03-15 (YYYY-MM-DD)"
		s = s[:i]
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
