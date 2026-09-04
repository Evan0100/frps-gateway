package feishu

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"frps-gateway/interaction"
	"frps-gateway/store"
)

type cardTestExecutor struct {
	request interaction.Request
}

func (e *cardTestExecutor) Execute(_ context.Context, request interaction.Request) string {
	e.request = request
	return "授权已撤销：" + strings.TrimPrefix(request.Text, "撤销授权 ")
}

func TestMentionsBot(t *testing.T) {
	user, bot := "user", "bot"
	if mentionsBot([]*larkim.MentionEvent{{MentionedType: &user}}) {
		t.Fatal("user mention accepted")
	}
	if !mentionsBot([]*larkim.MentionEvent{{MentionedType: &bot}}) {
		t.Fatal("bot mention rejected")
	}
}

func TestDeliveryUUIDIsStable(t *testing.T) {
	a, b := deliveryUUID("message-1"), deliveryUUID("message-1")
	if a != b || len(a) != 36 {
		t.Fatalf("uuid=%q duplicate=%q", a, b)
	}
	if a == deliveryUUID("message-2") {
		t.Fatal("different messages share UUID")
	}
}

func TestMenuCardContainsExpectedActions(t *testing.T) {
	body, err := json.Marshal(menuCard())
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"申请授权", "我的授权", "撤销授权", `"action":"apply"`, `"action":"list"`, `"action":"revoke_menu"`} {
		if !strings.Contains(string(body), expected) {
			t.Errorf("menu card missing %q: %s", expected, body)
		}
	}
}

func TestCardListAndRevokeUseCallbackIdentity(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "card.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	if err = st.AddGrant(context.Background(), store.Grant{OpenID: "ou_user", IP: "203.0.113.8", Source: "test", CreatedAt: now, ExpireAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	exec := &cardTestExecutor{}
	bot := NewSecure("cli_test", "secret", "", "", "", true, true, 1, 10, 10, exec, slog.Default())
	bot.SetReplyOutbox(st)

	listResp, err := bot.onCardAction(context.Background(), cardEvent("evt_list", "ou_user", "oc_chat", map[string]interface{}{"action": "list"}))
	if err != nil || listResp.Card == nil {
		t.Fatalf("list response=%+v err=%v", listResp, err)
	}
	encoded, _ := json.Marshal(listResp.Card.Data)
	if !strings.Contains(string(encoded), "203.0.113.8") {
		t.Fatalf("list card does not contain grant: %s", encoded)
	}

	revokeResp, err := bot.onCardAction(context.Background(), cardEvent("evt_revoke", "ou_user", "oc_chat", map[string]interface{}{"action": "revoke", "ip": "203.0.113.8"}))
	if err != nil || revokeResp.Toast == nil {
		t.Fatalf("revoke response=%+v err=%v", revokeResp, err)
	}
	if exec.request.OperatorOpenID != "ou_user" || exec.request.Text != "撤销授权 203.0.113.8" || exec.request.MessageID != "evt_revoke" {
		t.Fatalf("unexpected executor request: %+v", exec.request)
	}
}

func cardEvent(eventID, openID, chatID string, value map[string]interface{}) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: eventID, AppID: "cli_test"}},
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: openID},
			Action:   &callback.CallBackAction{Value: value},
			Context:  &callback.Context{OpenMessageID: "om_card", OpenChatID: chatID},
		},
	}
}
