// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// webhookClient is the HTTP client behind every outbound webhook. It never follows a redirect: a
// destination that answers with one gets the error, not a second request to wherever it pointed.
var webhookClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// publicClient is webhookClient for a personal channel: it only connects to public internet addresses.
// Anyone signed in can save a personal channel, so its address must not reach the core's own network
// (a self-hosted ntfy on the LAN belongs in a global channel, which only admins set up). The address is
// checked when the connection is made, after DNS, so a name that resolves to a private address is
// refused too. It connects directly, without the environment's proxy, since through a proxy the
// connection goes to the proxy and the destination can't be checked.
var publicClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: webhookClient.CheckRedirect,
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
	},
}

// ErrNotPublic is the error for a personal channel whose address isn't on the public internet.
var ErrNotPublic = errors.New("a personal channel can only send to an address on the public internet")

func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !IsPublicIP(ip) {
		return ErrNotPublic
	}
	return nil
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
var thisNet = &net.IPNet{IP: net.IPv4(0, 0, 0, 0), Mask: net.CIDRMask(8, 32)}

// IsPublicIP reports whether ip is a public internet address: not loopback, private, link-local,
// multicast, unspecified, "this network" or carrier-grade NAT (where Tailscale addresses live).
func IsPublicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip) || thisNet.Contains(ip))
}

type publicKey struct{}

// clientFor is the client a send uses: the public-only one for a personal channel (see Send).
func clientFor(ctx context.Context) *http.Client {
	if ctx.Value(publicKey{}) != nil {
		return publicClient
	}
	return webhookClient
}

// discordHosts are the hosts a Discord webhook lives on.
var discordHosts = map[string]bool{
	"discord.com": true, "discordapp.com": true, "ptb.discord.com": true, "canary.discord.com": true,
}

// ValidDiscordWebhook reports whether raw is an https Discord webhook URL. Nothing else is
// accepted: the core posts alerts to this address on behalf of whoever saved it (viewers can save
// personal channels), so it must not be able to point at something on the core's own network.
func ValidDiscordWebhook(raw string) bool {
	u, ok := httpsURL(raw)
	return ok && discordHosts[strings.ToLower(u.Hostname())] && strings.HasPrefix(u.Path, "/api/webhooks/")
}

// DiscordWebhookHint is the validation message for a webhook that isn't one.
const DiscordWebhookHint = "the Discord webhook must be an https://discord.com/api/webhooks/... URL"

// teamsHostSuffixes are where a Teams Workflows webhook lives (Power Automate on Azure Logic Apps or
// the Power Platform), plus the older Office 365 connector hosts some tenants still have.
var teamsHostSuffixes = []string{".logic.azure.com", ".api.powerplatform.com", ".webhook.office.com"}

// ValidTeamsWebhook reports whether raw is an https Teams Workflows (or connector) webhook URL.
func ValidTeamsWebhook(raw string) bool {
	u, ok := httpsURL(raw)
	if !ok {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, s := range teamsHostSuffixes {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

// TeamsWebhookHint is the validation message for a Teams webhook that isn't one.
const TeamsWebhookHint = "the Teams webhook must be the https URL a Teams Workflow gives you (on logic.azure.com or powerplatform.com)"

// ValidSlackWebhook reports whether raw is an https Slack incoming webhook URL.
func ValidSlackWebhook(raw string) bool {
	u, ok := httpsURL(raw)
	if !ok {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return (h == "hooks.slack.com" || h == "hooks.slack-gov.com") && strings.HasPrefix(u.Path, "/services/")
}

// SlackWebhookHint is the validation message for a Slack webhook that isn't one.
const SlackWebhookHint = "the Slack webhook must be an https://hooks.slack.com/services/... URL"

// httpsURL parses raw as an https URL with no user info and the default port.
func httpsURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || (u.Port() != "" && u.Port() != "443") {
		return nil, false
	}
	return u, true
}

// ValidServerURL reports whether raw is an http(s) address without user info: a self-hosted server
// (ntfy, Gotify) or a generic webhook.
func ValidServerURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.User == nil && u.Hostname() != ""
}

// secretPatterns match credentials that can end up inside an error message: a transport error
// prints the request URL, which for Telegram carries the bot token, for Discord and Slack the
// webhook's own token, and for Teams the signature. Every error notify returns is passed through
// Redact so the token never reaches the log, the channel's health line or a system notice.
var secretPatterns = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`bot[0-9]+:[A-Za-z0-9_-]+`), "bot<redacted>"},
	{regexp.MustCompile(`(/api/webhooks/[0-9]+/)[A-Za-z0-9_.-]+`), "${1}<redacted>"},
	{regexp.MustCompile(`(/services/[A-Za-z0-9]+/[A-Za-z0-9]+/)[A-Za-z0-9]+`), "${1}<redacted>"},
	{regexp.MustCompile(`([?&]sig=)[^&\s"]+`), "${1}<redacted>"},
}

// Redact strips known secret shapes from s.
func Redact(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.with)
	}
	return s
}

// redactErr is Redact for an error (nil stays nil).
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	if r := Redact(err.Error()); r != err.Error() {
		return errors.New(r)
	}
	return err
}

// hideURL replaces a destination URL inside an error with its scheme and host only, for addresses
// whose path or query may itself be the secret (a generic webhook, a self-hosted server).
func hideURL(err error, raw string) error {
	if err == nil {
		return nil
	}
	raw = strings.TrimSpace(raw)
	u, perr := url.Parse(raw)
	if perr != nil || raw == "" || !strings.Contains(err.Error(), raw) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), raw, u.Scheme+"://"+u.Host+"/..."))
}
