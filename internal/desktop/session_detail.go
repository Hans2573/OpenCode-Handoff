package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
)

const (
	sessionDetailMessageLimit = 500
	sessionSlowInputLimit     = 4096
	sessionSlowOperationLimit = 100
)

var (
	slowBearerPattern      = regexp.MustCompile(`(?i)\bBearer\s+[^\s"']+`)
	slowAssignmentPattern  = regexp.MustCompile(`(?i)((?:--?)?(?:password|passwd|token|access[_-]?token|api[_-]?key|secret|credential)(?:=|\s+))[^\s"']+`)
	slowURLPasswordPattern = regexp.MustCompile(`(?i)(https?://[^:/@\s]+:)[^@\s]+@`)
)

type sessionMessageResult struct {
	session  opencode.Session
	messages []opencode.Message
	err      error
}

func (m *Manager) GetSessionDetail(sessionID, directory string) (SessionDetailView, error) {
	sessionID = strings.TrimSpace(sessionID)
	directory = strings.TrimSpace(directory)
	if sessionID == "" || directory == "" {
		return SessionDetailView{}, fmt.Errorf("Session ID 和项目目录不能为空")
	}
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()

	m.mu.RLock()
	client := m.raw
	m.mu.RUnlock()
	sessions, err := client.ListSessions(ctx, directory)
	if err != nil {
		return SessionDetailView{}, fmt.Errorf("读取 Session：%w", err)
	}
	byID := make(map[string]opencode.Session, len(sessions))
	children := make(map[string][]opencode.Session)
	for _, session := range sessions {
		byID[session.ID] = session
		children[session.ParentID] = append(children[session.ParentID], session)
	}
	root, ok := byID[sessionID]
	if !ok {
		return SessionDetailView{}, fmt.Errorf("Session %s 不存在", sessionID)
	}
	for root.ParentID != "" {
		parent, exists := byID[root.ParentID]
		if !exists {
			break
		}
		root = parent
	}
	group := []opencode.Session{root}
	queue := append([]opencode.Session(nil), children[root.ID]...)
	for len(queue) > 0 && len(group) < 100 {
		next := queue[0]
		queue = queue[1:]
		group = append(group, next)
		queue = append(queue, children[next.ID]...)
	}
	truncated := len(queue) > 0

	results := make(chan sessionMessageResult, len(group))
	semaphore := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for _, session := range group {
		session := session
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- sessionMessageResult{session: session, err: ctx.Err()}
				return
			}
			messages, readErr := client.GetMessages(ctx, session.ID, directory, sessionDetailMessageLimit)
			results <- sessionMessageResult{session: session, messages: messages, err: readErr}
		}()
	}
	wg.Wait()
	close(results)

	statuses, _ := client.GetSessionStatuses(ctx, directory)
	status, statusLabel, _ := mapSessionStatus(true, statuses[root.ID])
	now := time.Now().UTC()
	view := SessionDetailView{
		ID: root.ID, Title: sessionTitle(root), Directory: directory,
		Status: status, StatusLabel: statusLabel,
		CreatedAt: unixMilliTime(root.Time.Created), UpdatedAt: unixMilliTime(root.Time.Updated),
		SubagentCount: len(group) - 1, Truncated: truncated,
	}
	elapsedEnd := view.UpdatedAt
	if isOpenCodeBusy(statuses[root.ID].Type) {
		elapsedEnd = now
	}
	if !view.CreatedAt.IsZero() && elapsedEnd.After(view.CreatedAt) {
		view.ElapsedSeconds = int64(elapsedEnd.Sub(view.CreatedAt).Seconds())
	}

	operations := make(map[string]SessionOperationView)
	subagentSessions := make(map[string]opencode.Session)
	for result := range results {
		if result.err != nil {
			if result.session.ID == root.ID {
				return SessionDetailView{}, fmt.Errorf("读取 Session 消息：%w", result.err)
			}
			continue
		}
		if len(result.messages) >= sessionDetailMessageLimit {
			view.Truncated = true
		}
		view.MessageCount += len(result.messages)
		m.captureSlowOperations(ctx, directory, root, result.session, result.messages)
		if result.session.ID != root.ID {
			subagentSessions[result.session.ID] = result.session
		}
		for _, message := range result.messages {
			for index, part := range message.Parts {
				operation, ok := sessionOperationFromPart(root, result.session, message, part, index, now)
				if ok {
					operations[operation.ID] = operation
				}
			}
		}
	}
	_ = m.store.PruneSlowOperations(ctx, root.ID, directory, sessionSlowOperationLimit)

	stored, err := m.store.ListSlowOperations(ctx, root.ID, directory, sessionSlowOperationLimit)
	if err == nil {
		for _, item := range stored {
			key := slowOperationKey(item.SourceSessionID, item.MessageID, item.PartID)
			if current, exists := operations[key]; exists {
				current.Persisted = true
				operations[key] = current
				continue
			}
			operations[key] = slowOperationView(item)
		}
	}
	view.Operations, view.ToolStats, view.Subagents = summarizeSessionOperations(operations, subagentSessions)
	view.ToolCallCount = len(operations)
	for _, operation := range operations {
		view.TotalToolSeconds += operation.DurationSeconds
		if operation.Running {
			view.RunningOperationCount++
		}
		if operation.Failed {
			view.FailedOperationCount++
		}
	}
	return view, nil
}

func (m *Manager) captureSlowOperations(ctx context.Context, directory string, root, source opencode.Session, messages []opencode.Message) {
	m.mu.RLock()
	threshold := m.cfg.Activity.SlowOperationAfter.Duration
	m.mu.RUnlock()
	if threshold <= 0 {
		threshold = 30 * time.Second
	}
	now := time.Now().UTC()
	for _, message := range messages {
		for index, part := range message.Parts {
			view, ok := sessionOperationFromPart(root, source, message, part, index, now)
			if !ok || time.Duration(view.DurationSeconds)*time.Second < threshold {
				continue
			}
			item := domain.SlowOperation{
				RootSessionID: root.ID, SourceSessionID: source.ID, Directory: directory,
				MessageID: message.Info.ID, PartID: partIdentity(part, index), Tool: view.Tool,
				InputPreview: view.InputPreview, InputHash: slowInputHash(part.State.Input),
				Summary: view.Summary, Status: view.Status, SourceTitle: sessionTitle(source),
				SourceAgent: view.Agent, SourceIsSubagent: source.ID != root.ID,
				StartedAt: view.StartedAt, EndedAt: view.EndedAt,
				DurationSeconds: view.DurationSeconds, UpdatedAt: now,
			}
			if err := m.store.UpsertSlowOperation(ctx, item); err != nil {
				m.logger.Debug("save slow operation", "session", root.ID, "error", err)
			}
		}
	}
}

func sessionOperationFromPart(root, source opencode.Session, message opencode.Message, part opencode.Part, index int, now time.Time) (SessionOperationView, bool) {
	if part.Type != "tool" {
		return SessionOperationView{}, false
	}
	startedAt := unixMilliTime(part.State.Time.Start)
	if startedAt.IsZero() {
		startedAt = unixMilliTime(message.Info.Time.Created)
	}
	if startedAt.IsZero() {
		return SessionOperationView{}, false
	}
	endedAt := unixMilliTime(part.State.Time.End)
	terminal := toolStateTerminal(part.State.Status)
	if endedAt.IsZero() && terminal {
		endedAt = unixMilliTime(message.Info.Time.Completed)
	}
	running := endedAt.IsZero() && !terminal
	durationEnd := endedAt
	if durationEnd.IsZero() {
		durationEnd = now
	}
	duration := int64(0)
	if durationEnd.After(startedAt) {
		duration = int64(durationEnd.Sub(startedAt).Seconds())
	}
	status := strings.TrimSpace(part.State.Status)
	if status == "" {
		if running {
			status = "running"
		} else {
			status = "completed"
		}
	}
	summary := strings.TrimSpace(part.State.Title)
	if summary == "" {
		summary = toolInputSummary(part.State.Input)
	}
	if summary == "" {
		summary = part.Tool
	}
	return SessionOperationView{
		ID:   slowOperationKey(source.ID, message.Info.ID, partIdentity(part, index)),
		Tool: part.Tool, InputPreview: slowInputPreview(part.State.Input),
		Summary: truncateActivityText(summary, 180), Status: status,
		SessionID: source.ID, SessionTitle: sessionTitle(source), Agent: message.Info.Agent,
		FromSubagent: source.ID != root.ID, StartedAt: startedAt, EndedAt: endedAt,
		DurationSeconds: duration, Running: running,
		Failed: part.State.Error != "" || strings.Contains(strings.ToLower(status), "error") || strings.Contains(strings.ToLower(status), "fail"),
	}, true
}

func toolStateTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "success", "error", "failed", "cancelled", "canceled", "aborted", "rejected":
		return true
	default:
		return false
	}
}

func partIdentity(part opencode.Part, index int) string {
	if strings.TrimSpace(part.ID) != "" {
		return part.ID
	}
	return fmt.Sprintf("part-%d", index)
}

func slowOperationKey(sessionID, messageID, partID string) string {
	return strings.Join([]string{sessionID, messageID, partID}, "\x00")
}

func slowInputPreview(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	encoded, err := json.Marshal(redactSlowInput(input))
	if err != nil {
		return ""
	}
	return truncateUTF8Bytes(string(encoded), sessionSlowInputLimit)
}

func truncateUTF8Bytes(value string, limit int) string {
	if limit < 1 || len(value) <= limit {
		return value
	}
	value = value[:limit-3]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "..."
}

func redactSlowInput(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "credential") || strings.Contains(lower, "authorization") {
			result[key] = "***"
			continue
		}
		result[key] = redactSlowValue(value)
	}
	return result
}

func redactSlowValue(value any) any {
	switch nested := value.(type) {
	case map[string]any:
		return redactSlowInput(nested)
	case []any:
		items := make([]any, len(nested))
		for index, item := range nested {
			items[index] = redactSlowValue(item)
		}
		return items
	case string:
		result := slowBearerPattern.ReplaceAllString(nested, "Bearer ***")
		result = slowAssignmentPattern.ReplaceAllString(result, "${1}***")
		return slowURLPasswordPattern.ReplaceAllString(result, "${1}***@")
	default:
		return value
	}
}

func slowOperationView(item domain.SlowOperation) SessionOperationView {
	status := strings.ToLower(item.Status)
	return SessionOperationView{
		ID:   slowOperationKey(item.SourceSessionID, item.MessageID, item.PartID),
		Tool: item.Tool, InputPreview: item.InputPreview, Summary: item.Summary,
		Status: item.Status, SessionID: item.SourceSessionID, SessionTitle: item.SourceTitle,
		Agent: item.SourceAgent, FromSubagent: item.SourceIsSubagent,
		StartedAt: item.StartedAt, EndedAt: item.EndedAt, DurationSeconds: item.DurationSeconds,
		Running: item.EndedAt.IsZero() && !toolStateTerminal(status),
		Failed:  strings.Contains(status, "error") || strings.Contains(status, "fail"), Persisted: true,
	}
}

func summarizeSessionOperations(items map[string]SessionOperationView, subagentSessions map[string]opencode.Session) ([]SessionOperationView, []SessionToolStatView, []SessionSubagentView) {
	all := make([]SessionOperationView, 0, len(items))
	toolStats := make(map[string]*SessionToolStatView)
	subagentStats := make(map[string]*SessionSubagentView)
	for _, operation := range items {
		all = append(all, operation)
		tool := operation.Tool
		if tool == "" {
			tool = "tool"
		}
		stat := toolStats[tool]
		if stat == nil {
			stat = &SessionToolStatView{Tool: tool}
			toolStats[tool] = stat
		}
		stat.CallCount++
		stat.TotalSeconds += operation.DurationSeconds
		stat.LongestSeconds = maxInt64(stat.LongestSeconds, operation.DurationSeconds)
		if operation.Running {
			stat.RunningCount++
		}
		if operation.Failed {
			stat.FailedCount++
		}
		if operation.FromSubagent {
			sub := subagentStats[operation.SessionID]
			if sub == nil {
				sub = &SessionSubagentView{ID: operation.SessionID, Title: operation.SessionTitle, Agent: operation.Agent}
				subagentStats[operation.SessionID] = sub
			}
			sub.ToolCallCount++
			sub.TotalToolSeconds += operation.DurationSeconds
			sub.LongestSeconds = maxInt64(sub.LongestSeconds, operation.DurationSeconds)
			if operation.Running {
				sub.RunningCount++
			}
			if operation.Failed {
				sub.FailedCount++
			}
		}
	}
	for id, session := range subagentSessions {
		if subagentStats[id] == nil {
			subagentStats[id] = &SessionSubagentView{ID: id, Title: sessionTitle(session)}
		}
	}

	sort.Slice(all, func(i, j int) bool { return all[i].StartedAt.After(all[j].StartedAt) })
	recent := append([]SessionOperationView(nil), all...)
	if len(recent) > 200 {
		recent = recent[:200]
	}
	sort.Slice(all, func(i, j int) bool { return all[i].DurationSeconds > all[j].DurationSeconds })
	if len(all) > sessionSlowOperationLimit {
		all = all[:sessionSlowOperationLimit]
	}
	selected := make(map[string]SessionOperationView, len(all)+len(recent))
	for _, operation := range append(all, recent...) {
		selected[operation.ID] = operation
	}
	operations := make([]SessionOperationView, 0, len(selected))
	for _, operation := range selected {
		operations = append(operations, operation)
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].StartedAt.After(operations[j].StartedAt) })

	tools := make([]SessionToolStatView, 0, len(toolStats))
	for _, stat := range toolStats {
		if stat.CallCount > 0 {
			stat.AverageSeconds = stat.TotalSeconds / int64(stat.CallCount)
		}
		tools = append(tools, *stat)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].TotalSeconds > tools[j].TotalSeconds })
	subagents := make([]SessionSubagentView, 0, len(subagentStats))
	for _, stat := range subagentStats {
		subagents = append(subagents, *stat)
	}
	sort.Slice(subagents, func(i, j int) bool { return subagents[i].TotalToolSeconds > subagents[j].TotalToolSeconds })
	return operations, tools, subagents
}

func slowInputHash(input map[string]any) string {
	encoded, _ := json.Marshal(redactSlowInput(input))
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:12])
}
