package server

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"argus/internal/notify"
	"argus/internal/store"
)

func TestChannelMatches(t *testing.T) {
	groups := []string{"site1", "site2"}
	cases := []struct {
		name  string
		sites []string
		min   int
		sev   int
		want  bool
	}{
		{"all-sites, sev at floor", nil, 3, 3, true},
		{"all-sites, sev below floor", nil, 4, 3, false},
		{"one matching site", []string{"site1"}, 2, 5, true},
		{"other site", []string{"site9"}, 2, 5, false},
		{"multi-site, one matches", []string{"site9", "site2"}, 2, 5, true},
		{"multi-site, none match", []string{"site8", "site9"}, 2, 5, false},
		{"matching site but below floor", []string{"site2"}, 5, 3, false},
	}
	for _, c := range cases {
		if got := channelMatches(c.sites, c.min, groups, c.sev); got != c.want {
			t.Errorf("%s: channelMatches(%v,%d,%v,%d)=%v want %v", c.name, c.sites, c.min, groups, c.sev, got, c.want)
		}
	}
}

func TestMatchingUserChannels(t *testing.T) {
	chans := []store.UserNotifyChannel{
		{ID: 1, Type: "telegram", Sites: nil, MinSeverity: 2},              // all sites, low floor -> matches
		{ID: 2, Type: "discord", Sites: []string{"site1"}, MinSeverity: 4}, // site match but floor too high for sev 3
		{ID: 3, Type: "discord", Sites: []string{"siteX"}, MinSeverity: 2}, // wrong site
	}
	got := matchingUserChannels(chans, []string{"site1"}, 3)
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("expected only channel 1, got %+v", got)
	}
}

func TestAnyEmailToUsers(t *testing.T) {
	no := []store.NotifyChannel{
		{Type: "telegram"},
		{Type: "email", Config: map[string]string{"recipients": "fixed"}},
		{Type: "email", Config: nil},
	}
	if anyEmailToUsers(no) {
		t.Fatalf("did not expect a users-mode email channel")
	}
	yes := append(no, store.NotifyChannel{Type: "email", Config: map[string]string{"recipients": "users"}})
	if !anyEmailToUsers(yes) {
		t.Fatalf("expected a users-mode email channel to be detected")
	}
}

func TestSendEmailToUsersNoRecipients(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := store.NotifyChannel{Type: "email", Config: map[string]string{"recipients": "users"}}
	err := sendEmailToUsers(context.Background(), c, nil, notify.Event{Kind: "problem"}, logger)
	if err == nil {
		t.Fatalf("expected an error when there are no active users")
	}
}

func TestUserChannelRequestValidate(t *testing.T) {
	// Email is admin-only; a personal channel must reject it.
	if _, msg := (userChannelRequest{Type: "email", Config: map[string]string{"to": "x@y"}}).validate(); msg == "" {
		t.Fatalf("expected email to be rejected for a personal channel")
	}
	// Telegram needs both keys.
	if _, msg := (userChannelRequest{Type: "telegram", Config: map[string]string{"bot_token": "t"}}).validate(); msg == "" {
		t.Fatalf("expected missing chat_id to be rejected")
	}
	// Discord needs a webhook.
	if _, msg := (userChannelRequest{Type: "discord"}).validate(); msg == "" {
		t.Fatalf("expected missing webhook to be rejected")
	}
	// A valid telegram channel with an out-of-range severity clamps to 2..5.
	ch, msg := (userChannelRequest{Type: "telegram", MinSeverity: 9, Config: map[string]string{"bot_token": "t", "chat_id": "1"}}).validate()
	if msg != "" {
		t.Fatalf("unexpected validation error: %s", msg)
	}
	if ch.MinSeverity != 5 {
		t.Fatalf("expected severity clamped to 5, got %d", ch.MinSeverity)
	}
}

func TestChannelRequestEmailRecipients(t *testing.T) {
	if _, msg := (channelRequest{Type: "email", Name: "x", Config: map[string]string{"recipients": "users"}}).validate(); msg != "" {
		t.Fatalf("expected recipients=users to be accepted, got %q", msg)
	}
	if _, msg := (channelRequest{Type: "email", Name: "x", Config: map[string]string{"recipients": "bogus"}}).validate(); msg == "" {
		t.Fatalf("expected an invalid recipients value to be rejected")
	}
}
