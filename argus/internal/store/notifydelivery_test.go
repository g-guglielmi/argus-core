package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A channel's delivery health: a success stamps last_sent_at and counts; a failure records the (truncated)
// reason and time without touching the last success, so the card can show both.
func TestRecordNotifyDelivery(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id, err := st.CreateNotifyChannel(ctx, NotifyChannel{Type: "discord", Name: "noc", Enabled: true, MinSeverity: 2, Config: map[string]string{"webhook_url": "https://example.invalid/hook"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetNotifyChannel(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.LastSentAt != 0 || c.SentCount != 0 || c.LastError != "" || c.LastErrorAt != 0 {
		t.Fatalf("fresh channel should have no delivery history: %+v", c)
	}

	if err := st.RecordNotifyDelivery(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	c, _ = st.GetNotifyChannel(ctx, id)
	if c.LastSentAt == 0 || c.SentCount != 1 {
		t.Fatalf("success not recorded: %+v", c)
	}
	sent := c.LastSentAt

	if err := st.RecordNotifyDelivery(ctx, id, errors.New(strings.Repeat("x", 400))); err != nil {
		t.Fatal(err)
	}
	c, _ = st.GetNotifyChannel(ctx, id)
	if c.LastErrorAt == 0 || !strings.HasPrefix(c.LastError, "xxx") || len([]rune(c.LastError)) != 301 {
		t.Fatalf("failure not recorded/truncated: at=%d len=%d", c.LastErrorAt, len([]rune(c.LastError)))
	}
	if c.LastSentAt != sent || c.SentCount != 1 {
		t.Fatalf("a failure must not touch the success stamps: %+v", c)
	}

	// Listing carries the same fields (the Notifications page reads the list, not GetNotifyChannel).
	all, err := st.ListNotifyChannels(ctx)
	if err != nil || len(all) != 1 || all[0].SentCount != 1 || all[0].LastErrorAt == 0 {
		t.Fatalf("list should carry delivery health: %v %+v", err, all)
	}
}
