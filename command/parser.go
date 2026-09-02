// Package command parses whitelist management instructions from plain chat text.
package command

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"frps-gateway/duration"
)

type Action string

const (
	ActionAdd    Action = "add"
	ActionRemove Action = "remove"
	ActionList   Action = "list"
	ActionHelp   Action = "help"
)

// Long aliases first so e.g. "list..." is not split by "ls".
var (
	addAliases    = []string{"加白", "添加", "add"}
	removeAliases = []string{"删白", "删除", "remove", "del"}
	listAliases   = []string{"白名单", "列表", "list", "ls"}
	helpAliases   = []string{"帮助", "help", "?", "？"}

	// feishu renders @mentions inside text as @_user_1 placeholders
	mentionPattern = regexp.MustCompile(`@_user_\d+`)

	aliasPrefixes = concat(addAliases, removeAliases, listAliases)
)

// Usage is the help text replied for unknown or malformed instructions.
const Usage = `可用指令：
  加白 <IP> [有效期]   添加 IP 到白名单，有效期如 30m / 2h / 3d，缺省用默认值
  删白 <IP>            从白名单移除 IP
  白名单               查看当前白名单及剩余有效期
示例：加白 1.2.3.4 2h`

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
	case contains(addAliases, head):
		return parseAdd(fields[1:])
	case contains(removeAliases, head):
		return parseRemove(fields[1:])
	case contains(listAliases, head):
		return &Command{Action: ActionList}, nil
	case contains(helpAliases, head):
		return &Command{Action: ActionHelp}, nil
	default:
		return &Command{Action: ActionHelp}, fmt.Errorf("无法识别的指令 %q", fields[0])
	}
}

// normalize strips mention placeholders and inserts a space between a glued
// alias and its argument, so "加白1.2.3.4" behaves like "加白 1.2.3.4".
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
		return &Command{Action: ActionHelp}, fmt.Errorf("缺少 IP，示例：加白 1.2.3.4 2h")
	}
	if len(args) > 2 {
		return &Command{Action: ActionHelp}, fmt.Errorf("参数过多，示例：加白 1.2.3.4 2h")
	}
	ip := net.ParseIP(args[0])
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
		return &Command{Action: ActionHelp}, fmt.Errorf("缺少 IP，示例：删白 1.2.3.4")
	}
	if len(args) > 1 {
		return &Command{Action: ActionHelp}, fmt.Errorf("参数过多，示例：删白 1.2.3.4")
	}
	ip := net.ParseIP(args[0])
	if ip == nil {
		return &Command{Action: ActionHelp}, fmt.Errorf("%q 不是合法的 IP 地址", args[0])
	}
	return &Command{Action: ActionRemove, IP: ip.String()}, nil
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
