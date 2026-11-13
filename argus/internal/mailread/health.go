// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package mailread

import (
	"context"
	"net/url"
	"strings"
)

// ServiceIssue is one open Microsoft 365 service health issue of a tenant (Graph
// admin/serviceAnnouncement/issues): an incident or an advisory on one service.
type ServiceIssue struct {
	ID             string `json:"id"`             // "EX123456"
	Title          string `json:"title"`          // "Users can't access their mailbox"
	Service        string `json:"service"`        // "Exchange Online"
	Classification string `json:"classification"` // incident | advisory
	Status         string `json:"status"`         // serviceDegradation, serviceInterruption, restoringService, ...
	Start          string `json:"startDateTime"`  // RFC 3339
	IsResolved     bool   `json:"isResolved"`
}

// ServiceIssues reads a tenant's open service health issues through Graph, as the app the tenant,
// client ID and secret name (application permission ServiceHealth.Read.All).
func ServiceIssues(ctx context.Context, tenant, clientID, secret string) ([]ServiceIssue, error) {
	token, err := getGraphToken(ctx, Config{Tenant: tenant, ClientID: clientID, Secret: secret})
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"$filter": {"isResolved eq false"},
		"$select": {"id,title,service,classification,status,startDateTime,isResolved"},
	}
	next := graphBase + "/admin/serviceAnnouncement/issues?" + strings.ReplaceAll(q.Encode(), "+", "%20")
	var out []ServiceIssue
	for page := 0; next != "" && page < 5; page++ {
		var d struct {
			Value []ServiceIssue `json:"value"`
			Next  string         `json:"@odata.nextLink"`
		}
		if err := graphGetHint(ctx, token, next, &d, "does the app have ServiceHealth.Read.All, with an admin's consent?"); err != nil {
			return nil, err
		}
		for _, is := range d.Value {
			if !is.IsResolved {
				out = append(out, is)
			}
		}
		next = d.Next
	}
	return out, nil
}
