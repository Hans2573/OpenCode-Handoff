package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
)

type observedOperation struct {
	activityAt  time.Time
	startedAt   time.Time
	typeName    string
	summary     string
	status      string
	agent       string
	fingerprint string
}

func (m *Manager) sessionActivityLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	m.refreshSessionActivities()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.refreshSessionActivities()
		}
	}
}

func (m *Manager) refreshSessionActivities() {
	ctx, cancel := context.WithTimeout(m.ctx, 45*time.Second)
	defer cancel()
	routes, err := m.store.ListProjectRoutes(ctx)
	if err != nil {
		m.logger.Debug("list routes for session activity", "error", err)
		return
	}
	semaphore := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, route := range routes {
		route := route
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			m.mu.RLock()
			client := m.raw
			m.mu.RUnlock()
			sessions, err := client.ListSessions(ctx, route.Directory)
			if err != nil {
				return
			}
			statuses, err := client.GetSessionStatuses(ctx, route.Directory)
			if err != nil {
				return
			}
			waiting := m.activityWaitingSessions(ctx, client, route.Directory)
			m.observeDirectoryActivities(ctx, route.Directory, sessions, statuses, waiting)
		}()
	}
	wg.Wait()
}

func (m *Manager) activityWaitingSessions(ctx context.Context, client *opencode.Client, directory string) map[string]struct{} {
	waiting := make(map[string]struct{})
	if questions, err := client.ListQuestions(ctx, directory); err == nil {
		for _, item := range questions {
			waiting[item.SessionID] = struct{}{}
		}
	}
	if permissions, err := client.ListPermissions(ctx, directory); err == nil {
		for _, item := range permissions {
			waiting[item.SessionID] = struct{}{}
		}
	}
	return waiting
}

func (m *Manager) observeDirectoryActivities(ctx context.Context, directory string, sessions []opencode.Session, statuses map[string]opencode.SessionStatus, waiting map[string]struct{}) map[string]domain.SessionActivitySnapshot {
	children := make(map[string][]opencode.Session)
	for _, session := range sessions {
		children[session.ParentID] = append(children[session.ParentID], session)
	}
	result := make(map[string]domain.SessionActivitySnapshot)
	rootIDs := make([]string, 0, len(sessions))
	for _, root := range sessions {
		if root.ParentID != "" {
			continue
		}
		rootIDs = append(rootIDs, root.ID)
		group := []opencode.Session{root}
		queue := append([]opencode.Session(nil), children[root.ID]...)
		for len(queue) > 0 && len(group) < 50 {
			item := queue[0]
			queue = queue[1:]
			group = append(group, item)
			queue = append(queue, children[item.ID]...)
		}
		status := statuses[root.ID]
		isWaiting := sessionGroupWaiting(group, waiting)
		result[root.ID] = m.observeSessionActivity(ctx, directory, root, group, status, isWaiting)
	}
	_ = m.store.DeleteSessionActivitiesNotIn(ctx, directory, rootIDs)
	return result
}

func sessionGroupWaiting(group []opencode.Session, waiting map[string]struct{}) bool {
	for _, session := range group {
		if _, ok := waiting[session.ID]; ok {
			return true
		}
	}
	return false
}

func (m *Manager) observeSessionActivity(ctx context.Context, directory string, root opencode.Session, group []opencode.Session, status opencode.SessionStatus, waiting bool) domain.SessionActivitySnapshot {
	now := time.Now().UTC()
	readMessages := isOpenCodeBusy(status.Type)
	if !readMessages {
		if previous, err := m.store.GetSessionActivity(ctx, root.ID, directory); err == nil {
			readMessages = isOpenCodeBusy(previous.SessionStatus)
		}
	}
	sort.Slice(group, func(i, j int) bool { return group[i].Time.Updated > group[j].Time.Updated })
	if len(group) > 25 {
		group = group[:25]
	}
	fingerprintParts := []string{status.Type, fmt.Sprint(status.Attempt), status.Message}
	latest := observedOperation{activityAt: unixMilliTime(root.Time.Updated), typeName: "Session", summary: "Session 状态更新", status: status.Type}
	for _, session := range group {
		fingerprintParts = append(fingerprintParts, session.ID, fmt.Sprint(session.Time.Updated))
		candidate := observedOperation{activityAt: unixMilliTime(session.Time.Updated), typeName: "Session", summary: sessionTitle(session), status: status.Type}
		if readMessages {
			m.mu.RLock()
			client := m.raw
			m.mu.RUnlock()
			if messages, err := client.GetMessages(ctx, session.ID, directory, 12); err == nil {
				m.captureSlowOperations(ctx, directory, root, session, messages)
				if operation, ok := lastObservedOperation(messages); ok {
					if operation.activityAt.After(candidate.activityAt) || operation.activityAt.IsZero() {
						candidate = operation
					} else {
						candidate.typeName = operation.typeName
						candidate.summary = operation.summary
						candidate.status = operation.status
						candidate.startedAt = operation.startedAt
						candidate.agent = operation.agent
					}
					fingerprintParts = append(fingerprintParts, operation.fingerprint)
				}
			}
		}
		if !candidate.activityAt.Before(latest.activityAt) || latest.activityAt.IsZero() {
			latest = candidate
			latest.summary = strings.TrimSpace(latest.summary)
			if latest.summary == "" {
				latest.summary = sessionTitle(session)
			}
			latest.fingerprint = session.ID
		}
	}
	_ = m.store.PruneSlowOperations(ctx, root.ID, directory, sessionSlowOperationLimit)
	fingerprint := activityFingerprint(fingerprintParts)
	m.activityMu.Lock()
	defer m.activityMu.Unlock()
	existing, err := m.store.GetSessionActivity(ctx, root.ID, directory)
	if err == nil && existing.Fingerprint == fingerprint {
		latest.activityAt = existing.LastActivityAt
	} else if err == nil && !latest.activityAt.After(existing.LastActivityAt) {
		latest.activityAt = now
	}
	if latest.activityAt.IsZero() || latest.activityAt.After(now) {
		latest.activityAt = now
	}

	source := group[0]
	for _, session := range group {
		if latest.fingerprint == session.ID {
			source = session
			break
		}
	}
	item := domain.SessionActivitySnapshot{
		SessionID: root.ID, Directory: directory, Fingerprint: fingerprint,
		SessionStatus: status.Type, LastActivityAt: latest.activityAt,
		OperationStartedAt: latest.startedAt, OperationType: latest.typeName,
		OperationSummary: truncateActivityText(latest.summary, 180), OperationStatus: latest.status,
		SourceSessionID: source.ID, SourceSessionTitle: sessionTitle(source),
		SourceAgent: latest.agent, SourceIsSubagent: source.ID != root.ID, UpdatedAt: now,
	}
	if item.SourceAgent == "" {
		if item.SourceIsSubagent {
			item.SourceAgent = "Subagent"
		} else {
			item.SourceAgent = "主 Agent"
		}
	}
	if err == nil && existing.Fingerprint == fingerprint {
		item.SnoozedUntil = existing.SnoozedUntil
	}
	item.Level = activityLevel(now, item, waiting, m.activityThresholds())
	if err == nil && existing.Level != item.Level {
		m.recordActivityTransition(root, item, existing.Level)
	} else if err != nil && item.Level != domain.SessionActivityNormal {
		m.recordActivityTransition(root, item, "")
	}
	if saveErr := m.store.UpsertSessionActivity(ctx, item); saveErr != nil {
		m.logger.Debug("save session activity", "session", root.ID, "error", saveErr)
	}
	m.notifySessionInactivity(root, item)
	return item
}

func (m *Manager) notifySessionInactivity(session opencode.Session, item domain.SessionActivitySnapshot) {
	key := item.Directory + "\x00" + item.SessionID
	m.mu.RLock()
	enabled := m.cfg.Activity.NotifySystem
	after := m.cfg.Activity.SystemNotificationAfter.Duration
	interval := m.cfg.Activity.SystemNotificationInterval.Duration
	m.mu.RUnlock()
	now := time.Now().UTC()
	eligible := isOpenCodeBusy(item.SessionStatus) &&
		item.Level != domain.SessionActivityWaiting &&
		!item.LastActivityAt.IsZero() &&
		!item.SnoozedUntil.After(now) &&
		now.Sub(item.LastActivityAt) >= after
	if !enabled || !eligible || after <= 0 || interval <= 0 {
		m.reminderMu.Lock()
		delete(m.activityReminders, key)
		m.reminderMu.Unlock()
		return
	}

	m.notifierMu.RLock()
	notifier := m.systemNotifier
	m.notifierMu.RUnlock()
	if notifier == nil {
		return
	}

	m.reminderMu.Lock()
	if m.activityReminders == nil {
		m.activityReminders = make(map[string]activityReminder)
	}
	previous, sent := m.activityReminders[key]
	if sent && previous.fingerprint == item.Fingerprint && now.Sub(previous.sentAt) < interval {
		m.reminderMu.Unlock()
		return
	}
	// Reserve this interval before calling the platform service so overlapping
	// refreshes and delivery failures cannot generate a notification storm.
	m.activityReminders[key] = activityReminder{fingerprint: item.Fingerprint, sentAt: now}
	m.reminderMu.Unlock()

	inactiveMinutes := maxInt64(1, int64(now.Sub(item.LastActivityAt).Minutes()))
	operation := strings.Trim(strings.TrimSpace(strings.Join([]string{item.OperationType, item.OperationSummary}, " · ")), " ·")
	if operation == "" {
		operation = "未识别到最近操作"
	}
	notification := SystemNotification{
		ID:        fmt.Sprintf("session-inactive-%s-%d", item.SessionID, now.Unix()),
		Title:     "OpenCode Session 长时间无活动",
		Body:      fmt.Sprintf("%s 已 %d 分钟没有新活动。最后操作：%s", sessionTitle(session), inactiveMinutes, operation),
		SessionID: item.SessionID,
		Directory: item.Directory,
	}
	if err := notifier(notification); err != nil {
		m.logger.Warn("send system inactivity notification", "session_id", item.SessionID, "error", err)
	}
}

func (m *Manager) activityThresholds() [2]time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return [2]time.Duration{m.cfg.Activity.SuspectedAfter.Duration, m.cfg.Activity.StalledAfter.Duration}
}

func applySessionActivityView(view *SessionView, item domain.SessionActivitySnapshot) {
	view.ActivityLevel = item.Level
	view.LastActivityAt = item.LastActivityAt
	view.NoActivitySeconds = maxInt64(0, int64(time.Since(item.LastActivityAt).Seconds()))
	view.OperationStartedAt = item.OperationStartedAt
	view.OperationType = item.OperationType
	view.OperationSummary = item.OperationSummary
	view.OperationStatus = item.OperationStatus
	view.ActivitySourceSessionID = item.SourceSessionID
	view.ActivitySourceTitle = item.SourceSessionTitle
	view.ActivitySourceAgent = item.SourceAgent
	view.ActivityFromSubagent = item.SourceIsSubagent
	view.StallSnoozedUntil = item.SnoozedUntil
}

func applyGoalActivityView(view *GoalLoopView, item domain.SessionActivitySnapshot) {
	view.ActivityLevel = item.Level
	view.LastActivityAt = item.LastActivityAt
	view.NoActivitySeconds = maxInt64(0, int64(time.Since(item.LastActivityAt).Seconds()))
	view.OperationType = item.OperationType
	view.OperationSummary = item.OperationSummary
	view.OperationStatus = item.OperationStatus
	view.ActivitySourceTitle = item.SourceSessionTitle
	view.ActivitySourceAgent = item.SourceAgent
	view.ActivityFromSubagent = item.SourceIsSubagent
}

func activityLevel(now time.Time, item domain.SessionActivitySnapshot, waiting bool, thresholds [2]time.Duration) string {
	if waiting {
		return domain.SessionActivityWaiting
	}
	if !isOpenCodeBusy(item.SessionStatus) || item.SnoozedUntil.After(now) {
		return domain.SessionActivityNormal
	}
	inactiveFor := now.Sub(item.LastActivityAt)
	if inactiveFor >= thresholds[1] {
		return domain.SessionActivityStalled
	}
	if inactiveFor >= thresholds[0] {
		return domain.SessionActivitySuspected
	}
	return domain.SessionActivityNormal
}

func isOpenCodeBusy(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "idle", "error", "failed", "stopped", "interrupted":
		return false
	default:
		return true
	}
}

func (m *Manager) recordActivityTransition(session opencode.Session, item domain.SessionActivitySnapshot, previous string) {
	if item.Level == domain.SessionActivitySuspected || item.Level == domain.SessionActivityStalled {
		label := "疑似停滞"
		level := "warn"
		if item.Level == domain.SessionActivityStalled {
			label = "长时间停滞"
			level = "error"
		}
		_ = m.appendEvent(level, "session."+item.Level, "activity", fmt.Sprintf("Session %s %s：%s", sessionTitle(session), label, item.OperationSummary), map[string]any{
			"sessionId": item.SessionID, "directory": item.Directory,
			"sourceSessionId": item.SourceSessionID, "subagent": item.SourceIsSubagent,
		})
		return
	}
	if previous == domain.SessionActivitySuspected || previous == domain.SessionActivityStalled {
		_ = m.appendEvent("info", "session.activity_resumed", "activity", fmt.Sprintf("Session %s 已恢复活动", sessionTitle(session)), map[string]any{"sessionId": item.SessionID, "directory": item.Directory})
	}
}

func lastObservedOperation(messages []opencode.Message) (observedOperation, bool) {
	for messageIndex := len(messages) - 1; messageIndex >= 0; messageIndex-- {
		message := messages[messageIndex]
		for partIndex := len(message.Parts) - 1; partIndex >= 0; partIndex-- {
			part := message.Parts[partIndex]
			operation, ok := operationFromPart(message, part)
			if ok {
				return operation, true
			}
		}
	}
	return observedOperation{}, false
}

func operationFromPart(message opencode.Message, part opencode.Part) (observedOperation, bool) {
	activityAt := unixMilliTime(message.Info.Time.Completed)
	if activityAt.IsZero() {
		activityAt = unixMilliTime(message.Info.Time.Created)
	}
	fingerprint := strings.Join([]string{
		message.Info.ID, fmt.Sprint(message.Info.Time.Completed), part.ID, part.Type,
		part.Tool, part.State.Status, part.State.Title, part.State.Error,
		fmt.Sprint(part.State.Time.Start), fmt.Sprint(part.State.Time.End),
		activityValueFingerprint(part.State.Input, part.State.Output, part.State.Metadata),
		activityValueFingerprint(part.Text),
	}, "\x00")
	switch part.Type {
	case "tool":
		startedAt := unixMilliTime(part.State.Time.Start)
		if part.State.Time.End > 0 {
			activityAt = unixMilliTime(part.State.Time.End)
		} else if !startedAt.IsZero() {
			activityAt = startedAt
		}
		summary := strings.TrimSpace(part.State.Title)
		if summary == "" {
			summary = toolInputSummary(part.State.Input)
		}
		if summary == "" {
			summary = part.Tool
		}
		return observedOperation{activityAt: activityAt, startedAt: startedAt, typeName: part.Tool, summary: summary, status: part.State.Status, agent: message.Info.Agent, fingerprint: fingerprint}, true
	case "text":
		if text := strings.TrimSpace(part.Text); text != "" {
			return observedOperation{activityAt: activityAt, typeName: "message", summary: text, status: completionStatus(message.Info.Time.Completed), agent: message.Info.Agent, fingerprint: fingerprint}, true
		}
	case "reasoning":
		return observedOperation{activityAt: activityAt, typeName: "reasoning", summary: "模型正在推理", status: completionStatus(message.Info.Time.Completed), agent: message.Info.Agent, fingerprint: fingerprint}, true
	case "step-start":
		return observedOperation{activityAt: activityAt, typeName: "step", summary: "Agent 开始执行新步骤", status: "running", agent: message.Info.Agent, fingerprint: fingerprint}, true
	case "step-finish":
		return observedOperation{activityAt: activityAt, typeName: "step", summary: "Agent 已完成当前步骤", status: "completed", agent: message.Info.Agent, fingerprint: fingerprint}, true
	}
	return observedOperation{}, false
}

func completionStatus(completed int64) string {
	if completed > 0 {
		return "completed"
	}
	return "running"
}

func toolInputSummary(input map[string]any) string {
	for _, key := range []string{"command", "description", "prompt", "filePath", "path", "pattern", "query"} {
		if value, ok := input[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func activityFingerprint(parts []string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:12])
}

func activityValueFingerprint(values ...any) string {
	value, err := json.Marshal(values)
	if err != nil {
		value = []byte(fmt.Sprint(values...))
	}
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:12])
}

func unixMilliTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

func truncateActivityText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}

func (m *Manager) AbortSessionExecution(sessionID, directory string) error {
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
	defer cancel()
	if err := m.raw.AbortSession(ctx, strings.TrimSpace(sessionID), strings.TrimSpace(directory)); err != nil {
		return err
	}
	_ = m.appendEvent("warn", "session.aborted", "activity", "用户从 Session 详情中断了主 Session", map[string]any{"sessionId": sessionID, "directory": directory})
	return nil
}

func (m *Manager) SnoozeSessionStall(sessionID, directory string, minutes int) error {
	if minutes < 1 || minutes > 1440 {
		return fmt.Errorf("继续等待时间必须在 1 到 1440 分钟之间")
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	until := time.Now().UTC().Add(time.Duration(minutes) * time.Minute)
	if err := m.store.SnoozeSessionActivity(ctx, strings.TrimSpace(sessionID), strings.TrimSpace(directory), until); err != nil {
		return err
	}
	return m.appendEvent("info", "session.stall_snoozed", "activity", fmt.Sprintf("已继续等待 %d 分钟", minutes), map[string]any{"sessionId": sessionID, "directory": directory})
}
