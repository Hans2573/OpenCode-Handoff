package feishu

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/store"
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

type Health struct {
	State           string
	Message         string
	PairingRequired bool
	PairingCode     string
}

const handoffSeparatorQuietPeriod = 10 * time.Second

type Client struct {
	api            *lark.Client
	ws             *larkws.Client
	sendCard       func(context.Context, string, string, string) (*larkim.CreateMessageResp, error)
	sendText       func(context.Context, string, string, string) error
	now            func() time.Time
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
	separatorMu    sync.Mutex
	lastHandoffAt  map[string]time.Time
	healthMu       sync.RWMutex
	health         Health
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
		lastHandoffAt:  make(map[string]time.Time),
		health: Health{
			State:           "stopped",
			Message:         "飞书监听未启动",
			PairingRequired: options.PairingCode != "",
			PairingCode:     strings.ToUpper(options.PairingCode),
		},
	}
	client.replyText = client.sendTextReply
	client.sendCard = client.sendInteractiveMessage
	client.sendText = client.sendTextMessage
	client.now = time.Now
	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(client.onMessage).
		OnP2CardActionTrigger(client.onCardAction)
	client.ws = larkws.NewClient(
		options.AppID,
		options.AppSecret,
		larkws.WithEventHandler(handler),
		larkws.WithLogLevel(larkcore.LogLevelWarn),
		larkws.WithOnReady(func() { client.setHealth("connected", "飞书 WebSocket 已连接") }),
		larkws.WithOnReconnecting(func() { client.setHealth("reconnecting", "飞书 WebSocket 正在重连") }),
		larkws.WithOnReconnected(func() { client.setHealth("connected", "飞书 WebSocket 已重新连接") }),
		larkws.WithOnDisconnected(func() { client.setHealth("disconnected", "飞书 WebSocket 已断开") }),
		larkws.WithOnError(func(err error) { client.setHealth("error", err.Error()) }),
	)
	return client
}

func (c *Client) setHealth(state, message string) {
	c.healthMu.Lock()
	c.health.State = state
	c.health.Message = message
	c.healthMu.Unlock()
}

func (c *Client) markPaired() {
	c.healthMu.Lock()
	c.health.PairingRequired = false
	c.health.PairingCode = ""
	c.healthMu.Unlock()
}

func (c *Client) Health() Health {
	c.healthMu.RLock()
	defer c.healthMu.RUnlock()
	return c.health
}

func (c *Client) SendHandoff(ctx context.Context, handoff domain.Handoff) (domain.MessageRef, error) {
	chatID, err := c.resolveChat(ctx)
	if err != nil {
		return domain.MessageRef{}, err
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	if c.sendText != nil && c.beginHandoffBatch(handoff.SessionID, now) {
		separator := fmt.Sprintf("=== %s ===", now.Format("2006-01-02 15:04:05"))
		if err := c.sendText(ctx, chatID, handoff.ID+":separator", separator); err != nil && c.logger != nil {
			c.logger.Warn("send Feishu notification separator", "handoff_id", handoff.ID, "error", err)
		}
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

func (c *Client) beginHandoffBatch(sessionID string, now time.Time) bool {
	c.separatorMu.Lock()
	defer c.separatorMu.Unlock()
	if c.lastHandoffAt == nil {
		c.lastHandoffAt = make(map[string]time.Time)
	}
	last, exists := c.lastHandoffAt[sessionID]
	c.lastHandoffAt[sessionID] = now
	return !exists || now.Before(last) || now.Sub(last) >= handoffSeparatorQuietPeriod
}

func (c *Client) sendTextMessage(ctx context.Context, chatID, id, text string) error {
	content, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	if err != nil {
		return fmt.Errorf("encode Feishu text message: %w", err)
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("text").
			Content(string(content)).
			Uuid(id).
			Build()).
		Build()
	response, err := c.api.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("send Feishu text message: %w", err)
	}
	if !response.Success() {
		return fmt.Errorf("send Feishu text message: code=%d message=%s request_id=%s", response.Code, response.Msg, response.RequestId())
	}
	return nil
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
	c.setHealth("connecting", "正在连接飞书 WebSocket")
	go func() {
		if err := c.ws.Start(ctx); err != nil && ctx.Err() == nil {
			c.setHealth("error", err.Error())
			c.logger.Error("Feishu WebSocket stopped", "error", err)
		} else if ctx.Err() != nil {
			c.setHealth("stopped", "飞书监听已停止")
		}
		close(c.replies)
	}()
	return c.replies, nil
}

func (c *Client) Reply(ctx context.Context, messageID, text string) error {
	return c.sendTextReply(ctx, messageID, text)
}

func (c *Client) ReplyWithRef(ctx context.Context, messageID, text string) (domain.MessageRef, error) {
	return c.sendTextReplyWithRef(ctx, messageID, text)
}

func (c *Client) ReplyProjects(ctx context.Context, messageID string, page domain.ProjectPage) error {
	content, err := formatProjectCard(page)
	if err != nil {
		return fmt.Errorf("encode project card: %w", err)
	}
	return c.sendCardReply(ctx, messageID, content)
}

func (c *Client) ReplyModels(ctx context.Context, messageID string, page domain.ModelPage) error {
	content, err := formatModelCard(page)
	if err != nil {
		return fmt.Errorf("encode model card: %w", err)
	}
	err = c.sendCardReply(ctx, messageID, content)
	if err == nil || !page.Home || !isInvalidCardReplyError(err) {
		return err
	}
	if c.logger != nil {
		c.logger.Warn("Feishu rejected the model search form; retrying without the search input", "error", err)
	}
	content, encodeErr := formatModelCardWithoutSearch(page)
	if encodeErr != nil {
		return fmt.Errorf("encode fallback model card: %w", encodeErr)
	}
	return c.sendCardReply(ctx, messageID, content)
}

func (c *Client) ReplyModelVariants(ctx context.Context, messageID string, page domain.ModelVariantPage) error {
	content, err := formatModelVariantCard(page)
	if err != nil {
		return fmt.Errorf("encode model variant card: %w", err)
	}
	return c.sendCardReply(ctx, messageID, content)
}

func (c *Client) ReplyRunningSessions(ctx context.Context, messageID string, running domain.RunningSessions) error {
	content, err := formatRunningSessionsCard(running)
	if err != nil {
		return fmt.Errorf("encode running sessions card: %w", err)
	}
	return c.sendCardReply(ctx, messageID, content)
}

func (c *Client) ReplyAssistantOutput(ctx context.Context, messageID string, detail domain.AssistantOutputDetail) error {
	content, err := formatAssistantOutputCard(detail)
	if err != nil {
		return fmt.Errorf("encode assistant output card: %w", err)
	}
	return c.sendCardReply(ctx, messageID, content)
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
	if isHelpCommand(text) {
		if c.replyText == nil {
			return errors.New("Feishu text reply is not configured")
		}
		return c.replyText(ctx, stringValue(message.MessageId), helpMessage)
	}
	reply := domain.UserReply{
		MessageID:       stringValue(message.MessageId),
		ParentMessageID: stringValue(message.ParentId),
		ChatID:          stringValue(message.ChatId),
		Text:            text,
		SenderIDs:       senderIDs,
		AbortSession:    isAbortCommand(text),
	}
	if page, ok := parseProjectCommand(text); ok {
		reply.ListProjects = true
		reply.ProjectPage = page
	}
	if isRunningCommand(text) {
		reply.ListRunning = true
	}
	if query, page, ok := parseModelsCommand(text); ok && (strings.HasPrefix(strings.TrimSpace(text), "/") || reply.ParentMessageID == "") {
		reply.ListModels = true
		reply.ModelPage = page
		reply.ModelQuery = query
		reply.ModelContext = domain.ModelContext{Target: domain.ModelTargetBrowse}
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

func isHelpCommand(text string) bool {
	return strings.EqualFold(strings.TrimSpace(text), "/help")
}

func isAbortCommand(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "/stop":
		return true
	default:
		return false
	}
}

func parseProjectCommand(text string) (int, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.EqualFold(fields[0], "/project") {
		return 0, false
	}
	if len(fields) == 1 {
		return 1, true
	}
	if len(fields) != 2 {
		return 1, true
	}
	page, err := strconv.Atoi(fields[1])
	if err != nil || page < 1 {
		return 1, true
	}
	return page, true
}

func isRunningCommand(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "/running", "/r":
		return true
	default:
		return false
	}
}

func parseModelsCommand(text string) (string, int, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || (!strings.EqualFold(fields[0], "/models") && !strings.EqualFold(fields[0], "models")) {
		return "", 0, false
	}
	if len(fields) == 1 {
		return "", 0, true
	}
	page := 1
	queryFields := fields[1:]
	if parsed, err := strconv.Atoi(queryFields[len(queryFields)-1]); err == nil && parsed > 0 {
		page = parsed
		queryFields = queryFields[:len(queryFields)-1]
	}
	return strings.Join(queryFields, " "), page, true
}

const helpMessage = `OpenCode Handoff 使用说明

1. 继续任务
引用对应的 Handoff、Question 或 Permission 消息，直接回复内容，或点击卡片按钮。

2. 中断任务
引用对应的 Handoff 消息，回复 /stop。

3. 绑定会话
首次使用时，按服务日志中的提示发送 /bind <配对码>。

4. 项目与新 Session
发送 /project 查看项目；点击“新建 Session”后先选择模型。模型会在第一次引用回复任务时生效。

5. 查看运行中的 Session
发送 /running（或 /r），查看状态、当前模型和距离上次用户输入的时长；可选择模型，从下一条飞书任务起生效。

6. 查看可用模型
发送 /models 查看最近使用与 Provider 分组；发送 /models <关键词> 可按 Provider、模型名称或模型 ID 搜索。

7. 获取帮助
发送 /help。`

func (c *Client) onCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil || event.Event.Context == nil || event.Event.Operator == nil {
		return cardToast("error", "无效的卡片操作"), nil
	}
	action := stringMapValue(event.Event.Action.Value, "action")
	if action != "question_reply" && action != "question_custom_reply" && action != "question_reject" && action != "permission_reply" && action != "project_page" && action != "project_create" && action != "model_home" && action != "model_all" && action != "model_search" && action != "model_provider" && action != "model_page" && action != "model_variants" && action != "model_apply" && action != "session_models" && action != "assistant_output" && action != "goal_complete" && action != "goal_continue" {
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
	} else if action == "permission_reply" {
		decision := strings.ToLower(strings.TrimSpace(stringMapValue(event.Event.Action.Value, "decision")))
		if decision != "once" && decision != "always" && decision != "reject" {
			return cardToast("error", "权限决定无效"), nil
		}
		reply.PermissionReply = decision
	} else if action == "goal_complete" {
		reply.GoalComplete = true
	} else if action == "goal_continue" {
		reply.GoalContinue = true
	} else if action == "project_page" {
		reply.ListProjects = true
		reply.ProjectPage = intMapValue(event.Event.Action.Value, "page")
		if reply.ProjectPage < 1 {
			return cardToast("error", "项目页码无效"), nil
		}
	} else if action == "project_create" {
		reply.ListModels = true
		reply.ModelPage = 0
		reply.ProjectDirectory = strings.TrimSpace(stringMapValue(event.Event.Action.Value, "directory"))
		if reply.ProjectDirectory == "" {
			return cardToast("error", "项目目录无效"), nil
		}
		reply.ModelContext = domain.ModelContext{Target: domain.ModelTargetCreate, ProjectDirectory: reply.ProjectDirectory}
	} else if action == "model_home" {
		reply.ListModels = true
		reply.ModelPage = 0
		reply.ModelContext = modelContextFromAction(event.Event.Action.Value)
	} else if action == "model_all" {
		reply.ListModels = true
		reply.ModelPage = 1
		reply.ModelContext = modelContextFromAction(event.Event.Action.Value)
	} else if action == "model_search" {
		reply.ListModels = true
		reply.ModelPage = 1
		reply.ModelQuery = strings.TrimSpace(stringMapValue(event.Event.Action.FormValue, "model_query"))
		reply.ModelContext = modelContextFromAction(event.Event.Action.Value)
		if reply.ModelQuery == "" {
			return cardToast("error", "请输入模型关键词"), nil
		}
	} else if action == "model_provider" {
		reply.ListModels = true
		reply.ModelPage = 1
		reply.ModelProviderID = strings.TrimSpace(stringMapValue(event.Event.Action.Value, "filter_provider"))
		reply.ModelContext = modelContextFromAction(event.Event.Action.Value)
		if reply.ModelProviderID == "" {
			return cardToast("error", "Provider 信息无效"), nil
		}
	} else if action == "model_page" {
		reply.ListModels = true
		reply.ModelPage = intMapValue(event.Event.Action.Value, "page")
		reply.ModelQuery = strings.TrimSpace(stringMapValue(event.Event.Action.Value, "query"))
		reply.ModelProviderID = strings.TrimSpace(stringMapValue(event.Event.Action.Value, "filter_provider"))
		if reply.ModelPage < 1 {
			return cardToast("error", "模型页码无效"), nil
		}
		reply.ModelContext = modelContextFromAction(event.Event.Action.Value)
	} else if action == "session_models" {
		reply.ListModels = true
		reply.ModelPage = 0
		reply.ModelContext = modelContextFromAction(event.Event.Action.Value)
		if reply.ModelContext.Target != domain.ModelTargetSwitch || reply.ModelContext.SessionID == "" || reply.ModelContext.ProjectDirectory == "" {
			return cardToast("error", "Session 信息无效"), nil
		}
	} else if action == "model_variants" {
		reply.ListModelVariants = true
		reply.ModelContext = modelContextFromAction(event.Event.Action.Value)
		reply.ProviderID = strings.TrimSpace(stringMapValue(event.Event.Action.Value, "provider_id"))
		reply.ModelID = strings.TrimSpace(stringMapValue(event.Event.Action.Value, "model_id"))
	} else if action == "model_apply" {
		reply.ApplyModel = true
		reply.ModelContext = modelContextFromAction(event.Event.Action.Value)
		reply.ProviderID = strings.TrimSpace(stringMapValue(event.Event.Action.Value, "provider_id"))
		reply.ModelID = strings.TrimSpace(stringMapValue(event.Event.Action.Value, "model_id"))
		reply.ModelVariant = strings.TrimSpace(stringMapValue(event.Event.Action.Value, "variant"))
	} else if action == "assistant_output" {
		reply.ViewOutput = true
		reply.HandoffID = strings.TrimSpace(stringMapValue(event.Event.Action.Value, "handoff_id"))
		if reply.HandoffID == "" {
			return cardToast("error", "详细答复信息无效"), nil
		}
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
		switch reply.PermissionReply {
		case "once":
			return cardToast("success", "已允许本次操作"), nil
		case "always":
			return cardToast("success", "已设置始终允许"), nil
		case "reject":
			return cardToast("success", "已拒绝权限请求"), nil
		}
		if reply.ListProjects {
			return cardToast("success", "项目列表已发送"), nil
		}
		if reply.ListModels || reply.ListModelVariants {
			return cardToast("success", "模型选择已发送"), nil
		}
		if reply.ViewOutput {
			return cardToast("success", "详细答复已发送"), nil
		}
		if reply.ApplyModel && reply.ModelContext.Target == domain.ModelTargetSwitch {
			return cardToast("success", "模型将在下一条飞书任务中生效"), nil
		}
		if reply.ApplyModel && reply.ModelContext.Target == domain.ModelTargetCreate {
			return cardToast("success", "OpenCode Session 已创建，模型将在第一条任务中生效"), nil
		}
		if reply.CreateSession {
			return cardToast("success", "OpenCode Session 已创建"), nil
		}
		if reply.GoalComplete {
			return cardToast("success", "Goal 已确认完成"), nil
		}
		if reply.GoalContinue {
			return cardToast("success", "Goal 已在原 Session 中继续"), nil
		}
		return cardToast("success", "答案已提交到原 Session"), nil
	case <-ctx.Done():
		return cardToast("warning", "答案正在提交，请稍后查看 OpenCode"), nil
	case <-time.After(5 * time.Second):
		return cardToast("warning", "答案正在提交，请稍后查看 OpenCode"), nil
	}
}

func modelContextFromAction(values map[string]any) domain.ModelContext {
	return domain.ModelContext{
		Target:           domain.ModelTarget(strings.TrimSpace(stringMapValue(values, "target"))),
		ProjectDirectory: strings.TrimSpace(stringMapValue(values, "directory")),
		SessionID:        strings.TrimSpace(stringMapValue(values, "session_id")),
		SessionName:      strings.TrimSpace(stringMapValue(values, "session_name")),
	}
}

func cardToast(kind, content string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: kind, Content: content}}
}

func stringMapValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func intMapValue(values map[string]any, key string) int {
	switch value := values[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		parsed, _ := strconv.Atoi(value)
		return parsed
	default:
		return 0
	}
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
	c.markPaired()
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
	_, err := c.sendTextReplyWithRef(ctx, messageID, text)
	return err
}

func (c *Client) sendTextReplyWithRef(ctx context.Context, messageID, text string) (domain.MessageRef, error) {
	content, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	if err != nil {
		return domain.MessageRef{}, err
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
		return domain.MessageRef{}, fmt.Errorf("reply to Feishu message: %w", err)
	}
	if !response.Success() {
		return domain.MessageRef{}, fmt.Errorf("reply to Feishu message: code=%d message=%s request_id=%s", response.Code, response.Msg, response.RequestId())
	}
	if response.Data == nil || response.Data.MessageId == nil || strings.TrimSpace(*response.Data.MessageId) == "" {
		return domain.MessageRef{}, errors.New("reply to Feishu message: response omitted message_id")
	}
	ref := domain.MessageRef{MessageID: strings.TrimSpace(*response.Data.MessageId)}
	if response.Data.ChatId != nil {
		ref.ChatID = strings.TrimSpace(*response.Data.ChatId)
	}
	return ref, nil
}

func (c *Client) sendCardReply(ctx context.Context, messageID, content string) error {
	request := larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("interactive").
			Content(content).
			Build()).
		Build()
	response, err := c.api.Im.V1.Message.Reply(ctx, request)
	if err != nil {
		return fmt.Errorf("reply card to Feishu message: %w", err)
	}
	if !response.Success() {
		return fmt.Errorf("reply card to Feishu message: code=%d message=%s request_id=%s", response.Code, response.Msg, response.RequestId())
	}
	return nil
}

func isInvalidCardReplyError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "code=230099") || strings.Contains(message, "200621") ||
		strings.Contains(message, "parse card json") || strings.Contains(message, "failed to create card content")
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
	if handoff.Type == domain.HandoffPermission {
		return formatPermissionCard(handoff)
	}
	if handoff.Type == domain.HandoffSession {
		return formatCreatedSessionCard(handoff)
	}
	title := "✅ OpenCode · Task Finished"
	if handoff.Type == domain.HandoffError {
		title = "🚨 OpenCode · Interrupted"
	} else if handoff.Type == domain.HandoffGoalCompletion {
		title = "🎯 Goal Loop · 等待完成确认"
	} else if handoff.Type == domain.HandoffGoalStatus {
		title = "♾️ Goal Loop · 状态更新"
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
		preview, omitted := tailOutputPreview(handoff.LastAssistantText, maxOutputChars)
		expanded := false
		panelContent := preview
		if omitted > 0 {
			panelContent = fmt.Sprintf("*已省略前 %d 字；可点击下方按钮查看详细答复。*\n\n%s", omitted, preview)
		}
		elements = append(elements, handoffCardElement{
			Tag:      "collapsible_panel",
			Expanded: &expanded,
			Header: &handoffCardPanelHeader{Title: handoffCardText{
				Tag:     "plain_text",
				Content: fmt.Sprintf("💬 最后输出（末尾 %d 字）", maxOutputChars),
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
				Content: panelContent,
			}},
		})
	}
	buttons := make([]handoffCardElement, 0, 2)
	if len([]rune(strings.TrimSpace(handoff.LastAssistantText))) > maxOutputChars {
		buttons = append(buttons, callbackButton("查看详细答复", map[string]any{
			"action": "assistant_output", "handoff_id": handoff.ID,
		}, "primary"))
	}
	if handoff.Type == domain.HandoffGoalCompletion {
		buttons = append(buttons,
			callbackButton("✅ 确认完成", map[string]any{"action": "goal_complete"}, "primary"),
			callbackButton("▶️ 继续 Goal", map[string]any{"action": "goal_continue"}, "default"),
		)
	} else if handoff.Type != domain.HandoffGoalStatus {
		buttons = append(buttons, callbackButton("切换模型（下一条任务生效）", map[string]any{
			"action": "session_models", "target": string(domain.ModelTargetSwitch), "directory": handoff.Directory,
			"session_id": handoff.SessionID, "session_name": sessionName,
		}, "default"))
	}
	if len(buttons) > 0 {
		elements = append(elements, permissionButtonRow(buttons...))
	}
	if handoff.Type == domain.HandoffError {
		elements = append(elements, handoffCardElement{
			Tag:     "markdown",
			Content: "↩️ 引用回复本消息可继续原 Session",
		})
	} else if handoff.Type == domain.HandoffGoalCompletion {
		elements = append(elements, handoffCardElement{
			Tag:     "markdown",
			Content: "💡 可点击按钮；也可引用回复“确认完成”或“继续”。若当前只有一个待办，直接 @机器人 回复也会路由到这个 Goal。",
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

func tailOutputPreview(text string, maxRunes int) (string, int) {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return text, 0
	}
	return "...\n" + string(runes[len(runes)-maxRunes:]), len(runes) - maxRunes
}

func formatAssistantOutputCard(detail domain.AssistantOutputDetail) (string, error) {
	sessionName := strings.TrimSpace(detail.SessionName)
	if sessionName == "" {
		sessionName = "Untitled Session"
	}
	expanded := false
	elements := []handoffCardElement{
		{Tag: "markdown", Content: fmt.Sprintf("💬 **详细答复**\n\n🏷️ %s\n🆔 `%s`", sessionName, detail.SessionID)},
		{Tag: "hr"},
		{
			Tag:      "collapsible_panel",
			Expanded: &expanded,
			Header: &handoffCardPanelHeader{Title: handoffCardText{
				Tag: "plain_text", Content: "展开查看全部最终答复",
			}},
			Border:          &handoffCardBorder{Color: "grey", CornerRadius: "5px"},
			VerticalSpacing: "8px",
			Padding:         "8px 8px 8px 8px",
			Elements:        []handoffCardElement{{Tag: "markdown", Content: detail.Content}},
		},
	}
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

func formatProjectCard(page domain.ProjectPage) (string, error) {
	elements := []handoffCardElement{{
		Tag: "markdown", Content: fmt.Sprintf("📂 **OpenCode Projects**\n共 %d 个项目 · 第 %d/%d 页", page.Total, page.Page, page.TotalPages),
	}}
	for _, project := range page.Projects {
		elements = append(elements,
			handoffCardElement{Tag: "hr"},
			handoffCardElement{Tag: "markdown", Content: fmt.Sprintf("**%s**\n`%s`", project.Name, sanitizeInlineCode(project.Directory))},
			permissionButtonRow(callbackButton("➕ 选择模型并创建", map[string]any{
				"action": "project_create", "directory": project.Directory,
			}, "primary")),
		)
	}
	if page.TotalPages > 1 {
		var buttons []handoffCardElement
		if page.Page > 1 {
			buttons = append(buttons, callbackButton("← 上一页", map[string]any{"action": "project_page", "page": page.Page - 1}, "default"))
		}
		if page.Page < page.TotalPages {
			buttons = append(buttons, callbackButton("下一页 →", map[string]any{"action": "project_page", "page": page.Page + 1}, "default"))
		}
		elements = append(elements, handoffCardElement{Tag: "hr"}, permissionButtonRow(buttons...))
	}
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

func formatModelCard(page domain.ModelPage) (string, error) {
	return formatModelCardWithSearch(page, true)
}

func formatModelCardWithoutSearch(page domain.ModelPage) (string, error) {
	return formatModelCardWithSearch(page, false)
}

func formatModelCardWithSearch(page domain.ModelPage, includeSearch bool) (string, error) {
	securityNote := "这里只展示 OpenCode 当前返回的脱敏模型信息，不会显示 API Key 或连接参数。"
	note := "选择模型前请确认 Provider 与模型 ID。"
	switch page.Context.Target {
	case domain.ModelTargetCreate:
		note = "选择后会创建空 Session；模型先记为“待使用”，第一次引用回复任务时才真正生效。"
	case domain.ModelTargetSwitch:
		note = "选择不会中断当前执行；模型从下一条通过飞书发送的普通任务起生效。"
	}
	elements := []handoffCardElement{}
	if page.Home {
		elements = append(elements, handoffCardElement{Tag: "markdown", Content: fmt.Sprintf(
			"🤖 **OpenCode Models**\n共 %d 个模型 · %d 个 Provider\n\nℹ️ %s\n🔒 %s", page.Total, len(page.Providers), note, securityNote,
		)})
	} else {
		title := "全部模型"
		if page.Query != "" {
			title = fmt.Sprintf("搜索：`%s`", sanitizeInlineCode(page.Query))
		} else if page.ProviderID != "" {
			title = "Provider：" + page.ProviderName
			if strings.TrimSpace(page.ProviderName) == "" {
				title = "Provider：" + page.ProviderID
			}
		}
		elements = append(elements, handoffCardElement{Tag: "markdown", Content: fmt.Sprintf(
			"🤖 **%s**\n共 %d 个模型 · 第 %d/%d 页\n\nℹ️ %s\n🔒 %s", title, page.Total, page.Page, page.TotalPages, note, securityNote,
		)})
	}
	if page.Context.Target == domain.ModelTargetCreate {
		value := modelActionValue("model_apply", page.Context)
		elements = append(elements, permissionButtonRow(callbackButton("使用 OpenCode 默认模型创建", value, "default")))
	}
	if page.Home {
		if len(page.Recent) > 0 {
			elements = append(elements, handoffCardElement{Tag: "hr"}, handoffCardElement{Tag: "markdown", Content: "⭐ **最近使用**"})
			for _, recent := range page.Recent {
				label := recent.Model.Name
				if recent.Variant != "" {
					label += " · " + recent.Variant
				}
				detail := fmt.Sprintf("**%s**\n`%s/%s`", label, sanitizeInlineCode(recent.Model.ProviderID), sanitizeInlineCode(recent.Model.ID))
				elements = append(elements, handoffCardElement{Tag: "markdown", Content: detail})
				if page.Context.Target != domain.ModelTargetBrowse {
					value := modelActionValue("model_apply", page.Context)
					value["provider_id"] = recent.Model.ProviderID
					value["model_id"] = recent.Model.ID
					value["variant"] = recent.Variant
					elements = append(elements, permissionButtonRow(callbackButton("使用 "+label, value, "primary")))
				}
			}
		}
		elements = append(elements, handoffCardElement{Tag: "hr"}, handoffCardElement{Tag: "markdown", Content: "🏢 **按 Provider 查看**"})
		for index := 0; index < len(page.Providers); index += 2 {
			buttons := make([]handoffCardElement, 0, 2)
			for _, provider := range page.Providers[index:min(index+2, len(page.Providers))] {
				value := modelActionValue("model_provider", page.Context)
				value["filter_provider"] = provider.ID
				buttons = append(buttons, callbackButton(fmt.Sprintf("%s · %d", provider.Name, provider.Count), value, "default"))
			}
			elements = append(elements, permissionButtonRow(buttons...))
		}
		allValue := modelActionValue("model_all", page.Context)
		elements = append(elements, permissionButtonRow(callbackButton("查看全部模型", allValue, "default")))
		if includeSearch {
			elements = append(elements, modelSearchForm(page.Context))
		} else {
			elements = append(elements, handoffCardElement{Tag: "markdown", Content: "当前飞书环境不支持卡片搜索框，可按 Provider 查看，或发送 `/models <关键词>` 进行全局搜索。"})
		}
		elements = append(elements, handoffCardElement{Tag: "markdown", Content: "也可以直接发送 `/models <关键词>` 进行全局浏览，例如 `/models claude`。"})
		content, err := json.Marshal(handoffCard{
			Schema: "2.0", Config: handoffCardConfig{UpdateMulti: true, WidthMode: "fill"},
			Body: handoffCardBody{Direction: "vertical", Padding: "12px 12px 12px 12px", Elements: elements},
		})
		if err != nil {
			return "", err
		}
		return string(content), nil
	}
	for _, model := range page.Models {
		providerName := strings.TrimSpace(model.ProviderName)
		if providerName == "" {
			providerName = model.ProviderID
		}
		features := []string{}
		if model.ContextLimit > 0 {
			features = append(features, "上下文 "+formatTokenLimit(model.ContextLimit))
		}
		if model.Reasoning {
			features = append(features, "支持推理")
		}
		if model.Attachment {
			features = append(features, "支持附件")
		}
		if len(model.Variants) > 0 {
			features = append(features, "档位 "+strings.Join(model.Variants, "/"))
		}
		detail := fmt.Sprintf("**%s**\nProvider：%s\n`%s/%s`", model.Name, providerName, sanitizeInlineCode(model.ProviderID), sanitizeInlineCode(model.ID))
		if len(features) > 0 {
			detail += "\n" + strings.Join(features, " · ")
		}
		elements = append(elements, handoffCardElement{Tag: "hr"}, handoffCardElement{Tag: "markdown", Content: detail})
		if page.Context.Target != domain.ModelTargetBrowse {
			action := "model_apply"
			label := "使用此模型"
			if len(model.Variants) > 0 {
				action = "model_variants"
				label = "选择模型与档位"
			}
			value := modelActionValue(action, page.Context)
			value["provider_id"] = model.ProviderID
			value["model_id"] = model.ID
			elements = append(elements, permissionButtonRow(callbackButton(label, value, "primary")))
		}
	}
	if page.TotalPages > 1 {
		var buttons []handoffCardElement
		if page.Page > 1 {
			value := modelActionValue("model_page", page.Context)
			value["page"] = page.Page - 1
			value["query"] = page.Query
			value["filter_provider"] = page.ProviderID
			buttons = append(buttons, callbackButton("← 上一页", value, "default"))
		}
		if page.Page < page.TotalPages {
			value := modelActionValue("model_page", page.Context)
			value["page"] = page.Page + 1
			value["query"] = page.Query
			value["filter_provider"] = page.ProviderID
			buttons = append(buttons, callbackButton("下一页 →", value, "default"))
		}
		elements = append(elements, handoffCardElement{Tag: "hr"}, permissionButtonRow(buttons...))
	}
	homeValue := modelActionValue("model_home", page.Context)
	elements = append(elements, permissionButtonRow(callbackButton("← 返回模型首页", homeValue, "default")))
	content, err := json.Marshal(handoffCard{
		Schema: "2.0", Config: handoffCardConfig{UpdateMulti: true, WidthMode: "fill"},
		Body: handoffCardBody{Direction: "vertical", Padding: "12px 12px 12px 12px", Elements: elements},
	})
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func formatModelVariantCard(page domain.ModelVariantPage) (string, error) {
	note := "创建后第一次引用回复任务时生效。"
	if page.Context.Target == domain.ModelTargetSwitch {
		note = "不会中断当前执行，从下一条飞书任务起生效。"
	}
	elements := []handoffCardElement{
		{Tag: "markdown", Content: fmt.Sprintf("🧠 **选择模型档位**\n%s\n`%s/%s`\n\nℹ️ %s", page.Model.Name, sanitizeInlineCode(page.Model.ProviderID), sanitizeInlineCode(page.Model.ID), note)},
		{Tag: "hr"},
	}
	defaultValue := modelActionValue("model_apply", page.Context)
	defaultValue["provider_id"] = page.Model.ProviderID
	defaultValue["model_id"] = page.Model.ID
	elements = append(elements, permissionButtonRow(callbackButton("使用模型默认档位", defaultValue, "primary")))
	for _, variant := range page.Model.Variants {
		value := modelActionValue("model_apply", page.Context)
		value["provider_id"] = page.Model.ProviderID
		value["model_id"] = page.Model.ID
		value["variant"] = variant
		elements = append(elements, permissionButtonRow(callbackButton(modelVariantLabel(variant), value, "default")))
	}
	content, err := json.Marshal(handoffCard{
		Schema: "2.0", Config: handoffCardConfig{UpdateMulti: true, WidthMode: "fill"},
		Body: handoffCardBody{Direction: "vertical", Padding: "12px 12px 12px 12px", Elements: elements},
	})
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func modelActionValue(action string, context domain.ModelContext) map[string]any {
	return map[string]any{
		"action": action, "target": string(context.Target), "directory": context.ProjectDirectory,
		"session_id": context.SessionID, "session_name": context.SessionName,
	}
}

func modelSearchForm(context domain.ModelContext) handoffCardElement {
	required := true
	submit := callbackButton("搜索模型", modelActionValue("model_search", context), "primary")
	submit.Name = "model_search_submit"
	submit.ActionType = "form_submit"
	return handoffCardElement{
		Tag: "form", Name: "model_search_form",
		Elements: []handoffCardElement{
			{
				Tag: "input", Name: "model_query", InputType: "multiline_text", Rows: 1, MaxLength: 100, Required: &required,
				Label:       &handoffCardText{Tag: "plain_text", Content: "搜索模型"},
				Placeholder: &handoffCardText{Tag: "plain_text", Content: "Provider、模型名称、模型 ID 或档位"},
			},
			submit,
		},
	}
}

func modelVariantLabel(variant string) string {
	switch variant {
	case "none":
		return "关闭推理（none）"
	case "thinking":
		return "开启推理（thinking）"
	default:
		return "使用 " + variant + " 档位"
	}
}

func formatTokenLimit(value int64) string {
	if value >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	}
	if value >= 1_000 {
		return fmt.Sprintf("%dK", value/1_000)
	}
	return strconv.FormatInt(value, 10)
}

func formatCreatedSessionCard(handoff domain.Handoff) (string, error) {
	sessionName := strings.TrimSpace(handoff.SessionName)
	if sessionName == "" {
		sessionName = "Untitled Session"
	}
	modelText := "OpenCode 默认模型（第一条任务发送时确定）"
	if handoff.ModelID != "" {
		modelText = strings.TrimSpace(handoff.ModelName)
		if modelText == "" {
			modelText = handoff.ModelProviderID + "/" + handoff.ModelID
		}
		if handoff.ModelVariant != "" {
			modelText += " · " + handoff.ModelVariant
		}
		modelText += "（待使用）"
	}
	elements := []handoffCardElement{
		{Tag: "markdown", Content: fmt.Sprintf("🆔 Session ID: %s\n🏷️ Session Name: %s", handoff.SessionID, sessionName)},
		{Tag: "hr"},
		{Tag: "markdown", Content: fmt.Sprintf("✅ **OpenCode · Session Created**\n📁 Project: %s\n📂 Directory: `%s`\n🤖 模型：%s", handoff.ProjectName, sanitizeInlineCode(handoff.Directory), modelText)},
		{Tag: "markdown", Content: "ℹ️ 空 Session 尚未在 OpenCode 中固定模型；引用回复本消息并输入第一条任务后才真正生效。"},
		permissionButtonRow(callbackButton("切换待使用模型", map[string]any{
			"action": "session_models", "target": string(domain.ModelTargetSwitch), "directory": handoff.Directory,
			"session_id": handoff.SessionID, "session_name": sessionName,
		}, "default")),
	}
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

func formatRunningSessionsCard(running domain.RunningSessions) (string, error) {
	elements := []handoffCardElement{{
		Tag: "markdown", Content: fmt.Sprintf("🏃 **OpenCode · Running Sessions**\n共 %d 个运行中 Session · 已扫描 %d 个项目", running.Total, running.ScannedProjects),
	}}
	for _, session := range running.Items {
		stateIcon, stateText := runningStateDisplay(session.State)
		elapsed := "未知（未找到用户消息）"
		if session.HasLastUserInput {
			elapsed = formatElapsed(session.RunningFor)
		}
		lastInput := session.LastUserText
		if lastInput == "" && session.HasLastUserInput {
			lastInput = "（非文本输入）"
		}
		content := fmt.Sprintf("%s **%s**\n📁 Project: %s\n🆔 `%s`\n⏱️ 距上次用户输入：%s", stateIcon+" "+stateText, session.SessionName, session.ProjectName, sanitizeInlineCode(session.SessionID), elapsed)
		modelText := session.CurrentModel
		if modelText == "" {
			modelText = "OpenCode 默认/尚未识别"
		}
		if session.CurrentVariant != "" {
			modelText += " · " + session.CurrentVariant
		}
		content += "\n🤖 当前模型：`" + sanitizeInlineCode(modelText) + "`"
		elements = append(elements, handoffCardElement{Tag: "hr"}, handoffCardElement{Tag: "markdown", Content: content})
		if lastInput != "" {
			expanded := false
			elements = append(elements, handoffCardElement{
				Tag:      "collapsible_panel",
				Expanded: &expanded,
				Header: &handoffCardPanelHeader{Title: handoffCardText{
					Tag: "plain_text", Content: "💬 最后一次用户输入",
				}},
				Border:          &handoffCardBorder{Color: "grey", CornerRadius: "5px"},
				VerticalSpacing: "8px",
				Padding:         "8px 8px 8px 8px",
				Elements:        []handoffCardElement{{Tag: "markdown", Content: lastInput}},
			})
		}
		elements = append(elements, permissionButtonRow(callbackButton("切换模型", map[string]any{
			"action": "session_models", "target": string(domain.ModelTargetSwitch), "directory": session.Directory,
			"session_id": session.SessionID, "session_name": session.SessionName,
		}, "default")))
	}
	if running.Total > len(running.Items) {
		elements = append(elements, handoffCardElement{Tag: "markdown", Content: fmt.Sprintf("ℹ️ 卡片仅显示前 %d 条，共 %d 条运行中 Session。", len(running.Items), running.Total)})
	}
	if running.FailedProjects > 0 {
		elements = append(elements, handoffCardElement{Tag: "markdown", Content: fmt.Sprintf("⚠️ 另有 %d 个项目读取失败，请检查 Handoff 日志。", running.FailedProjects)})
	}
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

func runningStateDisplay(state string) (string, string) {
	switch state {
	case "waiting_permission":
		return "🟠", "等待授权"
	case "waiting_question":
		return "🟡", "等待回答"
	case "retry":
		return "🟣", "重试中"
	default:
		return "🟢", "执行中"
	}
}

func formatElapsed(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	seconds := int64(duration / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%d 秒", seconds)
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%d 分 %d 秒", minutes, seconds%60)
	}
	hours := minutes / 60
	if hours < 24 {
		return fmt.Sprintf("%d 小时 %d 分", hours, minutes%60)
	}
	days := hours / 24
	return fmt.Sprintf("%d 天 %d 小时", days, hours%24)
}

func formatPermissionCard(handoff domain.Handoff) (string, error) {
	sessionName := strings.TrimSpace(handoff.SessionName)
	if sessionName == "" {
		sessionName = "Untitled Session"
	}
	permissionName := strings.TrimSpace(handoff.Permission.Name)
	elements := []handoffCardElement{
		{Tag: "markdown", Content: fmt.Sprintf("🆔 Session ID: %s\n🏷️ Session Name: %s", handoff.SessionID, sessionName)},
		{Tag: "hr"},
		{Tag: "markdown", Content: fmt.Sprintf("🔐 **OpenCode · 等待授权**\n📁 Project: %s", handoff.ProjectName)},
		{Tag: "markdown", Content: fmt.Sprintf("**权限类型**\n%s (`%s`)", permissionDisplayName(permissionName), sanitizeInlineCode(permissionName))},
		{Tag: "markdown", Content: "**本次请求范围**\n" + permissionValues(handoff.Permission.Patterns)},
	}
	if target := permissionMetadataString(handoff.Permission.Metadata, "filepath"); target != "" {
		elements = append(elements, handoffCardElement{
			Tag:     "markdown",
			Content: "**具体目标**\n- `" + sanitizeInlineCode(target) + "`",
		})
	}
	if len(handoff.Permission.Always) > 0 {
		elements = append(elements, handoffCardElement{
			Tag:     "markdown",
			Content: "**选择“始终允许”后保存的范围**\n" + permissionValues(handoff.Permission.Always),
		})
	} else {
		elements = append(elements, handoffCardElement{
			Tag:     "markdown",
			Content: "ℹ️ OpenCode 未提供可保存的范围；此请求选择“始终允许”可能只对本次生效。",
		})
	}
	elements = append(elements,
		handoffCardElement{Tag: "markdown", Content: "⚠️ “允许一次”仅处理当前这条请求，其他待处理卡片仍需分别处理；“始终允许”会放行后续匹配操作；“拒绝”还会拒绝该 Session 中其他待处理权限。"},
		permissionButtonRow(
			callbackButton("✅ 允许一次", map[string]any{"action": "permission_reply", "decision": "once"}, "primary"),
			callbackButton("🔓 始终允许", map[string]any{"action": "permission_reply", "decision": "always"}, "default"),
			callbackButton("❌ 拒绝", map[string]any{"action": "permission_reply", "decision": "reject"}, "danger"),
		),
		handoffCardElement{Tag: "markdown", Content: "↩️ 回调不可用时可引用回复本消息：允许一次 / 始终允许 / 拒绝（或 once / always / reject）。"},
	)
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

func permissionMetadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
}

func permissionDisplayName(permission string) string {
	switch permission {
	case "external_directory":
		return "访问项目目录外文件"
	case "read":
		return "读取文件"
	case "edit":
		return "修改文件"
	case "bash":
		return "执行 Shell 命令"
	case "task":
		return "启动子任务"
	case "webfetch":
		return "访问网页"
	case "websearch":
		return "搜索网络"
	case "doom_loop":
		return "继续可能重复的操作"
	default:
		if permission == "" {
			return "未知权限"
		}
		return "工具操作授权"
	}
}

func permissionValues(values []string) string {
	if len(values) == 0 {
		return "（未提供）"
	}
	lines := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
		if value != "" {
			lines = append(lines, "- `"+sanitizeInlineCode(value)+"`")
		}
	}
	if len(lines) == 0 {
		return "（未提供）"
	}
	return strings.Join(lines, "\n")
}

func sanitizeInlineCode(value string) string {
	return strings.ReplaceAll(value, "`", "ˋ")
}

func permissionButtonRow(buttons ...handoffCardElement) handoffCardElement {
	columns := make([]handoffCardElement, 0, len(buttons))
	for _, button := range buttons {
		columns = append(columns, handoffCardElement{
			Tag: "column", Width: "weighted", Weight: 1,
			Elements: []handoffCardElement{button},
		})
	}
	return handoffCardElement{Tag: "column_set", Columns: columns}
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
