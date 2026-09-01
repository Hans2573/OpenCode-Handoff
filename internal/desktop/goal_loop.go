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

var goalCompletionPattern = regexp.MustCompile("(?s)```goal-status\\s*<<<\\s*\\{\\s*\"completed\"\\s*:\\s*true\\s*\\}\\s*>>>\\s*```\\s*$")

const goalContinuationPrompt = `继续完成这个 Session 最初的目标。不要停留在进度汇报；请继续检查、执行和验证工作。只有当目标已经完全达成时，才在回复末尾单独输出以下标记；未完成时不要输出它：

` + "```goal-status\n<<<{\"completed\":true}>>>\n```"

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
		case domain.GoalLoopRunning, domain.GoalLoopRetrying, domain.GoalLoopWaitingApproval:
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
	if loop.SessionID == "" || loop.CycleCount == 0 {
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
	if sessionHasApproval(loop.SessionID, questions, permissions) {
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
	if busy && status.Type != "idle" {
		if loop.Status != domain.GoalLoopRunning || loop.ConsecutiveFailures != 0 {
			loop.Status = domain.GoalLoopRunning
			loop.ConsecutiveFailures = 0
			loop.LastError = ""
			loop.RetryAt = time.Time{}
			loop.UpdatedAt = time.Now().UTC()
			_ = m.store.SaveGoalLoop(ctx, loop)
		}
		return
	}

	messages, err := m.raw.GetMessages(ctx, loop.SessionID, loop.Directory, 50)
	if err != nil {
		m.recordGoalFailure(ctx, &loop, err)
		return
	}
	output, ok := opencode.LastAssistantOutput(messages)
	if !ok || output.MessageID == "" || output.MessageID == loop.LastAssistantMessageID {
		return
	}
	if output.Error != "" {
		loop.LastAssistantMessageID = output.MessageID
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
		}
		_ = m.syncGoalSessions(ctx)
		return
	}

	if err := m.raw.SendPrompt(ctx, loop.SessionID, loop.Directory, goalContinuationPrompt, goalModelRef(loop)); err != nil {
		m.recordGoalFailure(ctx, &loop, err)
		return
	}
	loop.LastAssistantMessageID = output.MessageID
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
	if err := m.raw.SendPrompt(ctx, loop.SessionID, loop.Directory, "/goal "+loop.Goal, goalModelRef(*loop)); err != nil {
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
	_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "started", "已发送 /goal 并开始监听 Session")
	return nil
}

func (m *Manager) recordGoalFailure(ctx context.Context, loop *domain.GoalLoop, cause error) {
	loop.ConsecutiveFailures++
	loop.LastError = cause.Error()
	loop.UpdatedAt = time.Now().UTC()
	if loop.ConsecutiveFailures >= loop.FailureLimit {
		loop.Status = domain.GoalLoopPaused
		loop.RetryAt = time.Time{}
		_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "paused_error", fmt.Sprintf("连续失败 %d 次，已暂停：%v", loop.ConsecutiveFailures, cause))
		_ = m.appendEvent("error", "goal_loop.paused", "loops", "Goal Loop 因连续技术故障暂停", map[string]any{"loopId": loop.ID, "error": cause.Error()})
	} else {
		loop.Status = domain.GoalLoopRetrying
		loop.RetryAt = time.Now().UTC().Add(goalRetryDelay(loop.ConsecutiveFailures))
		_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "retry", fmt.Sprintf("第 %d 次连续失败，将在 %s 后重试：%v", loop.ConsecutiveFailures, goalRetryDelay(loop.ConsecutiveFailures), cause))
	}
	_ = m.store.SaveGoalLoop(ctx, *loop)
	if loop.Status == domain.GoalLoopPaused {
		m.notifyGoalFailure(ctx, *loop)
	}
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
		ErrorText:              fmt.Sprintf("Goal Loop 连续失败 %d 次，已暂停。\n%s", loop.ConsecutiveFailures, loop.LastError),
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
		result = append(result, GoalLoopEventView{ID: item.ID, Type: item.Type, Message: item.Message, CreatedAt: item.CreatedAt})
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
	loop := domain.GoalLoop{
		ID: newGoalLoopID(), Name: goalName(input.Goal), Goal: strings.TrimSpace(input.Goal),
		ProjectID: project.ProjectID, ProjectName: project.Name, Directory: project.Directory,
		AgentID: store.DefaultAgentID, AgentName: "OpenCode", Status: domain.GoalLoopDraft,
		ModelProviderID: model.ProviderID, ModelID: model.ID, ModelName: model.Name, ModelVariant: input.ModelVariant,
		RequireCompletionConfirmation: input.RequireCompletionConfirmation,
		FailureLimit:                  input.FailureLimit, CreatedAt: now, UpdatedAt: now,
	}
	if err := m.store.CreateGoalLoop(ctx, loop); err != nil {
		return GoalLoopPage{}, err
	}
	_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "created", "已创建 Goal Loop")
	if input.StartNow {
		if err := m.launchGoalLoop(ctx, &loop); err != nil {
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
	if loop.Status != domain.GoalLoopDraft {
		return GoalLoopPage{}, errors.New("只能编辑草稿 Goal")
	}
	project, model, err := m.validateGoalLoopInput(ctx, input)
	if err != nil {
		return GoalLoopPage{}, err
	}
	loop.Name = goalName(input.Goal)
	loop.Goal = strings.TrimSpace(input.Goal)
	loop.ProjectID, loop.ProjectName, loop.Directory = project.ProjectID, project.Name, project.Directory
	loop.ModelProviderID, loop.ModelID, loop.ModelName, loop.ModelVariant = model.ProviderID, model.ID, model.Name, input.ModelVariant
	loop.FailureLimit = input.FailureLimit
	loop.RequireCompletionConfirmation = input.RequireCompletionConfirmation
	loop.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveGoalLoop(ctx, loop); err != nil {
		return GoalLoopPage{}, err
	}
	_ = m.store.AppendGoalLoopEvent(ctx, id, "updated", "已更新 Goal 草稿")
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
	if err := m.launchGoalLoop(ctx, &loop); err != nil {
		m.recordGoalFailure(ctx, &loop, err)
	}
	return m.GetGoalLoops()
}

func (m *Manager) PauseGoalLoop(id string) (GoalLoopPage, error) {
	return m.setGoalLoopPaused(id, false)
}

func (m *Manager) TerminateGoalLoop(id string) (GoalLoopPage, error) {
	return m.setGoalLoopPaused(id, true)
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
	if loop.SessionID != "" {
		_ = m.raw.AbortSession(ctx, loop.SessionID, loop.Directory)
	}
	loop.UpdatedAt = time.Now().UTC()
	loop.RetryAt = time.Time{}
	if terminate {
		loop.Status = domain.GoalLoopTerminated
		_ = m.store.AppendGoalLoopEvent(ctx, loop.ID, "terminated", "用户终止了 Goal Loop")
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
	if loop.Status == domain.GoalLoopRunning || loop.Status == domain.GoalLoopRetrying || loop.Status == domain.GoalLoopWaitingApproval {
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
		if loop.SessionID == "" || loop.Status == domain.GoalLoopCompleted || loop.Status == domain.GoalLoopTerminated {
			continue
		}
		items[routeKey(loop.Directory)+"\x00"+loop.SessionID] = struct{}{}
	}
	m.routes.ReplaceGoalSessions(items)
	return nil
}

func (m *Manager) ReplyLoopPermission(requestID, directory, decision string) (GoalLoopPage, error) {
	if !m.routes.Enabled(directory) {
		return GoalLoopPage{}, errors.New("该项目尚未接入飞书渠道")
	}
	reply := opencode.PermissionReply(decision)
	if !reply.Valid() {
		return GoalLoopPage{}, errors.New("无效的权限审批结果")
	}
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	handoff, _ := m.store.GetOpenHandoffByRequest(ctx, requestID)
	if err := m.raw.ReplyPermission(ctx, requestID, directory, reply); err != nil {
		return GoalLoopPage{}, err
	}
	_ = m.store.CloseHandoffRequest(ctx, requestID)
	m.replyApprovalResolved(ctx, handoff, "该 Permission 已在桌面应用中处理："+decision)
	return m.GetGoalLoops()
}

func (m *Manager) ReplyLoopQuestion(input QuestionReplyInput) (GoalLoopPage, error) {
	if !m.routes.Enabled(input.Directory) {
		return GoalLoopPage{}, errors.New("该项目尚未接入飞书渠道")
	}
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	handoff, _ := m.store.GetOpenHandoffByRequest(ctx, input.RequestID)
	var err error
	if input.Reject {
		err = m.raw.RejectQuestion(ctx, input.RequestID, input.Directory)
	} else {
		err = m.raw.ReplyQuestion(ctx, input.RequestID, input.Directory, input.Answers)
	}
	if err != nil {
		return GoalLoopPage{}, err
	}
	_ = m.store.CloseHandoffRequest(ctx, input.RequestID)
	result := "已在桌面应用中提交回答"
	if input.Reject {
		result = "已在桌面应用中拒绝该 Question"
	}
	m.replyApprovalResolved(ctx, handoff, result)
	return m.GetGoalLoops()
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

func (m *Manager) collectLoopApprovals(ctx context.Context, loops []domain.GoalLoop) []LoopApprovalView {
	routes, err := m.store.ListProjectRoutes(ctx)
	if err != nil {
		return nil
	}
	loopBySession := make(map[string]string)
	for _, loop := range loops {
		if loop.SessionID != "" {
			loopBySession[routeKey(loop.Directory)+"\x00"+loop.SessionID] = loop.ID
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
				view := LoopApprovalView{ID: item.ID, Type: "question", SessionID: item.SessionID, LoopID: loopBySession[routeKey(route.Directory)+"\x00"+item.SessionID], ProjectName: route.Name, Directory: route.Directory}
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
				result = append(result, LoopApprovalView{ID: item.ID, Type: "permission", SessionID: item.SessionID, LoopID: loopBySession[routeKey(route.Directory)+"\x00"+item.SessionID], ProjectName: route.Name, Directory: route.Directory, PermissionName: item.Permission, Patterns: item.Patterns})
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
		domain.GoalLoopPaused: "已暂停", domain.GoalLoopAwaitingConfirmation: "等待完成确认",
		domain.GoalLoopCompleted: "已完成", domain.GoalLoopTerminated: "已终止",
	}[status]
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
