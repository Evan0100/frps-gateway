// Package feishu connects to Feishu via the official SDK long-connection
// mode, filters messages by admin and dispatches them to an Executor.
package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"frps-gateway/interaction"
)

// Executor executes a chat command with identity and delivery metadata.
type Executor interface {
	Execute(ctx context.Context, req interaction.Request) string
}

type Bot struct {
	appID             string
	appSecret         string
	adminChatID       string
	adminOpenIDs      map[string]bool
	api               *lark.Client
	exec              Executor
	logger            *slog.Logger
	allowAllUsers     bool
	requireMention    bool
	queue             chan *larkim.P2MessageReceiveV1
	requestsPerMinute int
	rateMu            sync.Mutex
	rate              map[string][]time.Time
	encryptKey        string
	verificationToken string
}

// New creates a Bot. Exactly one of adminChatID / adminOpenIDs must be set
// (enforced by config validation): chat mode accepts every message from that
// chat, open-id mode accepts messages from those users only.
func New(appID, appSecret, adminChatID string, adminOpenIDs []string, exec Executor, logger *slog.Logger) *Bot {
	return NewSecure(appID, appSecret, "", "", adminChatID, adminOpenIDs, false, true, 4, 100, 10, exec, logger)
}
func NewSecure(appID, appSecret, encryptKey, verificationToken, adminChatID string, adminOpenIDs []string, allowAll, requireMention bool, workers, queueSize, rpm int, exec Executor, logger *slog.Logger) *Bot {
	opens := make(map[string]bool, len(adminOpenIDs))
	for _, id := range adminOpenIDs {
		opens[id] = true
	}
	b := &Bot{
		appID:        appID,
		appSecret:    appSecret,
		adminChatID:  adminChatID,
		adminOpenIDs: opens,
		api:          lark.NewClient(appID, appSecret),
		exec:         exec,
		logger:       logger,
		encryptKey:   encryptKey, verificationToken: verificationToken,
		allowAllUsers: allowAll, requireMention: requireMention, queue: make(chan *larkim.P2MessageReceiveV1, queueSize), requestsPerMinute: rpm, rate: map[string][]time.Time{},
	}
	for i := 0; i < workers; i++ {
		go func() {
			for e := range b.queue {
				b.handle(e)
			}
		}()
	}
	return b
}

// Run starts the WebSocket long connection; it blocks until ctx is cancelled
// or the connection fails terminally. No public webhook endpoint is needed.
func (b *Bot) Run(ctx context.Context) error {
	d := dispatcher.NewEventDispatcher(b.verificationToken, b.encryptKey).
		OnP2MessageReceiveV1(b.onMessage)
	cli := larkws.NewClient(b.appID, b.appSecret,
		larkws.WithEventHandler(d),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)
	return cli.Start(ctx)
}

// onMessage acks the event immediately and processes it asynchronously:
// Feishu redelivers events that are not handled within 3 seconds.
func (b *Bot) onMessage(_ context.Context, e *larkim.P2MessageReceiveV1) error {
	select {
	case b.queue <- e:
	default:
		b.logger.Warn("bot queue full; drop event")
	}
	return nil
}

func (b *Bot) handle(e *larkim.P2MessageReceiveV1) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if e.Event == nil || e.Event.Message == nil || e.Event.Sender == nil || e.Event.Sender.SenderId == nil {
		return
	}
	msg := e.Event.Message
	sender := e.Event.Sender

	if deref(sender.SenderType) != "user" {
		return
	}
	if deref(msg.MessageType) != "text" {
		b.logger.Debug("ignore non-text message", "message_id", deref(msg.MessageId), "type", deref(msg.MessageType))
		return
	}
	openID := deref(sender.SenderId.OpenId)
	chatID := deref(msg.ChatId)
	if !b.allowAllUsers && !b.isAdmin(openID, chatID) {
		b.logger.Warn("ignore message from unauthorized user", "open_id", openID, "chat_id", chatID)
		return
	}
	if deref(msg.ChatType) == "group" && b.requireMention && len(msg.Mentions) == 0 {
		return
	}
	if !b.allow(openID) {
		_ = b.sendText(ctx, chatID, "请求过于频繁，请稍后再试")
		return
	}
	text, err := extractText(deref(msg.Content))
	if err != nil {
		b.logger.Warn("decode message content", "message_id", deref(msg.MessageId), "err", err)
		return
	}
	b.logger.Info("handle command", "open_id", openID, "chat_id", chatID, "message_id", deref(msg.MessageId))

	reply := b.exec.Execute(ctx, interaction.Request{
		Text:           text,
		OperatorOpenID: openID,
		ChatID:         chatID,
		MessageID:      deref(msg.MessageId),
	})
	if err := b.sendText(ctx, chatID, reply); err != nil {
		b.logger.Error("send reply failed", "chat_id", chatID, "err", err)
	}
}

func (b *Bot) allow(id string) bool {
	b.rateMu.Lock()
	defer b.rateMu.Unlock()
	now := time.Now()
	cut := now.Add(-time.Minute)
	old := b.rate[id]
	n := old[:0]
	for _, t := range old {
		if t.After(cut) {
			n = append(n, t)
		}
	}
	if len(n) >= b.requestsPerMinute {
		b.rate[id] = n
		return false
	}
	b.rate[id] = append(n, now)
	return true
}

func (b *Bot) isAdmin(openID, chatID string) bool {
	if b.adminChatID != "" {
		return chatID == b.adminChatID
	}
	return b.adminOpenIDs[openID]
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

func (b *Bot) sendText(ctx context.Context, chatID, text string) error {
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	body := larkim.NewCreateMessageReqBodyBuilder().
		ReceiveId(chatID).
		MsgType("text").
		Content(string(content)).
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

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
