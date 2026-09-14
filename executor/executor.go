package executor

import (
	"context"
	"errors"
	"fmt"
	"frps-gateway/command"
	"frps-gateway/duration"
	"frps-gateway/frps"
	"frps-gateway/interaction"
	"frps-gateway/store"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type LinkIssuer interface {
	NewLink(context.Context, string, string, string, time.Duration, time.Duration) (string, error)
}
type Executor struct {
	client                      *frps.Client
	store                       *store.Store
	links                       LinkIssuer
	defaultTTL, maxTTL, linkTTL time.Duration
	logger                      *slog.Logger
	messageMu                   sync.Mutex
	reconcileMu                 sync.Mutex
	maxActiveIPs                int
}

func New(c *frps.Client, d time.Duration, l *slog.Logger) *Executor {
	return &Executor{client: c, defaultTTL: d, maxTTL: 72 * time.Hour, logger: l}
}
func NewManaged(c *frps.Client, s *store.Store, links LinkIssuer, d, max, link time.Duration, maxActiveIPs int, l *slog.Logger) *Executor {
	return &Executor{client: c, store: s, links: links, defaultTTL: d, maxTTL: max, linkTTL: link, maxActiveIPs: maxActiveIPs, logger: l}
}
func (e *Executor) Execute(ctx context.Context, r interaction.Request) string {
	e.messageMu.Lock()
	defer e.messageMu.Unlock()
	if e.store != nil {
		if v, ok, err := e.store.CachedReply(ctx, r.MessageID); err == nil && ok {
			return v
		}
	}
	c, err := command.Parse(r.Text)
	var out string
	if err != nil {
		out = err.Error() + "\n\n" + command.Usage
	} else {
		switch c.Action {
		case command.ActionAdd:
			out = e.add(ctx, r, c)
		case command.ActionRemove:
			out = e.remove(ctx, r, c)
		case command.ActionList:
			out = e.list(ctx, r)
		default:
			out = command.Usage
		}
	}
	if e.store != nil {
		if err := e.store.SaveReply(ctx, r.MessageID, out); err != nil {
			e.logger.Warn("save idempotent reply", "err", err)
		}
	}
	return out
}

// AuthorizeLink issues a one-time authorization URL for the requesting user;
// confirming it grants the configured default TTL.
func (e *Executor) AuthorizeLink(ctx context.Context, r interaction.Request) (string, error) {
	if e.links == nil {
		return "", errors.New("authorization links are not configured")
	}
	return e.links.NewLink(ctx, r.OperatorOpenID, r.OperatorName, r.MessageID, e.defaultTTL, e.linkTTL)
}
func (e *Executor) add(ctx context.Context, r interaction.Request, c *command.Command) string {
	ttl := c.TTL
	if ttl == 0 {
		ttl = e.defaultTTL
	}
	if ttl > e.maxTTL {
		return "有效期超过允许上限 " + duration.Format(e.maxTTL)
	}
	if e.store != nil {
		now := time.Now()
		if err := e.store.AddGrantLimited(ctx, store.Grant{OpenID: r.OperatorOpenID, OperatorName: r.OperatorName, IP: c.IP, Source: "feishu_command", CreatedAt: now, ExpireAt: now.Add(ttl)}, e.maxActiveIPs); errors.Is(err, store.ErrActiveIPLimit) {
			return fmt.Sprintf("你最多只能保留 %d 个有效 IP，请先删除一个旧 IP", e.maxActiveIPs)
		} else if err != nil {
			return "授权记录失败：" + err.Error()
		}
	}
	en, err := e.client.Add(ctx, c.IP, ttl)
	if err != nil {
		e.logger.Warn("sync whitelist failed", "ip", c.IP, "err", err)
		return "授权已记录，但同步 frps 失败，请联系管理员：" + err.Error()
	}
	return fmt.Sprintf("授权成功：%s，有效期 %s（至 %s）", en.IP, duration.Format(ttl), time.Unix(en.ExpireAt, 0).Format("01-02 15:04"))
}
func (e *Executor) remove(ctx context.Context, r interaction.Request, c *command.Command) string {
	if e.store == nil {
		err := e.client.Remove(ctx, c.IP)
		var ae *frps.APIError
		if errors.As(err, &ae) && ae.NotFound() {
			return c.IP + " 不在白名单中"
		}
		if err != nil {
			return "撤销失败：" + err.Error()
		}
		return "授权已撤销：" + c.IP
	}
	n, err := e.store.RevokeUserIP(ctx, r.OperatorOpenID, c.IP)
	if err != nil {
		return "撤销失败：" + err.Error()
	}
	if n == 0 {
		return c.IP + " 不在你的有效授权中"
	}
	expiry, ok, err := e.store.EffectiveExpiry(ctx, c.IP)
	if err != nil {
		return "撤销已记录，但对账失败：" + err.Error()
	}
	if ok {
		_, err = e.client.Add(ctx, c.IP, time.Until(expiry))
	} else {
		err = e.client.Remove(ctx, c.IP)
		var ae *frps.APIError
		if errors.As(err, &ae) && ae.NotFound() {
			err = nil
		}
	}
	if err != nil {
		return "撤销已记录，但同步 frps 失败，请联系管理员：" + err.Error()
	}
	if ok {
		return "已撤销你的授权；该 IP 仍有其他用户的有效授权"
	}
	return "授权已撤销：" + c.IP
}
func (e *Executor) list(ctx context.Context, r interaction.Request) string {
	if e.store == nil {
		es, err := e.client.List(ctx)
		if err != nil {
			return "查询失败：" + err.Error()
		}
		if len(es) == 0 {
			return "当前没有有效授权"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "有效授权共 %d 条：", len(es))
		for _, en := range es {
			fmt.Fprintf(&b, "\n%s  剩余至 %s", en.IP, time.Unix(en.ExpireAt, 0).Format("01-02 15:04"))
		}
		return b.String()
	}
	gs, err := e.store.ListUser(ctx, r.OperatorOpenID)
	if err != nil {
		return "查询失败：" + err.Error()
	}
	if len(gs) == 0 {
		return "你当前没有有效授权"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "你的有效授权共 %d 条：", len(gs))
	for _, g := range gs {
		fmt.Fprintf(&b, "\n%s  至 %s", g.IP, g.ExpireAt.Format("01-02 15:04"))
	}
	return b.String()
}
func (e *Executor) Reconcile(ctx context.Context) error {
	e.reconcileMu.Lock()
	defer e.reconcileMu.Unlock()
	if e.store == nil {
		return nil
	}
	gs, err := e.store.ListActive(ctx)
	if err != nil {
		return err
	}
	m := map[string]time.Time{}
	for _, g := range gs {
		if g.ExpireAt.After(m[g.IP]) {
			m[g.IP] = g.ExpireAt
		}
	}
	for ip, x := range m {
		if _, err = e.client.Add(ctx, ip, time.Until(x)); err != nil {
			return fmt.Errorf("reconcile %s: %w", ip, err)
		}
	}
	managed, err := e.store.ListManagedIPs(ctx)
	if err != nil {
		return fmt.Errorf("list managed IPs: %w", err)
	}
	for _, ip := range managed {
		if _, active := m[ip]; active {
			continue
		}
		err = e.client.Remove(ctx, ip)
		var apiErr *frps.APIError
		if errors.As(err, &apiErr) && apiErr.NotFound() {
			err = nil
		}
		if err != nil {
			return fmt.Errorf("remove stale managed IP %s: %w", ip, err)
		}
		if err = e.store.ForgetManagedIP(ctx, ip); err != nil {
			return fmt.Errorf("forget managed IP %s: %w", ip, err)
		}
	}
	return nil
}

// Ready verifies the durable store and the frps management API used by all
// authorization paths.
func (e *Executor) Ready(ctx context.Context) error {
	if e.store != nil {
		if err := e.store.Ping(ctx); err != nil {
			return fmt.Errorf("sqlite: %w", err)
		}
	}
	if _, err := e.client.List(ctx); err != nil {
		return fmt.Errorf("frps API: %w", err)
	}
	return nil
}
