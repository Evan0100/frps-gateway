// Package config loads and validates bot.toml.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"frps-gateway/duration"
)

type Config struct {
	Frps    Frps    `toml:"frps"`
	Feishu  Feishu  `toml:"feishu"`
	Bot     Bot     `toml:"bot"`
	Server  Server  `toml:"server"`
	Storage Storage `toml:"storage"`
}

type Frps struct {
	ApiAddr      string `toml:"apiAddr"`
	User         string `toml:"user"`
	Password     string `toml:"password"`
	PasswordEnv  string `toml:"passwordEnv"`
	PasswordFile string `toml:"passwordFile"`
}

type Feishu struct {
	AppID                 string `toml:"appID"`
	AppSecret             string `toml:"appSecret"`
	AppSecretEnv          string `toml:"appSecretEnv"`
	AppSecretFile         string `toml:"appSecretFile"`
	EncryptKey            string `toml:"encryptKey"`
	EncryptKeyEnv         string `toml:"encryptKeyEnv"`
	EncryptKeyFile        string `toml:"encryptKeyFile"`
	VerificationToken     string `toml:"verificationToken"`
	VerificationTokenEnv  string `toml:"verificationTokenEnv"`
	VerificationTokenFile string `toml:"verificationTokenFile"`
}

type Bot struct {
	AdminChatID           string   `toml:"adminChatID"`
	AdminOpenIDs          []string `toml:"adminOpenIDs"`
	DefaultTTL            string   `toml:"defaultTTL"`
	MaxTTL                string   `toml:"maxTTL"`
	LinkTTL               string   `toml:"linkTTL"`
	AllowAllUsers         bool     `toml:"allowAllUsers"`
	RequireMentionInGroup bool     `toml:"requireMentionInGroup"`
	Workers               int      `toml:"workers"`
	QueueSize             int      `toml:"queueSize"`
	RequestsPerMinute     int      `toml:"requestsPerMinute"`
	MaxActiveIPs          int      `toml:"maxActiveIPs"`
	Enabled               *bool    `toml:"enabled"`

	defaultTTL time.Duration
	maxTTL     time.Duration
	linkTTL    time.Duration
}

// TTL returns the parsed defaultTTL; valid after Load.
func (b *Bot) TTL() time.Duration             { return b.defaultTTL }
func (b *Bot) MaxTTLDuration() time.Duration  { return b.maxTTL }
func (b *Bot) LinkTTLDuration() time.Duration { return b.linkTTL }
func (b *Bot) IsEnabled() bool                { return b.Enabled == nil || *b.Enabled }

type Server struct {
	ListenAddr        string   `toml:"listenAddr"`
	PublicBaseURL     string   `toml:"publicBaseURL"`
	TLSCertFile       string   `toml:"tlsCertFile"`
	TLSKeyFile        string   `toml:"tlsKeyFile"`
	TrustedProxyCIDRs []string `toml:"trustedProxyCIDRs"`
}
type Storage struct {
	SQLiteFile string `toml:"sqliteFile"`
}

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
	baseDir := filepath.Dir(path)
	if cfg.Frps.Password, err = resolveSecret(cfg.Frps.Password, cfg.Frps.PasswordEnv, cfg.Frps.PasswordFile, baseDir, "frps.password"); err != nil {
		return nil, err
	}
	if cfg.Feishu.AppSecret, err = resolveSecret(cfg.Feishu.AppSecret, cfg.Feishu.AppSecretEnv, cfg.Feishu.AppSecretFile, baseDir, "feishu.appSecret"); err != nil {
		return nil, err
	}
	if cfg.Feishu.EncryptKey, err = resolveSecret(cfg.Feishu.EncryptKey, cfg.Feishu.EncryptKeyEnv, cfg.Feishu.EncryptKeyFile, baseDir, "feishu.encryptKey"); err != nil {
		return nil, err
	}
	if cfg.Feishu.VerificationToken, err = resolveSecret(cfg.Feishu.VerificationToken, cfg.Feishu.VerificationTokenEnv, cfg.Feishu.VerificationTokenFile, baseDir, "feishu.verificationToken"); err != nil {
		return nil, err
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
	if len(c.Bot.AdminOpenIDs) == 0 && c.Bot.AdminChatID == "" && !c.Bot.AllowAllUsers {
		return fmt.Errorf("config error: configure bot.adminOpenIDs or enable bot.allowAllUsers")
	}
	if len(c.Bot.AdminOpenIDs) > 0 && c.Bot.AdminChatID != "" {
		return fmt.Errorf("config error: bot.adminChatID and bot.adminOpenIDs are mutually exclusive")
	}
	ttl, err := duration.Parse(c.Bot.DefaultTTL)
	if err != nil || ttl <= 0 {
		return fmt.Errorf("config error: bot.defaultTTL must be a positive duration like \"2h\" or \"30m\"")
	}
	c.Bot.defaultTTL = ttl
	if c.Bot.MaxTTL == "" {
		c.Bot.MaxTTL = "24h"
	}
	c.Bot.maxTTL, err = duration.Parse(c.Bot.MaxTTL)
	if err != nil || c.Bot.maxTTL < c.Bot.defaultTTL {
		return fmt.Errorf("config error: bot.maxTTL must be valid and >= defaultTTL")
	}
	if c.Bot.LinkTTL == "" {
		c.Bot.LinkTTL = "5m"
	}
	c.Bot.linkTTL, err = duration.Parse(c.Bot.LinkTTL)
	if err != nil || c.Bot.linkTTL <= 0 {
		return fmt.Errorf("config error: bot.linkTTL must be positive")
	}
	if c.Bot.Workers <= 0 {
		c.Bot.Workers = 4
	}
	if c.Bot.QueueSize <= 0 {
		c.Bot.QueueSize = 100
	}
	if c.Bot.RequestsPerMinute <= 0 {
		c.Bot.RequestsPerMinute = 10
	}
	if c.Bot.MaxActiveIPs <= 0 {
		c.Bot.MaxActiveIPs = 3
	}
	if c.Server.ListenAddr == "" {
		c.Server.ListenAddr = "127.0.0.1:8080"
	}
	u, err := url.Parse(c.Server.PublicBaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("config error: server.publicBaseURL must be an https URL")
	}
	if (c.Server.TLSCertFile == "") != (c.Server.TLSKeyFile == "") {
		return fmt.Errorf("config error: server.tlsCertFile and tlsKeyFile must be configured together")
	}
	if c.Storage.SQLiteFile == "" {
		c.Storage.SQLiteFile = "./frps-gateway.db"
	}
	return nil
}

func resolveSecret(direct, envName, fileName, baseDir, label string) (string, error) {
	sources := 0
	for _, v := range []string{direct, envName, fileName} {
		if strings.TrimSpace(v) != "" {
			sources++
		}
	}
	if sources > 1 {
		return "", fmt.Errorf("config error: configure only one of %s, %sEnv, or %sFile", label, label, label)
	}
	if envName != "" {
		v, ok := os.LookupEnv(envName)
		if !ok || strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("config error: environment variable %s for %s is empty", envName, label)
		}
		return strings.TrimSpace(v), nil
	}
	if fileName != "" {
		if !filepath.IsAbs(fileName) {
			fileName = filepath.Join(baseDir, fileName)
		}
		b, err := os.ReadFile(fileName)
		if err != nil {
			return "", fmt.Errorf("config error: read %sFile: %w", label, err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("config error: %sFile is empty", label)
		}
		return v, nil
	}
	return strings.TrimSpace(direct), nil
}
