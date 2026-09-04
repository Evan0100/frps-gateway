package feishu

import (
	"fmt"
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"frps-gateway/store"
)

func menuCard() map[string]interface{} {
	return card("临时网络访问授权", "blue", "当前授权范围：全部现有内网服务\n\n请选择需要执行的操作。", []interface{}{
		button("申请授权", "primary", map[string]interface{}{"action": "apply"}, nil),
		button("我的授权", "default", map[string]interface{}{"action": "list"}, nil),
		button("撤销授权", "danger", map[string]interface{}{"action": "revoke_menu"}, nil),
	})
}

func applyInstructionsCard() map[string]interface{} {
	return card("申请授权", "wathet", "请查询当前网络的公网 IP，然后发送：\n\n**申请授权 118.25.93.30 4h**\n\n有效期可填写 `30m`、`2h`、`1d`，不填写时默认 4 小时。", []interface{}{
		button("返回菜单", "default", map[string]interface{}{"action": "menu"}, nil),
	})
}

func grantsCard(grants []store.Grant, revocable bool) map[string]interface{} {
	title := "我的授权"
	template := "green"
	if revocable {
		title = "撤销授权"
		template = "orange"
	}
	if len(grants) == 0 {
		return card(title, template, "你当前没有有效授权。", []interface{}{
			button("返回菜单", "default", map[string]interface{}{"action": "menu"}, nil),
		})
	}
	var lines strings.Builder
	for i, grant := range grants {
		fmt.Fprintf(&lines, "%d. **%s**　有效至 %s", i+1, grant.IP, grant.ExpireAt.Local().Format("01-02 15:04"))
		if i+1 < len(grants) {
			lines.WriteString("\n")
		}
	}
	actions := make([]interface{}, 0, len(grants)+2)
	if revocable {
		for _, grant := range grants {
			actions = append(actions, button("撤销 "+grant.IP, "danger", map[string]interface{}{"action": "revoke", "ip": grant.IP}, map[string]interface{}{
				"title": map[string]interface{}{"tag": "plain_text", "content": "确认撤销"},
				"text":  map[string]interface{}{"tag": "plain_text", "content": "撤销后，该网络可能立即无法访问公司服务。"},
			}))
		}
	} else {
		actions = append(actions, button("撤销授权", "danger", map[string]interface{}{"action": "revoke_menu"}, nil))
	}
	actions = append(actions, button("返回菜单", "default", map[string]interface{}{"action": "menu"}, nil))
	return card(title, template, lines.String(), actions)
}

func card(title, template, markdown string, actions []interface{}) map[string]interface{} {
	elements := []interface{}{
		map[string]interface{}{"tag": "markdown", "content": markdown},
	}
	if len(actions) > 0 {
		elements = append(elements, map[string]interface{}{"tag": "action", "layout": "flow", "actions": actions})
	}
	return map[string]interface{}{
		"config": map[string]interface{}{"wide_screen_mode": true},
		"header": map[string]interface{}{
			"template": template,
			"title":    map[string]interface{}{"tag": "plain_text", "content": title},
		},
		"elements": elements,
	}
}

func button(text, buttonType string, value, confirm map[string]interface{}) map[string]interface{} {
	b := map[string]interface{}{
		"tag":   "button",
		"text":  map[string]interface{}{"tag": "plain_text", "content": text},
		"type":  buttonType,
		"value": value,
	}
	if confirm != nil {
		b["confirm"] = confirm
	}
	return b
}

func cardResponse(data map[string]interface{}) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{Card: &callback.Card{Type: "card_json", Data: data}}
}

func toastResponse(kind, content string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: kind, Content: content}}
}
