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
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
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
	sendCard       func(context.Context, string, string, string) (*larkim.CreateMessageResp, error)
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
	client.sendCard = client.sendInteractiveMessage
	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(client.onMessage).
		OnP2CardActionTrigger(client.onCardAction)
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
	response, err := c.sendCard(ctx, chatID, handoff.ID, content)
	if err != nil {
		return domain.MessageRef{}, err
	}
	if handoff.Type == domain.HandoffQuestion && isInvalidCardResponse(response) {
		if c.logger != nil {
			c.logger.Warn("Feishu rejected the question form; retrying without the custom-answer input", "code", response.Code, "message", response.Msg)
		}
		content, err = formatQuestionCard(handoff, false)
		if err != nil {
			return domain.MessageRef{}, fmt.Errorf("encode fallback Feishu message: %w", err)
		}
		response, err = c.sendCard(ctx, chatID, handoff.ID+":fallback", content)
		if err != nil {
			return domain.MessageRef{}, err
		}
	}
	if !response.Success() {
		return domain.MessageRef{}, fmt.Errorf("send Feishu message: code=%d message=%s request_id=%s", response.Code, response.Msg, response.RequestId())
	}
	if response.Data == nil || response.Data.MessageId == nil || *response.Data.MessageId == "" {
		return domain.MessageRef{}, errors.New("send Feishu message: response omitted message_id")
	}
	return domain.MessageRef{ChatID: chatID, MessageID: *response.Data.MessageId}, nil
}

func (c *Client) sendInteractiveMessage(ctx context.Context, chatID, id, content string) (*larkim.CreateMessageResp, error) {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("interactive").
			Content(content).
			Uuid(id).
			Build()).
		Build()
	response, err := c.api.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("send Feishu message: %w", err)
	}
	return response, nil
}

func isInvalidCardResponse(response *larkim.CreateMessageResp) bool {
	if response == nil || response.Code != 230099 {
		return false
	}
	message := strings.ToLower(response.Msg)
	return strings.Contains(message, "200621") || strings.Contains(message, "parse card json") || strings.Contains(message, "failed to create card content")
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

func (c *Client) onCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil || event.Event.Context == nil || event.Event.Operator == nil {
		return cardToast("error", "无效的卡片操作"), nil
	}
	action := stringMapValue(event.Event.Action.Value, "action")
	if action != "question_reply" && action != "question_custom_reply" && action != "question_reject" {
		return cardToast("error", "不支持的卡片操作"), nil
	}
	messageID := ""
	if event.EventV2Base != nil && event.EventV2Base.Header != nil {
		messageID = event.EventV2Base.Header.EventID
	}
	if messageID == "" {
		messageID = fmt.Sprintf("card:%s:%d", event.Event.Context.OpenMessageID, time.Now().UnixNano())
	}
	userIDs := []string{event.Event.Operator.OpenID}
	if event.Event.Operator.UserID != nil {
		userIDs = append(userIDs, *event.Event.Operator.UserID)
	}
	reply := domain.UserReply{
		MessageID:       messageID,
		ParentMessageID: event.Event.Context.OpenMessageID,
		ChatID:          event.Event.Context.OpenChatID,
		SenderID:        event.Event.Operator.OpenID,
		SenderIDs:       compactStrings(userIDs),
		RejectQuestion:  action == "question_reject",
		CardAction:      true,
		Result:          make(chan error, 1),
	}
	if action == "question_reply" {
		encoded, err := json.Marshal(event.Event.Action.Value["answers"])
		if err != nil || json.Unmarshal(encoded, &reply.QuestionAnswers) != nil {
			return cardToast("error", "选项数据无效"), nil
		}
	} else if action == "question_custom_reply" {
		answer := strings.TrimSpace(stringMapValue(event.Event.Action.FormValue, "custom_answer"))
		if answer == "" {
			return cardToast("error", "请输入自定义答案"), nil
		}
		reply.QuestionAnswers = [][]string{{answer}}
	}
	select {
	case <-ctx.Done():
		return cardToast("error", "操作已取消，请重试"), nil
	case c.replies <- reply:
	}
	select {
	case err := <-reply.Result:
		if err != nil {
			return cardToast("error", err.Error()), nil
		}
		if reply.RejectQuestion {
			return cardToast("success", "已忽略该问题"), nil
		}
		return cardToast("success", "答案已提交到原 Session"), nil
	case <-ctx.Done():
		return cardToast("warning", "答案正在提交，请稍后查看 OpenCode"), nil
	case <-time.After(5 * time.Second):
		return cardToast("warning", "答案正在提交，请稍后查看 OpenCode"), nil
	}
}

func cardToast(kind, content string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: kind, Content: content}}
}

func stringMapValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func compactStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
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
	Columns         []handoffCardElement    `json:"columns,omitempty"`
	Text            *handoffCardText        `json:"text,omitempty"`
	Label           *handoffCardText        `json:"label,omitempty"`
	Placeholder     *handoffCardText        `json:"placeholder,omitempty"`
	Type            string                  `json:"type,omitempty"`
	Size            string                  `json:"size,omitempty"`
	Width           string                  `json:"width,omitempty"`
	Weight          int                     `json:"weight,omitempty"`
	Name            string                  `json:"name,omitempty"`
	ActionType      string                  `json:"action_type,omitempty"`
	InputType       string                  `json:"input_type,omitempty"`
	Rows            int                     `json:"rows,omitempty"`
	MaxLength       int                     `json:"max_length,omitempty"`
	Required        *bool                   `json:"required,omitempty"`
	Behaviors       []handoffCardBehavior   `json:"behaviors,omitempty"`
}

type handoffCardBehavior struct {
	Type  string         `json:"type"`
	Value map[string]any `json:"value"`
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
	if handoff.Type == domain.HandoffQuestion {
		return formatQuestionCard(handoff, true)
	}
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

func formatQuestionCard(handoff domain.Handoff, includeCustomForm bool) (string, error) {
	sessionName := strings.TrimSpace(handoff.SessionName)
	if sessionName == "" {
		sessionName = "Untitled Session"
	}
	elements := []handoffCardElement{
		{Tag: "markdown", Content: fmt.Sprintf("🆔 Session ID: %s\n🏷️ Session Name: %s", handoff.SessionID, sessionName)},
		{Tag: "hr"},
		{Tag: "markdown", Content: fmt.Sprintf("❓ **OpenCode · 等待选择**\n📁 Project: %s", handoff.ProjectName)},
	}
	for index, question := range handoff.Questions {
		header := strings.TrimSpace(question.Header)
		if header == "" {
			header = fmt.Sprintf("问题 %d", index+1)
		}
		elements = append(elements, handoffCardElement{Tag: "markdown", Content: fmt.Sprintf("**%s**\n%s", header, question.Text)})
		for optionIndex, option := range question.Options {
			label := fmt.Sprintf("**%d. %s**", optionIndex+1, option.Label)
			if option.Description != "" {
				label += "\n" + option.Description
			}
			if len(handoff.Questions) == 1 && !question.Multiple {
				elements = append(elements,
					handoffCardElement{Tag: "markdown", Content: label},
					questionButton(fmt.Sprintf("选择 %d", optionIndex+1), map[string]any{
						"action": "question_reply", "answers": [][]string{{option.Label}},
					}, "default"),
				)
			} else {
				elements = append(elements, handoffCardElement{Tag: "markdown", Content: label})
			}
		}
	}
	if includeCustomForm && len(handoff.Questions) == 1 && handoff.Questions[0].AllowsCustom() {
		elements = append(elements, customAnswerForm())
	}
	elements = append(elements, handoffCardElement{Tag: "markdown", Content: questionReplyFallback(handoff.Questions)})
	elements = append(elements, questionButton("忽略", map[string]any{"action": "question_reject"}, "danger"))
	content, err := json.Marshal(handoffCard{
		Schema: "2.0",
		Config: handoffCardConfig{UpdateMulti: true, WidthMode: "fill"},
		Body:   handoffCardBody{Direction: "vertical", Padding: "12px 12px 12px 12px", Elements: elements},
	})
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func questionReplyFallback(questions []domain.Question) string {
	instruction := "↩️ 回调不可用时可引用回复本消息："
	if len(questions) > 1 {
		instruction += "每题一行，单选填序号，多选用逗号分隔"
	} else if len(questions) == 1 && questions[0].Multiple {
		instruction += "用逗号分隔多个选项序号"
	} else {
		instruction += "填写选项序号"
	}
	if len(questions) == 1 && questions[0].AllowsCustom() {
		instruction += "，或直接输入自定义答案"
	}
	return instruction + "；回复“忽略”可跳过。"
}

func questionButton(label string, value map[string]any, buttonType string) handoffCardElement {
	return buttonColumn(callbackButton(label, value, buttonType))
}

func buttonColumn(button handoffCardElement) handoffCardElement {
	return handoffCardElement{
		Tag: "column_set",
		Columns: []handoffCardElement{{
			Tag: "column", Width: "weighted", Weight: 1,
			Elements: []handoffCardElement{button},
		}},
	}
}

func callbackButton(label string, value map[string]any, buttonType string) handoffCardElement {
	return handoffCardElement{
		Tag: "button", Text: &handoffCardText{Tag: "plain_text", Content: label},
		Type: buttonType, Size: "medium",
		Behaviors: []handoffCardBehavior{{Type: "callback", Value: value}},
	}
}

func customAnswerForm() handoffCardElement {
	required := true
	submit := callbackButton("提交自定义答案", map[string]any{"action": "question_custom_reply"}, "primary")
	submit.Name = "custom_submit"
	submit.ActionType = "form_submit"
	return handoffCardElement{
		Tag: "form", Name: "custom_answer_form",
		Elements: []handoffCardElement{
			{
				Tag: "input", Name: "custom_answer", InputType: "multiline_text", Rows: 3, MaxLength: 1000, Required: &required,
				Label:       &handoffCardText{Tag: "plain_text", Content: "输入自己的答案"},
				Placeholder: &handoffCardText{Tag: "plain_text", Content: "请输入答案"},
			},
			submit,
		},
	}
}
