// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package unifi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sitesBody = `{"meta":{"rc":"ok"},"data":[{"name":"default","desc":"Site 1"},{"name":"a1b2c3","desc":"Site 2"}]}`

const devicesDefault = `{"meta":{"rc":"ok"},"data":[
  {"ip":"10.0.0.2","mac":"aa:bb:cc:00:00:01","name":"sw-rack","model":"US8P60","type":"usw","state":1,"version":"7.1.26","adopted":true},
  {"ip":"10.0.0.3","mac":"aa:bb:cc:00:00:02","name":"ap-hall","model":"U7PG2","type":"uap","state":0,"version":"6.6.55","adopted":true},
  {"ip":"","mac":"aa:bb:cc:00:00:03","name":"sw-noip","model":"USMINI","type":"usw","state":0,"version":"","adopted":true},
  {"ip":"10.0.0.9","mac":"aa:bb:cc:00:00:04","name":"pending","model":"USMINI","type":"usw","state":2,"version":"","adopted":false}
]}`

const devicesSite2 = `{"meta":{"rc":"ok"},"data":[
  {"ip":"203.0.113.5","lan_ip":"10.0.1.1","mac":"AA:BB:CC:00:00:05","name":"gw-branch","model":"UXGLITE","type":"uxg","state":1,"version":"3.2.12","adopted":true}
]}`

func serve(t *testing.T, prefix string, requireKey string) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireKey != "" && r.Header.Get("X-API-KEY") != requireKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := r.URL.Path
		if prefix != "" {
			if !strings.HasPrefix(path, prefix) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			path = strings.TrimPrefix(path, prefix)
		}
		switch path {
		case "/api/self/sites":
			_, _ = w.Write([]byte(sitesBody))
		case "/api/s/default/stat/device":
			_, _ = w.Write([]byte(devicesDefault))
		case "/api/s/a1b2c3/stat/device":
			_, _ = w.Write([]byte(devicesSite2))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestSweepUniFiOSPrefix(t *testing.T) {
	srv := serve(t, "/proxy/network", "k-123")
	defer srv.Close()
	devs, err := Sweep(context.Background(), srv.URL+"/", "k-123")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(devs) != 3 {
		t.Fatalf("want 3 devices (no-IP and unadopted skipped), got %d: %+v", len(devs), devs)
	}
	if devs[0].Name != "sw-rack" || devs[0].Type != "usw" || devs[0].Site != "default" || devs[0].SiteDesc != "Site 1" {
		t.Fatalf("first device wrong: %+v", devs[0])
	}
	if devs[2].MAC != "aa:bb:cc:00:00:05" {
		t.Fatalf("MAC not lowercased: %+v", devs[2])
	}
	if devs[2].Site != "a1b2c3" || devs[2].SiteDesc != "Site 2" {
		t.Fatalf("second site wrong: %+v", devs[2])
	}
	if devs[2].IP != "10.0.1.1" {
		t.Fatalf("gateway must use lan_ip (its \"ip\" is the WAN address), got %q", devs[2].IP)
	}
}

func TestSweepBarePathFallback(t *testing.T) {
	srv := serve(t, "", "")
	defer srv.Close()
	devs, err := Sweep(context.Background(), srv.URL, "any")
	if err != nil {
		t.Fatalf("Sweep with bare-path controller: %v", err)
	}
	if len(devs) != 3 {
		t.Fatalf("want 3 devices, got %d", len(devs))
	}
}

func TestSweepBadKey(t *testing.T) {
	srv := serve(t, "/proxy/network", "k-123")
	defer srv.Close()
	_, err := Sweep(context.Background(), srv.URL, "wrong")
	if err == nil || !strings.Contains(err.Error(), "rejected the API key") {
		t.Fatalf("want an API-key rejection, got %v", err)
	}
}
