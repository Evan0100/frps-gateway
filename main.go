// frps-gateway manages frps access control through Feishu and HTTP APIs.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"frps-gateway/config"
	"frps-gateway/executor"
	"frps-gateway/feishu"
	"frps-gateway/frps"
)

func main() {
	configPath := flag.String("c", "bot.toml", "config file path")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load config failed", "err", err)
		os.Exit(1)
	}

	client := frps.New(cfg.Frps.ApiAddr, cfg.Frps.User, cfg.Frps.Password)
	exec := executor.New(client, cfg.Bot.TTL(), logger)
	bot := feishu.New(cfg.Feishu.AppID, cfg.Feishu.AppSecret,
		cfg.Bot.AdminChatID, cfg.Bot.AdminOpenIDs, exec, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("frps-gateway starting", "frps", cfg.Frps.ApiAddr)
	if err := bot.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("bot exited", "err", err)
		os.Exit(1)
	}
	logger.Info("frps-gateway stopped")
}
