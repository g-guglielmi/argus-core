// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package unifi enumerates a UniFi Network controller's adopted devices for the §B discovery
// sweep. It speaks the same API the UniFi class templates poll (X-API-KEY against the Network
// API proxied through UniFi OS), so a controller that works for monitoring works for the sweep.
// TLS is not verified - consoles ship self-signed certificates (same posture as the templates).
package unifi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Device is one adopted UniFi device a sweep found. Site is the API site name (the {$UNIFI.SITE}
// macro value); SiteDesc is its display name.
type Device struct {
	IP       string
	MAC      string
	Name     string
	Model    string
	Type     string // usw | uap | ugw | udm | uxg | ucg | ...
	State    int    // 1 = connected
	Version  string
	Site     string
	SiteDesc string
}

// Client is one client the controller currently knows (stat/sta) - used only as a naming hint
// for scan enrichment, never imported as a device.
type Client struct {
	IP       string
	MAC      string
	Name     string // the alias set in the controller, if any
	Hostname string
	Wired    bool
}

const requestTimeout = 30 * time.Second

// Clients lists the controller's currently-known clients across all sites (naming hints for
// scan enrichment). Same path/fallback/auth rules as Sweep.
func Clients(ctx context.Context, baseURL, apiKey string) ([]Client, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("controller URL is empty")
	}
	client := newHTTPClient()
	sites, prefix, err := fetchSites(ctx, client, base, apiKey)
	if err != nil {
		return nil, err
	}
	var out []Client
	for _, site := range sites {
		var rows struct {
			Data []struct {
				IP       string `json:"ip"`
				MAC      string `json:"mac"`
				Name     string `json:"name"`
				Hostname string `json:"hostname"`
				Wired    bool   `json:"is_wired"`
			} `json:"data"`
		}
		if err := getJSON(ctx, client, base+prefix+"/api/s/"+site.Name+"/stat/sta", apiKey, &rows); err != nil {
			return nil, fmt.Errorf("site %s: %w", site.Name, err)
		}
		for _, c := range rows.Data {
			out = append(out, Client{
				IP:       strings.TrimSpace(c.IP),
				MAC:      strings.ToLower(strings.TrimSpace(c.MAC)),
				Name:     strings.TrimSpace(c.Name),
				Hostname: strings.TrimSpace(c.Hostname),
				Wired:    c.Wired,
			})
		}
	}
	return out, nil
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

type siteRef struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// fetchSites resolves the controller's sites and which path prefix it speaks (UniFi OS proxy vs
// bare Network application).
func fetchSites(ctx context.Context, client *http.Client, base, apiKey string) ([]siteRef, string, error) {
	var sites struct {
		Data []siteRef `json:"data"`
	}
	prefix := "/proxy/network"
	err := getJSON(ctx, client, base+prefix+"/api/self/sites", apiKey, &sites)
	if isNotFound(err) {
		prefix = ""
		err = getJSON(ctx, client, base+"/api/self/sites", apiKey, &sites)
	}
	if err != nil {
		return nil, "", err
	}
	if len(sites.Data) == 0 {
		return nil, "", fmt.Errorf("the controller reported no sites")
	}
	return sites.Data, prefix, nil
}

// Sweep lists every adopted device (with an IP) across all of the controller's sites. It tries
// the UniFi OS path first (/proxy/network/...) and falls back to the bare Network-application
// path on 404 (plain self-hosted controllers).
func Sweep(ctx context.Context, baseURL, apiKey string) ([]Device, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("controller URL is empty")
	}
	client := newHTTPClient()
	sites, prefix, err := fetchSites(ctx, client, base, apiKey)
	if err != nil {
		return nil, err
	}

	var out []Device
	for _, site := range sites {
		var devices struct {
			Data []struct {
				IP      string `json:"ip"`
				LanIP   string `json:"lan_ip"` // gateways report their WAN address as "ip"
				MAC     string `json:"mac"`
				Name    string `json:"name"`
				Model   string `json:"model"`
				Type    string `json:"type"`
				State   int    `json:"state"`
				Version string `json:"version"`
				Adopted bool   `json:"adopted"`
			} `json:"data"`
		}
		if err := getJSON(ctx, client, base+prefix+"/api/s/"+site.Name+"/stat/device", apiKey, &devices); err != nil {
			return nil, fmt.Errorf("site %s: %w", site.Name, err)
		}
		for _, d := range devices.Data {
			// Prefer lan_ip: for the gateway itself "ip" is the WAN address, and monitoring (plus
			// the already-monitored dedupe) wants the LAN one. Switches/APs just carry "ip".
			ip := strings.TrimSpace(d.LanIP)
			if ip == "" {
				ip = strings.TrimSpace(d.IP)
			}
			if !d.Adopted || ip == "" {
				continue
			}
			out = append(out, Device{
				IP:       ip,
				MAC:      strings.ToLower(strings.TrimSpace(d.MAC)),
				Name:     strings.TrimSpace(d.Name),
				Model:    d.Model,
				Type:     strings.ToLower(d.Type),
				State:    d.State,
				Version:  d.Version,
				Site:     site.Name,
				SiteDesc: site.Desc,
			})
		}
	}
	return out, nil
}

// httpStatusError carries the status code so the /proxy/network 404 fallback can trigger.
type httpStatusError struct {
	status int
	url    string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("controller returned HTTP %d for %s", e.status, e.url)
}

func isNotFound(err error) bool {
	se, ok := err.(*httpStatusError)
	return ok && se.status == http.StatusNotFound
}

func getJSON(ctx context.Context, client *http.Client, url, apiKey string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-KEY", apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("the controller rejected the API key (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return &httpStatusError{status: resp.StatusCode, url: url}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("unexpected response from the controller: %w", err)
	}
	return nil
}
