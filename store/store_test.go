package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenSingleUseAndSharedIP(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	raw := []byte("01234567890123456789012345678901")
	if err = s.CreateToken(ctx, raw, "u1", "Alice", "m1", time.Hour, time.Minute); err != nil {
		t.Fatal(err)
	}
	g, err := s.ConsumeToken(ctx, raw, "203.0.113.8", 3)
	if err != nil || g.OpenID != "u1" {
		t.Fatalf("grant=%+v err=%v", g, err)
	}
	if _, err = s.ConsumeToken(ctx, raw, "203.0.113.9", 3); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("second consume=%v", err)
	}
	now := time.Now()
	if err = s.AddGrant(ctx, Grant{OpenID: "u2", IP: "203.0.113.8", Source: "test", CreatedAt: now, ExpireAt: now.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RevokeUserIP(ctx, "u1", "203.0.113.8"); err != nil || n != 1 {
		t.Fatalf("revoke n=%d err=%v", n, err)
	}
	expiry, ok, err := s.EffectiveExpiry(ctx, "203.0.113.8")
	if err != nil || !ok || time.Until(expiry) < time.Hour {
		t.Fatalf("shared grant lost: %v %v %v", expiry, ok, err)
	}
}

func TestActiveIPLimitAllowsRefreshButRejectsFourthIP(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now()
	grant := func(ip string) error {
		return s.AddGrantLimited(ctx, Grant{OpenID: "u1", IP: ip, Source: "test", CreatedAt: now, ExpireAt: now.Add(time.Hour)}, 3)
	}
	for _, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		if err = grant(ip); err != nil {
			t.Fatal(err)
		}
	}
	if err = grant("192.0.2.1"); err != nil {
		t.Fatalf("refresh existing IP: %v", err)
	}
	if err = grant("192.0.2.4"); !errors.Is(err, ErrActiveIPLimit) {
		t.Fatalf("fourth IP err=%v", err)
	}
}
func TestMessageIdempotency(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.SaveReply(ctx, "m1", "ok"); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveReply(ctx, "m1", "changed"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.CachedReply(ctx, "m1")
	if err != nil || !ok || v != "ok" {
		t.Fatalf("%q %v %v", v, ok, err)
	}
}

func TestReplyOutboxPersistsRetriesAndCompletion(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.EnqueueReply(ctx, "m1", "c1", "ok"); err != nil {
		t.Fatal(err)
	}
	items, err := s.PendingReplies(ctx, 10)
	if err != nil || len(items) != 1 || items[0].Text != "ok" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if err = s.RetryReply(ctx, "m1", 0); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PendingReplyCount(ctx); err != nil || n != 1 {
		t.Fatalf("pending=%d err=%v", n, err)
	}
	if err = s.MarkReplySent(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PendingReplyCount(ctx); err != nil || n != 0 {
		t.Fatalf("pending=%d err=%v", n, err)
	}
}

func TestInboxDeduplicatesAndCompletes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.EnqueueInbox(ctx, "event-1", []byte(`{"event":"one"}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.EnqueueInbox(ctx, "event-1", []byte(`{"event":"duplicate"}`)); err != nil {
		t.Fatal(err)
	}
	item, ok, err := s.ClaimInbox(ctx)
	if err != nil || !ok || item.ID != "event-1" || string(item.Payload) != `{"event":"one"}` {
		t.Fatalf("item=%+v ok=%v err=%v", item, ok, err)
	}
	if err = s.CompleteInbox(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = s.ClaimInbox(ctx); err != nil || ok {
		t.Fatalf("second claim ok=%v err=%v", ok, err)
	}
}

func TestAccessRecordsInsertDedupAndPrune(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	batch := []AccessRecord{
		{Instance: "boot1", Seq: 1, Time: 1000, IP: "192.0.2.1", User: "alice", Source: "tcp/p1", Action: "allow", Reason: "whitelisted"},
		{Instance: "boot1", Seq: 2, Time: 1001, IP: "198.51.100.7", Source: "http/example.com", Action: "deny", Reason: "not_in_whitelist"},
		{Instance: "boot1", Seq: 3, Time: 1002, IP: "192.0.2.1", Source: "tcp/p1", Action: "deny", Reason: "outside_time_window"},
	}
	inserted, err := s.InsertAccessRecords(ctx, batch)
	if err != nil || inserted != 3 {
		t.Fatalf("first insert = %d, %v", inserted, err)
	}

	// Re-polling the same page must not duplicate rows.
	inserted, err = s.InsertAccessRecords(ctx, batch)
	if err != nil || inserted != 0 {
		t.Fatalf("reinsert = %d, %v", inserted, err)
	}

	// A frps restart produces a new instance; same seq values coexist.
	restart := []AccessRecord{
		{Instance: "boot2", Seq: 1, Time: 2000, IP: "192.0.2.1", Source: "tcp/p1", Action: "allow", Reason: "whitelisted"},
	}
	if inserted, err = s.InsertAccessRecords(ctx, restart); err != nil || inserted != 1 {
		t.Fatalf("restart insert = %d, %v", inserted, err)
	}

	if n, err := s.CountAccessRecords(ctx); err != nil || n != 4 {
		t.Fatalf("count = %d, %v", n, err)
	}

	// Pruning removes only records older than the cutoff.
	if removed, err := s.PruneAccessRecords(ctx, time.Unix(1500, 0)); err != nil || removed != 3 {
		t.Fatalf("prune = %d, %v", removed, err)
	}
	if n, err := s.CountAccessRecords(ctx); err != nil || n != 1 {
		t.Fatalf("count after prune = %d, %v", n, err)
	}
}
