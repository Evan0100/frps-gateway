package feishu

import (
	"context"
	"encoding/json"
	"errors"
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
	request     interaction.Request
	linkRequest interaction.Request
	link        string
	linkErr     error
}

func (e *cardTestExecutor) Execute(_ context.Context, request interaction.Request) string {
	e.request = request
	return "授权已撤销：" + strings.TrimPrefix(request.Text, "撤销授权 ")
}

func (e *cardTestExecutor) AuthorizeLink(_ context.Context, request interaction.Request) (string, error) {
	e.linkRequest = request
	return e.link, e.linkErr
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
	body, err := json.Marshal(menuCard("", ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"申请授权", "我的授权", "撤销授权", `"action":"apply"`, `"action":"list"`, `"action":"revoke_menu"`} {
		if !strings.Contains(string(body), expected) {
			t.Errorf("menu card missing %q: %s", expected, body)
		}
	}
}

func TestStartMenuDeliversCardWithAuthorizationLink(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "menu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	exec := &cardTestExecutor{link: "https://auth.example.com/authorize/tok_menu"}
	bot := NewSecure("cli_test", "secret", "", "", "", true, true, 1, 10, 10, exec, slog.Default())
	bot.SetLinkTTL("10m")
	bot.SetReplyOutbox(st)

	if err = bot.handle(msgEvent("om_start", "ou_user", "oc_chat", "/start")); err != nil {
		t.Fatal(err)
	}
	replies, err := st.PendingReplies(context.Background(), 10)
	if err != nil || len(replies) != 1 {
		t.Fatalf("replies=%d err=%v", len(replies), err)
	}
	if replies[0].ID != "om_start:menu" {
		t.Fatalf("reply id=%q", replies[0].ID)
	}
	body := replies[0].Text
	for _, expected := range []string{interactiveReplyPrefix, "打开授权页面", "https://auth.example.com/authorize/tok_menu", "10m"} {
		if !strings.Contains(body, expected) {
			t.Errorf("menu reply missing %q: %s", expected, body)
		}
	}
	if exec.linkRequest.OperatorOpenID != "ou_user" || exec.linkRequest.MessageID != "om_start:menu" {
		t.Fatalf("unexpected link request: %+v", exec.linkRequest)
	}

	// Link issuing failures fall back to the manual apply action.
	exec.link, exec.linkErr = "", errors.New("store down")
	resp, err := bot.onCardAction(context.Background(), cardEvent("evt_apply", "ou_user", "oc_chat", map[string]interface{}{"action": "apply"}))
	if err != nil || resp.Card == nil {
		t.Fatalf("apply fallback response=%+v err=%v", resp, err)
	}
	encoded, _ := json.Marshal(resp.Card.Data)
	if !strings.Contains(string(encoded), "申请授权 203.0.113.10") {
		t.Fatalf("apply fallback lacks manual instructions: %s", encoded)
	}
}

func TestAuthorizeCommandDeliversLinkCard(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "authorize.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	exec := &cardTestExecutor{link: "https://auth.example.com/authorize/tok_auth"}
	bot := NewSecure("cli_test", "secret", "", "", "", true, true, 1, 10, 10, exec, slog.Default())
	bot.SetDefaultTTL("30d")
	bot.SetLinkTTL("10m")
	bot.SetReplyOutbox(st)

	if err = bot.handle(msgEvent("om_auth", "ou_user", "oc_chat", "授权")); err != nil {
		t.Fatal(err)
	}
	replies, err := st.PendingReplies(context.Background(), 10)
	if err != nil || len(replies) != 1 {
		t.Fatalf("replies=%d err=%v", len(replies), err)
	}
	if replies[0].ID != "om_auth:apply" {
		t.Fatalf("reply id=%q", replies[0].ID)
	}
	body := replies[0].Text
	for _, expected := range []string{interactiveReplyPrefix, "打开授权页面", "https://auth.example.com/authorize/tok_auth", "30d", "10m"} {
		if !strings.Contains(body, expected) {
			t.Errorf("authorize reply missing %q: %s", expected, body)
		}
	}
	if exec.linkRequest.OperatorOpenID != "ou_user" || exec.linkRequest.MessageID != "om_auth:apply" {
		t.Fatalf("unexpected link request: %+v", exec.linkRequest)
	}

	// Link issuing failures fall back to the manual instructions card.
	exec.link, exec.linkErr = "", errors.New("store down")
	if err = bot.handle(msgEvent("om_auth2", "ou_user", "oc_chat", "授权")); err != nil {
		t.Fatal(err)
	}
	replies, err = st.PendingReplies(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	fallback := ""
	for _, r := range replies {
		if r.ID == "om_auth2:apply" {
			fallback = r.Text
		}
	}
	if !strings.Contains(fallback, "申请授权 203.0.113.10") {
		t.Fatalf("authorize fallback lacks manual instructions: %q", fallback)
	}
}

func TestCardApplyActionReturnsFreshLinkCard(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "apply.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	exec := &cardTestExecutor{link: "https://auth.example.com/authorize/tok_apply"}
	bot := NewSecure("cli_test", "secret", "", "", "", true, true, 1, 10, 10, exec, slog.Default())
	bot.SetDefaultTTL("8h")
	bot.SetReplyOutbox(st)

	resp, err := bot.onCardAction(context.Background(), cardEvent("evt_apply", "ou_user", "oc_chat", map[string]interface{}{"action": "apply"}))
	if err != nil || resp.Card == nil {
		t.Fatalf("apply response=%+v err=%v", resp, err)
	}
	encoded, _ := json.Marshal(resp.Card.Data)
	for _, expected := range []string{"打开授权页面", "https://auth.example.com/authorize/tok_apply", "8h"} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("apply card missing %q: %s", expected, encoded)
		}
	}
	if exec.linkRequest.OperatorOpenID != "ou_user" || exec.linkRequest.MessageID != "evt_apply" {
		t.Fatalf("unexpected link request: %+v", exec.linkRequest)
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
	bot.SetDefaultTTL("24h")
	bot.SetReplyOutbox(st)

	applyResp, err := bot.onCardAction(context.Background(), cardEvent("evt_apply", "ou_user", "oc_chat", map[string]interface{}{"action": "apply"}))
	if err != nil || applyResp.Card == nil {
		t.Fatalf("apply response=%+v err=%v", applyResp, err)
	}
	applyEncoded, _ := json.Marshal(applyResp.Card.Data)
	if !strings.Contains(string(applyEncoded), "24h") {
		t.Fatalf("apply card does not contain configured default TTL: %s", applyEncoded)
	}

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

func msgEvent(messageID, openID, chatID, text string) *larkim.P2MessageReceiveV1 {
	s := func(v string) *string { return &v }
	return &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{AppID: "cli_test"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender:  &larkim.EventSender{SenderId: &larkim.UserId{OpenId: s(openID)}, SenderType: s("user")},
			Message: &larkim.EventMessage{MessageId: s(messageID), ChatId: s(chatID), ChatType: s("p2p"), MessageType: s("text"), Content: s(`{"text":"` + text + `"}`)},
		},
	}
}
