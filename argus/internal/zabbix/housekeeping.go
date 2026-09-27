// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package zabbix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Housekeeping is the subset of Zabbix's global housekeeping settings Argus manages: how long raw
// history and hourly trends are kept, and TimescaleDB compression. Periods use Zabbix's time-suffix
// syntax ("30d", "1w", "86400").
type Housekeeping struct {
	HistoryMode             flexString `json:"hk_history_mode"`   // "1" = housekeeping deletes old history
	HistoryGlobal           flexString `json:"hk_history_global"` // "1" = the period below overrides every item's own
	History                 flexString `json:"hk_history"`
	TrendsMode              flexString `json:"hk_trends_mode"`
	TrendsGlobal            flexString `json:"hk_trends_global"`
	Trends                  flexString `json:"hk_trends"`
	DBExtension             flexString `json:"db_extension"`             // "timescaledb" when the history tables are hypertables, else ""
	CompressionStatus       flexString `json:"compression_status"`       // "1" = TimescaleDB compression on
	CompressOlder           flexString `json:"compress_older"`           // compress chunks older than this
	CompressionAvailability flexString `json:"compression_availability"` // documented, but 7.0's housekeeping.get doesn't return it
}

// flexString decodes a JSON string or number as a string. The API documents several housekeeping
// fields as integers; today Zabbix sends them as strings, but a numeric value must not break the
// whole read.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexString(n.String())
	return nil
}

// Housekeeping reads the global housekeeping settings. Zabbix allows this only for a Super admin
// token; see IsPermissionError.
func (c *Client) Housekeeping(ctx context.Context) (Housekeeping, error) {
	var hk Housekeeping
	err := c.call(ctx, "housekeeping.get", map[string]any{"output": "extend"}, true, &hk)
	return hk, err
}

// UpdateHousekeeping writes the given housekeeping fields (JSON field name -> value).
func (c *Client) UpdateHousekeeping(ctx context.Context, fields map[string]any) error {
	return c.call(ctx, "housekeeping.update", fields, true, nil)
}

// IsPermissionError reports whether err is Zabbix refusing the call for lack of rights (for
// example a housekeeping call with a token that isn't a Super admin).
func IsPermissionError(err error) bool {
	var re *rpcError
	if !errors.As(err, &re) {
		return false
	}
	msg := strings.ToLower(re.Message + " " + re.Data)
	return strings.Contains(msg, "permission")
}

// ParsePeriod converts a Zabbix time-suffix period ("30d", "2w", "12h", "90m", "3600s", "3600")
// to seconds.
func ParsePeriod(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty period")
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 's':
		s = s[:len(s)-1]
	case 'm':
		mult, s = 60, s[:len(s)-1]
	case 'h':
		mult, s = 3600, s[:len(s)-1]
	case 'd':
		mult, s = 86400, s[:len(s)-1]
	case 'w':
		mult, s = 7*86400, s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid period %q", s)
	}
	return n * mult, nil
}
