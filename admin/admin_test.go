package admin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"frps-gateway/frps"
	"frps-gateway/store"
)

type fakeClient struct{ entries []frps.Entry }

func (f *fakeClient) List(context.Context) ([]frps.Entry, error) { return f.entries, nil }

type fakeReconciler struct{ calls int }

func (f *fakeReconciler) Reconcile(context.Context) error { f.calls++; return nil }

func newTestHandler(t *testing.T, client *fakeClient, reconcile *fakeReconciler) (*Handler, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h, err := New(st, client, reconcile, "gateway-admin", "a-long-admin-password", time.Hour, 24*time.Hour, KnockConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return h, st
}

func request(t *testing.T, h http.Handler, method, target string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, target, body)
	r.RemoteAddr = "192.0.2.10:1234"
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func loginSession(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	w := request(t, h, http.MethodGet, "https://access.example.com/admin/login", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("login page status=%d", w.Code)
	}
	var nonceCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == loginCookie {
			nonceCookie = c
		}
	}
	if nonceCookie == nil {
		t.Fatal("missing login nonce cookie")
	}
	form := url.Values{"username": {"gateway-admin"}, "password": {"a-long-admin-password"}, "login_nonce": {nonceCookie.Value}}
	w = request(t, h, http.MethodPost, "https://access.example.com/admin/login", form, nonceCookie)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatal("missing session cookie")
	return nil
}

func csrfFromPage(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(body)
	if len(m) != 2 {
		t.Fatal("missing csrf token")
	}
	return m[1]
}

func TestAdminRequiresLoginAndShowsManualFrpsEntries(t *testing.T) {
	h, _ := newTestHandler(t, &fakeClient{entries: []frps.Entry{{IP: "203.0.113.7", ExpireAt: time.Now().Add(time.Hour).Unix()}}}, &fakeReconciler{})
	w := request(t, h, http.MethodGet, "https://access.example.com/admin", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/login" {
		t.Fatalf("status=%d location=%q", w.Code, w.Header().Get("Location"))
	}
	session := loginSession(t, h)
	w = request(t, h, http.MethodGet, "https://access.example.com/admin", nil, session)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "frps 应急/手工") || !strings.Contains(w.Body.String(), "只读保留") {
		t.Fatalf("dashboard status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminMutationRequiresCSRFAndCreatesAudit(t *testing.T) {
	reconcile := &fakeReconciler{}
	h, st := newTestHandler(t, &fakeClient{}, reconcile)
	session := loginSession(t, h)
	w := request(t, h, http.MethodPost, "https://access.example.com/admin/grants", url.Values{"subject_id": {"employee:u1"}, "subject_name": {"Alice"}, "ip": {"203.0.113.8"}, "ttl": {"4h"}}, session)
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", w.Code)
	}
	w = request(t, h, http.MethodGet, "https://access.example.com/admin", nil, session)
	csrf := csrfFromPage(t, w.Body.String())
	form := url.Values{"csrf": {csrf}, "subject_id": {"employee:u1"}, "subject_name": {"Alice"}, "ip": {"203.0.113.8"}, "ttl": {"4h"}}
	w = request(t, h, http.MethodPost, "https://access.example.com/admin/grants", form, session)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("add status=%d body=%s", w.Code, w.Body.String())
	}
	if reconcile.calls != 1 {
		t.Fatalf("reconcile calls=%d", reconcile.calls)
	}
	grants, _, err := st.ListGrantRecords(context.Background(), store.GrantFilter{Status: "active"})
	if err != nil || len(grants) != 1 || grants[0].IP != "203.0.113.8" {
		t.Fatalf("grants=%v err=%v", grants, err)
	}
	audits, _, err := st.ListAdminAudits(context.Background(), store.AuditFilter{})
	if err != nil || len(audits) != 1 || audits[0].Result != "synced" || audits[0].Actor != "gateway-admin" {
		t.Fatalf("audits=%v err=%v", audits, err)
	}
}

func TestAdminLoginIgnoresOriginHeader(t *testing.T) {
	h, _ := newTestHandler(t, &fakeClient{}, &fakeReconciler{})
	w := request(t, h, http.MethodGet, "https://access.example.com/admin/login", nil)
	nonce := w.Result().Cookies()[0]
	form := url.Values{"username": {"gateway-admin"}, "password": {"a-long-admin-password"}, "login_nonce": {nonce.Value}}
	var body io.Reader = strings.NewReader(form.Encode())
	r := httptest.NewRequest(http.MethodPost, "https://access.example.com/admin/login", body)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://evil.example")
	r.AddCookie(nonce)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var hasSession bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			hasSession = true
		}
	}
	if !hasSession {
		t.Fatal("missing session cookie after cross-origin login")
	}
}

func TestAdminKnockHidesLoginUntilConfiguredHit(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "knock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h, err := New(st, &fakeClient{}, &fakeReconciler{}, "gateway-admin", "a-long-admin-password", time.Hour, 24*time.Hour, KnockConfig{
		Secret: "a-very-long-random-knock-secret",
		Hits:   3,
		Window: 30 * time.Second,
		TTL:    5 * time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	w := request(t, h, http.MethodGet, "https://access.example.com/admin", nil)
	if w.Code != http.StatusNotFound || w.Body.Len() != 0 {
		t.Fatalf("hidden admin status=%d body=%q", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodGet, "https://access.example.com/admin/knock/wrong", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("wrong knock status=%d", w.Code)
	}
	var progress *http.Cookie
	for hit := 1; hit <= 3; hit++ {
		if progress == nil {
			w = request(t, h, http.MethodGet, "https://access.example.com/admin/knock/a-very-long-random-knock-secret", nil)
		} else {
			w = request(t, h, http.MethodGet, "https://access.example.com/admin/knock/a-very-long-random-knock-secret", nil, progress)
		}
		if hit < 3 {
			if w.Code != http.StatusNotFound || w.Body.Len() != 0 {
				t.Fatalf("hit %d status=%d body=%q", hit, w.Code, w.Body.String())
			}
			for _, c := range w.Result().Cookies() {
				if c.Name == knockCookie {
					progress = c
				}
			}
		} else if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/login" {
			t.Fatalf("final hit status=%d location=%q", w.Code, w.Header().Get("Location"))
		}
	}
	var gate *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == gateCookie && c.Value != "" {
			gate = c
		}
	}
	if gate == nil {
		t.Fatal("missing short-lived gate cookie")
	}
	w = request(t, h, http.MethodGet, "https://access.example.com/admin/login", nil, gate)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "gateway 管理后台") {
		t.Fatalf("gated login status=%d body=%s", w.Code, w.Body.String())
	}
	var nonce *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == loginCookie {
			nonce = c
		}
	}
	if nonce == nil {
		t.Fatal("missing login nonce after knock")
	}
	form := url.Values{"username": {"gateway-admin"}, "password": {"a-long-admin-password"}, "login_nonce": {nonce.Value}}
	w = request(t, h, http.MethodPost, "https://access.example.com/admin/login", form, gate, nonce)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("gated login submit status=%d body=%s", w.Code, w.Body.String())
	}
	var session *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("missing session after gated login")
	}
	w = request(t, h, http.MethodGet, "https://access.example.com/admin", nil, session)
	if w.Code != http.StatusOK {
		t.Fatalf("session admin status=%d", w.Code)
	}
	w = request(t, h, http.MethodGet, "https://access.example.com/admin/login", nil, gate)
	if w.Code != http.StatusNotFound {
		t.Fatalf("consumed gate still works: status=%d", w.Code)
	}
}
