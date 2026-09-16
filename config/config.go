// Package config loads and validates bot.toml.
package config

import (
	"fmt"
	"net"
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
	Admin   Admin   `toml:"admin"`
	Storage Storage `toml:"storage"`
	Ingest  Ingest  `toml:"ingest"`
}

// Ingest controls shipping frps access records into SQLite.
type Ingest struct {
	Enabled       *bool  `toml:"enabled"`
	Interval      string `toml:"interval"`
	RetentionDays int    `toml:"retentionDays"`

	interval time.Duration
}

func (i *Ingest) IsEnabled() bool { return i.Enabled == nil || *i.Enabled }

// IntervalDuration returns the poll interval; valid after Load.
func (i *Ingest) IntervalDuration() time.Duration { return i.interval }

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
	AdminChatID           string `toml:"adminChatID"`
	DefaultTTL            string `toml:"defaultTTL"`
	MaxTTL                string `toml:"maxTTL"`
	LinkTTL               string `toml:"linkTTL"`
	AllowAllUsers         bool   `toml:"allowAllUsers"`
	RequireMentionInGroup bool   `toml:"requireMentionInGroup"`
	Workers               int    `toml:"workers"`
	QueueSize             int    `toml:"queueSize"`
	RequestsPerMinute     int    `toml:"requestsPerMinute"`
	MaxActiveIPs          int    `toml:"maxActiveIPs"`
	Enabled               *bool  `toml:"enabled"`

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

// Admin configures the gateway-owned management console. Its credentials are
// deliberately separate from the frps dashboard account.
type Admin struct {
	Enabled         bool   `toml:"enabled"`
	User            string `toml:"user"`
	Password        string `toml:"password"`
	PasswordEnv     string `toml:"passwordEnv"`
	PasswordFile    string `toml:"passwordFile"`
	SessionTTL      string `toml:"sessionTTL"`
	MaxGrantTTL     string `toml:"maxGrantTTL"`
	KnockSecret     string `toml:"knockSecret"`
	KnockSecretEnv  string `toml:"knockSecretEnv"`
	KnockSecretFile string `toml:"knockSecretFile"`
	KnockHits       int    `toml:"knockHits"`
	KnockWindow     string `toml:"knockWindow"`
	KnockTTL        string `toml:"knockTTL"`

	sessionTTL  time.Duration
	maxGrantTTL time.Duration
	knockWindow time.Duration
	knockTTL    time.Duration
}

func (a *Admin) SessionTTLDuration() time.Duration  { return a.sessionTTL }
func (a *Admin) MaxGrantTTLDuration() time.Duration { return a.maxGrantTTL }
func (a *Admin) KnockWindowDuration() time.Duration { return a.knockWindow }
func (a *Admin) KnockTTLDuration() time.Duration    { return a.knockTTL }
func (a *Admin) IsKnockEnabled() bool               { return a.KnockSecret != "" }

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
	if cfg.Admin.Password, err = resolveSecret(cfg.Admin.Password, cfg.Admin.PasswordEnv, cfg.Admin.PasswordFile, baseDir, "admin.password"); err != nil {
		return nil, err
	}
	if cfg.Admin.KnockSecret, err = resolveSecret(cfg.Admin.KnockSecret, cfg.Admin.KnockSecretEnv, cfg.Admin.KnockSecretFile, baseDir, "admin.knockSecret"); err != nil {
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
	frpsURL, err := url.Parse(c.Frps.ApiAddr)
	if err != nil || frpsURL.Host == "" || (frpsURL.Scheme != "http" && frpsURL.Scheme != "https") {
		return fmt.Errorf("config error: frps.apiAddr must be an http or https URL")
	}
	if frpsURL.User != nil || (frpsURL.Path != "" && frpsURL.Path != "/") || frpsURL.RawQuery != "" || frpsURL.Fragment != "" {
		return fmt.Errorf("config error: frps.apiAddr must be an origin URL without credentials, path, query, or fragment")
	}
	if frpsURL.Scheme == "http" && !isLoopbackHost(frpsURL.Hostname()) {
		return fmt.Errorf("config error: plaintext frps.apiAddr is allowed only on loopback; use https for remote management APIs")
	}
	if c.Frps.User == "" || c.Frps.Password == "" {
		return fmt.Errorf("config error: frps.user and frps.password are required (dashboard basic auth)")
	}
	if c.Feishu.AppID == "" || c.Feishu.AppSecret == "" {
		return fmt.Errorf("config error: feishu.appID and feishu.appSecret are required")
	}
	if c.Bot.AdminChatID == "" && !c.Bot.AllowAllUsers {
		return fmt.Errorf("config error: configure bot.adminChatID or enable bot.allowAllUsers")
	}
	ttl, err := duration.Parse(c.Bot.DefaultTTL)
	if err != nil || ttl <= 0 {
		return fmt.Errorf("config error: bot.defaultTTL must be a positive duration like \"2h\" or \"30m\"")
	}
	c.Bot.defaultTTL = ttl
	if c.Bot.MaxTTL == "" {
		c.Bot.MaxTTL = "30d"
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
		c.Bot.MaxActiveIPs = 10
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
	listenHost, _, err := net.SplitHostPort(c.Server.ListenAddr)
	if err != nil {
		return fmt.Errorf("config error: server.listenAddr must be host:port")
	}
	if c.Server.TLSCertFile == "" && !isLoopbackHost(listenHost) {
		return fmt.Errorf("config error: a non-loopback server.listenAddr requires native TLS")
	}
	if c.Admin.Enabled {
		c.Admin.User = strings.TrimSpace(c.Admin.User)
		if strings.TrimSpace(c.Admin.User) == "" || strings.TrimSpace(c.Admin.Password) == "" {
			return fmt.Errorf("config error: admin.user and one admin password source are required when admin.enabled=true")
		}
		if len(c.Admin.Password) < 10 {
			return fmt.Errorf("config error: admin password must be at least 10 characters")
		}
		if c.Admin.User == c.Frps.User && c.Admin.Password == c.Frps.Password {
			return fmt.Errorf("config error: admin credentials must differ from frps dashboard credentials")
		}
		if c.Admin.SessionTTL == "" {
			c.Admin.SessionTTL = "8h"
		}
		c.Admin.sessionTTL, err = time.ParseDuration(c.Admin.SessionTTL)
		if err != nil || c.Admin.sessionTTL < 5*time.Minute || c.Admin.sessionTTL > 24*time.Hour {
			return fmt.Errorf("config error: admin.sessionTTL must be between 5m and 24h")
		}
		if c.Admin.MaxGrantTTL == "" {
			c.Admin.MaxGrantTTL = "24h"
		}
		c.Admin.maxGrantTTL, err = duration.Parse(c.Admin.MaxGrantTTL)
		if err != nil || c.Admin.maxGrantTTL <= 0 || c.Admin.maxGrantTTL > 30*24*time.Hour {
			return fmt.Errorf("config error: admin.maxGrantTTL must be positive and no more than 30d")
		}
		if c.Admin.KnockSecret != "" {
			if len(c.Admin.KnockSecret) < 24 {
				return fmt.Errorf("config error: admin knock secret must be at least 24 characters")
			}
			if !isURLSafeToken(c.Admin.KnockSecret) {
				return fmt.Errorf("config error: admin knock secret may contain only letters, digits, '-' and '_'")
			}
			if c.Admin.KnockHits == 0 {
				c.Admin.KnockHits = 3
			}
			if c.Admin.KnockHits < 2 || c.Admin.KnockHits > 10 {
				return fmt.Errorf("config error: admin.knockHits must be between 2 and 10")
			}
			if c.Admin.KnockWindow == "" {
				c.Admin.KnockWindow = "30s"
			}
			c.Admin.knockWindow, err = time.ParseDuration(c.Admin.KnockWindow)
			if err != nil || c.Admin.knockWindow < 5*time.Second || c.Admin.knockWindow > 5*time.Minute {
				return fmt.Errorf("config error: admin.knockWindow must be between 5s and 5m")
			}
			if c.Admin.KnockTTL == "" {
				c.Admin.KnockTTL = "5m"
			}
			c.Admin.knockTTL, err = time.ParseDuration(c.Admin.KnockTTL)
			if err != nil || c.Admin.knockTTL < time.Minute || c.Admin.knockTTL > 30*time.Minute {
				return fmt.Errorf("config error: admin.knockTTL must be between 1m and 30m")
			}
		} else if c.Admin.KnockHits != 0 || c.Admin.KnockWindow != "" || c.Admin.KnockTTL != "" {
			return fmt.Errorf("config error: admin knock timing requires a knockSecret source")
		}
	}
	if c.Storage.SQLiteFile == "" {
		c.Storage.SQLiteFile = "./frps-gateway.db"
	}
	if c.Ingest.Interval == "" {
		c.Ingest.Interval = "5s"
	}
	interval, err := time.ParseDuration(c.Ingest.Interval)
	if err != nil || interval <= 0 {
		return fmt.Errorf("config error: ingest.interval must be a positive duration like \"5s\" or \"1m\"")
	}
	c.Ingest.interval = interval
	if c.Ingest.RetentionDays == 0 {
		c.Ingest.RetentionDays = 30
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isURLSafeToken(value string) bool {
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return value != ""
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
