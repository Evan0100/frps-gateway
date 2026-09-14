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

func TestAdminGrantLifecycleAndAudit(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now()
	g, auditID, err := s.CreateAdminGrant(ctx, "gateway-admin", Grant{OpenID: "employee:u1", OperatorName: "Alice", IP: "203.0.113.8", Source: "admin_web", CreatedAt: now, ExpireAt: now.Add(time.Hour)})
	if err != nil || g.ID == 0 || auditID == 0 {
		t.Fatalf("create grant=%+v audit=%d err=%v", g, auditID, err)
	}
	if err = s.SetAdminAuditResult(ctx, auditID, "synced", ""); err != nil {
		t.Fatal(err)
	}
	extended, extendAudit, err := s.ExtendAdminGrant(ctx, "gateway-admin", g.ID, now.Add(2*time.Hour))
	if err != nil || !extended.ExpireAt.After(g.ExpireAt) || extendAudit == 0 {
		t.Fatalf("extend grant=%+v audit=%d err=%v", extended, extendAudit, err)
	}
	if _, revokeAudit, err := s.RevokeAdminGrant(ctx, "gateway-admin", g.ID); err != nil || revokeAudit == 0 {
		t.Fatalf("revoke audit=%d err=%v", revokeAudit, err)
	}
	if _, _, err := s.ExtendAdminGrant(ctx, "gateway-admin", g.ID, now.Add(3*time.Hour)); !errors.Is(err, ErrGrantInactive) {
		t.Fatalf("want inactive error, got %v", err)
	}
	records, total, err := s.ListGrantRecords(ctx, GrantFilter{IP: "203.0.113", User: "Alice", Status: "revoked"})
	if err != nil || total != 1 || len(records) != 1 || records[0].RevokedAt == nil {
		t.Fatalf("records=%v total=%d err=%v", records, total, err)
	}
	audits, total, err := s.ListAdminAudits(ctx, AuditFilter{Actor: "gateway-admin", IP: "203.0.113.8"})
	if err != nil || total != 3 || len(audits) != 3 {
		t.Fatalf("audits=%v total=%d err=%v", audits, total, err)
	}
}

func TestIsUniqueViolation(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "uniq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err = st.CreateToken(ctx, []byte("token-1"), "ou_1", "Alice", "om_dup", time.Hour, time.Minute); err != nil {
		t.Fatal(err)
	}
	err = st.CreateToken(ctx, []byte("token-2"), "ou_1", "Alice", "om_dup", time.Hour, time.Minute)
	if err == nil {
		t.Fatal("duplicate message ID accepted")
	}
	if !IsUniqueViolation(err) {
		t.Fatalf("duplicate token error not detected as UNIQUE violation: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = st.CreateToken(canceled, []byte("token-3"), "ou_1", "Alice", "om_other", time.Hour, time.Minute); err == nil || IsUniqueViolation(err) {
		t.Fatalf("canceled context misclassified: %v", err)
	}
}

func TestConsumeTokenIPsGrantsDeduplicatedBatch(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "multi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	raw := []byte("abcdefghijklmnopqrstuvwxyz012345")
	if err = st.CreateToken(ctx, raw, "u1", "Alice", "m1", time.Hour, time.Minute); err != nil {
		t.Fatal(err)
	}
	grants, err := st.ConsumeTokenIPs(ctx, raw, []string{"2001:db8::1", "203.0.113.9", "2001:db8::1"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 {
		t.Fatalf("grants=%d want 2: %+v", len(grants), grants)
	}
	seen := map[string]bool{}
	for _, g := range st.listUser(t, "u1") {
		seen[g.IP] = true
	}
	if !seen["2001:db8::1"] || !seen["203.0.113.9"] {
		t.Fatalf("user grants missing batch IPs: %v", seen)
	}
	if err = st.ValidateToken(ctx, raw); err == nil {
		t.Fatal("token still valid after consumption")
	}
}

func TestConsumeTokenIPsEnforcesBatchLimit(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	for _, ip := range []string{"192.0.2.1", "192.0.2.2"} {
		if err = st.AddGrantLimited(ctx, Grant{OpenID: "u1", IP: ip, Source: "test", CreatedAt: now, ExpireAt: now.Add(time.Hour)}, 3); err != nil {
			t.Fatal(err)
		}
	}
	raw := []byte("0123456789abcdef0123456789abcdef")
	if err = st.CreateToken(ctx, raw, "u1", "Alice", "m1", time.Hour, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ConsumeTokenIPs(ctx, raw, []string{"203.0.113.10", "203.0.113.11"}, 3); !errors.Is(err, ErrActiveIPLimit) {
		t.Fatalf("batch of 2 beyond limit accepted: %v", err)
	}
	if err = st.ValidateToken(ctx, raw); err != nil {
		t.Fatalf("token consumed by rejected batch: %v", err)
	}
	if _, err = st.ConsumeTokenIPs(ctx, raw, []string{"203.0.113.10"}, 3); err != nil {
		t.Fatalf("single IP within limit rejected: %v", err)
	}
}

func (s *Store) listUser(t *testing.T, openID string) []Grant {
	t.Helper()
	gs, err := s.ListUser(context.Background(), openID)
	if err != nil {
		t.Fatal(err)
	}
	return gs
}
