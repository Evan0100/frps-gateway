package authorize

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"frps-gateway/frps"
	"frps-gateway/store"
)

type Server struct {
	addr, publicURL, cert, key string
	trusted                    []netip.Prefix
	store                      *store.Store
	client                     *frps.Client
	logger                     *slog.Logger
	http                       *http.Server
	mux                        *http.ServeMux
	maxActiveIPs               int
	readiness                  func(context.Context) error
}

func New(addr, publicURL, cert, key string, trusted []string, maxActiveIPs int, st *store.Store, client *frps.Client, logger *slog.Logger) (*Server, error) {
	s := &Server{addr: addr, publicURL: strings.TrimRight(publicURL, "/"), cert: cert, key: key, store: st, client: client, logger: logger, maxActiveIPs: maxActiveIPs}
	for _, raw := range trusted {
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", raw, err)
		}
		if p.Bits() == 0 {
			return nil, fmt.Errorf("trusted proxy CIDR %q trusts the entire internet", raw)
		}
		s.trusted = append(s.trusted, p)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /authorize/{token}", s.get)
	mux.HandleFunc("POST /authorize/{token}", s.post)
	s.mux = mux
	s.http = &http.Server{Addr: addr, Handler: securityHeaders(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	return s, nil
}

func (s *Server) SetReadiness(check func(context.Context) error) { s.readiness = check }

// SetAdminHandler mounts the gateway-owned management console. It must be
// called before Run.
func (s *Server) SetAdminHandler(handler http.Handler) {
	if handler == nil {
		return
	}
	s.mux.Handle("/admin", handler)
	s.mux.Handle("/admin/", handler)
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.readiness == nil {
		http.Error(w, "readiness check unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.readiness(ctx); err != nil {
		s.logger.Warn("readiness check failed", "err", err)
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ready"))
}

// NewLink issues a one-time authorization URL bound to one chat message or
// card event. Redelivered events reuse the same messageID; since only the
// token hash is stored the original URL cannot be replayed, so a UNIQUE
// conflict on messageID issues a fresh token under a derived ID.
func (s *Server) NewLink(ctx context.Context, openID, name, messageID string, ttl, validFor time.Duration) (string, error) {
	for attempt := 0; ; attempt++ {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		id := messageID
		if attempt > 0 {
			id = fmt.Sprintf("%s#%d", messageID, attempt)
		}
		err := s.store.CreateToken(ctx, b, openID, name, id, ttl, validFor)
		if err == nil {
			return s.publicURL + "/authorize/" + base64.RawURLEncoding.EncodeToString(b), nil
		}
		if attempt >= 3 || !store.IsUniqueViolation(err) {
			return "", err
		}
	}
}

func (s *Server) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.http.Shutdown(c)
	}()
	if s.cert != "" {
		return s.http.ListenAndServeTLS(s.cert, s.key)
	}
	return s.http.ListenAndServe()
}

var page = template.Must(template.New("auth").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>frps 访问授权</title><style>body{font:16px system-ui;max-width:520px;margin:10vh auto;padding:24px;color:#17202a}main{border:1px solid #ddd;border-radius:12px;padding:24px}button{padding:12px 20px;background:#1769e0;color:white;border:0;border-radius:8px;font-size:16px}</style><main><h1>确认访问授权</h1><p>检测到公网 IP：<strong>{{.IP}}</strong></p><p id="v4line" hidden>IPv4 出口：<strong id="v4ip"></strong>（确认时将一并授权）</p><p>此链接只能使用一次，请勿转发。</p><form method="post"><input type="hidden" name="ipv4" id="v4field"><button type="submit">确认加入白名单</button></form></main><script>
(function(){
// IPv4-only probe hosts: they publish A records only, so dual-stack devices
// must reach them over IPv4, revealing the address family business ports use.
var probes=["https://api4.ipify.org/?format=text","https://ipv4.icanhazip.com/"];
function probe(i){
  if(i>=probes.length)return;
  fetch(probes[i]).then(function(r){return r.text()}).then(function(t){
    var ip=t.trim();
    if(/^(25[0-5]|2[0-4]\d|1?\d?\d)(\.(25[0-5]|2[0-4]\d|1?\d?\d)){3}$/.test(ip)){
      document.getElementById("v4ip").textContent=ip;
      document.getElementById("v4line").hidden=false;
      document.getElementById("v4field").value=ip;
    }else{probe(i+1)}
  }).catch(function(){probe(i+1)});
}
probe(0);
})();
</script></html>`))

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	raw, err := base64.RawURLEncoding.DecodeString(r.PathValue("token"))
	if err != nil || s.store.ValidateToken(r.Context(), raw) != nil {
		http.Error(w, "授权链接已失效或已使用", http.StatusGone)
		return
	}
	ip, err := s.clientIP(r)
	if err != nil {
		http.Error(w, "无法识别公网 IP", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = page.Execute(w, map[string]string{"IP": ip})
}
func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	ip, err := s.clientIP(r)
	if err != nil {
		http.Error(w, "无法识别公网 IP", 400)
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(r.PathValue("token"))
	if err != nil {
		http.Error(w, "授权链接无效", 410)
		return
	}
	// The browser reports its IPv4 egress from an IPv4-only probe host. A
	// client-claimed address is acceptable here: the manual Feishu command
	// already lets users whitelist arbitrary IPs, while this path still
	// requires a valid one-time token bound to the requester.
	ips := []string{ip}
	if v4 := s.reportedIPv4(r); v4 != "" && v4 != ip {
		ips = append(ips, v4)
	}
	grants, err := s.store.ConsumeTokenIPs(r.Context(), raw, ips, s.maxActiveIPs)
	if errors.Is(err, store.ErrTokenInvalid) {
		http.Error(w, "授权链接已失效或已使用", 410)
		return
	}
	if errors.Is(err, store.ErrActiveIPLimit) {
		http.Error(w, fmt.Sprintf("你最多只能保留 %d 个有效 IP，请先在飞书中删除一个旧 IP", s.maxActiveIPs), http.StatusConflict)
		return
	}
	if err != nil {
		s.logger.Error("consume authorization token", "err", err)
		http.Error(w, "授权失败", 500)
		return
	}
	// Label the frps entry with the Feishu user behind the token so the
	// whitelist page shows who the entry belongs to.
	operator := grants[0].OperatorName
	if operator == "" {
		operator = grants[0].OpenID
	}
	for _, g := range grants {
		if _, err = s.client.Add(r.Context(), g.IP, time.Until(g.ExpireAt), operator); err != nil {
			s.logger.Error("apply whitelist grant", "ip", g.IP, "err", err)
			http.Error(w, "授权已记录，但同步 frps 失败，请联系管理员", 502)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=utf-8><h1>授权成功</h1>")
	for _, g := range grants {
		fmt.Fprintf(&b, "<p>%s 已加入白名单，有效期至 %s。</p>", g.IP, g.ExpireAt.Format("2006-01-02 15:04"))
	}
	_, _ = w.Write([]byte(b.String()))
}

// reportedIPv4 extracts and validates the browser-reported IPv4 egress
// address; anything malformed, non-IPv4, or non-public is ignored.
func (s *Server) reportedIPv4(r *http.Request) string {
	a, err := netip.ParseAddr(strings.TrimSpace(r.FormValue("ipv4")))
	if err != nil || !a.Is4() || !grantablePublicIP(a) {
		return ""
	}
	return a.String()
}

func (s *Server) clientIP(r *http.Request) (string, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "", err
	}
	peer = peer.Unmap()
	if !s.isTrusted(peer) {
		if !grantablePublicIP(peer) {
			return "", errors.New("client address is not a public unicast IP")
		}
		return peer.String(), nil
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			continue
		}
		a = a.Unmap()
		if !s.isTrusted(a) {
			if !grantablePublicIP(a) {
				return "", errors.New("forwarded client address is not a public unicast IP")
			}
			return a.String(), nil
		}
	}
	return "", errors.New("trusted proxy request has no untrusted client address")
}
func (s *Server) isTrusted(a netip.Addr) bool {
	for _, p := range s.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func grantablePublicIP(a netip.Addr) bool {
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !a.IsMulticast()
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src https://api4.ipify.org https://ipv4.icanhazip.com; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
