// Package executor turns parsed chat commands into frps API calls
// and human-readable replies.
package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"frps-gateway/command"
	"frps-gateway/duration"
	"frps-gateway/frps"
	"frps-gateway/interaction"
)

type Executor struct {
	client     *frps.Client
	defaultTTL time.Duration
	logger     *slog.Logger
}

func New(client *frps.Client, defaultTTL time.Duration, logger *slog.Logger) *Executor {
	return &Executor{
		client:     client,
		defaultTTL: defaultTTL,
		logger:     logger,
	}
}

// Execute parses raw chat text, runs the command and returns the reply text.
// It never returns an error; failures are reported inside the reply so the
// admin always gets feedback in chat.
func (e *Executor) Execute(ctx context.Context, req interaction.Request) string {
	cmd, err := command.Parse(req.Text)
	if err != nil {
		return err.Error() + "\n\n" + command.Usage
	}
	switch cmd.Action {
	case command.ActionAdd:
		return e.add(ctx, cmd)
	case command.ActionRemove:
		return e.remove(ctx, cmd)
	case command.ActionList:
		return e.list(ctx)
	default:
		return command.Usage
	}
}

func (e *Executor) add(ctx context.Context, cmd *command.Command) string {
	ttl := cmd.TTL
	if ttl == 0 {
		ttl = e.defaultTTL
	}
	entry, err := e.client.Add(ctx, cmd.IP, ttl)
	if err != nil {
		e.logger.Warn("add whitelist entry failed", "ip", cmd.IP, "err", err)
		return "加白失败：" + err.Error()
	}
	return fmt.Sprintf("已加白 %s，有效期 %s（至 %s）",
		entry.IP, duration.Format(ttl), time.Unix(entry.ExpireAt, 0).Format("01-02 15:04"))
}

func (e *Executor) remove(ctx context.Context, cmd *command.Command) string {
	err := e.client.Remove(ctx, cmd.IP)
	var apiErr *frps.APIError
	if errors.As(err, &apiErr) && apiErr.NotFound() {
		return fmt.Sprintf("%s 不在白名单中", cmd.IP)
	}
	if err != nil {
		e.logger.Warn("remove whitelist entry failed", "ip", cmd.IP, "err", err)
		return "删白失败：" + err.Error()
	}
	return fmt.Sprintf("已移除 %s", cmd.IP)
}

func (e *Executor) list(ctx context.Context) string {
	entries, err := e.client.List(ctx)
	if err != nil {
		e.logger.Warn("list whitelist failed", "err", err)
		return "查询失败：" + err.Error()
	}
	if len(entries) == 0 {
		return "白名单为空"
	}
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "白名单共 %d 条：", len(entries))
	for _, en := range entries {
		expire := time.Unix(en.ExpireAt, 0)
		left := expire.Sub(now)
		if left < 0 {
			left = 0
		}
		fmt.Fprintf(&b, "\n%s  剩余 %s（至 %s）", en.IP, duration.Format(left), expire.Format("01-02 15:04"))
	}
	return b.String()
}
