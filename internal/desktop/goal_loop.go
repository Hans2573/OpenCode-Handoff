package desktop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
	"github.com/Hans2573/OpenCode-Handoff/internal/store"
)

var (
	goalCompletionPattern = regexp.MustCompile("(?s)```goal-status\\s*<<<\\s*\\{\\s*\"completed\"\\s*:\\s*true\\s*\\}\\s*>>>\\s*```\\s*$")
	goalBlockedPattern    = regexp.MustCompile("(?s)```goal-status\\s*<<<\\s*\\{.*\"completed\"\\s*:\\s*false.*\"blocked\"\\s*:\\s*true.*\\}\\s*>>>\\s*```\\s*$")
)

const goalContinuationPrompt = `继续完成这个 Session 最初的目标。不要停留在进度汇报；请继续检查、执行和验证工作。只有当目标已经完全达成时，才在回复末尾单独输出以下标记；未完成时不要输出它：

` + "```goal-status\n<<<{\"completed\":true}>>>\n```" + `

如果目标与不可关闭的安全边界存在确定冲突，并且已经穷尽安全替代方案，才在回复末尾输出：

` + "```goal-status\n<<<{\"completed\":false,\"blocked\":true,\"reason\":\"具体阻塞原因\",\"attempts\":[\"已经尝试的安全方案\"],\"required_capability\":\"缺少的条件\"}>>>\n```"

func (m *Manager) goalLoopSupervisor() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.processGoalLoops()
		}
	}
}

func (m *Manager) processGoalLoops() {
	if !m.goalMu.TryLock() {
		return
	}
	defer m.goalMu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 25*time.Second)
	defer cancel()
	loops, err := m.store.ListGoalLoops(ctx)
	if err != nil {
		m.logger.Error("list goal loops", "error", err)
		return
	}
	_ = m.syncGoalSessions(ctx)
	for _, loop := range loops {
		switch loop.Status {
		case domain.GoalLoopRunning, domain.GoalLoopRetrying, domain.GoalLoopWaitingApproval, domain.GoalLoopWaitingTakeover, domain.GoalLoopDeciding:
			if !loop.RetryAt.IsZero() && time.Now().UTC().Before(loop.RetryAt) {
				continue
			}
			m.processGoalLoop(ctx, loop)
		case domain.GoalLoopAwaitingConfirmation:
			if err := m.notifyGoalCompletion(ctx, loop, ""); err != nil {
				m.logger.Debug("notify Goal completion confirmation", "loop_id", loop.ID, "error", err)
			}
		}
	}
}

func (m *Manager) processGoalLoop(ctx context.Context, loop domain.GoalLoop) {
	if loop.SessionID == "" {
		if err := m.launchGoalLoop(ctx, &loop); err != nil {
			m.recordGoalFailure(ctx, &loop, err)
		}
		return
	}

	questions, err := m.raw.ListQuestions(ctx, loop.Directory)
	if err != nil {
		m.recordGoalFailure(ctx, &loop, err)
		return
	}
	permissions, err := m.raw.ListPermissions(ctx, loop.Directory)
	if err != nil {
		m.recordGoalFailure(ctx, &loop, err)
		return
	}
	if loop.AutomationMode == domain.GoalLoopAutonomous {
		handled, decisionErr := m.processAutonomousRequests(ctx, &loop, questions, permissions)
		if decisionErr != nil {
			m.recordSupervisorFailure(ctx, &loop, decisionErr)
			return
		}
		if handled {
			return
		}
	} else if sessionHasApproval(loop.SessionID, questions, permissions) {
		m.ensureGoalApprovalNotifications(ctx, loop, questions, permissions)
		if loop.Status != domain.GoalLoopWaitingApproval || loop.ConsecutiveFailures != 0 {
			loop.Status = domain.GoalLoopWaitingApproval
			loop.ConsecutiveFailures = 0
			loop.LastError = ""
			loop.RetryAt = time.Time{}
			loop.UpdatedAt = time.Now().UTC()
			_ = m.store.SaveGoalLoop(ctx, loop)
			_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "approval", "等待人工审批或回答")
		}
		return
	}

	statuses, err := m.raw.GetSessionStatuses(ctx, loop.Directory)
	if err != nil {
		m.recordGoalFailure(ctx, &loop, err)
		return
	}
	status, busy := statuses[loop.SessionID]
	if busy && !slices.Contains([]string{"", "idle", "error", "failed", "stopped", "interrupted"}, strings.ToLower(status.Type)) {
		nextStatus := domain.GoalLoopRunning
		if loop.CycleCount == 0 && loop.AttachedSession {
			nextStatus = domain.GoalLoopWaitingTakeover
		}
		if loop.Status != nextStatus || loop.ConsecutiveFailures != 0 {
			loop.Status = nextStatus
			loop.ConsecutiveFailures = 0
			loop.LastError = ""
			loop.RetryAt = time.Time{}
			loop.UpdatedAt = time.Now().UTC()
			_ = m.store.SaveGoalLoop(ctx, loop)
		}
		return
	}
	if loop.CycleCount == 0 {
		if err := m.launchGoalLoop(ctx, &loop); err != nil {
			m.recordGoalFailure(ctx, &loop, err)
		}
		return
	}

	messages, err := m.raw.GetMessages(ctx, loop.SessionID, loop.Directory, 50)
	if err != nil {
		m.recordGoalFailure(ctx, &loop, err)
		return
	}
	output, ok := opencode.LastAssistantOutput(messages)
	if !ok || output.MessageID == "" || (output.MessageID == loop.LastAssistantMessageID && loop.PendingFeedback == "") {
		return
	}
	if output.Error != "" {
		loop.LastAssistantMessageID = output.MessageID
		loop.PendingFeedback = "上一轮执行发生错误：" + output.Error + "\n请分析错误、选择安全的替代方案并继续完成 Goal。"
		m.recordGoalFailure(ctx, &loop, errors.New(output.Error))
		return
	}
	if goalCompletionPattern.MatchString(strings.TrimSpace(output.Text)) {
		loop.LastAssistantMessageID = output.MessageID
		loop.ConsecutiveFailures = 0
		loop.LastError = ""
		loop.RetryAt = time.Time{}
		loop.UpdatedAt = time.Now().UTC()
		if loop.RequireCompletionConfirmation {
			loop.Status = domain.GoalLoopAwaitingConfirmation
		} else {
			loop.Status = domain.GoalLoopCompleted
			loop.CompletedAt = loop.UpdatedAt
		}
		if err := m.store.SaveGoalLoop(ctx, loop); err != nil {
			m.logger.Error("save completed Goal Loop", "loop_id", loop.ID, "error", err)
			return
		}
		if loop.RequireCompletionConfirmation {
			_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "completion_pending", "Agent 已报告目标完成，等待人工确认")
			if err := m.notifyGoalCompletion(ctx, loop, output.Text); err != nil {
				m.logger.Warn("send Goal completion confirmation", "loop_id", loop.ID, "error", err)
			}
		} else {
			_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "completed", "Agent 已按协议报告目标完成")
			m.notifyGoalTerminal(ctx, loop, "Goal 已完成", output.Text)
		}
		_ = m.syncGoalSessions(ctx)
		return
	}
	if goalBlockedPattern.MatchString(strings.TrimSpace(output.Text)) {
		loop.LastAssistantMessageID = output.MessageID
		loop.Status = domain.GoalLoopBlocked
		loop.CompletedAt = time.Now().UTC()
		loop.UpdatedAt = loop.CompletedAt
		loop.ConsecutiveFailures = 0
		loop.LastError = "目标与安全边界冲突，且安全替代方案已经穷尽"
		loop.RetryAt = time.Time{}
		if err := m.store.SaveGoalLoop(ctx, loop); err != nil {
			m.logger.Error("save blocked Goal Loop", "loop_id", loop.ID, "error", err)
			return
		}
		_ = m.store.AppendGoalLoopEventDetails(ctx, loop.ID, "blocked", "Agent 已报告目标受阻", map[string]any{"output": strings.TrimSpace(output.Text)})
		_ = m.syncGoalSessions(ctx)
		m.notifyGoalTerminal(ctx, loop, "Goal 受阻", output.Text)
		return
	}

	prompt := goalContinuationPrompt
	if loop.PendingFeedback != "" {
		prompt = loop.PendingFeedback + "\n\n" + goalContinuationPrompt
	}
	if err := m.raw.SendPrompt(ctx, loop.SessionID, loop.Directory, prompt, goalModelRef(loop)); err != nil {
		m.recordGoalFailure(ctx, &loop, err)
		return
	}
	loop.LastAssistantMessageID = output.MessageID
	loop.PendingFeedback = ""
	loop.CycleCount++
	loop.Status = domain.GoalLoopRunning
	loop.ConsecutiveFailures = 0
	loop.LastError = ""
	loop.RetryAt = time.Time{}
	loop.UpdatedAt = time.Now().UTC()
	_ = m.store.SaveGoalLoop(ctx, loop)
	_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "continued", fmt.Sprintf("第 %d 轮：Session 空闲且目标未完成，已要求继续", loop.CycleCount))
}

func (m *Manager) ensureGoalApprovalNotifications(ctx context.Context, loop domain.GoalLoop, questions []opencode.QuestionRequest, permissions []opencode.PermissionRequest) {
	m.mu.RLock()
	service := m.engine
	m.mu.RUnlock()
	if service == nil || !service.Running() {
		return
	}
	for _, question := range questions {
		if question.SessionID != loop.SessionID {
			continue
		}
		if err := service.EnsureQuestion(ctx, loop.Directory, question); err != nil {
			m.logger.Warn("ensure Goal Question notification", "loop_id", loop.ID, "request_id", question.ID, "error", err)
		}
	}
	for _, permission := range permissions {
		if permission.SessionID != loop.SessionID {
			continue
		}
		if err := service.EnsurePermission(ctx, loop.Directory, permission); err != nil {
			m.logger.Warn("ensure Goal Permission notification", "loop_id", loop.ID, "request_id", permission.ID, "error", err)
		}
	}
}

func (m *Manager) launchGoalLoop(ctx context.Context, loop *domain.GoalLoop) error {
	if loop.SessionID == "" {
		session, err := m.raw.CreateSession(ctx, loop.Directory, loop.Name)
		if err != nil {
			return fmt.Errorf("创建 Session：%w", err)
		}
		loop.SessionID = session.ID
		loop.UpdatedAt = time.Now().UTC()
		if err := m.store.SaveGoalLoop(ctx, *loop); err != nil {
			return err
		}
		_ = m.syncGoalSessions(ctx)
		_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "session_created", "已创建 OpenCode Session "+session.ID)
	}
	prompt := "/goal " + loop.Goal
	if loop.AttachedSession {
		prompt += "\n\n这是一个接入现有 Session 的 Goal Loop。请先检查当前 Session 已经完成的工作和工作区现状，保留有效成果，然后继续完成剩余目标。不要假定此前任务失败，也不要无必要地重新实现。"
		prompt += "\n\n" + goalContinuationPrompt
	}
	if err := m.raw.SendPrompt(ctx, loop.SessionID, loop.Directory, prompt, goalModelRef(*loop)); err != nil {
		return fmt.Errorf("发送 /goal：%w", err)
	}
	loop.Status = domain.GoalLoopRunning
	loop.CycleCount = 1
	loop.ConsecutiveFailures = 0
	loop.LastError = ""
	loop.RetryAt = time.Time{}
	loop.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveGoalLoop(ctx, *loop); err != nil {
		return err
	}
	if loop.ModelProviderID != "" && loop.ModelID != "" {
		_ = m.store.RecordRecentModel(context.WithoutCancel(ctx), domain.SessionModel{
			ProviderID: loop.ModelProviderID, ModelID: loop.ModelID, ModelName: loop.ModelName, Variant: loop.ModelVariant,
		})
	}
	message := "已发送 /goal 并开始监听 Session"
	if loop.AttachedSession {
		message = "已接管现有 Session，发送 /goal 并开始监听"
	}
	_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "started", message)
	m.notifyGoalTerminal(ctx, *loop, "Goal 已启动", message)
	return nil
}

func (m *Manager) recordGoalFailure(ctx context.Context, loop *domain.GoalLoop, cause error) {
	loop.ConsecutiveFailures++
	loop.LastError = cause.Error()
	loop.UpdatedAt = time.Now().UTC()
	loop.Status = domain.GoalLoopRetrying
	loop.RetryAt = time.Now().UTC().Add(goalRetryDelay(loop.ConsecutiveFailures))
	_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "retry", fmt.Sprintf("第 %d 次连续失败，将在 %s 后自主恢复：%v", loop.ConsecutiveFailures, goalRetryDelay(loop.ConsecutiveFailures), cause))
	_ = m.store.SaveGoalLoop(ctx, *loop)
	if loop.ConsecutiveFailures == loop.FailureLimit {
		m.notifyGoalFailure(ctx, *loop)
	}
}

func (m *Manager) recordSupervisorFailure(ctx context.Context, loop *domain.GoalLoop, cause error) {
	m.recordGoalFailure(ctx, loop, cause)
	if loop.ConsecutiveFailures < loop.FailureLimit || loop.ConsecutiveFailures%loop.FailureLimit != 0 {
		return
	}
	oldModel := supervisorModelLabel(*loop)
	if loop.SupervisorModelProviderID != loop.ModelProviderID || loop.SupervisorModelID != loop.ModelID || loop.SupervisorModelVariant != loop.ModelVariant {
		loop.SupervisorModelProviderID = loop.ModelProviderID
		loop.SupervisorModelID = loop.ModelID
		loop.SupervisorModelName = loop.ModelName
		loop.SupervisorModelVariant = loop.ModelVariant
	} else if loop.SupervisorModelID != supervisorAgentDefaultModel {
		loop.SupervisorModelProviderID = ""
		loop.SupervisorModelID = supervisorAgentDefaultModel
		loop.SupervisorModelName = "Agent 默认模型"
		loop.SupervisorModelVariant = ""
	}
	loop.SupervisorSessionID = ""
	clearSupervisorDecision(loop)
	loop.UpdatedAt = time.Now().UTC()
	_ = m.store.SaveGoalLoop(ctx, *loop)
	_ = m.syncGoalSessions(ctx)
	_ = m.store.AppendGoalLoopEventDetails(ctx, loop.ID, "supervisor_recovery", "AI 监督连续失败，已重建监督 Session 并切换备用模型", map[string]any{"fromModel": oldModel, "toModel": supervisorModelLabel(*loop), "failures": loop.ConsecutiveFailures, "error": cause.Error()})
}

func (m *Manager) notifyGoalTerminal(ctx context.Context, loop domain.GoalLoop, title, detail string) {
	m.mu.RLock()
	service := m.engine
	m.mu.RUnlock()
	if service == nil || !service.Running() {
		return
	}
	sessionID := loop.SessionID
	if sessionID == "" {
		sessionID = loop.ID
	}
	item := domain.Handoff{
		ID: newGoalLoopID(), SessionID: sessionID, SessionName: loop.Name,
		Directory: loop.Directory, ProjectName: loop.ProjectName, Type: domain.HandoffGoalStatus,
		LastAssistantMessageID: fmt.Sprintf("goal-status-%s-%s-%d", loop.ID, loop.Status, loop.CycleCount),
		LastAssistantText:      strings.TrimSpace(title + "\n\n" + detail), CreatedAt: time.Now().UTC(),
	}
	if err := m.store.Create(ctx, item); err != nil {
		return
	}
	ref, err := service.SendHandoff(ctx, item)
	if err != nil {
		_ = m.store.DeleteUnbound(ctx, item.ID)
		return
	}
	_ = m.store.BindMessage(ctx, item.ID, ref)
	_ = m.store.CloseGoalStatusHandoff(ctx, item.ID)
}

func (m *Manager) notifyGoalFailure(ctx context.Context, loop domain.GoalLoop) {
	m.mu.RLock()
	service := m.engine
	m.mu.RUnlock()
	if service == nil || !service.Running() {
		return
	}
	sessionID := loop.SessionID
	if sessionID == "" {
		sessionID = loop.ID
	}
	item := domain.Handoff{
		ID: newGoalLoopID(), SessionID: sessionID, SessionName: loop.Name,
		Directory: loop.Directory, ProjectName: loop.ProjectName, Type: domain.HandoffError,
		LastAssistantMessageID: fmt.Sprintf("goal-loop-failure-%s-%d", loop.ID, loop.ConsecutiveFailures),
		ErrorText:              fmt.Sprintf("Goal Loop 连续失败 %d 次，已进入自动恢复并会持续重试。\n%s", loop.ConsecutiveFailures, loop.LastError),
		CreatedAt:              time.Now().UTC(),
	}
	if err := m.store.Create(ctx, item); err != nil {
		return
	}
	ref, err := service.SendHandoff(ctx, item)
	if err != nil {
		_ = m.store.DeleteUnbound(ctx, item.ID)
		return
	}
	_ = m.store.BindMessage(ctx, item.ID, ref)
	_ = m.store.CloseGoalStatusHandoff(ctx, item.ID)
}

func (m *Manager) notifyGoalCompletion(ctx context.Context, loop domain.GoalLoop, assistantText string) error {
	m.mu.RLock()
	service := m.engine
	m.mu.RUnlock()
	if service == nil || !service.Running() {
		return errors.New("飞书服务未运行")
	}
	handoffID := "goal-completion-" + loop.ID
	item := domain.Handoff{
		ID: handoffID, SessionID: loop.SessionID, SessionName: loop.Name,
		Directory: loop.Directory, ProjectName: loop.ProjectName, Type: domain.HandoffGoalCompletion,
		LastAssistantMessageID: loop.LastAssistantMessageID,
		LastAssistantText:      strings.TrimSpace(assistantText),
		CreatedAt:              time.Now().UTC(),
	}
	if item.LastAssistantMessageID == "" {
		item.LastAssistantMessageID = "goal-completion:" + loop.ID
	}
	if item.LastAssistantText == "" {
		messages, err := m.raw.GetMessages(ctx, loop.SessionID, loop.Directory, 50)
		if err == nil {
			if output, ok := opencode.LastAssistantOutput(messages); ok {
				item.LastAssistantText = strings.TrimSpace(output.Text)
			}
		}
	}
	if err := m.store.Create(ctx, item); errors.Is(err, store.ErrDuplicate) {
		existing, getErr := m.store.GetByID(ctx, handoffID)
		if getErr != nil {
			return getErr
		}
		if existing.FeishuMessageID != "" {
			return nil
		}
		item = existing
	} else if err != nil {
		return err
	}
	ref, err := service.SendHandoff(ctx, item)
	if err != nil {
		return err
	}
	if err := m.store.BindMessage(ctx, item.ID, ref); err != nil {
		return err
	}
	_ = m.store.AppendGoalLoopEvent(context.WithoutCancel(ctx), loop.ID, "completion_notified", "已发送飞书完成确认")
	return nil
}

func goalRetryDelay(failures int) time.Duration {
	delays := []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second, 2 * time.Minute, 5 * time.Minute}
	if failures < 1 {
		return delays[0]
	}
	if failures > len(delays) {
		return delays[len(delays)-1]
	}
	return delays[failures-1]
}

func sessionHasApproval(sessionID string, questions []opencode.QuestionRequest, permissions []opencode.PermissionRequest) bool {
	for _, item := range questions {
		if item.SessionID == sessionID {
			return true
		}
	}
	for _, item := range permissions {
		if item.SessionID == sessionID {
			return true
		}
	}
	return false
}

func (m *Manager) GetGoalLoops() (GoalLoopPage, error) {
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
	defer cancel()
	loops, err := m.store.ListGoalLoops(ctx)
	if err != nil {
		return GoalLoopPage{}, err
	}
	views := make([]GoalLoopView, 0, len(loops))
	for _, loop := range loops {
		views = append(views, goalLoopView(loop))
	}
	approvals := m.collectLoopApprovals(ctx, loops)
	return GoalLoopPage{GeneratedAt: time.Now().UTC(), Loops: views, Approvals: approvals}, nil
}

func (m *Manager) GetGoalLoopEvents(loopID string) ([]GoalLoopEventView, error) {
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	items, err := m.store.ListGoalLoopEvents(ctx, loopID, 100)
	if err != nil {
		return nil, err
	}
	result := make([]GoalLoopEventView, 0, len(items))
	for _, item := range items {
		result = append(result, GoalLoopEventView{ID: item.ID, Type: item.Type, Message: item.Message, Metadata: item.Metadata, CreatedAt: item.CreatedAt})
	}
	return result, nil
}

func (m *Manager) CreateGoalLoop(input GoalLoopInput) (GoalLoopPage, error) {
	m.goalMu.Lock()
	defer m.goalMu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()
	project, model, err := m.validateGoalLoopInput(ctx, input)
	if err != nil {
		return GoalLoopPage{}, err
	}
	if input.StartNow && !input.GoalCommandConfirmed {
		return GoalLoopPage{}, errors.New("请先确认所选 Agent 支持 /goal")
	}
	now := time.Now().UTC()
	automationMode := input.AutomationMode
	if automationMode == "" {
		automationMode = domain.GoalLoopAutonomous
	}
	permissionApprovalMode := input.PermissionApprovalMode
	if permissionApprovalMode == "" {
		permissionApprovalMode = domain.GoalPermissionAI
	}
	supervisorModel, err := m.goalSupervisorModel(ctx, input, model)
	if err != nil {
		return GoalLoopPage{}, err
	}
	supervisorVariant := input.SupervisorModelVariant
	if input.SupervisorModelProviderID == "" || input.SupervisorModelID == "" {
		supervisorVariant = input.ModelVariant
	}
	if err := m.validateGoalSessionBinding(ctx, input.SessionID, project.Directory, ""); err != nil {
		return GoalLoopPage{}, err
	}
	loop := domain.GoalLoop{
		ID: newGoalLoopID(), Name: goalName(input.Goal), Goal: strings.TrimSpace(input.Goal),
		ProjectID: project.ProjectID, ProjectName: project.Name, Directory: project.Directory,
		AgentID: store.DefaultAgentID, AgentName: "OpenCode", Status: domain.GoalLoopDraft,
		ModelProviderID: model.ProviderID, ModelID: model.ID, ModelName: model.Name, ModelVariant: input.ModelVariant,
		SessionID: strings.TrimSpace(input.SessionID), AttachedSession: strings.TrimSpace(input.SessionID) != "",
		AutomationMode: automationMode, PermissionApprovalMode: permissionApprovalMode,
		AllowedDirectories:        compactStrings(input.AllowedDirectories),
		SupervisorModelProviderID: supervisorModel.ProviderID, SupervisorModelID: supervisorModel.ID,
		SupervisorModelName: supervisorModel.Name, SupervisorModelVariant: supervisorVariant,
		RequireCompletionConfirmation: input.RequireCompletionConfirmation,
		FailureLimit:                  input.FailureLimit, CreatedAt: now, UpdatedAt: now,
	}
	if err := m.store.CreateGoalLoop(ctx, loop); err != nil {
		return GoalLoopPage{}, err
	}
	_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "created", "已创建 Goal Loop")
	if input.StartNow {
		if loop.AttachedSession {
			if err := m.prepareAttachedGoalLoop(ctx, &loop); err != nil {
				m.recordGoalFailure(ctx, &loop, err)
			} else {
				m.processGoalLoop(ctx, loop)
			}
		} else if err := m.launchGoalLoop(ctx, &loop); err != nil {
			m.recordGoalFailure(ctx, &loop, err)
		}
	}
	return m.GetGoalLoops()
}

func (m *Manager) UpdateGoalLoop(id string, input GoalLoopInput) (GoalLoopPage, error) {
	m.goalMu.Lock()
	defer m.goalMu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	loop, err := m.store.GetGoalLoop(ctx, id)
	if err != nil {
		return GoalLoopPage{}, err
	}
	if !slices.Contains([]string{domain.GoalLoopDraft, domain.GoalLoopBlocked, domain.GoalLoopTerminated}, loop.Status) {
		return GoalLoopPage{}, errors.New("只能编辑草稿、受阻或已终止的 Goal")
	}
	terminalEdit := loop.Status == domain.GoalLoopBlocked || loop.Status == domain.GoalLoopTerminated
	if terminalEdit && (input.ProjectID != loop.ProjectID || strings.TrimSpace(input.SessionID) != loop.SessionID) {
		return GoalLoopPage{}, errors.New("编辑终态 Goal 时不能更换项目或 Session；如需更换，请创建新 Goal")
	}
	project, model, err := m.validateGoalLoopInput(ctx, input)
	if err != nil {
		return GoalLoopPage{}, err
	}
	supervisorModel, err := m.goalSupervisorModel(ctx, input, model)
	if err != nil {
		return GoalLoopPage{}, err
	}
	supervisorVariant := input.SupervisorModelVariant
	if input.SupervisorModelProviderID == "" || input.SupervisorModelID == "" {
		supervisorVariant = input.ModelVariant
	}
	if err := m.validateGoalSessionBinding(ctx, input.SessionID, project.Directory, loop.ID); err != nil {
		return GoalLoopPage{}, err
	}
	loop.Name = goalName(input.Goal)
	loop.Goal = strings.TrimSpace(input.Goal)
	loop.ProjectID, loop.ProjectName, loop.Directory = project.ProjectID, project.Name, project.Directory
	loop.ModelProviderID, loop.ModelID, loop.ModelName, loop.ModelVariant = model.ProviderID, model.ID, model.Name, input.ModelVariant
	if !terminalEdit {
		loop.SessionID, loop.AttachedSession = strings.TrimSpace(input.SessionID), strings.TrimSpace(input.SessionID) != ""
	}
	loop.AutomationMode = input.AutomationMode
	if loop.AutomationMode == "" {
		loop.AutomationMode = domain.GoalLoopAutonomous
	}
	loop.PermissionApprovalMode = input.PermissionApprovalMode
	if loop.PermissionApprovalMode == "" {
		loop.PermissionApprovalMode = domain.GoalPermissionAI
	}
	loop.AllowedDirectories = compactStrings(input.AllowedDirectories)
	loop.SupervisorModelProviderID, loop.SupervisorModelID, loop.SupervisorModelName, loop.SupervisorModelVariant = supervisorModel.ProviderID, supervisorModel.ID, supervisorModel.Name, supervisorVariant
	loop.FailureLimit = input.FailureLimit
	loop.RequireCompletionConfirmation = input.RequireCompletionConfirmation
	loop.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveGoalLoop(ctx, loop); err != nil {
		return GoalLoopPage{}, err
	}
	eventMessage := "已更新 Goal 草稿"
	if loop.Status == domain.GoalLoopBlocked || loop.Status == domain.GoalLoopTerminated {
		eventMessage = "已更新终态 Goal 配置，可重新启动"
	}
	_ = m.store.AppendGoalLoopEvent(ctx, id, "updated", eventMessage)
	return m.GetGoalLoops()
}

func (m *Manager) StartGoalLoop(id string, goalCommandConfirmed bool) (GoalLoopPage, error) {
	if !goalCommandConfirmed {
		return GoalLoopPage{}, errors.New("请先确认所选 Agent 支持 /goal")
	}
	m.goalMu.Lock()
	defer m.goalMu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()
	loop, err := m.store.GetGoalLoop(ctx, id)
	if err != nil {
		return GoalLoopPage{}, err
	}
	if loop.Status != domain.GoalLoopDraft {
		return GoalLoopPage{}, errors.New("只有草稿可以启动")
	}
	if err := m.validateGoalSessionBinding(ctx, loop.SessionID, loop.Directory, loop.ID); err != nil {
		return GoalLoopPage{}, err
	}
	if loop.AttachedSession {
		if err := m.prepareAttachedGoalLoop(ctx, &loop); err != nil {
			m.recordGoalFailure(ctx, &loop, err)
		} else {
			m.processGoalLoop(ctx, loop)
		}
	} else if err := m.launchGoalLoop(ctx, &loop); err != nil {
		m.recordGoalFailure(ctx, &loop, err)
	}
	return m.GetGoalLoops()
}

func (m *Manager) RestartGoalLoop(id string, goalCommandConfirmed bool) (GoalLoopPage, error) {
	if !goalCommandConfirmed {
		return GoalLoopPage{}, errors.New("请先确认所选 Agent 支持 /goal")
	}
	m.goalMu.Lock()
	defer m.goalMu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()
	loop, err := m.store.GetGoalLoop(ctx, id)
	if err != nil {
		return GoalLoopPage{}, err
	}
	if loop.Status != domain.GoalLoopBlocked && loop.Status != domain.GoalLoopTerminated {
		return GoalLoopPage{}, errors.New("只有受阻或已终止的 Goal 可以重新启动")
	}
	if err := m.validateGoalSessionBinding(ctx, loop.SessionID, loop.Directory, loop.ID); err != nil {
		return GoalLoopPage{}, err
	}
	if loop.SessionID == "" {
		resetGoalLoopForRestart(&loop)
		if err := m.store.SaveGoalLoop(ctx, loop); err != nil {
			return GoalLoopPage{}, err
		}
		_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "restart_requested", "已使用更新后的配置重新启动 Goal")
		if err := m.launchGoalLoop(ctx, &loop); err != nil {
			m.recordGoalFailure(ctx, &loop, err)
		}
	} else if err := m.prepareGoalLoopRestart(ctx, &loop); err != nil {
		m.recordGoalFailure(ctx, &loop, err)
	} else {
		m.processGoalLoop(ctx, loop)
	}
	return m.GetGoalLoops()
}

func resetGoalLoopForRestart(loop *domain.GoalLoop) {
	loop.Status = domain.GoalLoopWaitingTakeover
	loop.CycleCount = 0
	loop.ConsecutiveFailures = 0
	loop.LastAssistantMessageID = ""
	loop.LastError = ""
	loop.PendingFeedback = ""
	loop.SupervisorLastMessageID = ""
	clearSupervisorDecision(loop)
	loop.RetryAt = time.Time{}
	loop.CompletedAt = time.Time{}
	loop.UpdatedAt = time.Now().UTC()
}

func (m *Manager) prepareGoalLoopRestart(ctx context.Context, loop *domain.GoalLoop) error {
	messages, err := m.raw.GetMessages(ctx, loop.SessionID, loop.Directory, 50)
	if err != nil {
		return fmt.Errorf("读取原 Session 历史：%w", err)
	}
	resetGoalLoopForRestart(loop)
	if output, ok := opencode.LastAssistantOutput(messages); ok {
		loop.LastAssistantMessageID = output.MessageID
	}
	if err := m.store.SaveGoalLoop(ctx, *loop); err != nil {
		return err
	}
	_ = m.syncGoalSessions(ctx)
	_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "restart_requested", "已复用原 Session，并使用更新后的配置重新启动 Goal")
	return nil
}

func (m *Manager) PauseGoalLoop(id string) (GoalLoopPage, error) {
	return m.setGoalLoopPaused(id, false)
}

func (m *Manager) TerminateGoalLoop(id string) (GoalLoopPage, error) {
	return m.setGoalLoopPaused(id, true)
}

func (m *Manager) TerminateGoalLoopAndSession(id string) (GoalLoopPage, error) {
	page, err := m.setGoalLoopPaused(id, true)
	if err != nil {
		return page, err
	}
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	loop, err := m.store.GetGoalLoop(ctx, id)
	if err == nil && loop.SessionID != "" {
		err = m.raw.AbortSession(ctx, loop.SessionID, loop.Directory)
	}
	return page, err
}

func (m *Manager) setGoalLoopPaused(id string, terminate bool) (GoalLoopPage, error) {
	m.goalMu.Lock()
	defer m.goalMu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
	defer cancel()
	loop, err := m.store.GetGoalLoop(ctx, id)
	if err != nil {
		return GoalLoopPage{}, err
	}
	wasAwaitingConfirmation := loop.Status == domain.GoalLoopAwaitingConfirmation
	handoff, _ := m.store.GetOpenGoalCompletionHandoff(ctx, loop.SessionID)
	loop.UpdatedAt = time.Now().UTC()
	loop.RetryAt = time.Time{}
	if terminate {
		loop.Status = domain.GoalLoopTerminated
		_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "terminated", "用户终止了 Goal Loop")
		m.notifyGoalTerminal(ctx, loop, "Goal 已终止", "自动监督和继续消息已经停止，原 Session 保留。")
	} else {
		loop.Status = domain.GoalLoopPaused
		_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "paused", "用户暂停了 Goal Loop")
	}
	if err := m.store.SaveGoalLoop(ctx, loop); err != nil {
		return GoalLoopPage{}, err
	}
	if wasAwaitingConfirmation {
		_ = m.store.CloseGoalCompletionHandoff(ctx, loop.SessionID)
		message := "Goal 已在桌面应用中暂停"
		if terminate {
			message = "Goal 已在桌面应用中终止"
		}
		m.replyApprovalResolved(ctx, handoff, message)
	}
	_ = m.syncGoalSessions(ctx)
	return m.GetGoalLoops()
}

func (m *Manager) ResumeGoalLoop(id string) (GoalLoopPage, error) {
	m.goalMu.Lock()
	defer m.goalMu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()
	loop, err := m.store.GetGoalLoop(ctx, id)
	if err != nil {
		return GoalLoopPage{}, err
	}
	if loop.Status != domain.GoalLoopPaused && loop.Status != domain.GoalLoopAwaitingConfirmation {
		return GoalLoopPage{}, errors.New("只有已暂停或等待完成确认的 Goal 可以继续")
	}
	wasAwaitingConfirmation := loop.Status == domain.GoalLoopAwaitingConfirmation
	handoff, _ := m.store.GetOpenGoalCompletionHandoff(ctx, loop.SessionID)
	loop.ConsecutiveFailures = 0
	loop.LastError = ""
	loop.RetryAt = time.Time{}
	if loop.SessionID == "" || loop.CycleCount == 0 {
		err = m.launchGoalLoop(ctx, &loop)
	} else {
		err = m.raw.SendPrompt(ctx, loop.SessionID, loop.Directory, goalContinuationPrompt, goalModelRef(loop))
		if err == nil {
			loop.CycleCount++
			loop.Status = domain.GoalLoopRunning
			loop.UpdatedAt = time.Now().UTC()
			err = m.store.SaveGoalLoop(ctx, loop)
		}
	}
	if err != nil {
		m.recordGoalFailure(ctx, &loop, err)
	} else if wasAwaitingConfirmation {
		_ = m.store.CloseGoalCompletionHandoff(ctx, loop.SessionID)
		m.replyApprovalResolved(ctx, handoff, "Goal 已在桌面应用中选择继续")
	}
	eventMessage := "用户恢复了 Goal Loop"
	if wasAwaitingConfirmation {
		eventMessage = "用户未确认完成，Goal Loop 继续运行"
	}
	_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "resumed", eventMessage)
	return m.GetGoalLoops()
}

func (m *Manager) ConfirmGoalLoopComplete(id string) (GoalLoopPage, error) {
	m.goalMu.Lock()
	defer m.goalMu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	loop, err := m.store.GetGoalLoop(ctx, id)
	if err != nil {
		return GoalLoopPage{}, err
	}
	if loop.Status != domain.GoalLoopAwaitingConfirmation {
		return GoalLoopPage{}, errors.New("该 Goal 当前不等待完成确认")
	}
	handoff, _ := m.store.GetOpenGoalCompletionHandoff(ctx, loop.SessionID)
	loop.Status = domain.GoalLoopCompleted
	loop.CompletedAt = time.Now().UTC()
	loop.UpdatedAt = loop.CompletedAt
	if err := m.store.SaveGoalLoop(ctx, loop); err != nil {
		return GoalLoopPage{}, err
	}
	_ = m.store.CloseGoalCompletionHandoff(ctx, loop.SessionID)
	m.replyApprovalResolved(ctx, handoff, "Goal 已在桌面应用中确认完成")
	_ = m.syncGoalSessions(ctx)
	_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "completed", "用户确认目标已完成")
	m.notifyGoalTerminal(ctx, loop, "Goal 已完成", "用户已确认目标完成。")
	return m.GetGoalLoops()
}

func (m *Manager) DeleteGoalLoop(id string) (GoalLoopPage, error) {
	m.goalMu.Lock()
	defer m.goalMu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	loop, err := m.store.GetGoalLoop(ctx, id)
	if err != nil {
		return GoalLoopPage{}, err
	}
	if slices.Contains([]string{domain.GoalLoopRunning, domain.GoalLoopRetrying, domain.GoalLoopWaitingApproval, domain.GoalLoopWaitingTakeover, domain.GoalLoopDeciding}, loop.Status) {
		return GoalLoopPage{}, errors.New("请先暂停或终止运行中的 Goal")
	}
	handoff, _ := m.store.GetOpenGoalCompletionHandoff(ctx, loop.SessionID)
	if err := m.store.DeleteGoalLoop(ctx, id); err != nil {
		return GoalLoopPage{}, err
	}
	_ = m.store.CloseGoalCompletionHandoff(ctx, loop.SessionID)
	m.replyApprovalResolved(ctx, handoff, "Goal 已在桌面应用中删除")
	_ = m.syncGoalSessions(ctx)
	return m.GetGoalLoops()
}

func (m *Manager) syncGoalSessions(ctx context.Context) error {
	loops, err := m.store.ListGoalLoops(ctx)
	if err != nil {
		return err
	}
	items := make(map[string]struct{})
	for _, loop := range loops {
		if loop.SessionID == "" || !goalLoopActive(loop.Status) {
			continue
		}
		items[routeKey(loop.Directory)+"\x00"+loop.SessionID] = struct{}{}
		if loop.SupervisorSessionID != "" {
			items[routeKey(loop.Directory)+"\x00"+loop.SupervisorSessionID] = struct{}{}
		}
	}
	m.routes.ReplaceGoalSessions(items)
	autonomous := make(map[string]struct{})
	for _, loop := range loops {
		if loop.AutomationMode != domain.GoalLoopAutonomous || !goalLoopActive(loop.Status) {
			continue
		}
		if loop.SessionID != "" {
			autonomous[routeKey(loop.Directory)+"\x00"+loop.SessionID] = struct{}{}
		}
		if loop.SupervisorSessionID != "" {
			autonomous[routeKey(loop.Directory)+"\x00"+loop.SupervisorSessionID] = struct{}{}
		}
	}
	m.routes.ReplaceAutonomousGoalSessions(autonomous)
	return nil
}

func (m *Manager) ReplyLoopPermission(requestID, directory, decision string) (GoalLoopPage, error) {
	if !m.routes.Enabled(directory) {
		return GoalLoopPage{}, errors.New("该项目尚未接入飞书渠道")
	}
	m.goalMu.Lock()
	defer m.goalMu.Unlock()
	reply := opencode.PermissionReply(decision)
	if !reply.Valid() {
		return GoalLoopPage{}, errors.New("无效的权限审批结果")
	}
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	permissions, err := m.raw.ListPermissions(ctx, directory)
	if err != nil {
		return GoalLoopPage{}, err
	}
	permission, found := findPermissionRequest(requestID, permissions)
	if !found {
		return GoalLoopPage{}, errors.New("该权限请求已不存在或已被处理")
	}
	loop, autonomous, err := m.autonomousGoalForSession(ctx, permission.SessionID, directory)
	if err != nil {
		return GoalLoopPage{}, err
	}
	if autonomous {
		if reply == opencode.PermissionAlways {
			return GoalLoopPage{}, errors.New("完全自主 Goal 不允许永久授权；请选择允许一次或拒绝")
		}
		if reply == opencode.PermissionOnce && loop.PermissionApprovalMode != domain.GoalPermissionAllowAll {
			if reason := hardBlockedPermission(loop, permission); reason != "" {
				return GoalLoopPage{}, errors.New("该请求触发不可关闭的安全边界，不能手动覆盖：" + reason)
			}
		}
	}
	handoff, _ := m.store.GetOpenHandoffByRequest(ctx, requestID)
	if err := m.raw.ReplyPermission(ctx, requestID, directory, reply); err != nil {
		return GoalLoopPage{}, err
	}
	if autonomous {
		m.finishManualGoalOverride(ctx, &loop, "permission", requestID, decision, reply == opencode.PermissionReject)
	}
	_ = m.store.CloseHandoffRequest(ctx, requestID)
	m.replyApprovalResolved(ctx, handoff, "该 Permission 已在桌面应用中处理："+decision)
	return m.GetGoalLoops()
}

func (m *Manager) ReplyLoopQuestion(input QuestionReplyInput) (GoalLoopPage, error) {
	if !m.routes.Enabled(input.Directory) {
		return GoalLoopPage{}, errors.New("该项目尚未接入飞书渠道")
	}
	m.goalMu.Lock()
	defer m.goalMu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	questions, err := m.raw.ListQuestions(ctx, input.Directory)
	if err != nil {
		return GoalLoopPage{}, err
	}
	question, found := findQuestionRequest(input.RequestID, questions)
	if !found {
		return GoalLoopPage{}, errors.New("该选择请求已不存在或已被处理")
	}
	loop, autonomous, err := m.autonomousGoalForSession(ctx, question.SessionID, input.Directory)
	if err != nil {
		return GoalLoopPage{}, err
	}
	if !input.Reject {
		if err := validateQuestionAnswers(question, input.Answers); err != nil {
			return GoalLoopPage{}, err
		}
		if autonomous && questionAnswersHardBlocked(question, input.Answers) {
			return GoalLoopPage{}, errors.New("该答案触发不可关闭的安全边界，不能手动覆盖")
		}
	}
	handoff, _ := m.store.GetOpenHandoffByRequest(ctx, input.RequestID)
	err = nil
	if input.Reject {
		err = m.raw.RejectQuestion(ctx, input.RequestID, input.Directory)
	} else {
		err = m.raw.ReplyQuestion(ctx, input.RequestID, input.Directory, input.Answers)
	}
	if err != nil {
		return GoalLoopPage{}, err
	}
	if autonomous {
		m.finishManualGoalOverride(ctx, &loop, "question", input.RequestID, map[bool]string{true: "reject", false: "answer"}[input.Reject], input.Reject)
	}
	_ = m.store.CloseHandoffRequest(ctx, input.RequestID)
	result := "已在桌面应用中提交回答"
	if input.Reject {
		result = "已在桌面应用中拒绝该 Question"
	}
	m.replyApprovalResolved(ctx, handoff, result)
	return m.GetGoalLoops()
}

func (m *Manager) autonomousGoalForSession(ctx context.Context, sessionID, directory string) (domain.GoalLoop, bool, error) {
	loop, err := m.store.GetGoalLoopBySession(ctx, sessionID, directory)
	if errors.Is(err, store.ErrNotFound) {
		return domain.GoalLoop{}, false, nil
	}
	if err != nil {
		return domain.GoalLoop{}, false, err
	}
	return loop, goalLoopActive(loop.Status) && loop.AutomationMode == domain.GoalLoopAutonomous, nil
}

func (m *Manager) finishManualGoalOverride(ctx context.Context, loop *domain.GoalLoop, requestType, requestID, decision string, rejected bool) {
	if rejected {
		loop.PendingFeedback = rejectedDecisionFeedback + "\n\n原因：用户在桌面应用中手动拒绝了当前请求。"
	}
	clearSupervisorDecision(loop)
	loop.Status = goalActiveStatus(*loop)
	loop.ConsecutiveFailures = 0
	loop.LastError = ""
	loop.RetryAt = time.Time{}
	loop.UpdatedAt = time.Now().UTC()
	_ = m.store.SaveGoalLoop(ctx, *loop)
	_ = m.store.AppendGoalLoopEventDetails(ctx, loop.ID, "decision_overridden", "用户在 AI 提交前手动处理了请求", map[string]any{"requestId": requestID, "requestType": requestType, "decision": decision})
}

func findPermissionRequest(requestID string, items []opencode.PermissionRequest) (opencode.PermissionRequest, bool) {
	for _, item := range items {
		if item.ID == requestID {
			return item, true
		}
	}
	return opencode.PermissionRequest{}, false
}

func findQuestionRequest(requestID string, items []opencode.QuestionRequest) (opencode.QuestionRequest, bool) {
	for _, item := range items {
		if item.ID == requestID {
			return item, true
		}
	}
	return opencode.QuestionRequest{}, false
}

func (m *Manager) replyApprovalResolved(ctx context.Context, handoff domain.Handoff, message string) {
	if handoff.FeishuMessageID == "" {
		return
	}
	m.mu.RLock()
	service := m.engine
	m.mu.RUnlock()
	if service != nil && service.Running() {
		_ = service.ReplyToHandoff(ctx, handoff.FeishuMessageID, "✅ "+message+"。此待办已关闭。")
	}
}

func (m *Manager) validateGoalLoopInput(ctx context.Context, input GoalLoopInput) (domain.ProjectRoute, opencode.Model, error) {
	if strings.TrimSpace(input.Goal) == "" {
		return domain.ProjectRoute{}, opencode.Model{}, errors.New("目标不能为空")
	}
	if input.FailureLimit < 1 || input.FailureLimit > 100 {
		return domain.ProjectRoute{}, opencode.Model{}, errors.New("连续失败次数必须在 1 到 100 之间")
	}
	if input.AutomationMode != "" && input.AutomationMode != domain.GoalLoopAutonomous && input.AutomationMode != domain.GoalLoopManual {
		return domain.ProjectRoute{}, opencode.Model{}, errors.New("无效的 Goal 自动化模式")
	}
	if input.PermissionApprovalMode != "" && input.PermissionApprovalMode != domain.GoalPermissionAI && input.PermissionApprovalMode != domain.GoalPermissionAllowAll {
		return domain.ProjectRoute{}, opencode.Model{}, errors.New("无效的 Goal 权限审批策略")
	}
	models, err := m.raw.ListModels(ctx)
	if err != nil {
		return domain.ProjectRoute{}, opencode.Model{}, fmt.Errorf("读取 OpenCode 模型：%w", err)
	}
	var selectedModel opencode.Model
	for _, model := range models {
		if model.ProviderID == input.ModelProviderID && model.ID == input.ModelID {
			selectedModel = model
			break
		}
	}
	if selectedModel.ProviderID == "" || selectedModel.ID == "" {
		return domain.ProjectRoute{}, opencode.Model{}, errors.New("请选择可用模型")
	}
	if input.ModelVariant != "" && !slices.Contains(selectedModel.Variants, input.ModelVariant) {
		return domain.ProjectRoute{}, opencode.Model{}, errors.New("所选模型档位已不可用，请重新选择")
	}
	routes, err := m.store.ListProjectRoutes(ctx)
	if err != nil {
		return domain.ProjectRoute{}, opencode.Model{}, err
	}
	for _, route := range routes {
		if route.ProjectID == input.ProjectID {
			if !route.RouteEnabled {
				return domain.ProjectRoute{}, opencode.Model{}, errors.New("Goal Loop 仅支持已接入飞书的项目")
			}
			return route, selectedModel, nil
		}
	}
	return domain.ProjectRoute{}, opencode.Model{}, errors.New("未找到所选项目")
}

func (m *Manager) goalSupervisorModel(ctx context.Context, input GoalLoopInput, fallback opencode.Model) (opencode.Model, error) {
	if input.SupervisorModelProviderID == "" || input.SupervisorModelID == "" {
		return fallback, nil
	}
	models, err := m.raw.ListModels(ctx)
	if err != nil {
		return opencode.Model{}, err
	}
	for _, model := range models {
		if model.ProviderID == input.SupervisorModelProviderID && model.ID == input.SupervisorModelID {
			if input.SupervisorModelVariant != "" && !slices.Contains(model.Variants, input.SupervisorModelVariant) {
				return opencode.Model{}, errors.New("所选监督模型档位已不可用")
			}
			return model, nil
		}
	}
	return opencode.Model{}, errors.New("请选择可用的监督模型")
}

func (m *Manager) validateGoalSessionBinding(ctx context.Context, sessionID, directory, exceptLoopID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	session, err := m.raw.GetSession(ctx, sessionID, directory)
	if err != nil {
		return fmt.Errorf("读取现有 Session：%w", err)
	}
	if session.ParentID != "" {
		return errors.New("只能将顶层 Session 接入 Goal Loop")
	}
	loops, err := m.store.ListGoalLoops(ctx)
	if err != nil {
		return err
	}
	for _, loop := range loops {
		if loop.ID != exceptLoopID && loop.SessionID == sessionID && routeKey(loop.Directory) == routeKey(directory) && goalLoopActive(loop.Status) {
			return errors.New("该 Session 已绑定到另一个活动 Goal")
		}
	}
	return nil
}

func (m *Manager) prepareAttachedGoalLoop(ctx context.Context, loop *domain.GoalLoop) error {
	messages, err := m.raw.GetMessages(ctx, loop.SessionID, loop.Directory, 50)
	if err != nil {
		return fmt.Errorf("读取现有 Session 历史：%w", err)
	}
	if output, ok := opencode.LastAssistantOutput(messages); ok {
		loop.LastAssistantMessageID = output.MessageID
	}
	loop.Status = domain.GoalLoopWaitingTakeover
	loop.CycleCount = 0
	loop.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveGoalLoop(ctx, *loop); err != nil {
		return err
	}
	_ = m.syncGoalSessions(ctx)
	_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "attached", "已绑定现有 Session，等待安全接管")
	return nil
}

func (m *Manager) GetGoalModels() ([]GoalModelView, error) {
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
	defer cancel()
	models, err := m.raw.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]GoalModelView, 0, len(models))
	for _, model := range models {
		if model.ProviderID == "" || model.ID == "" {
			continue
		}
		result = append(result, GoalModelView{ProviderID: model.ProviderID, ProviderName: model.ProviderName, ID: model.ID, Name: model.Name, Variants: model.Variants})
	}
	sort.SliceStable(result, func(i, j int) bool {
		return strings.ToLower(result[i].ProviderName+"\x00"+result[i].Name) < strings.ToLower(result[j].ProviderName+"\x00"+result[j].Name)
	})
	return result, nil
}

func (m *Manager) GetSessionModel(sessionID, directory string) (SessionModelView, error) {
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	messages, err := m.raw.GetMessages(ctx, strings.TrimSpace(sessionID), directory, 100)
	if err != nil {
		return SessionModelView{}, err
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if model := messages[index].Info.Model; model != nil && model.ProviderID != "" && model.ModelID != "" {
			return SessionModelView{ProviderID: model.ProviderID, ModelID: model.ModelID, Variant: model.Variant}, nil
		}
	}
	return SessionModelView{}, nil
}

func (m *Manager) collectLoopApprovals(ctx context.Context, loops []domain.GoalLoop) []LoopApprovalView {
	routes, err := m.store.ListProjectRoutes(ctx)
	if err != nil {
		return nil
	}
	loopBySession := make(map[string]domain.GoalLoop)
	for _, loop := range loops {
		if loop.SessionID != "" {
			loopBySession[routeKey(loop.Directory)+"\x00"+loop.SessionID] = loop
		}
	}
	var result []LoopApprovalView
	for _, route := range routes {
		if !route.RouteEnabled {
			continue
		}
		questions, questionErr := m.raw.ListQuestions(ctx, route.Directory)
		if questionErr == nil {
			for _, item := range questions {
				loop := loopBySession[routeKey(route.Directory)+"\x00"+item.SessionID]
				view := LoopApprovalView{ID: item.ID, Type: "question", SessionID: item.SessionID, LoopID: loop.ID, Autonomous: loop.AutomationMode == domain.GoalLoopAutonomous, ProjectName: route.Name, Directory: route.Directory}
				for _, question := range item.Questions {
					q := ApprovalQuestionView{Question: question.Question, Header: question.Header, Multiple: question.Multiple, Custom: question.AllowsCustom()}
					for _, option := range question.Options {
						q.Options = append(q.Options, ApprovalOptionView{Label: option.Label, Description: option.Description})
					}
					view.Questions = append(view.Questions, q)
				}
				result = append(result, view)
			}
		}
		permissions, permissionErr := m.raw.ListPermissions(ctx, route.Directory)
		if permissionErr == nil {
			for _, item := range permissions {
				loop := loopBySession[routeKey(route.Directory)+"\x00"+item.SessionID]
				result = append(result, LoopApprovalView{ID: item.ID, Type: "permission", SessionID: item.SessionID, LoopID: loop.ID, Autonomous: loop.AutomationMode == domain.GoalLoopAutonomous, ProjectName: route.Name, Directory: route.Directory, PermissionName: item.Permission, Patterns: item.Patterns})
			}
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].ProjectName < result[j].ProjectName })
	return result
}

func goalLoopView(loop domain.GoalLoop) GoalLoopView {
	return GoalLoopView{
		ID: loop.ID, Name: loop.Name, Goal: loop.Goal, ProjectID: loop.ProjectID,
		ProjectName: loop.ProjectName, Directory: loop.Directory, AgentID: loop.AgentID,
		AgentName: loop.AgentName, SessionID: loop.SessionID, Status: loop.Status,
		AttachedSession: loop.AttachedSession, AutomationMode: loop.AutomationMode,
		PermissionApprovalMode: loop.PermissionApprovalMode, AllowedDirectories: loop.AllowedDirectories,
		SupervisorModelProviderID: loop.SupervisorModelProviderID, SupervisorModelID: loop.SupervisorModelID,
		SupervisorModelName: loop.SupervisorModelName, SupervisorModelVariant: loop.SupervisorModelVariant,
		SupervisorSessionID: loop.SupervisorSessionID, PendingRequestID: loop.PendingRequestID, PendingRequestType: loop.PendingRequestType,
		ModelProviderID: loop.ModelProviderID, ModelID: loop.ModelID, ModelName: loop.ModelName, ModelVariant: loop.ModelVariant,
		StatusLabel: goalLoopStatusLabel(loop.Status), RequireCompletionConfirmation: loop.RequireCompletionConfirmation,
		FailureLimit: loop.FailureLimit, ConsecutiveFailures: loop.ConsecutiveFailures,
		CycleCount: loop.CycleCount, LastError: loop.LastError, RetryAt: loop.RetryAt,
		CreatedAt: loop.CreatedAt, UpdatedAt: loop.UpdatedAt, CompletedAt: loop.CompletedAt,
	}
}

func goalModelRef(loop domain.GoalLoop) *opencode.ModelRef {
	if loop.ModelProviderID == "" || loop.ModelID == "" {
		return nil
	}
	return &opencode.ModelRef{ProviderID: loop.ModelProviderID, ModelID: loop.ModelID, Variant: loop.ModelVariant}
}

func goalLoopStatusLabel(status string) string {
	return map[string]string{
		domain.GoalLoopDraft: "草稿", domain.GoalLoopRunning: "运行中",
		domain.GoalLoopRetrying: "重试中", domain.GoalLoopWaitingApproval: "等待审批",
		domain.GoalLoopWaitingTakeover: "等待接管", domain.GoalLoopDeciding: "AI 决策中",
		domain.GoalLoopPaused: "已暂停", domain.GoalLoopAwaitingConfirmation: "等待完成确认",
		domain.GoalLoopCompleted: "已完成", domain.GoalLoopBlocked: "受阻", domain.GoalLoopTerminated: "已终止",
	}[status]
}

func goalLoopActive(status string) bool {
	return slices.Contains([]string{domain.GoalLoopRunning, domain.GoalLoopRetrying, domain.GoalLoopWaitingApproval, domain.GoalLoopWaitingTakeover, domain.GoalLoopDeciding, domain.GoalLoopPaused, domain.GoalLoopAwaitingConfirmation}, status)
}

func goalName(goal string) string {
	line := strings.TrimSpace(strings.Split(strings.ReplaceAll(goal, "\r\n", "\n"), "\n")[0])
	if line == "" {
		line = "未命名 Goal"
	}
	if utf8.RuneCountInString(line) <= 40 {
		return line
	}
	runes := []rune(line)
	return string(runes[:40]) + "…"
}

func newGoalLoopID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("goal_%d", time.Now().UTC().UnixNano())
	}
	return "goal_" + hex.EncodeToString(buffer)
}
