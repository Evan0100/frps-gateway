// frps-gateway manages frps access control through Feishu and HTTP APIs.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"frps-gateway/authorize"
	"frps-gateway/config"
	"frps-gateway/executor"
	"frps-gateway/feishu"
	"frps-gateway/frps"
	"frps-gateway/store"
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
	st, err := store.Open(cfg.Storage.SQLiteFile)
	if err != nil {
		logger.Error("open sqlite failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	auth, err := authorize.New(cfg.Server.ListenAddr, cfg.Server.PublicBaseURL, cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile, cfg.Server.TrustedProxyCIDRs, cfg.Bot.MaxActiveIPs, st, client, logger)
	if err != nil {
		logger.Error("configure authorization server", "err", err)
		os.Exit(1)
	}
	exec := executor.NewManaged(client, st, auth, cfg.Bot.TTL(), cfg.Bot.MaxTTLDuration(), cfg.Bot.LinkTTLDuration(), cfg.Bot.MaxActiveIPs, logger)
	bot := feishu.NewSecure(cfg.Feishu.AppID, cfg.Feishu.AppSecret, cfg.Feishu.EncryptKey, cfg.Feishu.VerificationToken, cfg.Bot.AdminChatID, cfg.Bot.AdminOpenIDs, cfg.Bot.AllowAllUsers, cfg.Bot.RequireMentionInGroup, cfg.Bot.Workers, cfg.Bot.QueueSize, cfg.Bot.RequestsPerMinute, exec, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := exec.Reconcile(ctx); err != nil {
		logger.Warn("initial whitelist reconciliation failed", "err", err)
	}
	go func() {
		if err := auth.Run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("authorization server exited", "err", err)
			stop()
		}
	}()
	logger.Info("frps-gateway starting", "frps", cfg.Frps.ApiAddr, "listen", cfg.Server.ListenAddr)
	if !cfg.Bot.IsEnabled() {
		logger.Warn("Feishu bot disabled; authorization server remains available for existing links")
		<-ctx.Done()
	} else if err := bot.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("bot exited", "err", err)
		os.Exit(1)
	}
	logger.Info("frps-gateway stopped")
}
