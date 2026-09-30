// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package zabbix

import (
	"context"
	"strconv"
)

// Event is a past or open problem event (event.get, source trigger, value "problem"): when it
// started, its trigger, its host(s), and the recovery event that closed it ("0" while it is open).
type Event struct {
	EventID  string       `json:"eventid"`
	Clock    string       `json:"clock"`
	Name     string       `json:"name"`
	Severity string       `json:"severity"`
	REventID string       `json:"r_eventid"`
	ObjectID string       `json:"objectid"` // triggerid
	Hosts    []TargetHost `json:"hosts"`
}

// ProblemEvents returns the problem events that started since from, newest first, at most limit,
// for the given hosts (all hosts when hostIDs is empty) and, when triggerIDs is set, those triggers
// only. Closed ones carry their recovery event.
func (c *Client) ProblemEvents(ctx context.Context, hostIDs, triggerIDs []string, from int64, limit int) ([]Event, error) {
	params := map[string]any{
		"output":      []string{"eventid", "clock", "name", "severity", "r_eventid", "objectid"},
		"selectHosts": []string{"hostid", "name", "status"},
		"source":      0, // triggers
		"object":      0, // trigger events
		"value":       1, // problem (not the OK events)
		"time_from":   from,
		"sortfield":   []string{"clock", "eventid"},
		"sortorder":   "DESC",
		"limit":       limit,
	}
	if len(hostIDs) > 0 {
		params["hostids"] = hostIDs
	}
	if len(triggerIDs) > 0 {
		params["objectids"] = triggerIDs
	}
	var evs []Event
	return evs, c.call(ctx, "event.get", params, true, &evs)
}

// EventClocks returns when each of the given events happened (unix seconds), for the recovery
// events that closed problems.
func (c *Client) EventClocks(ctx context.Context, eventIDs []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(eventIDs) == 0 {
		return out, nil
	}
	var evs []struct {
		EventID string `json:"eventid"`
		Clock   string `json:"clock"`
	}
	params := map[string]any{"output": []string{"eventid", "clock"}, "eventids": eventIDs}
	if err := c.call(ctx, "event.get", params, true, &evs); err != nil {
		return nil, err
	}
	for _, e := range evs {
		n, _ := strconv.ParseInt(e.Clock, 10, 64)
		out[e.EventID] = n
	}
	return out, nil
}

// TrendRow is one hourly trend of an item with its sample count, so averages can be weighted.
type TrendRow struct {
	ItemID   string `json:"itemid"`
	Clock    string `json:"clock"`
	Num      string `json:"num"`
	ValueAvg string `json:"value_avg"`
}

// TrendRows returns the hourly trends of many items within [from, to] (unix seconds).
func (c *Client) TrendRows(ctx context.Context, itemIDs []string, from, to int64) ([]TrendRow, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	params := map[string]any{
		"output":    []string{"itemid", "clock", "num", "value_avg"},
		"itemids":   itemIDs,
		"time_from": from,
		"time_till": to,
	}
	var rows []TrendRow
	return rows, c.call(ctx, "trend.get", params, true, &rows)
}

// TextHistory returns the text values of one item within [from, to], oldest first (history type 4).
func (c *Client) TextHistory(ctx context.Context, itemID string, from, to int64) ([]HistoryPoint, error) {
	return c.History(ctx, itemID, 4, from, to)
}

// HostItemID returns the id of one host's item by exact key ("" when it has none).
func (c *Client) HostItemID(ctx context.Context, hostID, key string) (string, error) {
	params := map[string]any{
		"output":  []string{"itemid"},
		"hostids": hostID,
		"filter":  map[string]any{"key_": key},
	}
	var items []Item
	if err := c.call(ctx, "item.get", params, true, &items); err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", nil
	}
	return items[0].ItemID, nil
}
