package command

import (
	"testing"
	"time"
)

func TestParseCurrentCommands(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		action  Action
		ip      string
		ttl     time.Duration
		wantErr bool
	}{
		{"grant default", "申请授权 1.2.3.4", ActionAdd, "1.2.3.4", 0, false},
		{"grant ttl", "申请授权 1.2.3.4 2h", ActionAdd, "1.2.3.4", 2 * time.Hour, false},
		{"grant glued", "申请授权1.2.3.4", ActionAdd, "1.2.3.4", 0, false},
		{"grant zero width", "申请授权 118.\u200b25.93.30 1d", ActionAdd, "118.25.93.30", 24 * time.Hour, false},
		{"grant ipv6", "申请授权 ::1", ActionAdd, "::1", 0, false},
		{"revoke", "撤销授权 1.2.3.4", ActionRemove, "1.2.3.4", 0, false},
		{"list", "我的授权", ActionList, "", 0, false},
		{"start", "/start", ActionMenu, "", 0, false},
		{"start chinese", "开始", ActionMenu, "", 0, false},
		{"menu", "菜单", ActionMenu, "", 0, false},
		{"bad ip", "申请授权 256.1.1.1", ActionHelp, "", 0, true},
		{"bad ttl", "申请授权 1.2.3.4 abc", ActionHelp, "", 0, true},
		{"missing ip", "申请授权", ActionHelp, "", 0, true},
		{"unknown", "随便说点什么", ActionHelp, "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := Parse(tc.in)
			if tc.wantErr != (err != nil) {
				t.Fatalf("Parse(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			}
			if cmd.Action != tc.action || cmd.IP != tc.ip || cmd.TTL != tc.ttl {
				t.Errorf("Parse(%q)=%+v want action=%q ip=%q ttl=%v", tc.in, cmd, tc.action, tc.ip, tc.ttl)
			}
		})
	}
}

func TestLegacyCommandsAreRejected(t *testing.T) {
	for _, input := range []string{
		"申请访问 1.2.3.4", "我的访问", "撤销 1.2.3.4",
		"加白 1.2.3.4", "添加 1.2.3.4", "add 1.2.3.4",
		"加白当前IP", "授权当前IP", "authorize",
		"删白 1.2.3.4", "删除 1.2.3.4", "remove 1.2.3.4", "del 1.2.3.4",
		"白名单", "我的白名单", "列表", "list", "ls", "帮助", "help",
	} {
		t.Run(input, func(t *testing.T) {
			cmd, err := Parse(input)
			if err == nil || cmd.Action != ActionHelp {
				t.Fatalf("legacy command %q accepted: cmd=%+v err=%v", input, cmd, err)
			}
		})
	}
}
