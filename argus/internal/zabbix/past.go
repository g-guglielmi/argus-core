// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package zabbix

import (
	"context"
	"sort"
	"strconv"
)

// A renamed sensor's earlier readings (server/renames.go): when discovery loses a sensor under one
// name and finds it under another, someone links the two and Argus keeps the old one's readings
// before Zabbix deletes it. History, Trends, TrendRows and HistoryMulti add them to what Zabbix has
// for the new sensor, so every chart, uptime and report reads across both names.

// PastPoint is one kept raw reading.
type PastPoint struct {
	Clock int64
	V     float64
}

// PastTrend is one kept hour.
type PastTrend struct {
	Clock         int64
	Min, Avg, Max float64
	Num           int64
}

// Past hands out a sensor's kept readings from its earlier names (nothing for most sensors, and
// without a lookup: it knows which sensors were renamed).
type Past interface {
	PastHistory(ctx context.Context, itemID string, from, to int64) []PastPoint
	PastTrends(ctx context.Context, itemID string, from, to int64) []PastTrend
}

// SetPast sets where kept readings come from (once, at start).
func (c *Client) SetPast(p Past) {
	c.mu.Lock()
	c.past = p
	c.mu.Unlock()
}

type noPastKey struct{}

// WithoutPast asks for what Zabbix itself has, without kept readings (when keeping them).
func WithoutPast(ctx context.Context) context.Context {
	return context.WithValue(ctx, noPastKey{}, true)
}

func (c *Client) pastFor(ctx context.Context, valueType int) Past {
	if valueType != 0 && valueType != 3 {
		return nil
	}
	if v, _ := ctx.Value(noPastKey{}).(bool); v {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.past
}

func fnum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func withPastHistory(ctx context.Context, p Past, itemID string, from, to int64, pts []HistoryPoint) []HistoryPoint {
	old := p.PastHistory(ctx, itemID, from, to)
	if len(old) == 0 {
		return pts
	}
	out := make([]HistoryPoint, 0, len(old)+len(pts))
	for _, o := range old {
		out = append(out, HistoryPoint{Clock: strconv.FormatInt(o.Clock, 10), Value: fnum(o.V)})
	}
	out = append(out, pts...)
	sort.SliceStable(out, func(i, j int) bool { return atoi64(out[i].Clock) < atoi64(out[j].Clock) })
	return out
}

func atoi64(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
