// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package zabbix

import (
	"context"
	"encoding/json"
	"fmt"
)

// Web scenarios (Zabbix httptest): steps a host's proxy runs in order, keeping cookies, so a login is
// checked end to end (server/webscenarios.go).

// HTTPTestStep is one step of a web scenario.
type HTTPTestStep struct {
	No              int         `json:"no,string"`
	Name            string      `json:"name"`
	URL             string      `json:"url"`
	Posts           HTTPFields  `json:"posts"`
	Required        string      `json:"required"`
	StatusCodes     string      `json:"status_codes"`
	FollowRedirects int         `json:"follow_redirects,string"`
	Timeout         string      `json:"timeout"`
}

// HTTPFields are a step's form fields. Zabbix sends them as a list, or as a string (raw post data, or
// none): a string reads as no fields.
type HTTPFields []HTTPField

func (f *HTTPFields) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		*f = nil
		return nil
	}
	var out []HTTPField
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	*f = out
	return nil
}

// HTTPField is a name and value (a form field, a header).
type HTTPField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// HTTPTest is a web scenario on a host.
type HTTPTest struct {
	HTTPTestID string         `json:"httptestid,omitempty"`
	HostID     string         `json:"hostid"`
	Name       string         `json:"name"`
	Delay      string         `json:"delay"`
	Retries    int            `json:"retries,string"`
	Agent      string         `json:"agent"`
	Status     int            `json:"status,string"` // 0 enabled
	Steps      []HTTPTestStep `json:"steps"`
}

// HTTPTests returns a host's web scenarios with their steps.
func (c *Client) HTTPTests(ctx context.Context, hostID string) ([]HTTPTest, error) {
	params := map[string]any{
		"output":      []string{"httptestid", "hostid", "name", "delay", "retries", "agent", "status"},
		"selectSteps": []string{"no", "name", "url", "posts", "required", "status_codes", "follow_redirects", "timeout"},
		"hostids":     hostID,
	}
	var out []HTTPTest
	return out, c.call(ctx, "httptest.get", params, true, &out)
}

// HTTPTestByID returns one web scenario, or nil.
func (c *Client) HTTPTestByID(ctx context.Context, id string) (*HTTPTest, error) {
	params := map[string]any{
		"output":      []string{"httptestid", "hostid", "name", "delay", "retries", "agent", "status"},
		"selectSteps": []string{"no", "name", "url", "posts", "required", "status_codes", "follow_redirects", "timeout"},
		"httptestids": id,
	}
	var out []HTTPTest
	if err := c.call(ctx, "httptest.get", params, true, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

func httpTestParams(t HTTPTest) map[string]any {
	steps := make([]map[string]any, 0, len(t.Steps))
	for i, s := range t.Steps {
		posts := s.Posts
		if posts == nil {
			posts = []HTTPField{}
		}
		steps = append(steps, map[string]any{
			"no": i + 1, "name": s.Name, "url": s.URL, "posts": posts, "post_type": 0,
			"required": s.Required, "status_codes": s.StatusCodes, "follow_redirects": 1, "timeout": s.Timeout,
		})
	}
	return map[string]any{"name": t.Name, "delay": t.Delay, "retries": 1, "agent": t.Agent, "status": t.Status, "steps": steps}
}

// CreateHTTPTest makes a web scenario on a host and returns its id.
func (c *Client) CreateHTTPTest(ctx context.Context, t HTTPTest) (string, error) {
	p := httpTestParams(t)
	p["hostid"] = t.HostID
	var res struct {
		IDs []string `json:"httptestids"`
	}
	if err := c.call(ctx, "httptest.create", p, true, &res); err != nil {
		return "", err
	}
	if len(res.IDs) == 0 {
		return "", fmt.Errorf("Zabbix returned no web scenario id")
	}
	return res.IDs[0], nil
}

// UpdateHTTPTest replaces a web scenario's settings and steps.
func (c *Client) UpdateHTTPTest(ctx context.Context, t HTTPTest) error {
	p := httpTestParams(t)
	p["httptestid"] = t.HTTPTestID
	return c.call(ctx, "httptest.update", p, true, nil)
}

// DeleteHTTPTest removes a web scenario, with its items and the triggers on them.
func (c *Client) DeleteHTTPTest(ctx context.Context, id string) error {
	return c.call(ctx, "httptest.delete", []string{id}, true, nil)
}

// NewTrigger is a trigger Argus makes on a host's own items.
type NewTrigger struct {
	Description string            `json:"description"`
	Expression  string            `json:"expression"`
	Priority    int               `json:"priority,string"`
	Comments    string            `json:"comments"`
	Tags        map[string]string `json:"-"`
}

// CreateTriggers makes triggers.
func (c *Client) CreateTriggers(ctx context.Context, ts []NewTrigger) error {
	if len(ts) == 0 {
		return nil
	}
	params := make([]map[string]any, 0, len(ts))
	for _, t := range ts {
		var tags []map[string]string
		for k, v := range t.Tags {
			tags = append(tags, map[string]string{"tag": k, "value": v})
		}
		params = append(params, map[string]any{"description": t.Description, "expression": t.Expression, "priority": t.Priority, "comments": t.Comments, "tags": tags})
	}
	return c.call(ctx, "trigger.create", params, true, nil)
}

// TriggerIDsByTag returns a host's triggers carrying a tag with this value.
func (c *Client) TriggerIDsByTag(ctx context.Context, hostID, tag, value string) ([]string, error) {
	params := map[string]any{
		"output":  []string{"triggerid"},
		"hostids": hostID,
		"tags":    []map[string]any{{"tag": tag, "value": value, "operator": 1}},
	}
	var out []struct {
		ID string `json:"triggerid"`
	}
	if err := c.call(ctx, "trigger.get", params, true, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out))
	for _, t := range out {
		ids = append(ids, t.ID)
	}
	return ids, nil
}

// DeleteTriggers removes triggers.
func (c *Client) DeleteTriggers(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return c.call(ctx, "trigger.delete", ids, true, nil)
}

// HostTechnicalName returns a host's technical name (the "host" field), as trigger expressions name it.
func (c *Client) HostTechnicalName(ctx context.Context, hostID string) (string, error) {
	var hs []struct {
		Host string `json:"host"`
	}
	if err := c.call(ctx, "host.get", map[string]any{"output": []string{"host"}, "hostids": hostID}, true, &hs); err != nil {
		return "", err
	}
	if len(hs) == 0 {
		return "", fmt.Errorf("host %s not found", hostID)
	}
	return hs[0].Host, nil
}
