package feishu

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/xiaohang2/opencode-handoff/internal/domain"
	"github.com/xiaohang2/opencode-handoff/internal/store"
)

type BindingStore interface {
	GetChannelBinding(context.Context) (domain.ChannelBinding, error)
	BindChannel(context.Context, domain.ChannelBinding) error
}

type Options struct {
	AppID          string
	AppSecret      string
	ChatID         string
	PairingCode    string
	AllowedUsers   []string
	BindingStore   BindingStore
	MaxOutputChars int
}

type Client struct {
	api            *lark.Client
	ws             *larkws.Client
	chatID         string
	pairingCode    string
	bindingStore   BindingStore
	allowed        map[string]struct{}
	paired         chan struct{}
	pairOnce       sync.Once
	replies        chan domain.UserReply
	logger         *slog.Logger
	replyText      func(context.Context, string, string) error
	maxOutputChars int
	mu             sync.Mutex
	started        bool
}

func New(options Options, logger *slog.Logger) *Client {
	replies := make(chan domain.UserReply, 64)
	allowed := make(map[string]struct{}, len(options.AllowedUsers))
	for _, user := range options.AllowedUsers {
		allowed[user] = struct{}{}
	}
	client := &Client{
		api:            lark.NewClient(options.AppID, options.AppSecret),
		chatID:         options.ChatID,
		pairingCode:    strings.ToUpper(options.PairingCode),
		bindingStore:   options.BindingStore,
		allowed:        allowed,
		paired:         make(chan struct{}),
		replies:        replies,
		logger:         logger,
		maxOutputChars: options.MaxOutputChars,
	}
	client.replyText = client.sendTextReply
	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(client.onMessage)
	client.ws = larkws.NewClient(
		options.AppID,
		options.AppSecret,
		larkws.WithEventHandler(handler),
		larkws.WithLogLevel(larkcore.LogLevelWarn),
	)
	return client
}

func (c *Client) SendHandoff(ctx context.Context, handoff domain.Handoff) (domain.MessageRef, error) {
	chatID, err := c.resolveChat(ctx)
	if err != nil {
		return domain.MessageRef{}, err
	}
	content, err := formatHandoffCard(handoff, c.maxOutputChars)
	if err != nil {
		return domain.MessageRef{}, fmt.Errorf("encode Feishu message: %w", err)
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("interactive").
			Content(content).
			Uuid(handoff.ID).
			Build()).
		Build()
	response, err := c.api.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return domain.MessageRef{}, fmt.Errorf("send Feishu message: %w", err)
	}
	if !response.Success() {
		return domain.MessageRef{}, fmt.Errorf("send Feishu message: code=%d message=%s request_id=%s", response.Code, response.Msg, response.RequestId())
	}
	if response.Data == nil || response.Data.MessageId == nil || *response.Data.MessageId == "" {
		return domain.MessageRef{}, errors.New("send Feishu message: response omitted message_id")
	}
	return domain.MessageRef{ChatID: chatID, MessageID: *response.Data.MessageId}, nil
}

func (c *Client) resolveChat(ctx context.Context) (string, error) {
	if c.bindingStore == nil {
		if c.chatID == "" {
			return "", errors.New("Feishu is not paired and no chat_id is configured")
		}
		return c.chatID, nil
	}
	for {
		binding, err := c.bindingStore.GetChannelBinding(ctx)
		if err == nil {
			if c.chatID != "" && binding.ChatID != c.chatID {
				return "", fmt.Errorf("stored Feishu binding %s does not match configured chat_id %s", binding.ChatID, c.chatID)
			}
			return binding.ChatID, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		if c.pairingCode == "" && c.chatID != "" {
			return c.chatID, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-c.paired:
		}
	}
}

func (c *Client) Receive(ctx context.Context) (<-chan domain.UserReply, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil, errors.New("Feishu receiver already started")
	}
	c.started = true
	go func() {
		if err := c.ws.Start(ctx); err != nil && ctx.Err() == nil {
			c.logger.Error("Feishu WebSocket stopped", "error", err)
		}
		close(c.replies)
	}()
	return c.replies, nil
}

func (c *Client) Reply(ctx context.Context, messageID, text string) error {
	return c.sendTextReply(ctx, messageID, text)
}

func (c *Client) onMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Sender == nil {
		return nil
	}
	message := event.Event.Message
	if stringValue(message.MessageType) != "text" {
		return nil
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(stringValue(message.Content)), &content); err != nil {
		c.logger.Warn("ignore malformed Feishu text message", "message_id", stringValue(message.MessageId), "error", err)
		return nil
	}
	senderIDs := senderIdentifiers(event.Event.Sender)
	text := stripMentions(strings.TrimSpace(content.Text), message.Mentions)
	if strings.HasPrefix(strings.ToLower(text), "/bind") {
		return c.handlePairing(ctx, stringValue(message.MessageId), stringValue(message.ChatId), senderIDs, text)
	}
	reply := domain.UserReply{
		MessageID:       stringValue(message.MessageId),
		ParentMessageID: stringValue(message.ParentId),
		ChatID:          stringValue(message.ChatId),
		Text:            text,
		SenderIDs:       senderIDs,
	}
	if len(senderIDs) > 0 {
		reply.SenderID = senderIDs[0]
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.replies <- reply:
		return nil
	}
}

func stripMentions(text string, mentions []*larkim.MentionEvent) string {
	for _, mention := range mentions {
		if mention != nil && mention.Key != nil {
			text = strings.ReplaceAll(text, *mention.Key, "")
		}
	}
	return strings.TrimSpace(text)
}

func (c *Client) handlePairing(ctx context.Context, messageID, chatID string, senderIDs []string, text string) error {
	parts := strings.Fields(text)
	if len(parts) != 2 || c.pairingCode == "" || subtle.ConstantTimeCompare([]byte(strings.ToUpper(parts[1])), []byte(c.pairingCode)) != 1 {
		if c.replyText != nil {
			_ = c.replyText(ctx, messageID, "配对码无效。")
		}
		return nil
	}
	if c.chatID != "" && chatID != c.chatID {
		return c.replyText(ctx, messageID, "该会话不是配置的通知会话。")
	}
	if len(c.allowed) > 0 && !c.senderAllowed(senderIDs) {
		return c.replyText(ctx, messageID, "当前用户不在允许列表中。")
	}
	if c.bindingStore == nil || chatID == "" || len(senderIDs) == 0 {
		return errors.New("cannot persist Feishu pairing without a chat and sender identity")
	}
	err := c.bindingStore.BindChannel(ctx, domain.ChannelBinding{
		ChatID:    chatID,
		UserIDs:   senderIDs,
		CreatedAt: time.Now().UTC(),
	})
	if errors.Is(err, store.ErrAlreadyBound) {
		return c.replyText(ctx, messageID, "机器人已经绑定到其他会话。")
	}
	if err != nil {
		return err
	}
	c.pairOnce.Do(func() { close(c.paired) })
	c.logger.Info("Feishu pairing completed", "chat_id", chatID, "sender_id", senderIDs[0])
	return c.replyText(ctx, messageID, "OpenCode Handoff 配对成功。")
}

func (c *Client) senderAllowed(senderIDs []string) bool {
	for _, senderID := range senderIDs {
		if _, ok := c.allowed[senderID]; ok {
			return true
		}
	}
	return false
}

func (c *Client) sendTextReply(ctx context.Context, messageID, text string) error {
	content, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	if err != nil {
		return err
	}
	request := larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("text").
			Content(string(content)).
			Build()).
		Build()
	response, err := c.api.Im.V1.Message.Reply(ctx, request)
	if err != nil {
		return fmt.Errorf("reply to Feishu message: %w", err)
	}
	if !response.Success() {
		return fmt.Errorf("reply to Feishu message: code=%d message=%s request_id=%s", response.Code, response.Msg, response.RequestId())
	}
	return nil
}

func senderIdentifiers(sender *larkim.EventSender) []string {
	if sender == nil || sender.SenderId == nil {
		return nil
	}
	var result []string
	for _, value := range []*string{sender.SenderId.OpenId, sender.SenderId.UserId, sender.SenderId.UnionId} {
		if identifier := stringValue(value); identifier != "" {
			result = append(result, identifier)
		}
	}
	return result
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type handoffCard struct {
	Schema string            `json:"schema"`
	Config handoffCardConfig `json:"config"`
	Body   handoffCardBody   `json:"body"`
}

type handoffCardConfig struct {
	UpdateMulti bool   `json:"update_multi"`
	WidthMode   string `json:"width_mode"`
}

type handoffCardBody struct {
	Direction string               `json:"direction"`
	Padding   string               `json:"padding"`
	Elements  []handoffCardElement `json:"elements"`
}

type handoffCardElement struct {
	Tag             string                  `json:"tag"`
	Content         string                  `json:"content,omitempty"`
	Expanded        *bool                   `json:"expanded,omitempty"`
	Header          *handoffCardPanelHeader `json:"header,omitempty"`
	Border          *handoffCardBorder      `json:"border,omitempty"`
	VerticalSpacing string                  `json:"vertical_spacing,omitempty"`
	Padding         string                  `json:"padding,omitempty"`
	Elements        []handoffCardElement    `json:"elements,omitempty"`
}

type handoffCardPanelHeader struct {
	Title             handoffCardText  `json:"title"`
	VerticalAlign     string           `json:"vertical_align,omitempty"`
	Icon              *handoffCardIcon `json:"icon,omitempty"`
	IconPosition      string           `json:"icon_position,omitempty"`
	IconExpandedAngle int              `json:"icon_expanded_angle,omitempty"`
}

type handoffCardText struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

type handoffCardIcon struct {
	Tag   string `json:"tag"`
	Token string `json:"token"`
	Size  string `json:"size"`
}

type handoffCardBorder struct {
	Color        string `json:"color"`
	CornerRadius string `json:"corner_radius"`
}

func formatHandoffCard(handoff domain.Handoff, maxOutputChars int) (string, error) {
	title := "✅ OpenCode · Task Finished"
	if handoff.Type == domain.HandoffError {
		title = "🚨 OpenCode · Interrupted"
	}
	sessionName := strings.TrimSpace(handoff.SessionName)
	if sessionName == "" {
		sessionName = "Untitled Session"
	}
	metadata := fmt.Sprintf("🆔 Session ID: %s\n🏷️ Session Name: %s", handoff.SessionID, sessionName)
	status := fmt.Sprintf("%s\n📁 Project: %s", title, handoff.ProjectName)
	elements := []handoffCardElement{
		{Tag: "markdown", Content: metadata},
		{Tag: "hr"},
		{Tag: "markdown", Content: status},
	}
	if handoff.ErrorText != "" {
		elements = append(elements, handoffCardElement{
			Tag:     "markdown",
			Content: "**⚠️ 执行异常**\n\n" + handoff.ErrorText,
		})
	}
	if handoff.LastAssistantText != "" {
		expanded := false
		elements = append(elements, handoffCardElement{
			Tag:      "collapsible_panel",
			Expanded: &expanded,
			Header: &handoffCardPanelHeader{Title: handoffCardText{
				Tag:     "plain_text",
				Content: fmt.Sprintf("💬 最后输出（%d）", maxOutputChars),
			},
				VerticalAlign: "center",
				Icon: &handoffCardIcon{
					Tag:   "standard_icon",
					Token: "down-small-ccm_outlined",
					Size:  "16px 16px",
				},
				IconPosition:      "right",
				IconExpandedAngle: -180,
			},
			Border: &handoffCardBorder{
				Color:        "grey",
				CornerRadius: "5px",
			},
			VerticalSpacing: "8px",
			Padding:         "8px 8px 8px 8px",
			Elements: []handoffCardElement{{
				Tag:     "markdown",
				Content: handoff.LastAssistantText,
			}},
		})
	}
	if handoff.Type == domain.HandoffError {
		elements = append(elements, handoffCardElement{
			Tag:     "markdown",
			Content: "↩️ 引用回复本消息可继续原 Session",
		})
	}
	content, err := json.Marshal(handoffCard{
		Schema: "2.0",
		Config: handoffCardConfig{
			UpdateMulti: true,
			WidthMode:   "fill",
		},
		Body: handoffCardBody{
			Direction: "vertical",
			Padding:   "12px 12px 12px 12px",
			Elements:  elements,
		},
	})
	if err != nil {
		return "", err
	}
	return string(content), nil
}
