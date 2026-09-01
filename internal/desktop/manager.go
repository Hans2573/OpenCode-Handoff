package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/config"
	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
	handoffruntime "github.com/Hans2573/OpenCode-Handoff/internal/runtime"
	"github.com/Hans2573/OpenCode-Handoff/internal/store"
)

type Manager struct {
	ctx    context.Context
	cancel context.CancelFunc
	paths  Paths
	logger *slog.Logger
	store  *store.SQLite
	routes *RouteRegistry

	mu             sync.RWMutex
	cfg            config.Config
	configError    string
	raw            *opencode.Client
	engine         *handoffruntime.Service
	engineCancel   context.CancelFunc
	instanceLock   handoffruntime.InstanceLock
	serviceState   string
	serviceMessage string
	opencodeOnline bool
	trackers       map[string]sessionTracker

	engineMu sync.Mutex
	goalMu   sync.Mutex
}

type sessionTracker struct {
	startedAt   time.Time
	lastInputAt time.Time
}

func NewManager(parent context.Context, paths Paths, logger *slog.Logger) (*Manager, error) {
	ctx, cancel := context.WithCancel(parent)
	cfg, loadErr := config.LoadUnvalidated(paths.ConfigPath)
	if loadErr != nil {
		cfg = config.Default()
		cfg.Store.Path = paths.StorePath
	}
	storePath := cfg.Store.Path
	if strings.TrimSpace(storePath) == "" {
		storePath = paths.StorePath
		cfg.Store.Path = storePath
	}
	database, err := store.OpenSQLite(ctx, storePath)
	if err != nil {
		cancel()
		return nil, err
	}
	if err := database.EnsureDesktopDefaults(ctx, cfg.OpenCode.BaseURL); err != nil {
		database.Close()
		cancel()
		return nil, err
	}
	routesReset, err := database.EnsureProjectRoutesOptIn(ctx)
	if err != nil {
		database.Close()
		cancel()
		return nil, err
	}
	client, err := newOpenCodeClient(cfg)
	if err != nil {
		database.Close()
		cancel()
		return nil, err
	}
	manager := &Manager{
		ctx:          ctx,
		cancel:       cancel,
		paths:        paths,
		logger:       logger,
		store:        database,
		routes:       NewRouteRegistry(),
		cfg:          cfg,
		raw:          client,
		serviceState: "stopped",
		trackers:     make(map[string]sessionTracker),
	}
	if loadErr != nil {
		manager.configError = loadErr.Error()
	} else if err := cfg.Validate(); err != nil {
		manager.configError = err.Error()
	}
	if err := manager.loadRoutes(ctx); err != nil {
		manager.Close()
		return nil, err
	}
	if err := manager.syncGoalSessions(ctx); err != nil {
		manager.Close()
		return nil, err
	}
	if routesReset {
		_ = manager.appendEvent("info", "routes.opt_in_initialized", "projects", "项目接入已初始化为全部未接入", nil)
	}
	_ = manager.RefreshProjects()
	manager.startEngine()
	_ = manager.appendEvent("info", "app.started", "desktop", "Agent Handoff 桌面应用已启动", nil)
	_ = manager.store.CleanupEvents(ctx, 30*24*time.Hour, 10_000)
	_ = manager.cleanupSessionExecutions(ctx)
	go manager.refreshLoop()
	go manager.goalLoopSupervisor()
	return manager, nil
}

func (m *Manager) Close() error {
	m.cancel()
	m.stopEngine()
	return m.store.Close()
}

func (m *Manager) refreshLoop() {
	ticker := time.NewTicker(15 * time.Second)
	cleanupTicker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	defer cleanupTicker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			_ = m.RefreshProjects()
		case <-cleanupTicker.C:
			_ = m.cleanupSessionExecutions(m.ctx)
		}
	}
}

func newOpenCodeClient(cfg config.Config) (*opencode.Client, error) {
	return opencode.NewClient(opencode.ClientOptions{
		BaseURL:   cfg.OpenCode.BaseURL,
		Directory: cfg.OpenCode.Directory,
		Username:  cfg.OpenCode.Username,
		Password:  cfg.OpenCode.Password,
	})
}

func (m *Manager) startEngine() {
	m.engineMu.Lock()
	defer m.engineMu.Unlock()
	m.mu.RLock()
	cfg := m.cfg
	client := m.raw
	m.mu.RUnlock()
	if err := cfg.Validate(); err != nil {
		m.setServiceState("config_error", err.Error(), false)
		return
	}
	lock, err := handoffruntime.AcquireInstanceLock("agent-handoff-engine")
	if err != nil {
		if errors.Is(err, handoffruntime.ErrAlreadyRunning) {
			m.setServiceState("conflict", "检测到 Handoff CLI 正在运行，请先关闭它，然后点击重试。", false)
		} else {
			m.setServiceState("error", err.Error(), false)
		}
		return
	}
	routed := NewRoutedAdapter(client, m.routes)
	service, err := handoffruntime.New(m.ctx, cfg, m.store, m.logger, routed)
	if err != nil {
		_ = lock.Close()
		m.setServiceState("error", err.Error(), false)
		return
	}
	engineCtx, engineCancel := context.WithCancel(m.ctx)
	if err := service.Start(engineCtx); err != nil {
		engineCancel()
		_ = lock.Close()
		m.setServiceState("error", err.Error(), false)
		return
	}
	m.mu.Lock()
	m.engine = service
	m.engineCancel = engineCancel
	m.instanceLock = lock
	m.serviceState = "running"
	m.serviceMessage = "服务运行中"
	m.configError = ""
	m.mu.Unlock()
	_ = m.appendEvent("info", "service.started", "handoff", "Handoff 引擎已启动", nil)
	go m.watchEngine(service)
}

func (m *Manager) watchEngine(service *handoffruntime.Service) {
	err := <-service.Done()
	if errors.Is(err, context.Canceled) || m.ctx.Err() != nil {
		return
	}
	m.engineMu.Lock()
	defer m.engineMu.Unlock()
	m.mu.Lock()
	if m.engine != service {
		m.mu.Unlock()
		return
	}
	lock := m.instanceLock
	m.engine = nil
	m.engineCancel = nil
	m.instanceLock = nil
	m.serviceState = "error"
	m.serviceMessage = fmt.Sprintf("Handoff 引擎已停止：%v", err)
	m.mu.Unlock()
	if lock != nil {
		_ = lock.Close()
	}
	_ = m.appendEvent("error", "service.stopped", "handoff", "Handoff 引擎意外停止", map[string]any{"error": fmt.Sprint(err)})
}

func (m *Manager) stopEngine() {
	m.engineMu.Lock()
	defer m.engineMu.Unlock()
	m.mu.Lock()
	service := m.engine
	cancel := m.engineCancel
	lock := m.instanceLock
	m.engine = nil
	m.engineCancel = nil
	m.instanceLock = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if service != nil {
		select {
		case <-service.Done():
		case <-time.After(5 * time.Second):
		}
	}
	if lock != nil {
		_ = lock.Close()
	}
}

func (m *Manager) RetryService() {
	m.mu.RLock()
	running := m.engine != nil && m.engine.Running()
	m.mu.RUnlock()
	if !running {
		m.startEngine()
	}
}

func (m *Manager) RestartService() {
	m.stopEngine()
	m.startEngine()
}

func (m *Manager) setServiceState(state, message string, online bool) {
	m.mu.Lock()
	m.serviceState = state
	m.serviceMessage = message
	m.opencodeOnline = online
	m.mu.Unlock()
}

func (m *Manager) RefreshProjects() error {
	m.mu.RLock()
	client := m.raw
	m.mu.RUnlock()
	ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
	defer cancel()
	projects, err := client.ListProjects(ctx)
	if err != nil {
		m.mu.Lock()
		m.opencodeOnline = false
		m.mu.Unlock()
		return err
	}
	now := time.Now().UTC()
	items := flattenProjects(projects, now)
	if _, err := m.store.GetSetting(ctx, "routes_initialized"); errors.Is(err, store.ErrNotFound) {
		if err := m.store.SetSetting(ctx, "routes_initialized", "true"); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := m.store.SyncProjects(ctx, items); err != nil {
		return err
	}
	if err := m.loadRoutes(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	m.opencodeOnline = true
	m.mu.Unlock()
	return nil
}

func flattenProjects(projects []opencode.Project, now time.Time) []domain.AgentProject {
	seen := make(map[string]struct{})
	var result []domain.AgentProject
	for _, project := range projects {
		for _, directory := range append([]string{project.Worktree}, project.Sandboxes...) {
			directory = strings.TrimSpace(directory)
			if directory == "" {
				continue
			}
			key := routeKey(directory)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			name := strings.TrimSpace(project.Name)
			if name == "" || directory != project.Worktree {
				name = filepath.Base(filepath.Clean(directory))
			}
			result = append(result, domain.AgentProject{
				ID: stableProjectID(store.DefaultAgentID, directory), AgentID: store.DefaultAgentID,
				Name: name, Directory: directory, Enabled: true, LastSeen: now,
			})
		}
	}
	return result
}

func stableProjectID(agentID, directory string) string {
	sum := sha256.Sum256([]byte(agentID + "\x00" + routeKey(directory)))
	return "project_" + hex.EncodeToString(sum[:8])
}

func (m *Manager) loadRoutes(ctx context.Context) error {
	routes, err := m.store.ListProjectRoutes(ctx)
	if err != nil {
		return err
	}
	m.routes.Replace(routes)
	return nil
}

func (m *Manager) SetProjectRoute(projectID string, enabled bool) error {
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	if err := m.store.SetProjectRoute(ctx, projectID, store.DefaultChannelID, enabled); err != nil {
		return err
	}
	if err := m.loadRoutes(ctx); err != nil {
		return err
	}
	action := "关闭"
	if enabled {
		action = "启用"
	}
	return m.appendEvent("info", "route.changed", "projects", action+"项目飞书路由", map[string]any{"projectId": projectID, "enabled": enabled})
}

func (m *Manager) GetDashboard() (Dashboard, error) {
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
	defer cancel()
	routes, err := m.store.ListProjectRoutes(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	projects := make([]ProjectView, 0, len(routes))
	for _, route := range routes {
		status := "未接入"
		if route.RouteEnabled {
			status = "已接入"
		}
		projects = append(projects, ProjectView{
			ID: route.ProjectID, Name: route.Name, Directory: route.Directory,
			AgentID: route.AgentID, AgentName: "OpenCode", ChannelID: route.ChannelID,
			ChannelName: "飞书", RouteEnabled: route.RouteEnabled, Status: status, LastSeen: route.LastSeen,
		})
	}
	sessions, online := m.collectSessions(ctx, routes)
	sessions = m.annotateGoalSessions(ctx, sessions)
	executionRuns, executionSessions, err := m.executionViews(ctx, sessions)
	if err != nil {
		return Dashboard{}, err
	}
	sortProjectsByRecentConversation(projects, sessions)
	m.mu.Lock()
	m.opencodeOnline = online
	m.mu.Unlock()
	service := m.serviceStatus()
	summary := DashboardSummary{}
	for _, project := range projects {
		if project.RouteEnabled {
			summary.ConnectedProjects++
		}
	}
	for _, session := range sessions {
		switch session.Status {
		case "running", "retrying":
			summary.RunningSessions++
		case "waiting_permission", "waiting_answer":
			summary.PendingActions++
		}
	}
	if service.FeishuConnected {
		summary.ConnectedChannels = 1
	}
	return Dashboard{
		GeneratedAt: time.Now().UTC(), Service: service, Summary: summary,
		Projects: projects, Sessions: sessions,
		ExecutionRuns: executionRuns, ExecutionSessions: executionSessions,
		ExecutionRetentionDays: m.executionRetentionDays(),
		Agents:                 m.agentViews(service), Channels: m.channelViews(service),
	}, nil
}

func (m *Manager) annotateGoalSessions(ctx context.Context, sessions []SessionView) []SessionView {
	loops, err := m.store.ListGoalLoops(ctx)
	if err != nil {
		return sessions
	}
	active := make(map[string]domain.GoalLoop)
	supervisors := make(map[string]struct{})
	for _, loop := range loops {
		if loop.SupervisorSessionID != "" {
			supervisors[routeKey(loop.Directory)+"\x00"+loop.SupervisorSessionID] = struct{}{}
		}
		if goalLoopActive(loop.Status) && loop.SessionID != "" {
			active[routeKey(loop.Directory)+"\x00"+loop.SessionID] = loop
		}
	}
	result := make([]SessionView, 0, len(sessions))
	for _, session := range sessions {
		key := routeKey(session.Directory) + "\x00" + session.ID
		if _, hidden := supervisors[key]; hidden {
			continue
		}
		if loop, ok := active[key]; ok {
			session.GoalLoopID, session.GoalLoopActive = loop.ID, true
		}
		result = append(result, session)
	}
	return result
}

func sortProjectsByRecentConversation(projects []ProjectView, sessions []SessionView) {
	latestByDirectory := make(map[string]time.Time)
	for _, session := range sessions {
		key := routeKey(session.Directory)
		if session.UpdatedAt.After(latestByDirectory[key]) {
			latestByDirectory[key] = session.UpdatedAt
		}
	}
	for index := range projects {
		projects[index].LastConversationAt = latestByDirectory[routeKey(projects[index].Directory)]
	}
	sort.SliceStable(projects, func(left, right int) bool {
		leftAt, rightAt := projects[left].LastConversationAt, projects[right].LastConversationAt
		if !leftAt.Equal(rightAt) {
			if leftAt.IsZero() {
				return false
			}
			if rightAt.IsZero() {
				return true
			}
			return leftAt.After(rightAt)
		}
		leftName, rightName := strings.ToLower(projects[left].Name), strings.ToLower(projects[right].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return strings.ToLower(projects[left].Directory) < strings.ToLower(projects[right].Directory)
	})
}

func (m *Manager) executionViews(ctx context.Context, sessions []SessionView) ([]ExecutionRunView, []ExecutionSessionView, error) {
	maxAge := time.Duration(m.executionRetentionDays()) * 24 * time.Hour
	stats, err := m.store.ListSessionExecutionStats(ctx, maxAge)
	if err != nil {
		return nil, nil, err
	}
	statsBySession := make(map[string]domain.SessionExecutionStats, len(stats))
	summaryBySession := make(map[string]ExecutionSessionView, len(stats)+len(sessions))
	for _, item := range stats {
		key := item.SessionID + "\x00" + routeKey(item.Directory)
		statsBySession[key] = item
		summaryBySession[key] = ExecutionSessionView{
			SessionID: item.SessionID, SessionTitle: item.SessionTitle, Directory: item.Directory,
			ProjectName: item.ProjectName, LatestExecutionSeconds: item.LatestExecutionSeconds,
			TotalExecutionSeconds: item.TotalExecutionSeconds, ExecutionCount: item.ExecutionCount,
			StatusLabel: "已结束",
		}
	}
	for index := range sessions {
		key := sessions[index].ID + "\x00" + routeKey(sessions[index].Directory)
		item := statsBySession[key]
		sessions[index].LatestExecutionSeconds = item.LatestExecutionSeconds
		sessions[index].TotalExecutionSeconds = item.TotalExecutionSeconds
		sessions[index].ExecutionCount = item.ExecutionCount
		if sessions[index].BusyForSeconds > 0 {
			sessions[index].LatestExecutionSeconds = sessions[index].BusyForSeconds
			sessions[index].TotalExecutionSeconds += sessions[index].BusyForSeconds
			sessions[index].ExecutionCount++
		}
		if sessions[index].ExecutionCount > 0 {
			summaryBySession[key] = ExecutionSessionView{
				SessionID: sessions[index].ID, SessionTitle: sessions[index].Title,
				Directory: sessions[index].Directory, ProjectName: sessions[index].ProjectName,
				LatestExecutionSeconds: sessions[index].LatestExecutionSeconds,
				TotalExecutionSeconds:  sessions[index].TotalExecutionSeconds,
				ExecutionCount:         sessions[index].ExecutionCount, StatusLabel: sessions[index].StatusLabel,
				Active: sessions[index].BusyForSeconds > 0,
			}
		}
	}

	stored, err := m.store.ListSessionExecutionRuns(ctx, maxAge, 200)
	if err != nil {
		return nil, nil, err
	}
	result := make([]ExecutionRunView, 0, len(stored)+len(sessions))
	for _, run := range stored {
		result = append(result, ExecutionRunView{
			ID: run.ID, SessionID: run.SessionID, SessionTitle: run.SessionTitle,
			Directory: run.Directory, ProjectName: run.ProjectName,
			DurationSeconds: run.DurationSeconds, StartedAt: run.StartedAt, EndedAt: run.EndedAt,
			EndReason: run.EndReason, StatusLabel: executionStatusLabel(run.EndReason),
		})
	}
	for _, session := range sessions {
		if session.BusyForSeconds <= 0 {
			continue
		}
		result = append(result, ExecutionRunView{
			SessionID: session.ID, SessionTitle: session.Title, Directory: session.Directory,
			ProjectName: session.ProjectName, DurationSeconds: session.BusyForSeconds,
			StartedAt:   time.Now().UTC().Add(-time.Duration(session.BusyForSeconds) * time.Second),
			StatusLabel: "运行中", Active: true,
		})
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].DurationSeconds != result[right].DurationSeconds {
			return result[left].DurationSeconds > result[right].DurationSeconds
		}
		return result[left].StartedAt.After(result[right].StartedAt)
	})
	if len(result) > 200 {
		result = result[:200]
	}
	summaries := make([]ExecutionSessionView, 0, len(summaryBySession))
	for _, item := range summaryBySession {
		summaries = append(summaries, item)
	}
	sort.SliceStable(summaries, func(left, right int) bool {
		if summaries[left].TotalExecutionSeconds != summaries[right].TotalExecutionSeconds {
			return summaries[left].TotalExecutionSeconds > summaries[right].TotalExecutionSeconds
		}
		return summaries[left].SessionTitle < summaries[right].SessionTitle
	})
	return result, summaries, nil
}

func executionStatusLabel(reason string) string {
	switch reason {
	case "completed":
		return "已完成"
	case "human_intervention":
		return "需要介入"
	case "new_input":
		return "已切换轮次"
	case "unmonitored":
		return "停止监控"
	default:
		return "已结束"
	}
}

func (m *Manager) executionRetentionDays() int {
	m.mu.RLock()
	days := m.cfg.Analytics.RetentionDays
	m.mu.RUnlock()
	if days < 1 {
		return 30
	}
	return days
}

func (m *Manager) cleanupSessionExecutions(ctx context.Context) error {
	return m.store.CleanupSessionExecutions(ctx, time.Duration(m.executionRetentionDays())*24*time.Hour)
}

func (m *Manager) serviceStatus() ServiceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	running := m.engine != nil && m.engine.Running()
	feishuState, feishuMessage := "stopped", "飞书监听未启动"
	if m.engine != nil {
		health := m.engine.ChannelHealth()
		feishuState, feishuMessage = health.State, health.Message
	}
	return ServiceStatus{
		State: m.serviceState, Message: m.serviceMessage, EngineRunning: running,
		OpenCodeOnline: m.opencodeOnline, FeishuConnected: feishuState == "connected",
		FeishuState: feishuState, FeishuMessage: feishuMessage,
		ConfigValid: m.configError == "", OpenCodeURL: m.cfg.OpenCode.BaseURL,
	}
}

func (m *Manager) collectSessions(ctx context.Context, routes []domain.ProjectRoute) ([]SessionView, bool) {
	if len(routes) == 0 {
		m.mu.RLock()
		client := m.raw
		m.mu.RUnlock()
		_, err := client.ListProjects(ctx)
		return nil, err == nil
	}
	results := make(chan directoryResult, len(routes))
	semaphore := make(chan struct{}, 6)
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
			results <- m.collectDirectory(ctx, route)
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()
	var sessions []SessionView
	online := len(routes) == 0
	for result := range results {
		sessions = append(sessions, result.sessions...)
		online = online || result.online
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
	if len(sessions) > 150 {
		sessions = sessions[:150]
	}
	return sessions, online
}

func (m *Manager) collectDirectory(ctx context.Context, route domain.ProjectRoute) directoryResult {
	m.mu.RLock()
	client := m.raw
	m.mu.RUnlock()
	sessions, err := client.ListSessions(ctx, route.Directory)
	if err != nil {
		return directoryResult{}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Time.Updated > sessions[j].Time.Updated })
	if len(sessions) > 30 {
		sessions = sessions[:30]
	}
	statuses := make(map[string]opencode.SessionStatus)
	questions := make(map[string]struct{})
	permissions := make(map[string]struct{})
	if route.RouteEnabled {
		if value, err := client.GetSessionStatuses(ctx, route.Directory); err == nil {
			statuses = value
		}
		if value, err := client.ListQuestions(ctx, route.Directory); err == nil {
			for _, item := range value {
				questions[item.SessionID] = struct{}{}
			}
		}
		if value, err := client.ListPermissions(ctx, route.Directory); err == nil {
			for _, item := range value {
				permissions[item.SessionID] = struct{}{}
			}
		}
	}
	result := make([]SessionView, 0, len(sessions))
	for index, session := range sessions {
		if session.ParentID != "" {
			continue
		}
		status, label, detail := mapSessionStatus(route.RouteEnabled, statuses[session.ID])
		if _, ok := permissions[session.ID]; ok {
			status, label, detail = "waiting_permission", "等待权限", "请在 OpenCode 或飞书中处理权限请求"
		} else if _, ok := questions[session.ID]; ok {
			status, label, detail = "waiting_answer", "等待回答", "请在 OpenCode 或飞书中回答问题"
		}
		view := SessionView{
			ID: session.ID, Title: sessionTitle(session), ProjectName: route.Name,
			Directory: route.Directory, AgentName: "OpenCode", Status: status,
			StatusLabel: label, StatusDetail: detail, RouteEnabled: route.RouteEnabled,
			UpdatedAt: time.UnixMilli(session.Time.Updated).UTC(),
		}
		if route.RouteEnabled {
			view.ChannelName = "飞书"
		} else {
			view.ChannelName = "—"
		}
		var lastInputAt time.Time
		if route.RouteEnabled && (index < 6 || status != "idle") {
			if messages, err := client.GetMessages(ctx, session.ID, route.Directory, 50); err == nil {
				if at, text, model, ok := lastUserMessage(messages); ok {
					lastInputAt = at
					view.HasLastInput = true
					view.LastInput = text
					view.SinceLastInputSeconds = maxInt64(0, int64(time.Since(at).Seconds()))
					if model != nil {
						view.CurrentModel = model.ProviderID + "/" + model.ModelID
						view.CurrentVariant = model.Variant
					}
				}
			}
		}
		view.BusyForSeconds = m.trackExecution(ctx, view, lastInputAt)
		result = append(result, view)
	}
	return directoryResult{sessions: result, online: true}
}

type directoryResult struct {
	sessions []SessionView
	online   bool
}

func mapSessionStatus(enabled bool, status opencode.SessionStatus) (string, string, string) {
	if !enabled {
		return "unmonitored", "未监控", "该项目未接入任何渠道"
	}
	switch strings.ToLower(status.Type) {
	case "busy", "running":
		return "running", "运行中", status.Message
	case "retry", "retrying":
		return "retrying", "重试中", status.Message
	case "idle", "":
		return "idle", "空闲", ""
	default:
		return "unknown", "状态未知", status.Type
	}
}

func (m *Manager) trackExecution(ctx context.Context, view SessionView, lastInputAt time.Time) int64 {
	now := time.Now().UTC()
	key := view.ID + "\x00" + routeKey(view.Directory)
	active := view.Status == "running" || view.Status == "retrying"
	var closeAt time.Time
	var closeReason string
	var startAt time.Time

	m.mu.Lock()
	tracker, tracked := m.trackers[key]
	if active {
		newInput := tracked && !lastInputAt.IsZero() && lastInputAt.After(tracker.startedAt) &&
			(tracker.lastInputAt.IsZero() || lastInputAt.After(tracker.lastInputAt))
		if newInput {
			closeAt, closeReason = lastInputAt, "new_input"
			delete(m.trackers, key)
			tracked = false
		}
		if !tracked {
			startAt = executionStartTime(now, lastInputAt, view.UpdatedAt)
			tracker = sessionTracker{startedAt: startAt, lastInputAt: lastInputAt}
			m.trackers[key] = tracker
		} else if lastInputAt.After(tracker.lastInputAt) {
			tracker.lastInputAt = lastInputAt
			m.trackers[key] = tracker
		}
	} else {
		closeAt = executionEndTime(now, view.UpdatedAt, tracker.startedAt)
		closeReason = executionEndReason(view.Status)
		delete(m.trackers, key)
	}
	m.mu.Unlock()

	if !closeAt.IsZero() {
		if err := m.store.CompleteOpenSessionExecutions(ctx, view.ID, view.Directory, closeAt, closeReason); err != nil {
			m.logger.Warn("complete session execution", "session", view.ID, "error", err)
		}
	}
	if !startAt.IsZero() {
		_, err := m.store.StartSessionExecution(ctx, domain.SessionExecutionRun{
			SessionID: view.ID, Directory: view.Directory, ProjectName: view.ProjectName,
			SessionTitle: view.Title, StartedAt: startAt,
		})
		if err != nil {
			m.logger.Warn("start session execution", "session", view.ID, "error", err)
		}
	}
	if !active {
		return 0
	}
	return maxInt64(0, int64(now.Sub(tracker.startedAt).Seconds()))
}

func executionStartTime(now, lastInputAt, fallback time.Time) time.Time {
	if !lastInputAt.IsZero() && lastInputAt.Before(now) {
		return lastInputAt.UTC().Truncate(time.Millisecond)
	}
	if !fallback.IsZero() && fallback.Before(now) {
		return fallback.UTC().Truncate(time.Millisecond)
	}
	return now.Truncate(time.Millisecond)
}

func executionEndTime(now, updatedAt, startedAt time.Time) time.Time {
	if !updatedAt.IsZero() && !updatedAt.After(now) && (startedAt.IsZero() || updatedAt.After(startedAt)) {
		return updatedAt.UTC().Truncate(time.Millisecond)
	}
	return now.Truncate(time.Millisecond)
}

func executionEndReason(status string) string {
	switch status {
	case "idle":
		return "completed"
	case "waiting_permission", "waiting_answer":
		return "human_intervention"
	case "unmonitored":
		return "unmonitored"
	default:
		return "stopped"
	}
}

func lastUserMessage(messages []opencode.Message) (time.Time, string, *opencode.ModelRef, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Info.Role != "user" {
			continue
		}
		var chunks []string
		for _, part := range message.Parts {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				chunks = append(chunks, part.Text)
			}
		}
		text := strings.TrimSpace(strings.Join(chunks, "\n\n"))
		if text != "" {
			return time.UnixMilli(message.Info.Time.Created), text, message.Info.Model, true
		}
	}
	return time.Time{}, "", nil, false
}

func sessionTitle(session opencode.Session) string {
	if value := strings.TrimSpace(session.Title); value != "" {
		return value
	}
	return "未命名 Session"
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func (m *Manager) agentViews(service ServiceStatus) []IntegrationView {
	status, label := "offline", "未连接"
	if service.OpenCodeOnline {
		status, label = "connected", "已连接"
	}
	return []IntegrationView{
		{ID: store.DefaultAgentID, Type: "opencode", Name: "OpenCode", Status: status, StatusLabel: label, Endpoint: service.OpenCodeURL, Available: true},
		{ID: "claude-code", Type: "claude-code", Name: "Claude Code", Status: "coming_soon", StatusLabel: "即将支持", ComingSoon: true},
		{ID: "codex", Type: "codex", Name: "Codex", Status: "coming_soon", StatusLabel: "即将支持", ComingSoon: true},
	}
}

func (m *Manager) channelViews(service ServiceStatus) []IntegrationView {
	status, label := "offline", "未连接"
	switch service.FeishuState {
	case "connected":
		status, label = "connected", "已连接"
	case "connecting":
		status, label = "connecting", "连接中"
	case "reconnecting":
		status, label = "connecting", "重连中"
	case "error":
		status, label = "error", "连接错误"
	}
	return []IntegrationView{
		{ID: store.DefaultChannelID, Type: "feishu", Name: "飞书", Status: status, StatusLabel: label, Endpoint: service.FeishuMessage, Available: true},
		{ID: "channel-future-1", Type: "future", Name: "更多渠道", Status: "coming_soon", StatusLabel: "即将支持", ComingSoon: true},
	}
}

func (m *Manager) GetSettings() SettingsView {
	m.mu.RLock()
	cfg := m.cfg
	configError := m.configError
	m.mu.RUnlock()
	return SettingsView{
		Paths: m.paths, FileSizes: fileSizes(m.paths), OpenCodeBaseURL: cfg.OpenCode.BaseURL, OpenCodeDirectory: cfg.OpenCode.Directory,
		OpenCodeUsername: cfg.OpenCode.Username, OpenCodePasswordSet: cfg.OpenCode.Password != "",
		AllowRemote: cfg.OpenCode.AllowRemote, FeishuAppID: cfg.Feishu.AppID,
		FeishuAppSecretSet: cfg.Feishu.AppSecret != "", FeishuChatID: cfg.Feishu.ChatID,
		AllowedUsers:    append([]string(nil), cfg.Security.AllowedUsers...),
		PollingInterval: cfg.Watcher.PollingInterval.Duration.String(), MaxOutputChars: cfg.Handoff.MaxOutputChars,
		NotifyIdle: cfg.Handoff.NotifyIdle, NotifyError: cfg.Handoff.NotifyError,
		NotifyQuestion: cfg.Handoff.NotifyQuestion, NotifyPermission: cfg.Handoff.NotifyPermission,
		LoggingLevel: cfg.Logging.Level, ExecutionRetentionDays: cfg.Analytics.RetentionDays,
		EnvironmentOverrides: config.EnvironmentOverrides(), ConfigError: configError,
	}
}

func (m *Manager) SaveSettings(input SettingsInput) error {
	m.mu.RLock()
	next := m.cfg
	m.mu.RUnlock()
	overrides := config.EnvironmentOverrides()
	if _, locked := overrides["opencode.base_url"]; !locked {
		next.OpenCode.BaseURL = strings.TrimSpace(input.OpenCodeBaseURL)
	}
	if _, locked := overrides["opencode.directory"]; !locked {
		next.OpenCode.Directory = strings.TrimSpace(input.OpenCodeDirectory)
	}
	if _, locked := overrides["opencode.username"]; !locked {
		next.OpenCode.Username = strings.TrimSpace(input.OpenCodeUsername)
	}
	if _, locked := overrides["opencode.password"]; !locked {
		if input.ClearOpenCodePassword {
			next.OpenCode.Password = ""
		} else if input.OpenCodePassword != "" {
			next.OpenCode.Password = input.OpenCodePassword
		}
	}
	next.OpenCode.AllowRemote = input.AllowRemote
	if _, locked := overrides["feishu.app_id"]; !locked {
		next.Feishu.AppID = strings.TrimSpace(input.FeishuAppID)
	}
	if _, locked := overrides["feishu.app_secret"]; !locked && input.FeishuAppSecret != "" {
		next.Feishu.AppSecret = input.FeishuAppSecret
	}
	if _, locked := overrides["feishu.chat_id"]; !locked {
		next.Feishu.ChatID = strings.TrimSpace(input.FeishuChatID)
	}
	if _, locked := overrides["security.allowed_users"]; !locked {
		next.Security.AllowedUsers = compactStrings(input.AllowedUsers)
	}
	if _, locked := overrides["handoff.max_output_chars"]; !locked {
		next.Handoff.MaxOutputChars = input.MaxOutputChars
	}
	if _, locked := overrides["handoff.notify_idle"]; !locked {
		next.Handoff.NotifyIdle = input.NotifyIdle
	}
	if _, locked := overrides["handoff.notify_error"]; !locked {
		next.Handoff.NotifyError = input.NotifyError
	}
	if _, locked := overrides["handoff.notify_question"]; !locked {
		next.Handoff.NotifyQuestion = input.NotifyQuestion
	}
	if _, locked := overrides["handoff.notify_permission"]; !locked {
		next.Handoff.NotifyPermission = input.NotifyPermission
	}
	if _, locked := overrides["analytics.retention_days"]; !locked {
		next.Analytics.RetentionDays = input.ExecutionRetentionDays
	}
	if value, err := time.ParseDuration(strings.TrimSpace(input.PollingInterval)); err == nil {
		next.Watcher.PollingInterval = config.Duration{Duration: value}
	} else {
		return fmt.Errorf("轮询间隔无效：%w", err)
	}
	next.Logging.Level = strings.ToLower(strings.TrimSpace(input.LoggingLevel))
	next.Store.Path = m.paths.StorePath
	if err := next.Validate(); err != nil {
		return err
	}
	if err := config.Save(m.paths.ConfigPath, next); err != nil {
		return err
	}
	client, err := newOpenCodeClient(next)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg = next
	m.raw = client
	m.configError = ""
	m.mu.Unlock()
	if err := m.store.EnsureDesktopDefaults(m.ctx, next.OpenCode.BaseURL); err != nil {
		return err
	}
	if err := m.cleanupSessionExecutions(m.ctx); err != nil {
		return err
	}
	m.RestartService()
	_ = m.RefreshProjects()
	return m.appendEvent("info", "settings.saved", "settings", "桌面配置已保存并重新加载", nil)
}

func compactStrings(values []string) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (m *Manager) GetEvents(search string, limit int) (EventPage, error) {
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	items, err := m.store.ListEvents(ctx, search, limit)
	if err != nil {
		return EventPage{}, err
	}
	result := EventPage{Items: make([]EventView, 0, len(items))}
	for _, item := range items {
		result.Items = append(result.Items, EventView{
			ID: item.ID, Level: item.Level, Type: item.Type, Source: item.Source,
			Message: item.Message, Metadata: item.Metadata, CreatedAt: item.CreatedAt,
		})
	}
	return result, nil
}

func (m *Manager) ClearEvents() error {
	return m.store.ClearEvents(m.ctx)
}

func (m *Manager) appendEvent(level, eventType, source, message string, metadata map[string]any) error {
	return m.store.AppendEvent(context.WithoutCancel(m.ctx), domain.EventLog{
		Level: level, Type: eventType, Source: source, Message: message,
		Metadata: redactMetadata(metadata), CreatedAt: time.Now().UTC(),
	})
}

func redactMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	result := make(map[string]any, len(metadata))
	for key, value := range metadata {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "prompt") {
			result[key] = "***"
		} else {
			result[key] = redactMetadataValue(value)
		}
	}
	return result
}

func redactMetadataValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return redactMetadata(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = redactMetadataValue(item)
		}
		return result
	default:
		return value
	}
}

func (m *Manager) OpenCodeURL() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.OpenCode.BaseURL
}

func (m *Manager) Paths() Paths {
	return m.paths
}
