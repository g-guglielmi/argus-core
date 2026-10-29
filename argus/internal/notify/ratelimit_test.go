// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A 429 comes back as a RateLimited error carrying the wait the service asked for, however it said it.
func TestRateLimitedReadsTheWait(t *testing.T) {
	cases := []struct {
		name   string
		header string
		body   string
		want   time.Duration
	}{
		{"telegram body", "", `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 30","parameters":{"retry_after":30}}`, 30 * time.Second},
		{"discord body", "", `{"message":"You are being rate limited.","retry_after":1.5,"global":false}`, 1500 * time.Millisecond},
		{"header", "7", `rate limited`, 7 * time.Second},
		{"nothing said", "", `slow down`, 0},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c.header != "" {
				w.Header().Set("Retry-After", c.header)
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(c.body))
		}))
		err := postJSON(context.Background(), srv.URL, []byte(`{}`))
		srv.Close()
		rl, ok := AsRateLimited(err)
		if !ok || rl.After != c.want {
			t.Errorf("%s: %v (want %s)", c.name, err, c.want)
		}
		if ok && c.want > 0 && rl.Error() == "" {
			t.Errorf("%s: no message", c.name)
		}
	}
	// Any other failure is what it was.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer srv.Close()
	if _, ok := AsRateLimited(postJSON(context.Background(), srv.URL, []byte(`{}`))); ok {
		t.Fatal("a 502 read as a rate limit")
	}
	// redactErr keeps the type (the notifier reads the wait from it).
	if _, ok := AsRateLimited(redactErr(&RateLimited{After: time.Second})); !ok {
		t.Fatal("redacted away")
	}
	if _, ok := AsRateLimited(errors.New("HTTP 429: plain")); ok {
		t.Fatal("a plain error read as a rate limit")
	}
}
