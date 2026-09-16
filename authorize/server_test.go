package authorize

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"frps-gateway/frps"
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
	r.Header.Del("X-Forwarded-For")
	if _, err = s.clientIP(r); err == nil {
		t.Fatal("trusted proxy without X-Forwarded-For was accepted")
	}
	r.Header.Set("X-Forwarded-For", "10.0.0.8")
	if _, err = s.clientIP(r); err == nil {
		t.Fatal("private forwarded client address was accepted")
	}
}

func TestRejectsTrustingEveryProxy(t *testing.T) {
	if _, err := New(":0", "https://access.example.com", "", "", []string{"0.0.0.0/0"}, 3, nil, nil, slog.Default()); err == nil {
		t.Fatal("want error for trusting the entire IPv4 internet")
	}
	if _, err := New(":0", "https://access.example.com", "", "", []string{"::/0"}, 3, nil, nil, slog.Default()); err == nil {
		t.Fatal("want error for trusting the entire IPv6 internet")
	}
}

func TestReadinessEndpoint(t *testing.T) {
	s, err := New(":0", "https://access.example.com", "", "", nil, 3, nil, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	s.SetReadiness(func(context.Context) error { return errors.New("frps unavailable") })
	r := httptest.NewRequest("GET", "http://gateway/readyz", nil)
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", w.Code)
	}
	s.SetReadiness(func(context.Context) error { return nil })
	w = httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
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

func TestNewLinkRetriesDuplicateMessageID(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "links.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := New(":0", "https://access.example.com", "", "", nil, 3, st, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := s.NewLink(ctx, "ou_1", "Alice", "om_redelivered", time.Hour, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// A redelivered event reuses the message ID; the stored token is a hash,
	// so a fresh link must be issued instead of failing.
	second, err := s.NewLink(ctx, "ou_1", "Alice", "om_redelivered", time.Hour, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("redelivery returned the same URL: %s", first)
	}
	for _, link := range []string{first, second} {
		raw, err := base64.RawURLEncoding.DecodeString(link[len("https://access.example.com/authorize/"):])
		if err != nil {
			t.Fatal(err)
		}
		if err = st.ValidateToken(ctx, raw); err != nil {
			t.Fatalf("token for %s invalid: %v", link, err)
		}
	}
}

func TestAuthorizePostGrantsConnectionAndReportedIPv4(t *testing.T) {
	var mu sync.Mutex
	var added []string
	frsp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/whitelist" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			IP string `json:"ip"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		added = append(added, body.IP)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"msg":"success","data":{"ip":"` + body.IP + `","expireAt":2000000000}}`))
	}))
	defer frsp.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "post.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := New(":0", "https://access.example.com", "", "", nil, 3, st, frps.New(frsp.URL, "admin", "secret"), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	link, err := s.NewLink(ctx, "u1", "Alice", "m1", time.Hour, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(link, "https://access.example.com/authorize/")

	form := url.Values{"ipv4": {"203.0.113.9"}}
	r := httptest.NewRequest("POST", "http://gateway/authorize/"+token, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "[2001:db8::25]:443"
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "2001:db8::25") || !strings.Contains(w.Body.String(), "203.0.113.9") {
		t.Fatalf("success page missing granted IPs: %s", w.Body.String())
	}
	mu.Lock()
	if len(added) != 2 {
		t.Fatalf("frps adds=%v want both IPs", added)
	}
	mu.Unlock()

	// A spoofed or malformed reported IPv4 is ignored and only the
	// connection IP is granted.
	link2, err := s.NewLink(ctx, "u2", "Bob", "m2", time.Hour, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token2 := strings.TrimPrefix(link2, "https://access.example.com/authorize/")
	form2 := url.Values{"ipv4": {"not-an-ip"}}
	r2 := httptest.NewRequest("POST", "http://gateway/authorize/"+token2, strings.NewReader(form2.Encode()))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r2.RemoteAddr = "198.51.100.7:443"
	w2 := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK || !strings.Contains(w2.Body.String(), "198.51.100.7") {
		t.Fatalf("status=%d body=%s", w2.Code, w2.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(added) != 3 {
		t.Fatalf("frps adds=%v want one add for second request", added)
	}
}
