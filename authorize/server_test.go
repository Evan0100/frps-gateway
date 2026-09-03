package authorize

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"frps-gateway/store"
)

func TestClientIPTrustBoundary(t *testing.T) {
	s, err := New(":0", "https://access.example.com", "", "", []string{"127.0.0.0/8"}, 3, nil, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://x", nil)
	r.RemoteAddr = "198.51.100.7:1234"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	ip, err := s.clientIP(r)
	if err != nil || ip != "198.51.100.7" {
		t.Fatalf("untrusted proxy ip=%s err=%v", ip, err)
	}
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.8, 127.0.0.2")
	ip, err = s.clientIP(r)
	if err != nil || ip != "203.0.113.8" {
		t.Fatalf("trusted proxy ip=%s err=%v", ip, err)
	}
}

func TestAuthorizeRejectsFourthActiveIPWithoutConsumingToken(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	for _, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		if err = st.AddGrantLimited(ctx, store.Grant{OpenID: "u1", IP: ip, Source: "test", CreatedAt: now, ExpireAt: now.Add(time.Hour)}, 3); err != nil {
			t.Fatal(err)
		}
	}
	raw := []byte("01234567890123456789012345678901")
	if err = st.CreateToken(ctx, raw, "u1", "Alice", "m1", time.Hour, time.Minute); err != nil {
		t.Fatal(err)
	}
	s, err := New(":0", "https://access.example.com", "", "", nil, 3, st, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	r := httptest.NewRequest("POST", "http://gateway/authorize/"+token, nil)
	r.RemoteAddr = "192.0.2.4:1234"
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if err = st.ValidateToken(ctx, raw); err != nil {
		t.Fatalf("token was consumed: %v", err)
	}
}
