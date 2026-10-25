// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package provision

import (
	"net/url"
	"strings"
)

// SaaSService is one of the common cloud services the Common SaaS add-on can check from a site's
// probe: a light address that answers without a login, and the name its sensors carry.
type SaaSService struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Default bool   `json:"default"` // checked on a new add-on
	// Status is where the vendor says whether it has an incident open (none = it publishes no
	// machine-readable status): beside the probe's own check, it tells "only us" from "the vendor".
	Status *SaaSStatus `json:"status,omitempty"`
}

// SaaSStatus is a vendor's public status source. Kind "statuspage" is an Atlassian Statuspage
// (API + /api/v2/incidents/unresolved.json), "indicator" a page with only /api/v2/status.json (its
// indicator, maintenance not counted), "slack" Slack's own API. Page is the status page for people.
type SaaSStatus struct {
	Kind string `json:"kind"`
	API  string `json:"-"`
	Page string `json:"page"`
}

// saasServices is the catalog. Each address answers small and fast (a health or robots page, a
// zero-byte probe); any answer short of a server error counts, since a login page or a 400 still says
// the service is there ({$SAAS.EXPECT}).
var saasServices = []SaaSService{
	{ID: "m365", Name: "Microsoft 365", URL: "https://login.microsoftonline.com/common/v2.0/.well-known/openid-configuration", Default: true},
	{ID: "teams", Name: "Microsoft Teams", URL: "https://teams.microsoft.com/robots.txt", Default: true},
	{ID: "azure", Name: "Microsoft Azure", URL: "https://management.azure.com/"},
	{ID: "google", Name: "Google", URL: "https://www.google.com/generate_204", Default: true},
	{ID: "youtube", Name: "YouTube", URL: "https://www.youtube.com/generate_204"},
	{ID: "aws", Name: "Amazon Web Services", URL: "https://dynamodb.eu-central-1.amazonaws.com/ping", Default: true},
	{ID: "cloudflare", Name: "Cloudflare", URL: "https://www.cloudflare.com/cdn-cgi/trace", Default: true,
		Status: &SaaSStatus{Kind: "statuspage", API: "https://www.cloudflarestatus.com", Page: "https://www.cloudflarestatus.com"}},
	{ID: "zoom", Name: "Zoom", URL: "https://zoom.us/robots.txt", Default: true,
		Status: &SaaSStatus{Kind: "statuspage", API: "https://www.zoomstatus.com", Page: "https://www.zoomstatus.com"}},
	{ID: "slack", Name: "Slack", URL: "https://slack.com/api/api.test",
		Status: &SaaSStatus{Kind: "slack", API: "https://slack-status.com/api/v2.0.0/current", Page: "https://slack-status.com"}},
	{ID: "salesforce", Name: "Salesforce", URL: "https://login.salesforce.com/"},
	{ID: "dropbox", Name: "Dropbox", URL: "https://www.dropbox.com/robots.txt", Default: true,
		Status: &SaaSStatus{Kind: "statuspage", API: "https://status.dropbox.com", Page: "https://status.dropbox.com"}},
	{ID: "github", Name: "GitHub", URL: "https://api.github.com/zen",
		Status: &SaaSStatus{Kind: "statuspage", API: "https://www.githubstatus.com", Page: "https://www.githubstatus.com"}},
	{ID: "apple", Name: "Apple iCloud", URL: "https://www.apple.com/library/test/success.html", Default: true},
	{ID: "chatgpt", Name: "ChatGPT", URL: "https://chatgpt.com/robots.txt",
		Status: &SaaSStatus{Kind: "indicator", API: "https://status.openai.com/api/v2/status.json", Page: "https://status.openai.com"}},
}

// SaaSServices is the catalog, in the order the add-on lists it.
func SaaSServices() []SaaSService { return saasServices }

// SaaSEntry is how a service reads in {$SAAS.URLS}: its address with its name
// ("https://www.google.com/generate_204#name=Google").
func SaaSEntry(name, address string) string {
	return address + "#name=" + strings.ReplaceAll(url.PathEscape(name), "+", "%2B")
}

// DefaultSaaSURLs is the {$SAAS.URLS} a new Common SaaS add-on starts with: the default services.
func DefaultSaaSURLs() string {
	var out []string
	for _, s := range saasServices {
		if s.Default {
			out = append(out, SaaSEntry(s.Name, s.URL))
		}
	}
	return strings.Join(out, ", ")
}
