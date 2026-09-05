package store

import (
	"context"
	"errors"
	"testing"
)

func TestUserNotifyChannelCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid := newTestUser(t, st)

	id, err := st.CreateUserNotifyChannel(ctx, UserNotifyChannel{
		UserID: uid, Type: "telegram", Enabled: true, Site: "site1", MinSeverity: 3,
		Config: map[string]string{"bot_token": "T", "chat_id": "42"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.GetUserNotifyChannel(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UserID != uid || got.Type != "telegram" || got.Site != "site1" || got.MinSeverity != 3 {
		t.Fatalf("unexpected row: %+v", got)
	}
	if got.Config["bot_token"] != "T" || got.Config["chat_id"] != "42" {
		t.Fatalf("config did not round-trip: %+v", got.Config)
	}
	if !got.Enabled {
		t.Fatalf("expected enabled")
	}

	list, err := st.ListUserNotifyChannels(ctx, uid)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: len=%d err=%v", len(list), err)
	}

	got.Site = "site2"
	got.Config["chat_id"] = "99"
	if err := st.UpdateUserNotifyChannel(ctx, *got); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := st.SetUserNotifyChannelEnabled(ctx, id, false); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	got2, _ := st.GetUserNotifyChannel(ctx, id)
	if got2.Site != "site2" || got2.Config["chat_id"] != "99" || got2.Enabled {
		t.Fatalf("update/toggle not applied: %+v", got2)
	}

	if err := st.DeleteUserNotifyChannel(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetUserNotifyChannel(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestEnabledUserNotifyChannels(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid := newTestUser(t, st)

	_, _ = st.CreateUserNotifyChannel(ctx, UserNotifyChannel{UserID: uid, Type: "discord", Enabled: true, Config: map[string]string{"webhook_url": "w"}})
	_, _ = st.CreateUserNotifyChannel(ctx, UserNotifyChannel{UserID: uid, Type: "telegram", Enabled: false, Config: map[string]string{"bot_token": "t", "chat_id": "1"}})

	enabled, err := st.EnabledUserNotifyChannels(ctx)
	if err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if len(enabled) != 1 || enabled[0].Type != "discord" {
		t.Fatalf("expected only the enabled discord channel, got %+v", enabled)
	}
}

func TestRecordUserNotifyDelivery(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid := newTestUser(t, st)
	id, _ := st.CreateUserNotifyChannel(ctx, UserNotifyChannel{UserID: uid, Type: "discord", Enabled: true, Config: map[string]string{"webhook_url": "w"}})

	if err := st.RecordUserNotifyDelivery(ctx, id, nil); err != nil {
		t.Fatalf("record success: %v", err)
	}
	c, _ := st.GetUserNotifyChannel(ctx, id)
	if c.SentCount != 1 || c.LastSentAt == 0 || c.LastError != "" {
		t.Fatalf("success not recorded: %+v", c)
	}

	if err := st.RecordUserNotifyDelivery(ctx, id, errors.New("boom")); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	c, _ = st.GetUserNotifyChannel(ctx, id)
	if c.LastError != "boom" || c.LastErrorAt == 0 || c.SentCount != 1 {
		t.Fatalf("failure not recorded (or clobbered success): %+v", c)
	}
}

func TestNotifyUserEmails(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	active, err := st.CreateUser(ctx, User{Email: "active@example.com", PasswordHash: "x", Role: "viewer"})
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	off, err := st.CreateUser(ctx, User{Email: "off@example.com", PasswordHash: "x", Role: "viewer"})
	if err != nil {
		t.Fatalf("create off: %v", err)
	}
	if err := st.SetUserDisabled(ctx, off, true); err != nil {
		t.Fatalf("disable: %v", err)
	}

	emails, err := st.NotifyUserEmails(ctx)
	if err != nil {
		t.Fatalf("emails: %v", err)
	}
	if len(emails) != 1 || emails[0] != "active@example.com" {
		t.Fatalf("expected only the active user, got %v", emails)
	}
	_ = active
}

func TestUserNotifyChannelCascade(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid := newTestUser(t, st)
	id, _ := st.CreateUserNotifyChannel(ctx, UserNotifyChannel{UserID: uid, Type: "discord", Enabled: true, Config: map[string]string{"webhook_url": "w"}})

	if err := st.DeleteUser(ctx, uid); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if _, err := st.GetUserNotifyChannel(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected channel removed by cascade, got %v", err)
	}
}
