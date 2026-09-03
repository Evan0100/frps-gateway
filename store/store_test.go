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
