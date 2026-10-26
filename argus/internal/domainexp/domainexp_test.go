// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package domainexp

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The domain a registry sells: the host name's registrable part, nothing for addresses, internal names
// and public suffixes.
func TestRegistrable(t *testing.T) {
	for in, want := range map[string]string{
		"shop.example.com": "example.com", "Portal.Example.CO.UK.": "example.co.uk", "www.example.it": "example.it",
		"example.com": "example.com", "10.0.0.10": "", "[2001:db8::1]": "", "nas.example.lan": "", "co.uk": "", "": "",
	} {
		if got := Registrable(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// WHOIS answers name the expiry many ways; a registry that has none says so, one that doesn't know the
// domain says that.
func TestParseWhois(t *testing.T) {
	for ans, want := range map[string]string{
		"Domain:             example.it\nStatus:             ok\nCreated:            2000-03-01 00:00:00\nExpire Date:        2027-04-21\n": "2027-04-21",
		"   Registry Expiry Date: 2027-08-13T04:00:00Z\n   Registrar: RESERVED-Internet Assigned Numbers Authority\n":                       "2027-08-13",
		"paid-till: 2026-12-01T21:00:00Z\n":            "2026-12-01",
		"Expiry date:  15-Mar-2027\n":                  "2027-03-15",
		"Valid Until: 2027-05-31 (YYYY-MM-DD)\n":       "2027-05-31",
		"Domain: example.ch\nExpires On: 31.12.2026\n": "2026-12-31",
	} {
		got, err := parseWhois(ans)
		if err != nil || got.Format("2006-01-02") != want {
			t.Errorf("%q: got %v %v, want %s", ans, got, err, want)
		}
	}
	if _, err := parseWhois("Domain: example.de\nStatus: connect\n"); !errors.Is(err, ErrNoExpiry) {
		t.Errorf("no expiry: %v", err)
	}
	if _, err := parseWhois("No match for \"NOPE.COM\".\n"); err == nil || !strings.Contains(err.Error(), "doesn't know this domain") {
		t.Errorf("unknown domain: %v", err)
	}
}

// A lookup asks the registry's RDAP when IANA lists one, else its WHOIS (the server named by IANA's
// WHOIS); an RDAP answer without an expiration falls back to WHOIS.
func TestLookup(t *testing.T) {
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/dns.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"services":[[["com","net"],["http://x.invalid/","` + srv.URL + `/com/"]],[["org"],["` + srv.URL + `/org/"]]]}`))
	})
	mux.HandleFunc("/com/domain/example.com", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"events":[{"eventAction":"registration","eventDate":"1995-08-14T04:00:00Z"},{"eventAction":"expiration","eventDate":"2027-08-13T04:00:00Z"}]}`))
	})
	mux.HandleFunc("/com/domain/nope.com", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/org/domain/example.org", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"events":[]}`)) })
	srv = httptest.NewTLSServer(mux)
	defer srv.Close()
	oldC := httpClient
	httpClient = srv.Client()
	defer func() { httpClient = oldC }()

	// One local WHOIS server plays both IANA (naming itself for .it and .org) and the registries.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				q, _ := bufio.NewReader(c).ReadString('\n')
				switch strings.TrimSpace(q) {
				case "it", "org":
					_, _ = c.Write([]byte("domain: IT\nwhois:        127.0.0.1\n"))
				case "example.it":
					_, _ = c.Write([]byte("Domain: example.it\nExpire Date: 2027-04-21\n"))
				case "example.org":
					_, _ = c.Write([]byte("Registry Expiry Date: 2028-01-02T00:00:00Z\n"))
				}
			}(c)
		}
	}()
	oldB, oldI, oldP := bootstrapURL, ianaWhois, whoisPort
	bootstrapURL, ianaWhois, whoisPort = srv.URL+"/dns.json", l.Addr().String(), strings.TrimPrefix(l.Addr().String(), "127.0.0.1:")
	defer func() { bootstrapURL, ianaWhois, whoisPort = oldB, oldI, oldP }()
	cache.Lock()
	cache.rdap, cache.whois, cache.whoisAt = nil, map[string]string{}, map[string]time.Time{}
	cache.Unlock()

	ctx := context.Background()
	for domain, want := range map[string]string{"example.com": "rdap 2027-08-13", "example.it": "whois 2027-04-21", "example.org": "whois 2028-01-02"} {
		at, via, err := Lookup(ctx, domain)
		if err != nil || via+" "+at.Format("2006-01-02") != want {
			t.Errorf("%s: %s %v %v, want %s", domain, via, at, err, want)
		}
	}
	if _, _, err := Lookup(ctx, "nope.com"); err == nil || !strings.Contains(err.Error(), "doesn't know this domain") {
		t.Errorf("an unregistered domain: %v", err)
	}
}
