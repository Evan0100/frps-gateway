// Package config loads and validates bot.toml.
package config

import (
	"fmt"
	"os"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"frps-gateway/duration"
)

type Config struct {
	Frps   Frps   `toml:"frps"`
	Feishu Feishu `toml:"feishu"`
	Bot    Bot    `toml:"bot"`
}

type Frps struct {
	ApiAddr  string `toml:"apiAddr"`
	User     string `toml:"user"`
	Password string `toml:"password"`
}

type Feishu struct {
	AppID     string `toml:"appID"`
	AppSecret string `toml:"appSecret"`
}

type Bot struct {
	AdminChatID  string   `toml:"adminChatID"`
	AdminOpenIDs []string `toml:"adminOpenIDs"`
	DefaultTTL   string   `toml:"defaultTTL"`

	defaultTTL time.Duration
}

// TTL returns the parsed defaultTTL; valid after Load.
func (b *Bot) TTL() time.Duration { return b.defaultTTL }

// Load reads path and validates the configuration.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Frps.ApiAddr == "" {
		return fmt.Errorf("config error: frps.apiAddr is required")
	}
	if c.Frps.User == "" || c.Frps.Password == "" {
		return fmt.Errorf("config error: frps.user and frps.password are required (dashboard basic auth)")
	}
	if c.Feishu.AppID == "" || c.Feishu.AppSecret == "" {
		return fmt.Errorf("config error: feishu.appID and feishu.appSecret are required")
	}
	hasChat := c.Bot.AdminChatID != ""
	hasOpenIDs := len(c.Bot.AdminOpenIDs) > 0
	if hasChat == hasOpenIDs {
		return fmt.Errorf("config error: bot.adminChatID and bot.adminOpenIDs are mutually exclusive, configure exactly one")
	}
	ttl, err := duration.Parse(c.Bot.DefaultTTL)
	if err != nil || ttl <= 0 {
		return fmt.Errorf("config error: bot.defaultTTL must be a positive duration like \"2h\" or \"30m\"")
	}
	c.Bot.defaultTTL = ttl
	return nil
}
