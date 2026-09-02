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
adminOpenIDs = ["ou_1"]
defaultTTL = "2h"`},
		{"missing feishu credentials", `
[frps]
apiAddr = "http://x"
user = "a"
password = "b"
[feishu]
appSecret = "y"
[bot]
adminOpenIDs = ["ou_1"]
defaultTTL = "2h"`},
		{"no admin config", base + `defaultTTL = "2h"` + "\n"},
		{"both admin configs", base + "adminChatID = \"oc_1\"\nadminOpenIDs = [\"ou_1\"]\ndefaultTTL = \"2h\"\n"},
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
