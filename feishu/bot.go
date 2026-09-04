// Package feishu connects to Feishu via the official SDK long-connection
// mode, filters messages by configured audience and dispatches them to an Executor.
package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"frps-gateway/command"
	"frps-gateway/interaction"
	"frps-gateway/store"
)

// Executor executes a chat command with identity and delivery metadata.
type Executor interface {
	Execute(ctx context.Context, req interaction.Request) string
}

type ReplyOutbox interface {
	EnqueueReply(context.Context, string, string, string) error
	PendingReplies(context.Context, int) ([]store.OutboxMessage, error)
	MarkReplySent(context.Context, string) error
	RetryReply(context.Context, string, int) error
	EnqueueInbox(context.Context, string, []byte) error
	ClaimInbox(context.Context) (store.InboxEvent, bool, error)
	CompleteInbox(context.Context, string) error
	RetryInbox(context.Context, string, int) error
	ListUser(context.Context, string) ([]store.Grant, error)
}

const interactiveReplyPrefix = "__FRPS_GATEWAY_INTERACTIVE_V1__:"

type Bot struct {
	appID             string
	appSecret         string
	adminChatID       string
	api               *lark.Client
	exec              Executor
	logger            *slog.Logger
	allowAllUsers     bool
	requireMention    bool
	queue             chan *larkim.P2MessageReceiveV1
	requestsPerMinute int
	rateMu            sync.Mutex
	rate              map[string][]time.Time
	rateCleanup       time.Time
	encryptKey        string
	verificationToken string
	outbox            ReplyOutbox
	inboxWorkers      int
}

// New creates a Bot restricted to one configured chat.
func New(appID, appSecret, adminChatID string, exec Executor, logger *slog.Logger) *Bot {
	return NewSecure(appID, appSecret, "", "", adminChatID, false, true, 4, 100, 10, exec, logger)
}

// SetReplyOutbox enables durable reply delivery and retry.
func (b *Bot) SetReplyOutbox(outbox ReplyOutbox) { b.outbox = outbox }
func NewSecure(appID, appSecret, encryptKey, verificationToken, adminChatID string, allowAll, requireMention bool, workers, queueSize, rpm int, exec Executor, logger *slog.Logger) *Bot {
	b := &Bot{
		appID:       appID,
		appSecret:   appSecret,
		adminChatID: adminChatID,
		api:         lark.NewClient(appID, appSecret),
		exec:        exec,
		logger:      logger,
		encryptKey:  encryptKey, verificationToken: verificationToken,
		allowAllUsers: allowAll, requireMention: requireMention, queue: make(chan *larkim.P2MessageReceiveV1, queueSize), requestsPerMinute: rpm, rate: map[string][]time.Time{},
		inboxWorkers: workers,
	}
	for i := 0; i < workers; i++ {
		go func() {
			for e := range b.queue {
				_ = b.handle(e)
			}
		}()
	}
	return b
}

// Run starts the WebSocket long connection; it blocks until ctx is cancelled
// or the connection fails terminally. No public webhook endpoint is needed.
func (b *Bot) Run(ctx context.Context) error {
	if b.outbox != nil {
		go b.runReplyOutbox(ctx)
		for i := 0; i < b.inboxWorkers; i++ {
			go b.runInbox(ctx)
		}
	}
	d := dispatcher.NewEventDispatcher(b.verificationToken, b.encryptKey).
		OnP2MessageReceiveV1(b.onMessage).
		OnP2CardActionTrigger(b.onCardAction)
	cli := larkws.NewClient(b.appID, b.appSecret,
		larkws.WithEventHandler(d),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)
	return cli.Start(ctx)
}

// onMessage acks the event immediately and processes it asynchronously:
// Feishu redelivers events that are not handled within 3 seconds.
func (b *Bot) onMessage(ctx context.Context, e *larkim.P2MessageReceiveV1) error {
	if b.outbox != nil {
		if e == nil || e.Event == nil || e.Event.Message == nil || deref(e.Event.Message.MessageId) == "" {
			return errors.New("invalid Feishu event")
		}
		payload, err := json.Marshal(e)
		if err != nil {
			return err
		}
		persistCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return b.outbox.EnqueueInbox(persistCtx, deref(e.Event.Message.MessageId), payload)
	}
	select {
	case b.queue <- e:
	default:
		b.logger.Warn("bot queue full; request Feishu redelivery")
		return errors.New("bot queue full")
	}
	return nil
}

func (b *Bot) handle(e *larkim.P2MessageReceiveV1) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if e.Event == nil || e.Event.Message == nil || e.Event.Sender == nil || e.Event.Sender.SenderId == nil {
		return nil
	}
	if e.EventV2Base == nil || e.EventV2Base.Header == nil || e.EventV2Base.Header.AppID != b.appID {
		b.logger.Warn("ignore event for unexpected app")
		return nil
	}
	msg := e.Event.Message
	sender := e.Event.Sender

	if deref(sender.SenderType) != "user" {
		return nil
	}
	if deref(msg.MessageType) != "text" {
		b.logger.Debug("ignore non-text message", "message_id", deref(msg.MessageId), "type", deref(msg.MessageType))
		return nil
	}
	openID := deref(sender.SenderId.OpenId)
	chatID := deref(msg.ChatId)
	if !b.allowAllUsers && chatID != b.adminChatID {
		b.logger.Warn("ignore message from unauthorized user", "open_id", openID, "chat_id", chatID)
		return nil
	}
	if deref(msg.ChatType) == "group" && b.requireMention && !mentionsBot(msg.Mentions) {
		return nil
	}
	if !b.allow(openID) {
		return b.deliverReply(ctx, deref(msg.MessageId)+":rate-limit", chatID, "请求过于频繁，请稍后再试")
	}
	text, err := extractText(deref(msg.Content))
	if err != nil {
		b.logger.Warn("decode message content", "message_id", deref(msg.MessageId), "err", err)
		return nil
	}
	b.logger.Info("handle command", "open_id", openID, "chat_id", chatID, "message_id", deref(msg.MessageId))
	if cmd, parseErr := command.Parse(text); parseErr == nil && cmd.Action == command.ActionMenu {
		return b.deliverCard(ctx, deref(msg.MessageId)+":menu", chatID, menuCard())
	}

	reply := b.exec.Execute(ctx, interaction.Request{
		Text:           text,
		OperatorOpenID: openID,
		ChatID:         chatID,
		MessageID:      deref(msg.MessageId),
	})
	return b.deliverReply(ctx, deref(msg.MessageId), chatID, reply)
}

func (b *Bot) onCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil || event.Event == nil || event.Event.Operator == nil || event.Event.Action == nil || event.Event.Context == nil {
		return toastResponse("error", "无效的卡片操作"), nil
	}
	if event.EventV2Base.Header.AppID != b.appID {
		b.logger.Warn("ignore card action for unexpected app")
		return toastResponse("error", "应用校验失败"), nil
	}
	openID := event.Event.Operator.OpenID
	chatID := event.Event.Context.OpenChatID
	if openID == "" || chatID == "" {
		return toastResponse("error", "无法识别操作者"), nil
	}
	if !b.allowAllUsers && chatID != b.adminChatID {
		b.logger.Warn("ignore card action from unauthorized user", "open_id", openID, "chat_id", chatID)
		return toastResponse("error", "你不在允许使用范围内"), nil
	}
	if !b.allow(openID) {
		return toastResponse("warning", "操作太频繁，请稍后再试"), nil
	}
	action, _ := event.Event.Action.Value["action"].(string)
	actionIP, _ := event.Event.Action.Value["ip"].(string)
	eventID := event.EventV2Base.Header.EventID
	if eventID == "" {
		eventID = event.Event.Context.OpenMessageID + ":" + action + ":" + actionIP
	}
	switch action {
	case "apply":
		return cardResponse(applyInstructionsCard()), nil
	case "list":
		grants, err := b.userGrants(ctx, openID)
		if err != nil {
			b.logger.Warn("list grants from card", "open_id", openID, "err", err)
			return toastResponse("error", "查询失败，请稍后重试"), nil
		}
		return cardResponse(grantsCard(grants, false)), nil
	case "revoke_menu":
		grants, err := b.userGrants(ctx, openID)
		if err != nil {
			b.logger.Warn("list revocable grants", "open_id", openID, "err", err)
			return toastResponse("error", "查询失败，请稍后重试"), nil
		}
		return cardResponse(grantsCard(grants, true)), nil
	case "revoke":
		if net.ParseIP(actionIP) == nil {
			return toastResponse("error", "无效的 IP 地址"), nil
		}
		reply := b.exec.Execute(ctx, interaction.Request{Text: "撤销授权 " + actionIP, OperatorOpenID: openID, ChatID: chatID, MessageID: eventID})
		grants, err := b.userGrants(ctx, openID)
		if err != nil {
			return toastResponse("warning", reply), nil
		}
		resp := cardResponse(grantsCard(grants, true))
		resp.Toast = &callback.Toast{Type: "success", Content: reply}
		return resp, nil
	case "menu":
		return cardResponse(menuCard()), nil
	default:
		return toastResponse("error", "不支持的操作"), nil
	}
}

func (b *Bot) userGrants(ctx context.Context, openID string) ([]store.Grant, error) {
	if b.outbox == nil {
		return nil, errors.New("grant store unavailable")
	}
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return b.outbox.ListUser(queryCtx, openID)
}

func mentionsBot(mentions []*larkim.MentionEvent) bool {
	for _, mention := range mentions {
		if mention != nil && deref(mention.MentionedType) == "bot" {
			return true
		}
	}
	return false
}

func (b *Bot) deliverReply(ctx context.Context, id, chatID, reply string) error {
	if b.outbox == nil {
		if err := b.sendReply(ctx, id, chatID, reply); err != nil {
			b.logger.Error("send reply failed", "chat_id", chatID, "err", err)
		}
		return nil
	}
	if err := b.outbox.EnqueueReply(ctx, id, chatID, reply); err != nil {
		b.logger.Error("enqueue reply failed", "message_id", id, "err", err)
		return err
	}
	return nil
}

func (b *Bot) deliverCard(ctx context.Context, id, chatID string, card map[string]interface{}) error {
	content, err := json.Marshal(card)
	if err != nil {
		return err
	}
	payload := interactiveReplyPrefix + string(content)
	if b.outbox == nil {
		return b.sendReply(ctx, id, chatID, payload)
	}
	return b.outbox.EnqueueReply(ctx, id, chatID, payload)
}

func (b *Bot) runInbox(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		item, ok, err := b.outbox.ClaimInbox(ctx)
		if err != nil {
			b.logger.Error("claim bot inbox", "err", err)
		} else if ok {
			var event larkim.P2MessageReceiveV1
			err = json.Unmarshal(item.Payload, &event)
			if err == nil {
				err = b.handle(&event)
			}
			if err == nil {
				err = b.outbox.CompleteInbox(ctx, item.ID)
			}
			if err != nil {
				b.logger.Warn("process bot inbox failed", "id", item.ID, "attempt", item.Attempts+1, "err", err)
				_ = b.outbox.RetryInbox(ctx, item.ID, item.Attempts)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Bot) runReplyOutbox(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		b.flushReplies(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Bot) flushReplies(ctx context.Context) {
	items, err := b.outbox.PendingReplies(ctx, 20)
	if err != nil {
		b.logger.Error("load reply outbox", "err", err)
		return
	}
	for _, item := range items {
		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = b.sendReply(sendCtx, item.ID, item.ChatID, item.Text)
		cancel()
		if err == nil {
			if markErr := b.outbox.MarkReplySent(ctx, item.ID); markErr != nil {
				b.logger.Error("mark reply sent", "id", item.ID, "err", markErr)
			}
			continue
		}
		b.logger.Warn("send queued reply failed", "id", item.ID, "attempt", item.Attempts+1, "err", err)
		if retryErr := b.outbox.RetryReply(ctx, item.ID, item.Attempts); retryErr != nil {
			b.logger.Error("schedule reply retry", "id", item.ID, "err", retryErr)
		}
	}
}

func (b *Bot) allow(id string) bool {
	b.rateMu.Lock()
	defer b.rateMu.Unlock()
	now := time.Now()
	cut := now.Add(-time.Minute)
	if now.Sub(b.rateCleanup) >= time.Minute {
		for key, entries := range b.rate {
			if len(entries) == 0 || !entries[len(entries)-1].After(cut) {
				delete(b.rate, key)
			}
		}
		b.rateCleanup = now
	}
	old := b.rate[id]
	n := old[:0]
	for _, t := range old {
		if t.After(cut) {
			n = append(n, t)
		}
	}
	if len(n) >= b.requestsPerMinute {
		if len(n) == 0 {
			delete(b.rate, id)
		} else {
			b.rate[id] = n
		}
		return false
	}
	b.rate[id] = append(n, now)
	return true
}

func extractText(content string) (string, error) {
	var c struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &c); err != nil {
		return "", err
	}
	return c.Text, nil
}

func (b *Bot) sendReply(ctx context.Context, id, chatID, reply string) error {
	msgType := "text"
	var content string
	if strings.HasPrefix(reply, interactiveReplyPrefix) {
		msgType = "interactive"
		content = strings.TrimPrefix(reply, interactiveReplyPrefix)
		if !json.Valid([]byte(content)) {
			return errors.New("invalid interactive card JSON")
		}
	} else {
		encoded, err := json.Marshal(map[string]string{"text": reply})
		if err != nil {
			return err
		}
		content = string(encoded)
	}
	body := larkim.NewCreateMessageReqBodyBuilder().
		ReceiveId(chatID).
		MsgType(msgType).
		Content(content).
		Uuid(deliveryUUID(id)).
		Build()
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(body).
		Build()
	resp, err := b.api.Im.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("im.message.create code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func deliveryUUID(id string) string {
	sum := sha256.Sum256([]byte("frps-gateway/reply/" + id))
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
