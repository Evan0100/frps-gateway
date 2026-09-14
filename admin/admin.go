// Package admin provides the gateway-owned management console. It manages
// durable grants and audit records; frps remains the enforcement layer.
package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"frps-gateway/duration"
	"frps-gateway/frps"
	"frps-gateway/store"
)

const (
	sessionCookie = "__Host-frps_gateway_admin"
	loginCookie   = "__Host-frps_gateway_login"
	knockCookie   = "__Host-frps_gateway_knock"
	gateCookie    = "__Host-frps_gateway_gate"
	pageSize      = 50
	maxKnockState = 1024
)

type Reconciler interface {
	Reconcile(context.Context) error
}

type WhitelistLister interface {
	List(context.Context) ([]frps.Entry, error)
}

type session struct {
	Actor, CSRF string
	ExpiresAt   time.Time
}

type KnockConfig struct {
	Secret string
	Hits   int
	Window time.Duration
	TTL    time.Duration
}

type knockProgress struct {
	Hits      int
	ExpiresAt time.Time
}

type Handler struct {
	store       *store.Store
	client      WhitelistLister
	reconciler  Reconciler
	logger      *slog.Logger
	user        string
	password    string
	origin      string
	sessionTTL  time.Duration
	maxGrantTTL time.Duration
	knock       KnockConfig
	mux         *http.ServeMux
	mutate      chan struct{}

	mu       sync.Mutex
	sessions map[string]session
	failures map[string][]time.Time
	progress map[string]knockProgress
	gates    map[string]time.Time
}

//go:embed page.html
var pageHTML string

var page = template.Must(template.New("admin").Funcs(template.FuncMap{
	"dec": func(v int) int {
		if v <= 1 {
			return 1
		}
		return v - 1
	},
	"inc":      func(v int) int { return v + 1 },
	"unixTime": func(v int64) time.Time { return time.Unix(v, 0) },
	"fmtTime": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Local().Format("2006-01-02 15:04:05")
	},
	"grantStatus": func(g store.GrantRecord) string {
		if g.RevokedAt != nil {
			return "已撤销"
		}
		if !g.ExpireAt.After(time.Now()) {
			return "已过期"
		}
		return "有效"
	},
	"sourceName": func(v string) string {
		switch v {
		case "admin_web":
			return "后台管理员"
		case "feishu_command":
			return "飞书指令"
		case "feishu_link":
			return "一次性链接"
		default:
			return v
		}
	},
	"actionName": func(v string) string {
		switch v {
		case "add":
			return "新增"
		case "extend":
			return "延期"
		case "revoke":
			return "撤销"
		default:
			return v
		}
	},
	"resultName": func(v string) string {
		switch v {
		case "synced":
			return "已同步"
		case "sync_failed":
			return "待重试"
		case "recorded":
			return "已记录"
		default:
			return v
		}
	},
	"accessName": func(v string) string {
		if v == "allow" {
			return "放行"
		}
		if v == "deny" {
			return "拒绝"
		}
		return v
	},
}).Parse(pageHTML))

func New(st *store.Store, client WhitelistLister, reconciler Reconciler, user, password, publicBaseURL string, sessionTTL, maxGrantTTL time.Duration, knock KnockConfig, logger *slog.Logger) (*Handler, error) {
	u, err := url.Parse(publicBaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("admin public URL must use https")
	}
	if st == nil || client == nil || reconciler == nil {
		return nil, errors.New("admin dependencies are required")
	}
	if knock.Secret != "" && (len(knock.Secret) < 24 || !validPathToken(knock.Secret) || knock.Hits < 2 || knock.Window <= 0 || knock.TTL <= 0) {
		return nil, errors.New("invalid admin knock configuration")
	}
	h := &Handler{
		store: st, client: client, reconciler: reconciler, logger: logger,
		user: user, password: password, origin: u.Scheme + "://" + u.Host,
		sessionTTL: sessionTTL, maxGrantTTL: maxGrantTTL, knock: knock,
		mux: http.NewServeMux(), sessions: map[string]session{}, failures: map[string][]time.Time{}, progress: map[string]knockProgress{}, gates: map[string]time.Time{},
		mutate: make(chan struct{}, 1),
	}
	h.mux.HandleFunc("GET /admin/login", h.loginPage)
	h.mux.HandleFunc("GET /admin/knock/{secret}", h.knockDoor)
	h.mux.HandleFunc("POST /admin/login", h.login)
	h.mux.HandleFunc("POST /admin/logout", h.logout)
	h.mux.HandleFunc("GET /admin", h.dashboard)
	h.mux.HandleFunc("POST /admin/grants", h.addGrant)
	h.mux.HandleFunc("POST /admin/grants/{id}/extend", h.extendGrant)
	h.mux.HandleFunc("POST /admin/grants/{id}/revoke", h.revokeGrant)
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.knock.Secret != "" && !strings.HasPrefix(r.URL.Path, "/admin/knock/") {
		if _, ok := h.currentSession(r); !ok && !h.hasGate(r) {
			emptyNotFound(w)
			return
		}
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) knockDoor(w http.ResponseWriter, r *http.Request) {
	if h.knock.Secret == "" || !constantEqual(r.PathValue("secret"), h.knock.Secret) {
		emptyNotFound(w)
		return
	}
	now := time.Now()
	var token string
	if cookie, err := r.Cookie(knockCookie); err == nil {
		token = cookie.Value
	}
	h.mu.Lock()
	h.pruneKnockLocked(now)
	p, ok := h.progress[tokenKey(token)]
	if token == "" || !ok || !p.ExpiresAt.After(now) {
		if len(h.progress) >= maxKnockState {
			h.mu.Unlock()
			emptyNotFound(w)
			return
		}
		var err error
		token, err = randomToken(24)
		if err != nil {
			h.mu.Unlock()
			h.internalError(w, "create knock progress", err)
			return
		}
		p = knockProgress{ExpiresAt: now.Add(h.knock.Window)}
	}
	p.Hits++
	p.ExpiresAt = now.Add(h.knock.Window)
	if p.Hits < h.knock.Hits {
		h.progress[tokenKey(token)] = p
		h.mu.Unlock()
		h.setCookie(w, knockCookie, token, h.knock.Window, true)
		emptyNotFound(w)
		return
	}
	delete(h.progress, tokenKey(token))
	h.mu.Unlock()
	h.clearCookie(w, knockCookie)
	if err := h.issueGate(w); err != nil {
		h.internalError(w, "create admin gate", err)
		return
	}
	h.logger.Info("admin knock completed", "remote", remoteHost(r.RemoteAddr))
	h.redirect(w, r, "/admin/login")
}

func (h *Handler) issueGate(w http.ResponseWriter) error {
	token, err := randomToken(24)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.pruneKnockLocked(time.Now())
	if len(h.gates) >= maxKnockState {
		h.mu.Unlock()
		return errors.New("admin gate capacity reached")
	}
	h.gates[tokenKey(token)] = time.Now().Add(h.knock.TTL)
	h.mu.Unlock()
	h.setCookie(w, gateCookie, token, h.knock.TTL, true)
	return nil
}

func (h *Handler) hasGate(r *http.Request) bool {
	cookie, err := r.Cookie(gateCookie)
	if err != nil {
		return false
	}
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneKnockLocked(now)
	expires, ok := h.gates[tokenKey(cookie.Value)]
	return ok && expires.After(now)
}

func (h *Handler) consumeGate(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(gateCookie); err == nil {
		h.mu.Lock()
		delete(h.gates, tokenKey(cookie.Value))
		h.mu.Unlock()
	}
	h.clearCookie(w, gateCookie)
}

func (h *Handler) pruneKnockLocked(now time.Time) {
	for key, p := range h.progress {
		if !p.ExpiresAt.After(now) {
			delete(h.progress, key)
		}
	}
	for key, expires := range h.gates {
		if !expires.After(now) {
			delete(h.gates, key)
		}
	}
}

func emptyNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusNotFound)
}

type loginData struct {
	Login bool
	Nonce string
	Error string
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.currentSession(r); ok {
		h.redirect(w, r, "/admin")
		return
	}
	nonce, err := randomToken(24)
	if err != nil {
		h.internalError(w, "create login nonce", err)
		return
	}
	h.setCookie(w, loginCookie, nonce, 10*time.Minute, true)
	h.render(w, loginData{Login: true, Nonce: nonce})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !h.parseForm(w, r) {
		return
	}
	cookie, err := r.Cookie(loginCookie)
	if err != nil || !constantEqual(cookie.Value, r.PostFormValue("login_nonce")) {
		http.Error(w, "登录页面已过期，请重新打开", http.StatusForbidden)
		return
	}
	username := strings.TrimSpace(r.PostFormValue("username"))
	key := remoteHost(r.RemoteAddr) + "\x00" + username
	if retry, blocked := h.loginBlocked(key); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		http.Error(w, "登录失败次数过多，请稍后重试", http.StatusTooManyRequests)
		return
	}
	validUser := constantEqual(username, h.user)
	validPassword := constantEqual(r.PostFormValue("password"), h.password)
	if !validUser || !validPassword {
		h.recordFailure(key)
		h.logger.Warn("admin login failed", "remote", remoteHost(r.RemoteAddr), "user", username)
		h.renderStatus(w, http.StatusUnauthorized, loginData{Login: true, Nonce: cookie.Value, Error: "用户名或密码错误"})
		return
	}
	h.clearFailures(key)
	token, err := randomToken(32)
	if err != nil {
		h.internalError(w, "create admin session", err)
		return
	}
	csrf, err := randomToken(24)
	if err != nil {
		h.internalError(w, "create csrf token", err)
		return
	}
	h.mu.Lock()
	h.pruneSessionsLocked(time.Now())
	h.sessions[tokenKey(token)] = session{Actor: username, CSRF: csrf, ExpiresAt: time.Now().Add(h.sessionTTL)}
	h.mu.Unlock()
	h.setCookie(w, sessionCookie, token, h.sessionTTL, true)
	h.clearCookie(w, loginCookie)
	if h.knock.Secret != "" {
		h.consumeGate(w, r)
	}
	h.logger.Info("admin login succeeded", "remote", remoteHost(r.RemoteAddr), "user", username)
	h.redirect(w, r, "/admin")
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireMutation(w, r)
	if !ok {
		return
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		h.mu.Lock()
		delete(h.sessions, tokenKey(cookie.Value))
		h.mu.Unlock()
	}
	h.clearCookie(w, sessionCookie)
	h.logger.Info("admin logged out", "user", s.Actor)
	h.redirect(w, r, "/admin/login")
}

type CurrentRow struct {
	IP, Origin, State, Subjects string
	ExpireAt                    time.Time
	GrantCount                  int
}

type dashboardData struct {
	Login, ErrorLogin                   bool
	Nonce, Error                        string
	Actor, CSRF, Notice, Warning        string
	Current                             []CurrentRow
	Grants                              []store.GrantRecord
	Access                              []store.AccessRecord
	Audits                              []store.AdminAudit
	GrantTotal, AccessTotal, AuditTotal int64
	GrantPage, AccessPage, AuditPage    int
	GrantPrev, GrantNext                bool
	AccessPrev, AccessNext              bool
	AuditPrev, AuditNext                bool
	GrantQuery, AccessQuery, AuditQuery string
	FilterIP, FilterUser, FilterStatus  string
	AccessAction, AuditActor            string
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	grantPage := pageNumber(r.URL.Query().Get("grantPage"))
	accessPage := pageNumber(r.URL.Query().Get("accessPage"))
	auditPage := pageNumber(r.URL.Query().Get("auditPage"))
	ip, user := cleanFilter(r.URL.Query().Get("ip")), cleanFilter(r.URL.Query().Get("user"))
	status, action := r.URL.Query().Get("status"), r.URL.Query().Get("action")
	from, to := parseDateRange(r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	grants, grantTotal, err := h.store.ListGrantRecords(ctx, store.GrantFilter{IP: ip, User: user, Status: status, From: from, To: to, Limit: pageSize, Offset: (grantPage - 1) * pageSize})
	if err != nil {
		h.internalError(w, "list grants", err)
		return
	}
	access, accessTotal, err := h.store.ListAccessRecords(ctx, store.AccessFilter{IP: ip, User: user, Action: action, From: from, To: to, Limit: pageSize, Offset: (accessPage - 1) * pageSize})
	if err != nil {
		h.internalError(w, "list access records", err)
		return
	}
	audits, auditTotal, err := h.store.ListAdminAudits(ctx, store.AuditFilter{IP: ip, Actor: cleanFilter(r.URL.Query().Get("actor")), From: from, To: to, Limit: pageSize, Offset: (auditPage - 1) * pageSize})
	if err != nil {
		h.internalError(w, "list admin audit", err)
		return
	}
	current, warning := h.currentRows(ctx)
	data := dashboardData{
		Actor: s.Actor, CSRF: s.CSRF, Notice: r.URL.Query().Get("notice"), Warning: warning,
		Current: current, Grants: grants, Access: access, Audits: audits,
		GrantTotal: grantTotal, AccessTotal: accessTotal, AuditTotal: auditTotal,
		GrantPage: grantPage, AccessPage: accessPage, AuditPage: auditPage,
		GrantPrev: grantPage > 1, GrantNext: int64(grantPage*pageSize) < grantTotal,
		AccessPrev: accessPage > 1, AccessNext: int64(accessPage*pageSize) < accessTotal,
		AuditPrev: auditPage > 1, AuditNext: int64(auditPage*pageSize) < auditTotal,
		FilterIP: ip, FilterUser: user, FilterStatus: status, AccessAction: action, AuditActor: r.URL.Query().Get("actor"),
	}
	data.GrantQuery = queryWithoutPage(r.URL.Query(), "grantPage")
	data.AccessQuery = queryWithoutPage(r.URL.Query(), "accessPage")
	data.AuditQuery = queryWithoutPage(r.URL.Query(), "auditPage")
	h.render(w, data)
}

func (h *Handler) currentRows(ctx context.Context) ([]CurrentRow, string) {
	entries, err := h.client.List(ctx)
	if err != nil {
		return nil, "无法读取 frps 当前白名单：" + err.Error()
	}
	active, err := h.store.ListActive(ctx)
	if err != nil {
		return nil, "无法读取 gateway 有效授权：" + err.Error()
	}
	managed, err := h.store.ListManagedIPs(ctx)
	if err != nil {
		return nil, "无法读取 gateway 管理范围：" + err.Error()
	}
	managedSet := map[string]bool{}
	for _, ip := range managed {
		managedSet[ip] = true
	}
	byIP := map[string][]store.Grant{}
	for _, g := range active {
		byIP[g.IP] = append(byIP[g.IP], g)
	}
	seen := map[string]bool{}
	rows := make([]CurrentRow, 0, len(entries)+len(byIP))
	for _, e := range entries {
		seen[e.IP] = true
		gs := byIP[e.IP]
		row := CurrentRow{IP: e.IP, ExpireAt: time.Unix(e.ExpireAt, 0), GrantCount: len(gs), Subjects: subjectNames(gs)}
		switch {
		case len(gs) > 0:
			row.Origin, row.State = "gateway 授权", "已同步"
		case managedSet[e.IP]:
			row.Origin, row.State = "gateway 管理", "待清理"
		default:
			row.Origin, row.State = "frps 应急/手工", "只读保留"
		}
		rows = append(rows, row)
	}
	for ip, gs := range byIP {
		if seen[ip] {
			continue
		}
		expiry := time.Time{}
		for _, g := range gs {
			if g.ExpireAt.After(expiry) {
				expiry = g.ExpireAt
			}
		}
		rows = append(rows, CurrentRow{IP: ip, Origin: "gateway 授权", State: "待同步", ExpireAt: expiry, GrantCount: len(gs), Subjects: subjectNames(gs)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].IP < rows[j].IP })
	return rows, ""
}

func subjectNames(gs []store.Grant) string {
	seen := map[string]bool{}
	var out []string
	for _, g := range gs {
		name := g.OperatorName
		if name == "" {
			name = g.OpenID
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "、")
}

func (h *Handler) addGrant(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireMutation(w, r)
	if !ok {
		return
	}
	ip, err := normalizedIP(r.PostFormValue("ip"))
	if err != nil {
		http.Error(w, "IP 地址不合法", http.StatusBadRequest)
		return
	}
	subjectID, subjectName := strings.TrimSpace(r.PostFormValue("subject_id")), strings.TrimSpace(r.PostFormValue("subject_name"))
	if !validLabel(subjectID, 128) || !validLabel(subjectName, 128) {
		http.Error(w, "授权对象不能为空，且不能包含控制字符", http.StatusBadRequest)
		return
	}
	ttl, err := h.parseTTL(r.PostFormValue("ttl"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.withMutation(w, r, func() (string, error) {
		now := time.Now()
		_, auditID, err := h.store.CreateAdminGrant(r.Context(), s.Actor, store.Grant{OpenID: subjectID, OperatorName: subjectName, IP: ip, Source: "admin_web", CreatedAt: now, ExpireAt: now.Add(ttl)})
		if err != nil {
			return "", err
		}
		return h.reconcileAudit(r.Context(), auditID, "授权已记录并同步")
	})
}

func (h *Handler) extendGrant(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireMutation(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "授权记录不存在", http.StatusNotFound)
		return
	}
	ttl, err := h.parseTTL(r.PostFormValue("ttl"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.withMutation(w, r, func() (string, error) {
		_, auditID, err := h.store.ExtendAdminGrant(r.Context(), s.Actor, id, time.Now().Add(ttl))
		if err != nil {
			return "", err
		}
		return h.reconcileAudit(r.Context(), auditID, "授权已延期并同步")
	})
}

func (h *Handler) revokeGrant(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireMutation(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "授权记录不存在", http.StatusNotFound)
		return
	}
	h.withMutation(w, r, func() (string, error) {
		_, auditID, err := h.store.RevokeAdminGrant(r.Context(), s.Actor, id)
		if err != nil {
			return "", err
		}
		return h.reconcileAudit(r.Context(), auditID, "授权已撤销并同步")
	})
}

func (h *Handler) withMutation(w http.ResponseWriter, r *http.Request, fn func() (string, error)) {
	select {
	case h.mutate <- struct{}{}:
		defer func() { <-h.mutate }()
	case <-r.Context().Done():
		http.Error(w, "request cancelled", http.StatusRequestTimeout)
		return
	}
	notice, err := fn()
	if errors.Is(err, store.ErrGrantNotFound) {
		http.Error(w, "授权记录不存在", http.StatusNotFound)
		return
	}
	if errors.Is(err, store.ErrGrantInactive) {
		http.Error(w, "授权已经过期或撤销", http.StatusConflict)
		return
	}
	if err != nil {
		h.internalError(w, "admin mutation", err)
		return
	}
	h.redirect(w, r, "/admin?notice="+url.QueryEscape(notice))
}

func (h *Handler) reconcileAudit(parent context.Context, auditID int64, success string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	if err := h.reconciler.Reconcile(ctx); err != nil {
		auditCtx, auditCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = h.store.SetAdminAuditResult(auditCtx, auditID, "sync_failed", err.Error())
		auditCancel()
		h.logger.Warn("admin grant recorded but reconciliation failed", "auditID", auditID, "err", err)
		return "授权变更已记录，frps 同步失败；后台会自动重试", nil
	}
	auditCtx, auditCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer auditCancel()
	if err := h.store.SetAdminAuditResult(auditCtx, auditID, "synced", ""); err != nil {
		h.logger.Warn("update admin audit result", "auditID", auditID, "err", err)
	}
	return success, nil
}

func (h *Handler) parseTTL(raw string) (time.Duration, error) {
	ttl, err := duration.Parse(strings.TrimSpace(raw))
	if err != nil || ttl <= 0 {
		return 0, errors.New("有效期格式不合法，例如 4h 或 1d")
	}
	if ttl > h.maxGrantTTL {
		return 0, fmt.Errorf("有效期不能超过 %s", duration.Format(h.maxGrantTTL))
	}
	return ttl, nil
}

func (h *Handler) requireMutation(w http.ResponseWriter, r *http.Request) (session, bool) {
	s, ok := h.requireSession(w, r)
	if !ok {
		return session{}, false
	}
	if !h.sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return session{}, false
	}
	if !h.parseForm(w, r) {
		return session{}, false
	}
	if !constantEqual(s.CSRF, r.PostFormValue("csrf")) {
		http.Error(w, "CSRF 校验失败", http.StatusForbidden)
		return session{}, false
	}
	return s, true
}

func (h *Handler) requireSession(w http.ResponseWriter, r *http.Request) (session, bool) {
	s, ok := h.currentSession(r)
	if !ok {
		h.redirect(w, r, "/admin/login")
		return session{}, false
	}
	return s, true
}

func (h *Handler) currentSession(r *http.Request) (session, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return session{}, false
	}
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneSessionsLocked(now)
	s, ok := h.sessions[tokenKey(cookie.Value)]
	return s, ok && s.ExpiresAt.After(now)
}

func (h *Handler) pruneSessionsLocked(now time.Time) {
	for key, s := range h.sessions {
		if !s.ExpiresAt.After(now) {
			delete(h.sessions, key)
		}
	}
}

func (h *Handler) loginBlocked(key string) (time.Duration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cutoff := time.Now().Add(-10 * time.Minute)
	items := h.failures[key][:0]
	for _, t := range h.failures[key] {
		if t.After(cutoff) {
			items = append(items, t)
		}
	}
	h.failures[key] = items
	if len(items) < 5 {
		return 0, false
	}
	return time.Until(items[0].Add(10 * time.Minute)), true
}

func (h *Handler) recordFailure(key string) {
	h.mu.Lock()
	h.failures[key] = append(h.failures[key], time.Now())
	h.mu.Unlock()
}

func (h *Handler) clearFailures(key string) {
	h.mu.Lock()
	delete(h.failures, key)
	h.mu.Unlock()
}

func (h *Handler) sameOrigin(r *http.Request) bool {
	origin := strings.TrimRight(r.Header.Get("Origin"), "/")
	return origin == "" || origin == h.origin
}

func (h *Handler) parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return false
	}
	return true
}

func (h *Handler) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: int(ttl.Seconds()), Expires: time.Now().Add(ttl), Secure: true, HttpOnly: httpOnly, SameSite: http.SameSiteStrictMode})
}

func (h *Handler) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func (h *Handler) render(w http.ResponseWriter, data any) {
	h.renderStatus(w, http.StatusOK, data)
}

func (h *Handler) renderStatus(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := page.Execute(w, data); err != nil {
		h.logger.Error("render admin page", "err", err)
	}
}

func (h *Handler) internalError(w http.ResponseWriter, operation string, err error) {
	h.logger.Error(operation, "err", err)
	http.Error(w, "后台操作失败", http.StatusInternalServerError)
}

func (h *Handler) redirect(w http.ResponseWriter, r *http.Request, location string) {
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenKey(token string) string {
	h := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func constantEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func remoteHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return host
	}
	return addr
}

func normalizedIP(raw string) (string, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return "", errors.New("invalid IP")
	}
	return ip.Unmap().String(), nil
}

func validLabel(v string, max int) bool {
	if v == "" || len(v) > max {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validPathToken(value string) bool {
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return value != ""
}

func cleanFilter(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 128 {
		v = v[:128]
	}
	return v
}

func pageNumber(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 100000 {
		return 1
	}
	return n
}

func parseDateRange(fromRaw, toRaw string) (time.Time, time.Time) {
	from, _ := time.ParseInLocation("2006-01-02", fromRaw, time.Local)
	to, _ := time.ParseInLocation("2006-01-02", toRaw, time.Local)
	if !to.IsZero() {
		to = to.Add(24 * time.Hour)
	}
	return from, to
}

func queryWithoutPage(values url.Values, pageKey string) string {
	copyValues := url.Values{}
	for key, items := range values {
		if key == pageKey || key == "notice" {
			continue
		}
		copyValues[key] = append([]string(nil), items...)
	}
	encoded := copyValues.Encode()
	if encoded == "" {
		return ""
	}
	return "&" + encoded
}
