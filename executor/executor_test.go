package executor

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frps-gateway/frps"
	"frps-gateway/interaction"
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
		{"add with explicit ttl", "加白 5.6.7.8 3d", []string{"已加白 5.6.7.8", "3d"}},
		{"add default ttl", "加白 5.6.7.8", []string{"已加白 5.6.7.8", "2h"}},
		{"remove ok", "删白 1.2.3.4", []string{"已移除 1.2.3.4"}},
		{"remove missing", "删白 9.9.9.9", []string{"9.9.9.9 不在白名单中"}},
		{"list", "白名单", []string{"白名单共 1 条", "1.2.3.4", "剩余"}},
		{"help", "帮助", []string{"可用指令"}},
		{"unknown", "你好", []string{"无法识别的指令", "可用指令"}},
		{"bad ip", "加白 999.1.1.1", []string{"不是合法的 IP 地址"}},
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
	reply := exec.Execute(context.Background(), interaction.Request{Text: "白名单"})
	if !strings.Contains(reply, "查询失败") {
		t.Errorf("reply = %q, want it to contain 查询失败", reply)
	}
}
