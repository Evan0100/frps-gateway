package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bot.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadOK(t *testing.T) {
	path := writeConfig(t, `
[frps]
apiAddr = "http://127.0.0.1:7500"
user = "admin"
password = "secret"

[feishu]
appID = "cli_xxx"
appSecret = "yyy"

[bot]
adminChatID = "oc_1"
defaultTTL = "2h"

[server]
publicBaseURL = "https://access.example.com"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Bot.TTL() != 2*time.Hour {
		t.Errorf("defaultTTL = %v, want 2h", cfg.Bot.TTL())
	}
}

func TestLoadValidation(t *testing.T) {
	base := `
[frps]
apiAddr = "http://127.0.0.1:7500"
user = "admin"
password = "secret"

[feishu]
appID = "cli_xxx"
appSecret = "yyy"

[bot]
`
	cases := []struct {
		name   string
		config string
	}{
		{"missing apiAddr", `
[frps]
user = "a"
password = "b"
[feishu]
appID = "x"
appSecret = "y"
[bot]
allowAllUsers = true
defaultTTL = "2h"`},
		{"missing feishu credentials", `
[frps]
apiAddr = "http://x"
user = "a"
password = "b"
[feishu]
appSecret = "y"
[bot]
allowAllUsers = true
defaultTTL = "2h"`},
		{"no admin config", base + `defaultTTL = "2h"` + "\n"},
		{"bad defaultTTL", base + "adminChatID = \"oc_1\"\ndefaultTTL = \"abc\"\n"},
		{"empty defaultTTL", base + "adminChatID = \"oc_1\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, tc.config)); err == nil {
				t.Error("want validation error, got nil")
			}
		})
	}
}

func TestLoadSecretsFromEnvironmentAndFile(t *testing.T) {
	t.Setenv("FRPS_GATEWAY_TEST_PASSWORD", "from-env")
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "feishu-secret")
	if err := os.WriteFile(secretFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, `[frps]
apiAddr="http://127.0.0.1:7500"
user="admin"
passwordEnv="FRPS_GATEWAY_TEST_PASSWORD"
[feishu]
appID="cli_xxx"
appSecretFile="`+filepath.ToSlash(secretFile)+`"
[bot]
allowAllUsers=true
defaultTTL="4h"
[server]
publicBaseURL="https://access.example.com"`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Frps.Password != "from-env" || cfg.Feishu.AppSecret != "from-file" {
		t.Fatalf("secrets not resolved")
	}
	if cfg.Bot.MaxTTLDuration() != 24*time.Hour || cfg.Bot.MaxActiveIPs != 3 {
		t.Fatalf("unsafe defaults: ttl=%v ips=%d", cfg.Bot.MaxTTLDuration(), cfg.Bot.MaxActiveIPs)
	}
}
