// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimited is a service's answer that it got too many messages at once (HTTP 429), with how long
// it asks for before the next one. Telegram says it in the body (parameters.retry_after), Discord in
// the body (retry_after) and the Retry-After header, Slack, Teams, ntfy, Gotify and Pushover in the
// header when at all; After is 0 when the service didn't say. The notifier paces its sends so this
// rarely happens, and when it does it waits the time asked and sends again (notifier.go, paced).
type RateLimited struct {
	After time.Duration
}

func (e *RateLimited) Error() string {
	if e.After <= 0 {
		return "HTTP 429: too many messages at once, the service asked for a pause"
	}
	return fmt.Sprintf("HTTP 429: too many messages at once, the service asked for a %s pause", e.After.Round(time.Second))
}

// AsRateLimited reports whether err is, or wraps, a service's rate limit.
func AsRateLimited(err error) (*RateLimited, bool) {
	var rl *RateLimited
	if errors.As(err, &rl) {
		return rl, true
	}
	return nil, false
}

// rateLimited reads a 429 answer's wait: the Retry-After header (seconds), else the body's
// retry_after, at the top (Discord, in seconds) or under parameters (Telegram).
func rateLimited(resp *http.Response, body []byte) error {
	after := time.Duration(0)
	if h := strings.TrimSpace(resp.Header.Get("Retry-After")); h != "" {
		if s, err := strconv.ParseFloat(h, 64); err == nil && s > 0 {
			after = time.Duration(s * float64(time.Second))
		}
	}
	if after == 0 {
		var b struct {
			RetryAfter float64 `json:"retry_after"`
			Parameters struct {
				RetryAfter float64 `json:"retry_after"`
			} `json:"parameters"`
		}
		if json.Unmarshal(body, &b) == nil {
			if b.RetryAfter > 0 {
				after = time.Duration(b.RetryAfter * float64(time.Second))
			} else if b.Parameters.RetryAfter > 0 {
				after = time.Duration(b.Parameters.RetryAfter * float64(time.Second))
			}
		}
	}
	return &RateLimited{After: after}
}
