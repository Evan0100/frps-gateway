package ingest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"frps-gateway/frps"
	"frps-gateway/store"
)

func TestPollerStoresRecordsWithoutDuplicates(t *testing.T) {
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/whitelist/accesslog" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		polls++
		// The second poll returns the same page plus one new record; the
		// store must end up with exactly four unique rows.
		records := []map[string]any{
			{"instance": "boot1", "seq": 1, "time": 1000, "ip": "192.0.2.1", "user": "alice", "source": "tcp/p1", "action": "allow", "reason": "whitelisted"},
			{"instance": "boot1", "seq": 2, "time": 1001, "ip": "198.51.100.7", "source": "http/example.com", "action": "deny", "reason": "not_in_whitelist"},
			{"instance": "boot1", "seq": 3, "time": 1002, "ip": "192.0.2.1", "source": "tcp/p1", "action": "deny", "reason": "outside_time_window"},
		}
		if polls >= 2 {
			records = append(records, map[string]any{
				"instance": "boot1", "seq": 4, "time": time.Now().Add(-time.Minute).Unix(),
				"ip": "203.0.113.9", "source": "tcp/p2", "action": "deny", "reason": "expired",
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "success", "data": records})
	}))
	defer srv.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client := frps.New(srv.URL, "admin", "secret")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	poller := New(client, st, time.Second, 30, logger)

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := poller.PollOnce(ctx); err != nil {
			t.Fatalf("poll %d: %v", i+1, err)
		}
	}
	if n, err := st.CountAccessRecords(ctx); err != nil || n != 4 {
		t.Fatalf("stored records = %d, %v, want 4", n, err)
	}

	// Pruning with retention removes the 1970-era fixture records and keeps
	// the recent one.
	poller.pruneOnce(ctx)
	if n, err := st.CountAccessRecords(ctx); err != nil || n != 1 {
		t.Fatalf("count after prune = %d, %v, want 1", n, err)
	}
}

func TestPollerSurvivesFrpsErrors(t *testing.T) {
	var failing bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		records := []map[string]any{
			{"instance": "boot1", "seq": 1, "time": 1000, "ip": "192.0.2.1", "source": "tcp/p1", "action": "allow", "reason": "whitelisted"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "success", "data": records})
	}))
	defer srv.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client := frps.New(srv.URL, "admin", "secret")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	poller := New(client, st, time.Second, 30, logger)

	ctx := context.Background()
	// A frps error must not store anything, and a later success must still
	// work — the poller never gives up because the ring keeps overwriting.
	failing = true
	if err := poller.PollOnce(ctx); err == nil {
		t.Fatal("expected error while frps is failing")
	}
	failing = false
	if err := poller.PollOnce(ctx); err != nil {
		t.Fatalf("poll after recovery: %v", err)
	}
	if n, err := st.CountAccessRecords(ctx); err != nil || n != 1 {
		t.Fatalf("stored records = %d, %v, want 1", n, err)
	}
}
