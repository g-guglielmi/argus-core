// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// The image is distroless (no shell, no interpreter), so the Docker HEALTHCHECK runs the binary
// itself: `argus healthcheck` asks the running server's /healthz over the loopback and exits 0 when
// it answers "ok", 1 otherwise. It opens no database and reads no configuration beyond the listen
// address, so it is safe to run every 30 s next to the server. Zabbix being unreachable is not a
// container fault (Argus keeps serving and says so), so it stays out of this check.

// healthURL is the /healthz address for a listen address as ARGUS_LISTEN writes it (":8080",
// "0.0.0.0:8080", "127.0.0.1:8081", "[::]:8080"): a wildcard or empty host becomes the loopback.
func healthURL(listen string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil || port == "" {
		host, port = "", "8080"
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}

// runHealthcheck performs the check and returns the process exit code.
func runHealthcheck() int {
	listen := os.Getenv("ARGUS_LISTEN")
	if listen == "" {
		listen = ":8080"
	}
	url := healthURL(listen)
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unhealthy: %s: %v\n", url, err)
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
		fmt.Fprintf(os.Stderr, "unhealthy: %s answered %d %q\n", url, resp.StatusCode, strings.TrimSpace(string(body)))
		return 1
	}
	fmt.Println("healthy")
	return 0
}
