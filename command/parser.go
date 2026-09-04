// Package command parses whitelist management instructions from plain chat text.
package command

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
	"unicode"

	"frps-gateway/duration"
)

type Action string

const (
	ActionAdd    Action = "add"
	ActionRemove Action = "remove"
	ActionList   Action = "list"
	ActionHelp   Action = "help"
	ActionMenu   Action = "menu"
)

// Long aliases first so e.g. "list..." is not split by "ls".
var (
	addAliases    = []string{"申请授权"}
	removeAliases = []string{"撤销授权"}
	listAliases   = []string{"我的授权"}
	menuAliases   = []string{"/start", "开始", "菜单"}

	// feishu renders @mentions inside text as @_user_1 placeholders
	mentionPattern = regexp.MustCompile(`@_user_\d+`)

	aliasPrefixes = concat(addAliases, removeAliases)
)

// Usage is the help text replied for unknown or malformed instructions.
const Usage = `可用指令：
  /start                    打开操作菜单
  申请授权 <IP> [有效期]   为指定公网 IP 添加临时授权
  我的授权                  查看自己的有效授权
  撤销授权 <IP>             撤销指定授权；其他人的授权不受影响
示例：申请授权 1.2.3.4 2h`

// Command is a parsed whitelist instruction.
type Command struct {
	Action Action
	IP     string
	TTL    time.Duration // valid only for ActionAdd; 0 means "use default TTL"
}

// Parse turns raw chat text into a Command. Mentions like "@_user_1" are
// stripped. Blank input yields a help command; malformed input yields a help
// command plus an error describing the problem.
func Parse(raw string) (*Command, error) {
	text := normalize(raw)
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return &Command{Action: ActionHelp}, nil
	}
	head := strings.ToLower(fields[0])
	switch {
	case contains(menuAliases, head):
		return &Command{Action: ActionMenu}, nil
	case contains(addAliases, head):
		return parseAdd(fields[1:])
	case contains(removeAliases, head):
		return parseRemove(fields[1:])
	case contains(listAliases, head):
		return &Command{Action: ActionList}, nil
	default:
		return &Command{Action: ActionHelp}, fmt.Errorf("无法识别该操作，请发送 /start 打开菜单")
	}
}

// normalize strips mention placeholders and inserts a space between a glued
// action name and its argument.
func normalize(raw string) string {
	text := strings.TrimSpace(mentionPattern.ReplaceAllString(raw, ""))
	lower := strings.ToLower(text)
	for _, alias := range aliasPrefixes {
		if len(text) > len(alias) && strings.HasPrefix(lower, alias) && text[len(alias)] != ' ' {
			text = text[:len(alias)] + " " + text[len(alias):]
			break
		}
	}
	return text
}

func parseAdd(args []string) (*Command, error) {
	if len(args) == 0 {
		return &Command{Action: ActionHelp}, fmt.Errorf("请填写公网 IP，例如：申请授权 1.2.3.4 2h")
	}
	if len(args) > 2 {
		return &Command{Action: ActionHelp}, fmt.Errorf("参数过多，例如：申请授权 1.2.3.4 2h")
	}
	ipText := sanitizeIPToken(args[0])
	ip := net.ParseIP(ipText)
	if ip == nil {
		return &Command{Action: ActionHelp}, fmt.Errorf("%q 不是合法的 IP 地址", args[0])
	}
	var ttl time.Duration
	if len(args) == 2 {
		t, err := duration.Parse(args[1])
		if err != nil || t <= 0 {
			return &Command{Action: ActionHelp}, fmt.Errorf("%q 不是合法的有效期（支持 30m / 2h / 3d）", args[1])
		}
		ttl = t
	}
	return &Command{Action: ActionAdd, IP: ip.String(), TTL: ttl}, nil
}

func parseRemove(args []string) (*Command, error) {
	if len(args) == 0 {
		return &Command{Action: ActionHelp}, fmt.Errorf("请选择或填写需要撤销的 IP，例如：撤销授权 1.2.3.4")
	}
	if len(args) > 1 {
		return &Command{Action: ActionHelp}, fmt.Errorf("参数过多，例如：撤销授权 1.2.3.4")
	}
	ipText := sanitizeIPToken(args[0])
	ip := net.ParseIP(ipText)
	if ip == nil {
		return &Command{Action: ActionHelp}, fmt.Errorf("%q 不是合法的 IP 地址", args[0])
	}
	return &Command{Action: ActionRemove, IP: ip.String()}, nil
}

// sanitizeIPToken removes Unicode format characters that can be introduced by
// chat clients or input methods (for example zero-width spaces and BOMs).
// No visible character is rewritten, so malformed addresses still fail closed.
func sanitizeIPToken(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

func contains(aliases []string, s string) bool {
	for _, a := range aliases {
		if strings.EqualFold(a, s) {
			return true
		}
	}
	return false
}

func concat(groups ...[]string) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}
