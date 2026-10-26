// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package mailread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Where Graph and its sign-in live (tests point them at a local server).
var (
	graphBase = "https://graph.microsoft.com/v1.0"
	loginBase = "https://login.microsoftonline.com"
)

var graphClient = &http.Client{Timeout: 30 * time.Second}

// tokens caches an app's access token until a minute before it expires.
var tokens = struct {
	sync.Mutex
	m map[string]graphToken
}{m: map[string]graphToken{}}

type graphToken struct {
	value string
	until time.Time
}

func tokenKey(c Config) string {
	h := sha256.Sum256([]byte(c.Tenant + "\x00" + c.ClientID + "\x00" + c.Secret))
	return hex.EncodeToString(h[:])
}

// graphToken signs the app in (client credentials) for Graph.
func getGraphToken(ctx context.Context, c Config) (string, error) {
	if c.Tenant == "" || c.ClientID == "" || c.Secret == "" {
		return "", errors.New("the tenant ID, the application ID and the client secret are needed")
	}
	key := tokenKey(c)
	tokens.Lock()
	if t, ok := tokens.m[key]; ok && time.Now().Before(t.until) {
		tokens.Unlock()
		return t.value, nil
	}
	tokens.Unlock()
	form := url.Values{"client_id": {c.ClientID}, "client_secret": {c.Secret}, "grant_type": {"client_credentials"}, "scope": {"https://graph.microsoft.com/.default"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginBase+"/"+url.PathEscape(c.Tenant)+"/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := graphClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach Microsoft's sign-in: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var d struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &d)
	if resp.StatusCode != http.StatusOK || d.AccessToken == "" {
		why := d.Description
		if i := strings.IndexAny(why, "\r\n"); i > 0 {
			why = why[:i]
		}
		if why == "" {
			why = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", errors.New("Microsoft refused the app's sign-in: " + why)
	}
	tokens.Lock()
	tokens.m[key] = graphToken{value: d.AccessToken, until: time.Now().Add(time.Duration(d.ExpiresIn-60) * time.Second)}
	tokens.Unlock()
	return d.AccessToken, nil
}

// graphGet asks Graph and decodes its answer into out, with Graph's own reason when it refuses.
func graphGet(ctx context.Context, token, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := graphClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Microsoft Graph: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		why := e.Error.Message
		if why == "" {
			why = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			why += " (does the app have Mail.Read, and does the access policy let it read this mailbox?)"
		}
		return errors.New("Microsoft Graph refused: " + why)
	}
	return json.Unmarshal(body, out)
}

func validAddress(a string) bool {
	return a != "" && strings.Count(a, "@") == 1 && !strings.ContainsAny(a, "/?#% \t")
}

// graphFolder finds the folder's id: the inbox by its well-known name, any other at the top of the
// mailbox by its display name.
func graphFolder(ctx context.Context, token string, c Config) (string, error) {
	if !validAddress(c.Address) {
		return "", errors.New("the mailbox address isn't valid")
	}
	user := graphBase + "/users/" + url.PathEscape(c.Address)
	if c.Folder == "" || strings.EqualFold(c.Folder, "inbox") {
		return user + "/mailFolders/inbox", nil
	}
	var d struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	q := url.Values{"$filter": {"displayName eq '" + strings.ReplaceAll(c.Folder, "'", "''") + "'"}, "$select": {"id"}}
	if err := graphGet(ctx, token, user+"/mailFolders?"+q.Encode(), &d); err != nil {
		return "", err
	}
	if len(d.Value) == 0 {
		return "", fmt.Errorf("the mailbox has no folder named %s at its top level", c.Folder)
	}
	return user + "/mailFolders/" + url.PathEscape(d.Value[0].ID), nil
}

type graphMessage struct {
	ID       string    `json:"id"`
	Subject  string    `json:"subject"`
	Received time.Time `json:"receivedDateTime"`
	Preview  string    `json:"bodyPreview"`
	From     struct {
		EmailAddress struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"from"`
}

// The Graph watermark is "<RFC 3339 time of the last mail read>|<ids read at exactly that time>".
func parseGraphWatermark(wm string) (time.Time, map[string]bool, bool) {
	ts, ids, found := strings.Cut(wm, "|")
	t, err := time.Parse(time.RFC3339Nano, ts)
	if !found || err != nil {
		return time.Time{}, nil, false
	}
	seen := map[string]bool{}
	for _, id := range strings.Split(ids, ",") {
		if id != "" {
			seen[id] = true
		}
	}
	return t, seen, true
}

func readGraph(ctx context.Context, c Config, watermark string, lookback time.Duration) ([]Mail, string, error) {
	token, err := getGraphToken(ctx, c)
	if err != nil {
		return nil, watermark, err
	}
	folder, err := graphFolder(ctx, token, c)
	if err != nil {
		return nil, watermark, err
	}
	since, seen, ok := parseGraphWatermark(watermark)
	if !ok {
		since, seen = time.Now().Add(-lookback).UTC(), map[string]bool{}
	}
	q := url.Values{
		"$select":  {"id,subject,from,receivedDateTime,bodyPreview"},
		"$filter":  {"receivedDateTime ge " + since.UTC().Format(time.RFC3339)},
		"$orderby": {"receivedDateTime asc"},
		"$top":     {"100"},
	}
	next := folder + "/messages?" + q.Encode()
	var mails []Mail
	for page := 0; next != "" && page < 4 && len(mails) < MaxPerRead; page++ {
		var d struct {
			Value    []graphMessage `json:"value"`
			NextLink string         `json:"@odata.nextLink"`
		}
		if err := graphGet(ctx, token, next, &d); err != nil {
			return nil, watermark, err
		}
		for _, m := range d.Value {
			if seen[m.ID] || m.Received.Before(since) {
				continue
			}
			from := m.From.EmailAddress.Address
			if n := m.From.EmailAddress.Name; n != "" && n != from {
				from = n + " <" + from + ">"
			}
			mails = append(mails, Mail{ID: m.ID, From: from, Subject: strings.Join(strings.Fields(m.Subject), " "), Text: clip(strings.Join(strings.Fields(m.Preview), " ")), At: m.Received})
			if len(mails) == MaxPerRead {
				break
			}
		}
		next = ""
		if d.NextLink != "" && strings.HasPrefix(d.NextLink, graphBase+"/") {
			next = d.NextLink
		}
	}
	if len(mails) == 0 {
		if !ok {
			return nil, since.Format(time.RFC3339Nano) + "|", nil
		}
		return nil, watermark, nil
	}
	last := mails[len(mails)-1].At
	var ids []string
	if last.Equal(since) {
		for id := range seen {
			ids = append(ids, id)
		}
	}
	for _, m := range mails {
		if m.At.Equal(last) {
			ids = append(ids, m.ID)
		}
	}
	return mails, last.UTC().Format(time.RFC3339Nano) + "|" + strings.Join(ids, ","), nil
}

func probeGraph(ctx context.Context, c Config) (int, time.Time, error) {
	token, err := getGraphToken(ctx, c)
	if err != nil {
		return 0, time.Time{}, err
	}
	folder, err := graphFolder(ctx, token, c)
	if err != nil {
		return 0, time.Time{}, err
	}
	var f struct {
		Total int `json:"totalItemCount"`
	}
	if err := graphGet(ctx, token, folder+"?$select=totalItemCount", &f); err != nil {
		return 0, time.Time{}, err
	}
	var d struct {
		Value []graphMessage `json:"value"`
	}
	q := url.Values{"$select": {"receivedDateTime"}, "$orderby": {"receivedDateTime desc"}, "$top": {"1"}}
	if err := graphGet(ctx, token, folder+"/messages?"+q.Encode(), &d); err != nil || len(d.Value) == 0 {
		return f.Total, time.Time{}, nil
	}
	return f.Total, d.Value[0].Received, nil
}
