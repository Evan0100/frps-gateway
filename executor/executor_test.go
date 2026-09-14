package executor

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frps-gateway/frps"
	"frps-gateway/interaction"
	"frps-gateway/store"
)

// mock frps whitelist API
func newMockServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/whitelist", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"msg":"success","data":[{"ip":"1.2.3.4","expireAt":2000000000}]}`))
	})
	mux.HandleFunc("POST /api/v2/whitelist", func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "admin" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"msg":"success","data":{"ip":"5.6.7.8","expireAt":2000000000}}`))
	})
	mux.HandleFunc("DELETE /api/v2/whitelist", func(w http.ResponseWriter, r *http.Request) {
		// pretend 9.9.9.9 is not whitelisted
		buf, _ := io.ReadAll(r.Body)
		if strings.Contains(string(buf), "9.9.9.9") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"msg":"success","data":null}`))
	})
	return httptest.NewServer(mux)
}

func TestReconcileRemovesStaleManagedIP(t *testing.T) {
	srv := newMockServer(t)
	defer srv.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	if err = st.AddGrant(context.Background(), store.Grant{OpenID: "u1", IP: "1.2.3.4", Source: "test", CreatedAt: now.Add(-2 * time.Hour), ExpireAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	exec := NewManaged(frps.New(srv.URL, "admin", "secret"), st, nil, time.Hour, 24*time.Hour, time.Minute, 3, slog.Default())
	if err = exec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	ips, err := st.ListManagedIPs(context.Background())
	if err != nil || len(ips) != 0 {
		t.Fatalf("managed IPs=%v err=%v", ips, err)
	}
}

func TestExecute(t *testing.T) {
	srv := newMockServer(t)
	defer srv.Close()

	client := frps.New(srv.URL, "admin", "secret")
	exec := New(client, 2*time.Hour, slog.Default())
	ctx := context.Background()

	cases := []struct {
		name     string
		input    string
		contains []string
	}{
		{"grant with explicit ttl", "申请授权 5.6.7.8 3d", []string{"授权成功：5.6.7.8", "3d"}},
		{"grant default ttl", "申请授权 5.6.7.8", []string{"授权成功：5.6.7.8", "2h"}},
		{"revoke ok", "撤销授权 1.2.3.4", []string{"授权已撤销：1.2.3.4"}},
		{"revoke missing", "撤销授权 9.9.9.9", []string{"9.9.9.9 不在白名单中"}},
		{"list", "我的授权", []string{"有效授权共 1 条", "1.2.3.4", "剩余"}},
		{"unknown", "你好", []string{"无法识别该操作", "请发送 /start"}},
		{"bad ip", "申请授权 999.1.1.1", []string{"不是合法的 IP 地址"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := exec.Execute(ctx, interaction.Request{
				Text:           tc.input,
				OperatorOpenID: "ou_test",
				ChatID:         "oc_test",
				MessageID:      "om_test",
			})
			for _, want := range tc.contains {
				if !strings.Contains(reply, want) {
					t.Errorf("Execute(%q) reply = %q, want it to contain %q", tc.input, reply, want)
				}
			}
		})
	}
}

func TestExecuteServerDown(t *testing.T) {
	client := frps.New("http://127.0.0.1:1", "admin", "secret")
	exec := New(client, time.Hour, slog.Default())
	reply := exec.Execute(context.Background(), interaction.Request{Text: "我的授权"})
	if !strings.Contains(reply, "查询失败") {
		t.Errorf("reply = %q, want it to contain 查询失败", reply)
	}
}

type fakeLinkIssuer struct {
	openID, name, messageID string
	ttl, validFor           time.Duration
	url                     string
	err                     error
}

func (f *fakeLinkIssuer) NewLink(_ context.Context, openID, name, messageID string, ttl, validFor time.Duration) (string, error) {
	f.openID, f.name, f.messageID, f.ttl, f.validFor = openID, name, messageID, ttl, validFor
	return f.url, f.err
}

func TestAuthorizeLinkPassesIdentityAndTTLs(t *testing.T) {
	links := &fakeLinkIssuer{url: "https://auth.example.com/authorize/tok"}
	exec := NewManaged(nil, nil, links, 4*time.Hour, 24*time.Hour, 5*time.Minute, 3, slog.Default())
	url, err := exec.AuthorizeLink(context.Background(), interaction.Request{OperatorOpenID: "ou_1", OperatorName: "Alice", MessageID: "m1"})
	if err != nil || url != links.url {
		t.Fatalf("url=%q err=%v", url, err)
	}
	if links.openID != "ou_1" || links.name != "Alice" || links.messageID != "m1" || links.ttl != 4*time.Hour || links.validFor != 5*time.Minute {
		t.Fatalf("unexpected link issuance: %+v", links)
	}
}

func TestAuthorizeLinkWithoutIssuerFails(t *testing.T) {
	exec := NewManaged(nil, nil, nil, time.Hour, 24*time.Hour, time.Minute, 3, slog.Default())
	if _, err := exec.AuthorizeLink(context.Background(), interaction.Request{OperatorOpenID: "ou_1"}); err == nil {
		t.Fatal("expected error when link issuer is not configured")
	}
}
