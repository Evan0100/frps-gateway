package command

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		action  Action
		ip      string
		ttl     time.Duration
		wantErr bool
	}{
		{"add default ttl", "加白 1.2.3.4", ActionAdd, "1.2.3.4", 0, false},
		{"add with ttl", "加白 1.2.3.4 2h", ActionAdd, "1.2.3.4", 2 * time.Hour, false},
		{"add with days", "加白 1.2.3.4 3d", ActionAdd, "1.2.3.4", 72 * time.Hour, false},
		{"add english", "add 10.0.0.1 30m", ActionAdd, "10.0.0.1", 30 * time.Minute, false},
		{"add glued", "加白1.2.3.4", ActionAdd, "1.2.3.4", 0, false},
		{"add glued english", "add1.2.3.4", ActionAdd, "1.2.3.4", 0, false},
		{"authorize current ip", "加白当前IP", ActionAuthorize, "", 0, false},
		{"authorize current ip lowercase", "加白当前ip", ActionAuthorize, "", 0, false},
		{"authorize english", "authorize", ActionAuthorize, "", 0, false},
		{"add trailing newline", "加白 1.2.3.4\n", ActionAdd, "1.2.3.4", 0, false},
		{"add ipv6", "加白 ::1", ActionAdd, "::1", 0, false},
		{"add mixed case alias", "ADD 1.2.3.4", ActionAdd, "1.2.3.4", 0, false},
		{"mention stripped", "@_user_1 加白 1.2.3.4 2h", ActionAdd, "1.2.3.4", 2 * time.Hour, false},
		{"bad ip", "加白 256.1.1.1", ActionHelp, "", 0, true},
		{"bad ttl", "加白 1.2.3.4 abc", ActionHelp, "", 0, true},
		{"negative ttl", "加白 1.2.3.4 -1h", ActionHelp, "", 0, true},
		{"too many args", "加白 1.2.3.4 2h extra", ActionHelp, "", 0, true},
		{"add missing ip", "加白", ActionHelp, "", 0, true},
		{"remove", "删白 1.2.3.4", ActionRemove, "1.2.3.4", 0, false},
		{"remove english", "remove ::1", ActionRemove, "::1", 0, false},
		{"remove glued", "删白1.2.3.4", ActionRemove, "1.2.3.4", 0, false},
		{"remove missing ip", "删白", ActionHelp, "", 0, true},
		{"remove bad ip", "删白 foo", ActionHelp, "", 0, true},
		{"list", "白名单", ActionList, "", 0, false},
		{"list mine", "我的白名单", ActionList, "", 0, false},
		{"list english", "list", ActionList, "", 0, false},
		{"list ls", "ls", ActionList, "", 0, false},
		{"help", "帮助", ActionHelp, "", 0, false},
		{"blank", "   ", ActionHelp, "", 0, false},
		{"unknown", "随便说点什么", ActionHelp, "", 0, true},
		{"only mention", "@_user_1", ActionHelp, "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := Parse(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) = %+v, want error", tc.in, cmd)
				}
			} else if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tc.in, err)
			}
			if cmd.Action != tc.action {
				t.Errorf("action = %q, want %q", cmd.Action, tc.action)
			}
			if cmd.IP != tc.ip {
				t.Errorf("ip = %q, want %q", cmd.IP, tc.ip)
			}
			if cmd.TTL != tc.ttl {
				t.Errorf("ttl = %v, want %v", cmd.TTL, tc.ttl)
			}
		})
	}
}
