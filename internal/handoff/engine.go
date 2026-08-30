package handoff

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	if text == "" && len(reply.QuestionAnswers) == 0 && !reply.RejectQuestion && reply.PermissionReply == "" && !reply.AbortSession && !reply.ListProjects && !reply.CreateSession && !reply.ListRunning && !reply.ListModels && !reply.ListModelVariants && !reply.ApplyModel {
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
	if reply.ListProjects {
		return e.handleProjectList(ctx, reply)
	}
	if reply.ListModels {
		return e.handleModelList(ctx, reply)
	}
	if reply.ListModelVariants {
		return e.handleModelVariants(ctx, reply)
	}
	if reply.ApplyModel {
		return e.handleApplyModel(ctx, reply)
	}
	if reply.CreateSession {
		return e.handleCreateSession(ctx, reply)
	}
	if reply.ListRunning {
		return e.handleRunningSessions(ctx, reply)
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
	var selected *opencode.ModelRef
	pending, pendingErr := e.store.GetPendingSessionModel(ctx, handoff.SessionID)
	if pendingErr == nil {
		selected = &opencode.ModelRef{ProviderID: pending.ProviderID, ModelID: pending.ModelID, Variant: pending.Variant}
	} else if !errors.Is(pendingErr, store.ErrNotFound) {
		return fmt.Errorf("read pending Session model: %w", pendingErr)
	}
	if err := e.opencode.SendPrompt(ctx, handoff.SessionID, handoff.Directory, text, selected); err != nil {
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
	if selected != nil {
		if err := e.store.ClearPendingSessionModel(context.WithoutCancel(ctx), handoff.SessionID); err != nil {
			e.logger.Warn("clear applied Session model", "session_id", handoff.SessionID, "error", err)
		}
	}
	e.logger.Info("session resumed", "session_id", handoff.SessionID, "sender_id", reply.SenderID)
	if !reply.CardAction {
		if err := e.channel.Reply(ctx, reply.MessageID, "已发送到 OpenCode Session，任务正在继续。"); err != nil {
			e.logger.Warn("confirm session resume in channel", "session_id", handoff.SessionID, "error", err)
		}
	}
	return nil
}

const projectPageSize = 8

const modelPageSize = 6

func (e *Engine) handleModelList(ctx context.Context, reply domain.UserReply) error {
	models, err := e.availableModels(ctx)
	if err != nil {
		return fmt.Errorf("list OpenCode models: %w", err)
	}
	if len(models) == 0 {
		return e.channel.Reply(ctx, reply.MessageID, "OpenCode 当前没有返回可用模型。")
	}
	page := reply.ModelPage
	if page < 1 {
		page = 1
	}
	totalPages := (len(models) + modelPageSize - 1) / modelPageSize
	if page > totalPages {
		return e.channel.Reply(ctx, reply.MessageID, fmt.Sprintf("模型列表只有 %d 页，请发送 /models 1。", totalPages))
	}
	start := (page - 1) * modelPageSize
	end := min(start+modelPageSize, len(models))
	messageID := reply.MessageID
	if reply.CardAction && reply.ParentMessageID != "" {
		messageID = reply.ParentMessageID
	}
	return e.channel.ReplyModels(ctx, messageID, domain.ModelPage{
		Models: models[start:end], Page: page, TotalPages: totalPages, Total: len(models), Context: reply.ModelContext,
	})
}

func (e *Engine) handleModelVariants(ctx context.Context, reply domain.UserReply) error {
	model, err := e.findModel(ctx, reply.ProviderID, reply.ModelID)
	if err != nil {
		return err
	}
	messageID := reply.MessageID
	if reply.CardAction && reply.ParentMessageID != "" {
		messageID = reply.ParentMessageID
	}
	return e.channel.ReplyModelVariants(ctx, messageID, domain.ModelVariantPage{Model: model, Context: reply.ModelContext})
}

func (e *Engine) handleApplyModel(ctx context.Context, reply domain.UserReply) error {
	if reply.ModelContext.Target == domain.ModelTargetCreate && reply.ProviderID == "" && reply.ModelID == "" {
		reply.ProjectDirectory = reply.ModelContext.ProjectDirectory
		return e.handleCreateSession(ctx, reply)
	}
	model, err := e.findModel(ctx, reply.ProviderID, reply.ModelID)
	if err != nil {
		return err
	}
	if reply.ModelVariant != "" && !slices.Contains(model.Variants, reply.ModelVariant) {
		return errors.New("该模型档位已不可用，请重新选择模型")
	}
	reply.ModelName = model.Name
	switch reply.ModelContext.Target {
	case domain.ModelTargetCreate:
		reply.ProjectDirectory = reply.ModelContext.ProjectDirectory
		return e.handleCreateSession(ctx, reply)
	case domain.ModelTargetSwitch:
		if reply.ModelContext.SessionID == "" || reply.ModelContext.ProjectDirectory == "" {
			return errors.New("Session 信息不完整，请重新打开模型列表")
		}
		projects, err := e.availableProjects(ctx)
		if err != nil {
			return fmt.Errorf("validate Session project route: %w", err)
		}
		allowedDirectory := false
		for _, project := range projects {
			if sameDirectory(project.Directory, reply.ModelContext.ProjectDirectory) {
				allowedDirectory = true
				break
			}
		}
		if !allowedDirectory {
			return errors.New("该 Session 所在项目未接入飞书渠道")
		}
		session, err := e.opencode.GetSession(ctx, reply.ModelContext.SessionID, reply.ModelContext.ProjectDirectory)
		if err != nil || session.ID == "" {
			return errors.New("该 Session 已不存在，请重新查看 Session 列表")
		}
		selected := domain.SessionModel{ProviderID: model.ProviderID, ModelID: model.ID, ModelName: model.Name, Variant: reply.ModelVariant}
		if err := e.store.SetPendingSessionModel(ctx, session.ID, selected); err != nil {
			return err
		}
		variant := "默认档位"
		if selected.Variant != "" {
			variant = selected.Variant
		}
		messageID := reply.MessageID
		if reply.ParentMessageID != "" {
			messageID = reply.ParentMessageID
		}
		return e.channel.Reply(ctx, messageID, fmt.Sprintf("已为 Session `%s` 选择 %s（%s）。不会中断当前执行；下一条从飞书发送的普通任务起生效。", session.ID, selected.ModelName, variant))
	default:
		return errors.New("模型操作目标无效")
	}
}

func (e *Engine) availableModels(ctx context.Context) ([]domain.Model, error) {
	models, err := e.opencode.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]domain.Model, 0, len(models))
	for _, model := range models {
		if model.ProviderID == "" || model.ID == "" {
			continue
		}
		result = append(result, domain.Model{
			ProviderID: model.ProviderID, ProviderName: model.ProviderName, ID: model.ID, Name: model.Name,
			Status: model.Status, Variants: model.Variants, Reasoning: model.Reasoning,
			Attachment: model.Attachment, ContextLimit: model.ContextLimit,
		})
	}
	return result, nil
}

func (e *Engine) findModel(ctx context.Context, providerID, modelID string) (domain.Model, error) {
	models, err := e.availableModels(ctx)
	if err != nil {
		return domain.Model{}, fmt.Errorf("refresh OpenCode models: %w", err)
	}
	for _, model := range models {
		if model.ProviderID == providerID && model.ID == modelID {
			return model, nil
		}
	}
	return domain.Model{}, errors.New("该模型已不可用，请重新发送 /models 或重新打开模型列表")
}

func (e *Engine) handleProjectList(ctx context.Context, reply domain.UserReply) error {
	projects, err := e.availableProjects(ctx)
	if err != nil {
		return fmt.Errorf("list OpenCode projects: %w", err)
	}
	if len(projects) == 0 {
		return e.channel.Reply(ctx, reply.MessageID, "OpenCode 当前没有可用于创建 Session 的项目。")
	}
	page := reply.ProjectPage
	if page < 1 {
		page = 1
	}
	totalPages := (len(projects) + projectPageSize - 1) / projectPageSize
	if page > totalPages {
		return e.channel.Reply(ctx, reply.MessageID, fmt.Sprintf("项目列表只有 %d 页，请发送 /project 1。", totalPages))
	}
	start := (page - 1) * projectPageSize
	end := min(start+projectPageSize, len(projects))
	messageID := reply.MessageID
	if reply.CardAction && reply.ParentMessageID != "" {
		messageID = reply.ParentMessageID
	}
	return e.channel.ReplyProjects(ctx, messageID, domain.ProjectPage{
		Projects: projects[start:end], Page: page, TotalPages: totalPages, Total: len(projects),
	})
}

func (e *Engine) handleCreateSession(ctx context.Context, reply domain.UserReply) error {
	projects, err := e.availableProjects(ctx)
	if err != nil {
		return fmt.Errorf("validate OpenCode project: %w", err)
	}
	directory := filepath.Clean(strings.TrimSpace(reply.ProjectDirectory))
	var selected domain.Project
	for _, project := range projects {
		if sameDirectory(project.Directory, directory) {
			selected = project
			break
		}
	}
	if selected.Directory == "" {
		return errors.New("该项目已不存在或不允许创建 Session，请重新发送 /project")
	}
	if err := e.store.ClaimSessionCreate(ctx, reply.MessageID); errors.Is(err, store.ErrDuplicateReply) {
		return errors.New("该操作已处理，请勿重复点击")
	} else if err != nil {
		return err
	}
	completed := false
	defer func() {
		if !completed {
			if err := e.store.ReleaseSessionCreate(context.WithoutCancel(ctx), reply.MessageID); err != nil {
				e.logger.Error("release failed session creation claim", "message_id", reply.MessageID, "error", err)
			}
		}
	}()

	title := "Feishu · " + selected.Name
	session, err := e.opencode.CreateSession(ctx, selected.Directory, title)
	if err != nil {
		return fmt.Errorf("create OpenCode session: %w", err)
	}
	if session.ID == "" {
		return errors.New("OpenCode 创建 Session 后未返回 session_id")
	}
	if session.Directory == "" {
		session.Directory = selected.Directory
	}
	if session.Title == "" {
		session.Title = title
	}
	if err := e.store.CompleteSessionCreate(context.WithoutCancel(ctx), reply.MessageID, session.ID); err != nil {
		completed = true
		return fmt.Errorf("persist created session receipt: %w", err)
	}
	completed = true

	handoff := domain.Handoff{
		ID:                     newID(),
		SessionID:              session.ID,
		SessionName:            sessionName(session),
		Directory:              session.Directory,
		ProjectName:            selected.Name,
		ModelName:              reply.ModelName,
		ModelProviderID:        reply.ProviderID,
		ModelID:                reply.ModelID,
		ModelVariant:           reply.ModelVariant,
		Type:                   domain.HandoffSession,
		LastAssistantMessageID: "session-created:" + session.ID,
		Status:                 domain.StatusOpen,
		CreatedAt:              time.Now().UTC(),
	}
	if reply.ProviderID != "" && reply.ModelID != "" {
		if err := e.store.SetPendingSessionModel(context.WithoutCancel(ctx), session.ID, domain.SessionModel{
			ProviderID: reply.ProviderID, ModelID: reply.ModelID, ModelName: reply.ModelName, Variant: reply.ModelVariant,
		}); err != nil {
			return fmt.Errorf("save selected Session model: %w", err)
		}
	}
	if err := e.persistAndSend(ctx, handoff); err != nil {
		if reply.ProviderID != "" && reply.ModelID != "" {
			if clearErr := e.store.ClearPendingSessionModel(context.WithoutCancel(ctx), session.ID); clearErr != nil {
				e.logger.Warn("clear model for unannounced Session", "session_id", session.ID, "error", clearErr)
			}
		}
		return fmt.Errorf("send created session notification for %s: %w", session.ID, err)
	}
	e.logger.Info("session created from channel", "session_id", session.ID, "directory", session.Directory, "sender_id", reply.SenderID)
	return nil
}

func (e *Engine) availableProjects(ctx context.Context) ([]domain.Project, error) {
	projects, err := e.opencode.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(projects))
	result := make([]domain.Project, 0, len(projects))
	for _, project := range projects {
		directory := filepath.Clean(strings.TrimSpace(project.Worktree))
		if project.ID == "global" || isRootDirectory(directory) || directory == "." || directory == "" {
			continue
		}
		key := strings.ToLower(directory)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		name := strings.TrimSpace(project.Name)
		if name == "" {
			name = filepath.Base(directory)
		}
		result = append(result, domain.Project{ID: project.ID, Name: name, Directory: directory})
	}
	return result, nil
}

const maxRunningSessionsInCard = 20

func (e *Engine) handleRunningSessions(ctx context.Context, reply domain.UserReply) error {
	directories, err := e.opencode.ListDirectories(ctx)
	if err != nil {
		return fmt.Errorf("list OpenCode project directories: %w", err)
	}
	type scanResult struct {
		items []domain.RunningSession
		err   error
	}
	results := make(chan scanResult, len(directories))
	semaphore := make(chan struct{}, 8)
	var group sync.WaitGroup
	for _, directory := range directories {
		directory := directory
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case <-ctx.Done():
				results <- scanResult{err: ctx.Err()}
				return
			case semaphore <- struct{}{}:
			}
			defer func() { <-semaphore }()
			items, err := e.scanRunningDirectory(ctx, directory)
			results <- scanResult{items: items, err: err}
		}()
	}
	go func() {
		group.Wait()
		close(results)
	}()

	var running []domain.RunningSession
	failed := 0
	for result := range results {
		if result.err != nil {
			failed++
			if !errors.Is(result.err, context.Canceled) {
				e.logger.Warn("scan running sessions", "error", result.err)
			}
			continue
		}
		running = append(running, result.items...)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sort.SliceStable(running, func(left, right int) bool {
		if running[left].HasLastUserInput != running[right].HasLastUserInput {
			return running[left].HasLastUserInput
		}
		return running[left].RunningFor > running[right].RunningFor
	})
	if len(running) == 0 {
		message := "当前没有运行中的 OpenCode Session。"
		if failed > 0 {
			message += fmt.Sprintf("（另有 %d 个项目读取失败，请检查服务日志。）", failed)
		}
		return e.channel.Reply(ctx, reply.MessageID, message)
	}
	total := len(running)
	if len(running) > maxRunningSessionsInCard {
		running = running[:maxRunningSessionsInCard]
	}
	return e.channel.ReplyRunningSessions(ctx, reply.MessageID, domain.RunningSessions{
		Items: running, Total: total, ScannedProjects: len(directories), FailedProjects: failed,
	})
}

func (e *Engine) scanRunningDirectory(ctx context.Context, directory string) ([]domain.RunningSession, error) {
	statuses, err := e.opencode.GetSessionStatuses(ctx, directory)
	if err != nil {
		return nil, fmt.Errorf("get statuses for %s: %w", directory, err)
	}
	runningStatuses := make(map[string]opencode.SessionStatus)
	for sessionID, status := range statuses {
		if status.Type == "busy" || status.Type == "retry" {
			runningStatuses[sessionID] = status
		}
	}
	if len(runningStatuses) == 0 {
		return nil, nil
	}
	sessions, err := e.opencode.ListSessions(ctx, directory)
	if err != nil {
		return nil, fmt.Errorf("list sessions for %s: %w", directory, err)
	}
	byID := make(map[string]opencode.Session, len(sessions))
	for _, session := range sessions {
		byID[session.ID] = session
	}
	waitingQuestions := make(map[string]struct{})
	if questions, err := e.opencode.ListQuestions(ctx, directory); err == nil {
		for _, question := range questions {
			waitingQuestions[question.SessionID] = struct{}{}
		}
	}
	waitingPermissions := make(map[string]struct{})
	if permissions, err := e.opencode.ListPermissions(ctx, directory); err == nil {
		for _, permission := range permissions {
			waitingPermissions[permission.SessionID] = struct{}{}
		}
	}

	now := time.Now()
	result := make([]domain.RunningSession, 0, len(runningStatuses))
	for sessionID, status := range runningStatuses {
		session := byID[sessionID]
		if session.ID == "" {
			session = opencode.Session{ID: sessionID, Directory: directory}
		}
		state := status.Type
		if _, ok := waitingPermissions[sessionID]; ok {
			state = "waiting_permission"
		} else if _, ok := waitingQuestions[sessionID]; ok {
			state = "waiting_question"
		}
		item := domain.RunningSession{
			SessionID: sessionID, SessionName: sessionName(session), ProjectName: projectName(session),
			Directory: directory, State: state,
		}
		messages, err := e.opencode.GetMessages(ctx, sessionID, directory, 100)
		if err != nil {
			e.logger.Warn("get running session messages", "session_id", sessionID, "error", err)
			result = append(result, item)
			continue
		}
		if createdAt, text, model, ok := lastUserInput(messages); ok {
			item.HasLastUserInput = true
			item.LastUserInputAt = createdAt
			item.LastUserText = strings.TrimSpace(text)
			item.RunningFor = now.Sub(createdAt)
			if item.RunningFor < 0 {
				item.RunningFor = 0
			}
			if model != nil {
				item.CurrentModel = model.ProviderID + "/" + model.ModelID
				item.CurrentVariant = model.Variant
			}
		}
		result = append(result, item)
	}
	return result, nil
}

func lastUserInput(messages []opencode.Message) (time.Time, string, *opencode.ModelRef, bool) {
	var latest time.Time
	var text string
	var model *opencode.ModelRef
	for _, message := range messages {
		if message.Info.Role != "user" || message.Info.Time.Created == 0 {
			continue
		}
		createdAt := time.UnixMilli(message.Info.Time.Created)
		if !latest.IsZero() && !createdAt.After(latest) {
			continue
		}
		var parts []string
		for _, part := range message.Parts {
			if value := strings.TrimSpace(part.Text); value != "" {
				parts = append(parts, value)
			}
		}
		latest = createdAt
		text = strings.Join(parts, " ")
		model = message.Info.Model
	}
	return latest, text, model, !latest.IsZero()
}

func sameDirectory(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func isRootDirectory(directory string) bool {
	directory = filepath.Clean(strings.TrimSpace(directory))
	if directory == string(filepath.Separator) {
		return true
	}
	volume := filepath.VolumeName(directory)
	return volume != "" && strings.EqualFold(directory, volume+string(filepath.Separator))
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
