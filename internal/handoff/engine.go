package handoff

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Hans2573/OpenCode-Handoff/internal/channel"
	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
	"github.com/Hans2573/OpenCode-Handoff/internal/store"
)

type EngineOptions struct {
	MaxOutputChars   int
	NotifyIdle       bool
	NotifyError      bool
	NotifyQuestion   bool
	NotifyPermission bool
	AllowedUsers     []string
	ChatID           string
}

type Engine struct {
	opencode opencode.Adapter
	channel  channel.Channel
	store    store.Store
	options  EngineOptions
	allowed  map[string]struct{}
	logger   *slog.Logger
}

func NewEngine(
	opencodeClient opencode.Adapter,
	channelClient channel.Channel,
	handoffStore store.Store,
	options EngineOptions,
	logger *slog.Logger,
) *Engine {
	allowed := make(map[string]struct{}, len(options.AllowedUsers))
	for _, user := range options.AllowedUsers {
		allowed[user] = struct{}{}
	}
	return &Engine{
		opencode: opencodeClient,
		channel:  channelClient,
		store:    handoffStore,
		options:  options,
		allowed:  allowed,
		logger:   logger,
	}
}

func (e *Engine) Run(ctx context.Context, signals <-chan Signal) error {
	replies, err := e.channel.Receive(ctx)
	if err != nil {
		return fmt.Errorf("start channel receiver: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case signal, ok := <-signals:
			if !ok {
				return nil
			}
			if err := e.handleSignal(ctx, signal); err != nil && ctx.Err() == nil {
				e.logger.Error("process handoff signal", "session_id", signal.SessionID, "error", err)
			}
		case reply, ok := <-replies:
			if !ok {
				return errors.New("channel receiver stopped")
			}
			replyErr := e.handleReply(ctx, reply)
			if reply.Result != nil {
				reply.Result <- replyErr
				close(reply.Result)
			}
			if replyErr != nil && ctx.Err() == nil {
				e.logger.Error("process channel reply", "message_id", reply.MessageID, "error", replyErr)
			}
		}
	}
}

func (e *Engine) handleSignal(ctx context.Context, signal Signal) error {
	if signal.Kind == SignalPermissionResolved {
		return e.store.ClosePermission(ctx, signal.PermissionID)
	}
	if signal.Kind == SignalQuestion {
		if !e.options.NotifyQuestion {
			return nil
		}
		return e.handleQuestion(ctx, signal.Directory, signal.Question)
	}
	if signal.Kind == SignalPermission {
		if !e.options.NotifyPermission {
			return nil
		}
		return e.handlePermission(ctx, signal.Directory, signal.Permission)
	}
	if signal.Kind == SignalError && !e.options.NotifyError {
		return nil
	}

	session, err := e.opencode.GetSession(ctx, signal.SessionID, signal.Directory)
	if err != nil {
		return fmt.Errorf("get session: %w", err)
	}
	if session.ParentID != "" {
		e.logger.Debug("ignore subagent session handoff", "session_id", session.ID, "parent_id", session.ParentID)
		return nil
	}
	if signal.Kind == SignalStopped && e.options.NotifyQuestion {
		questions, err := e.opencode.ListQuestions(ctx, signal.Directory)
		if err != nil {
			e.logger.Warn("could not check pending questions before idle handoff", "session_id", session.ID, "error", err)
		} else {
			for _, question := range questions {
				if question.SessionID == session.ID {
					return e.handleQuestionForSession(ctx, session, signal.Directory, question)
				}
			}
		}
	}
	if signal.Kind == SignalStopped && e.options.NotifyPermission {
		permissions, err := e.opencode.ListPermissions(ctx, signal.Directory)
		if err != nil {
			e.logger.Warn("could not check pending permissions before idle handoff", "session_id", session.ID, "error", err)
		} else {
			for _, permission := range permissions {
				if permission.SessionID == session.ID {
					return e.handlePermissionForSession(ctx, session, signal.Directory, permission)
				}
			}
		}
	}
	messages, err := e.opencode.GetMessages(ctx, signal.SessionID, signal.Directory, 100)
	if err != nil {
		return fmt.Errorf("get session messages: %w", err)
	}
	output, found := opencode.LastAssistantOutput(messages)
	if !found && signal.Kind != SignalError {
		e.logger.Debug("ignore idle session without assistant output", "session_id", signal.SessionID)
		return nil
	}

	errorText := strings.TrimSpace(signal.Error)
	if errorText == "" {
		errorText = output.Error
	}
	if signal.Kind == SignalError && errorText == "" {
		errorText = "OpenCode session error"
	}
	handoffType := domain.HandoffFinished
	if errorText != "" {
		handoffType = domain.HandoffError
		if !e.options.NotifyError {
			return nil
		}
	} else if !e.options.NotifyIdle {
		return nil
	}
	messageID := output.MessageID
	if messageID == "" {
		messageID = "error-without-message"
	}
	handoff := domain.Handoff{
		ID:                     newID(),
		SessionID:              session.ID,
		SessionName:            sessionName(session),
		Directory:              session.Directory,
		ProjectName:            projectName(session),
		Type:                   handoffType,
		LastAssistantMessageID: messageID,
		LastAssistantText:      truncateTail(output.Text, e.options.MaxOutputChars),
		ErrorText:              truncateTail(errorText, e.options.MaxOutputChars),
		Status:                 domain.StatusOpen,
		CreatedAt:              time.Now().UTC(),
	}
	return e.persistAndSend(ctx, handoff)
}

func (e *Engine) handleQuestion(ctx context.Context, directory string, question opencode.QuestionRequest) error {
	if question.ID == "" || question.SessionID == "" || len(question.Questions) == 0 {
		return errors.New("OpenCode question event is incomplete")
	}
	session, err := e.opencode.GetSession(ctx, question.SessionID, directory)
	if err != nil {
		return fmt.Errorf("get question session: %w", err)
	}
	return e.handleQuestionForSession(ctx, session, directory, question)
}

func (e *Engine) handleQuestionForSession(ctx context.Context, session opencode.Session, directory string, question opencode.QuestionRequest) error {
	if session.ParentID != "" {
		e.logger.Debug("ignore subagent question", "session_id", session.ID, "parent_id", session.ParentID)
		return nil
	}
	if session.Directory == "" {
		session.Directory = directory
	}
	questions := make([]domain.Question, 0, len(question.Questions))
	for _, item := range question.Questions {
		converted := domain.Question{
			Text: item.Question, Header: item.Header, Multiple: item.Multiple,
			Custom: item.AllowsCustom(), CustomSet: item.Custom != nil,
		}
		for _, option := range item.Options {
			converted.Options = append(converted.Options, domain.QuestionOption{
				Label: option.Label, Description: option.Description,
			})
		}
		questions = append(questions, converted)
	}
	handoff := domain.Handoff{
		ID:                     newID(),
		SessionID:              session.ID,
		SessionName:            sessionName(session),
		Directory:              session.Directory,
		ProjectName:            projectName(session),
		Type:                   domain.HandoffQuestion,
		LastAssistantMessageID: "question:" + question.ID,
		QuestionID:             question.ID,
		Questions:              questions,
		Status:                 domain.StatusOpen,
		CreatedAt:              time.Now().UTC(),
	}
	return e.persistAndSend(ctx, handoff)
}

func (e *Engine) handlePermission(ctx context.Context, directory string, permission opencode.PermissionRequest) error {
	if permission.ID == "" || permission.SessionID == "" || permission.Permission == "" || len(permission.Patterns) == 0 {
		return errors.New("OpenCode permission event is incomplete")
	}
	session, err := e.opencode.GetSession(ctx, permission.SessionID, directory)
	if err != nil {
		return fmt.Errorf("get permission session: %w", err)
	}
	return e.handlePermissionForSession(ctx, session, directory, permission)
}

func (e *Engine) handlePermissionForSession(ctx context.Context, session opencode.Session, directory string, permission opencode.PermissionRequest) error {
	if session.ParentID != "" {
		e.logger.Debug("ignore subagent permission", "session_id", session.ID, "parent_id", session.ParentID)
		return nil
	}
	if session.Directory == "" {
		session.Directory = directory
	}
	handoff := domain.Handoff{
		ID:                     newID(),
		SessionID:              session.ID,
		SessionName:            sessionName(session),
		Directory:              session.Directory,
		ProjectName:            projectName(session),
		Type:                   domain.HandoffPermission,
		LastAssistantMessageID: "permission:" + permission.ID,
		PermissionID:           permission.ID,
		Permission: domain.Permission{
			Name: permission.Permission, Patterns: permission.Patterns,
			Always: permission.Always, Metadata: permission.Metadata,
		},
		Status:    domain.StatusOpen,
		CreatedAt: time.Now().UTC(),
	}
	return e.persistAndSend(ctx, handoff)
}

func (e *Engine) persistAndSend(ctx context.Context, handoff domain.Handoff) error {
	if err := e.store.Create(ctx, handoff); errors.Is(err, store.ErrDuplicate) {
		e.logger.Debug("skip duplicate handoff", "session_id", handoff.SessionID, "message_id", handoff.LastAssistantMessageID, "type", handoff.Type)
		return nil
	} else if err != nil {
		return err
	}

	ref, err := e.sendWithRetry(ctx, handoff)
	if err != nil {
		if cleanupErr := e.store.DeleteUnbound(context.WithoutCancel(ctx), handoff.ID); cleanupErr != nil {
			e.logger.Error("clean up undelivered handoff", "handoff_id", handoff.ID, "error", cleanupErr)
		}
		return err
	}
	var bindErr error
	for attempt := 1; attempt <= 3; attempt++ {
		bindErr = e.store.BindMessage(ctx, handoff.ID, ref)
		if bindErr == nil {
			break
		}
	}
	if bindErr != nil {
		return fmt.Errorf("persist channel message mapping: %w", bindErr)
	}
	e.logger.Info("handoff sent", "session_id", handoff.SessionID, "type", handoff.Type, "message_id", ref.MessageID)
	return nil
}

func (e *Engine) sendWithRetry(ctx context.Context, handoff domain.Handoff) (domain.MessageRef, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ref, err := e.channel.SendHandoff(ctx, handoff)
		if err == nil {
			return ref, nil
		}
		lastErr = err
		if attempt == 3 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return domain.MessageRef{}, ctx.Err()
		case <-timer.C:
		}
	}
	return domain.MessageRef{}, fmt.Errorf("send handoff after retries: %w", lastErr)
}

func (e *Engine) handleReply(ctx context.Context, reply domain.UserReply) error {
	text := strings.TrimSpace(reply.Text)
	if text == "" && len(reply.QuestionAnswers) == 0 && !reply.RejectQuestion && reply.PermissionReply == "" && !reply.AbortSession {
		return nil
	}
	allowed, err := e.isAllowed(ctx, reply)
	if err != nil {
		return fmt.Errorf("authorize channel reply: %w", err)
	}
	if !allowed {
		e.logger.Warn("ignore reply from unauthorized user", "sender_id", reply.SenderID)
		if reply.CardAction {
			return errors.New("当前用户无权操作这条消息")
		}
		return nil
	}
	if reply.AbortSession && reply.ParentMessageID == "" {
		if err := e.channel.Reply(ctx, reply.MessageID, "请引用对应的 OpenCode Handoff 通知后回复 /stop。"); err != nil {
			return fmt.Errorf("explain missing handoff for abort: %w", err)
		}
		return nil
	}

	var handoff domain.Handoff
	if reply.ParentMessageID != "" {
		handoff, err = e.store.ClaimByMessage(ctx, reply.ParentMessageID, reply.MessageID)
	} else {
		handoff, err = e.store.ClaimOnlyOpenByChat(ctx, reply.ChatID, reply.MessageID)
	}
	if errors.Is(err, store.ErrAmbiguous) {
		if replyErr := e.channel.Reply(ctx, reply.MessageID, "当前有多个 OpenCode Session 等待输入，请引用回复对应的 Handoff 通知。"); replyErr != nil {
			return fmt.Errorf("explain ambiguous handoff route: %w", replyErr)
		}
		return nil
	}
	if errors.Is(err, store.ErrDuplicateReply) {
		e.logger.Debug("ignore duplicate channel reply", "message_id", reply.MessageID)
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		e.logger.Debug("ignore reply without an open handoff", "parent_message_id", reply.ParentMessageID)
		if reply.CardAction {
			return errors.New("该请求已处理或已过期")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if reply.AbortSession {
		return e.handleAbortReply(ctx, handoff, reply)
	}
	if handoff.Type == domain.HandoffQuestion {
		return e.handleQuestionReply(ctx, handoff, reply, text)
	}
	if handoff.Type == domain.HandoffPermission {
		return e.handlePermissionReply(ctx, handoff, reply, text)
	}
	if err := e.opencode.SendPrompt(ctx, handoff.SessionID, handoff.Directory, text); err != nil {
		if reopenErr := e.store.Reopen(context.WithoutCancel(ctx), handoff.ID); reopenErr != nil {
			e.logger.Error("reopen handoff after prompt failure", "handoff_id", handoff.ID, "error", reopenErr)
		}
		if !reply.CardAction {
			if replyErr := e.channel.Reply(ctx, reply.MessageID, "发送到 OpenCode Session 失败，请检查服务日志后重新发送。"); replyErr != nil {
				e.logger.Warn("report session resume failure in channel", "session_id", handoff.SessionID, "error", replyErr)
			}
		}
		return fmt.Errorf("resume OpenCode session: %w", err)
	}
	e.logger.Info("session resumed", "session_id", handoff.SessionID, "sender_id", reply.SenderID)
	if !reply.CardAction {
		if err := e.channel.Reply(ctx, reply.MessageID, "已发送到 OpenCode Session，任务正在继续。"); err != nil {
			e.logger.Warn("confirm session resume in channel", "session_id", handoff.SessionID, "error", err)
		}
	}
	return nil
}

func (e *Engine) handleAbortReply(ctx context.Context, handoff domain.Handoff, reply domain.UserReply) error {
	if err := e.opencode.AbortSession(ctx, handoff.SessionID, handoff.Directory); err != nil {
		if reopenErr := e.store.Reopen(context.WithoutCancel(ctx), handoff.ID); reopenErr != nil {
			e.logger.Error("reopen handoff after abort failure", "handoff_id", handoff.ID, "error", reopenErr)
		}
		if !reply.CardAction {
			_ = e.channel.Reply(ctx, reply.MessageID, "请求中断 OpenCode Session 失败，请检查服务日志后重试。")
		}
		return fmt.Errorf("abort OpenCode session: %w", err)
	}
	e.logger.Info("session abort requested", "session_id", handoff.SessionID, "sender_id", reply.SenderID)
	if err := e.channel.Reply(ctx, reply.MessageID, "已请求中断 OpenCode Session。引用的对话将停止当前任务。"); err != nil {
		e.logger.Warn("confirm session abort in channel", "session_id", handoff.SessionID, "error", err)
	}
	return nil
}

func (e *Engine) handlePermissionReply(ctx context.Context, handoff domain.Handoff, reply domain.UserReply, text string) error {
	reopen := func() {
		if err := e.store.Reopen(context.WithoutCancel(ctx), handoff.ID); err != nil {
			e.logger.Error("reopen permission handoff", "handoff_id", handoff.ID, "error", err)
		}
	}
	decision := opencode.PermissionReply(strings.ToLower(strings.TrimSpace(reply.PermissionReply)))
	if decision == "" {
		decision = parsePermissionReply(text)
	}
	if !decision.Valid() {
		reopen()
		if reply.CardAction {
			return errors.New("请选择允许一次、始终允许或拒绝")
		}
		_ = e.channel.Reply(ctx, reply.MessageID, "权限答复格式不正确，请回复“允许一次”“始终允许”或“拒绝”。")
		return nil
	}
	if err := e.opencode.ReplyPermission(ctx, handoff.PermissionID, handoff.Directory, decision); err != nil {
		reopen()
		if !reply.CardAction {
			_ = e.channel.Reply(ctx, reply.MessageID, "提交权限决定到 OpenCode 失败，请检查服务日志后重试。")
		}
		return fmt.Errorf("submit OpenCode permission response: %w", err)
	}
	pendingPermissions, pendingKnown := e.reconcilePermissionHandoffs(ctx, handoff, decision)
	e.logger.Info("permission answered", "session_id", handoff.SessionID, "permission_id", handoff.PermissionID, "decision", decision)
	message := permissionConfirmation(decision, pendingPermissions, pendingKnown)
	if err := e.channel.Reply(ctx, replyMessageID(reply), message); err != nil {
		e.logger.Warn("confirm permission response", "permission_id", handoff.PermissionID, "error", err)
	}
	return nil
}

func (e *Engine) reconcilePermissionHandoffs(ctx context.Context, handoff domain.Handoff, decision opencode.PermissionReply) (int, bool) {
	permissions, err := e.opencode.ListPermissions(ctx, handoff.Directory)
	if err != nil {
		if decision != opencode.PermissionReject {
			e.logger.Warn("could not reconcile pending permissions", "session_id", handoff.SessionID, "error", err)
			return 0, false
		}
		permissions = nil
	}
	var pendingIDs []string
	for _, permission := range permissions {
		if permission.SessionID == handoff.SessionID && permission.ID != handoff.PermissionID {
			pendingIDs = append(pendingIDs, permission.ID)
		}
	}
	if err := e.store.CloseResolvedPermissions(context.WithoutCancel(ctx), handoff.SessionID, pendingIDs); err != nil {
		e.logger.Warn("close resolved permission handoffs", "session_id", handoff.SessionID, "error", err)
	}
	return len(pendingIDs), true
}

func permissionConfirmation(decision opencode.PermissionReply, pending int, pendingKnown bool) string {
	if pendingKnown && pending > 0 {
		switch decision {
		case opencode.PermissionOnce:
			return fmt.Sprintf("已允许本次操作，但原 OpenCode Session 仍有 %d 条权限请求等待处理。请继续处理对应的飞书卡片。", pending)
		case opencode.PermissionAlways:
			return fmt.Sprintf("已设置始终允许，但原 OpenCode Session 仍有 %d 条权限请求等待处理。请继续处理对应的飞书卡片。", pending)
		case opencode.PermissionReject:
			return fmt.Sprintf("已拒绝权限请求，但原 OpenCode Session 仍有 %d 条权限请求等待处理。", pending)
		}
	}
	if !pendingKnown {
		switch decision {
		case opencode.PermissionOnce:
			return "已允许本次操作，OpenCode 已接收；暂时无法确认该 Session 是否还有其他待处理权限。"
		case opencode.PermissionAlways:
			return "已设置始终允许，OpenCode 已接收；暂时无法确认该 Session 是否还有其他待处理权限。"
		}
	}
	return map[opencode.PermissionReply]string{
		opencode.PermissionOnce:   "已允许本次操作，原 OpenCode Session 正在继续。",
		opencode.PermissionAlways: "已设置始终允许，原 OpenCode Session 正在继续。",
		opencode.PermissionReject: "已拒绝权限请求。",
	}[decision]
}

func parsePermissionReply(text string) opencode.PermissionReply {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "1", "允许一次", "仅此一次", "once", "allow once":
		return opencode.PermissionOnce
	case "2", "始终允许", "总是允许", "always", "allow always":
		return opencode.PermissionAlways
	case "3", "拒绝", "取消", "reject", "deny":
		return opencode.PermissionReject
	default:
		return ""
	}
}

func (e *Engine) handleQuestionReply(ctx context.Context, handoff domain.Handoff, reply domain.UserReply, text string) error {
	reopen := func() {
		if err := e.store.Reopen(context.WithoutCancel(ctx), handoff.ID); err != nil {
			e.logger.Error("reopen question handoff", "handoff_id", handoff.ID, "error", err)
		}
	}
	reject := reply.RejectQuestion || isQuestionReject(text)
	var answers [][]string
	var err error
	if !reject {
		answers = reply.QuestionAnswers
		if len(answers) == 0 {
			answers, err = parseQuestionAnswers(handoff.Questions, text)
		} else {
			err = validateQuestionAnswers(handoff.Questions, answers)
		}
		if err != nil {
			reopen()
			if reply.CardAction {
				return err
			}
			_ = e.channel.Reply(ctx, reply.MessageID, "答案格式不正确："+err.Error())
			return nil
		}
	}
	if reject {
		err = e.opencode.RejectQuestion(ctx, handoff.QuestionID, handoff.Directory)
	} else {
		err = e.opencode.ReplyQuestion(ctx, handoff.QuestionID, handoff.Directory, answers)
	}
	if err != nil {
		reopen()
		if !reply.CardAction {
			_ = e.channel.Reply(ctx, reply.MessageID, "提交到 OpenCode 失败，请检查服务日志后重试。")
		}
		return fmt.Errorf("submit OpenCode question response: %w", err)
	}
	e.logger.Info("question answered", "session_id", handoff.SessionID, "question_id", handoff.QuestionID, "rejected", reject)
	message := "答案已提交到原 OpenCode Session，任务正在继续。"
	if reject {
		message = "已忽略该问题。"
	}
	if err := e.channel.Reply(ctx, replyMessageID(reply), message); err != nil {
		e.logger.Warn("confirm question response", "question_id", handoff.QuestionID, "error", err)
	}
	return nil
}

func replyMessageID(reply domain.UserReply) string {
	if reply.CardAction && reply.ParentMessageID != "" {
		return reply.ParentMessageID
	}
	return reply.MessageID
}

func isQuestionReject(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "忽略", "拒绝", "reject", "skip", "cancel", "取消":
		return true
	default:
		return false
	}
}

func parseQuestionAnswers(questions []domain.Question, text string) ([][]string, error) {
	if len(questions) == 0 {
		return nil, errors.New("问题内容缺失")
	}
	parts := []string{strings.TrimSpace(text)}
	if len(questions) > 1 {
		parts = strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
		if len(parts) != len(questions) {
			return nil, fmt.Errorf("共有 %d 题，请每题一行作答", len(questions))
		}
	}
	answers := make([][]string, len(questions))
	for index, question := range questions {
		answer, err := parseQuestionAnswer(question, strings.TrimSpace(parts[index]))
		if err != nil {
			return nil, fmt.Errorf("第 %d 题：%w", index+1, err)
		}
		answers[index] = answer
	}
	return answers, nil
}

func parseQuestionAnswer(question domain.Question, text string) ([]string, error) {
	if text == "" {
		return nil, errors.New("答案不能为空")
	}
	items := []string{text}
	if question.Multiple {
		normalized := strings.NewReplacer("，", ",", "、", ",").Replace(text)
		items = strings.Split(normalized, ",")
	}
	answers := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if number, err := strconv.Atoi(item); err == nil && number >= 1 && number <= len(question.Options) {
			answers = append(answers, question.Options[number-1].Label)
			continue
		}
		matched := ""
		for _, option := range question.Options {
			if strings.EqualFold(item, option.Label) {
				matched = option.Label
				break
			}
		}
		if matched != "" {
			answers = append(answers, matched)
			continue
		}
		if question.AllowsCustom() {
			answers = append(answers, item)
			continue
		}
		return nil, fmt.Errorf("%q 不是有效选项", item)
	}
	if len(answers) == 0 {
		return nil, errors.New("答案不能为空")
	}
	if !question.Multiple && len(answers) != 1 {
		return nil, errors.New("只能选择一个答案")
	}
	return answers, nil
}

func validateQuestionAnswers(questions []domain.Question, answers [][]string) error {
	if len(answers) != len(questions) {
		return fmt.Errorf("需要提交 %d 题的答案", len(questions))
	}
	for index, values := range answers {
		parsed, err := parseQuestionAnswer(questions[index], strings.Join(values, ","))
		if err != nil {
			return fmt.Errorf("第 %d 题：%w", index+1, err)
		}
		answers[index] = parsed
	}
	return nil
}

func (e *Engine) isAllowed(ctx context.Context, reply domain.UserReply) (bool, error) {
	identifiers := append([]string{reply.SenderID}, reply.SenderIDs...)
	if len(e.allowed) > 0 {
		matched := false
		for _, identifier := range identifiers {
			if _, ok := e.allowed[identifier]; ok && identifier != "" {
				matched = true
				break
			}
		}
		if !matched {
			return false, nil
		}
	}

	binding, err := e.store.GetChannelBinding(ctx)
	if err == nil {
		return binding.ChatID == reply.ChatID && identifiersOverlap(binding.UserIDs, identifiers), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if e.options.ChatID == "" || len(e.allowed) == 0 {
		return false, nil
	}
	return reply.ChatID == e.options.ChatID, nil
}

func identifiersOverlap(left, right []string) bool {
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := values[value]; ok && value != "" {
			return true
		}
	}
	return false
}

func projectName(session opencode.Session) string {
	directory := filepath.Clean(session.Directory)
	name := filepath.Base(directory)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = strings.TrimSpace(session.Title)
	}
	if name == "" {
		name = session.ID
	}
	return name
}

func sessionName(session opencode.Session) string {
	if name := strings.TrimSpace(session.Title); name != "" {
		return name
	}
	return "Untitled Session"
}

func truncateTail(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return "...\n" + string(runes[len(runes)-maxRunes:])
}

func newID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("hof_%d", time.Now().UnixNano())
	}
	return "hof_" + hex.EncodeToString(buffer)
}
