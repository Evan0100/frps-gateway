package frps

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientV2Envelope(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/whitelist", func(w http.ResponseWriter, r *http.Request) {
		assertBasicAuth(t, r)
		writeJSON(t, w, map[string]any{
			"code": 200,
			"msg":  "success",
			"data": []Entry{{IP: "1.2.3.4", ExpireAt: 2_000_000_000}},
		})
	})
	mux.HandleFunc("POST /api/v2/whitelist", func(w http.ResponseWriter, r *http.Request) {
		assertBasicAuth(t, r)
		writeJSON(t, w, map[string]any{
			"code": 200,
			"msg":  "success",
			"data": Entry{IP: "5.6.7.8", ExpireAt: 2_000_000_001},
		})
	})
	mux.HandleFunc("DELETE /api/v2/whitelist", func(w http.ResponseWriter, r *http.Request) {
		assertBasicAuth(t, r)
		writeJSON(t, w, map[string]any{"code": 200, "msg": "success", "data": nil})
	})

	server := httptest.NewServer(mux)
	defer server.Close()
	client := New(server.URL, "admin", "secret")

	entries, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 || entries[0].IP != "1.2.3.4" {
		t.Fatalf("List() = %+v, want one expected entry", entries)
	}

	entry, err := client.Add(context.Background(), "5.6.7.8", 2*time.Hour)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if entry.IP != "5.6.7.8" || entry.ExpireAt != 2_000_000_001 {
		t.Fatalf("Add() = %+v, want server entry", entry)
	}

	if err := client.Remove(context.Background(), "5.6.7.8"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
}

func TestClientRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"msg":"success","data":null}` + strings.Repeat(" ", 1<<20)))
	}))
	defer server.Close()
	err := New(server.URL, "admin", "secret").do(context.Background(), http.MethodGet, "/", nil, nil)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

func TestClientV2EnvelopeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"code": 404, "msg": "whitelist entry not found", "data": nil})
	}))
	defer server.Close()

	err := New(server.URL, "admin", "secret").Remove(context.Background(), "1.2.3.4")
	apiErr, ok := err.(*APIError)
	if !ok || !apiErr.NotFound() {
		t.Fatalf("Remove() error = %#v, want not-found APIError", err)
	}
}

func TestClientRejectsRawV1Shape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []Entry{{IP: "1.2.3.4", ExpireAt: 2_000_000_000}})
	}))
	defer server.Close()

	if _, err := New(server.URL, "admin", "secret").List(context.Background()); err == nil {
		t.Fatal("List() error = nil, want invalid v2 envelope error")
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer server.Close()
	_, err := New(server.URL, "admin", "secret").List(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound {
		t.Fatalf("err=%v, want redirect API error", err)
	}
	if requests != 1 {
		t.Fatalf("requests=%d, redirect was followed", requests)
	}
}

func assertBasicAuth(t *testing.T, r *http.Request) {
	t.Helper()
	user, password, ok := r.BasicAuth()
	if !ok || user != "admin" || password != "secret" {
		t.Fatalf("BasicAuth() = %q, %q, %v", user, password, ok)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
