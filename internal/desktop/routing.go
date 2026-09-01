package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
)

type RouteRegistry struct {
	mu                     sync.RWMutex
	directories            map[string]struct{}
	goalSessions           map[string]struct{}
	autonomousGoalSessions map[string]struct{}
}

func NewRouteRegistry() *RouteRegistry {
	return &RouteRegistry{directories: make(map[string]struct{}), goalSessions: make(map[string]struct{}), autonomousGoalSessions: make(map[string]struct{})}
}

func (r *RouteRegistry) ReplaceAutonomousGoalSessions(items map[string]struct{}) {
	r.mu.Lock()
	r.autonomousGoalSessions = items
	r.mu.Unlock()
}

func (r *RouteRegistry) AutonomousGoalSession(directory, sessionID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.autonomousGoalSessions[routeKey(directory)+"\x00"+sessionID]
	return ok
}

func (r *RouteRegistry) ReplaceGoalSessions(items map[string]struct{}) {
	r.mu.Lock()
	r.goalSessions = items
	r.mu.Unlock()
}

func (r *RouteRegistry) GoalSession(directory, sessionID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.goalSessions[routeKey(directory)+"\x00"+sessionID]
	return ok
}

func (r *RouteRegistry) Replace(routes []domain.ProjectRoute) {
	next := make(map[string]struct{})
	for _, route := range routes {
		if route.RouteEnabled {
			next[routeKey(route.Directory)] = struct{}{}
		}
	}
	r.mu.Lock()
	r.directories = next
	r.mu.Unlock()
}

func (r *RouteRegistry) Enabled(directory string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.directories[routeKey(directory)]
	return ok
}

func (r *RouteRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.directories)
}

func routeKey(directory string) string {
	return strings.ToLower(filepath.Clean(strings.TrimSpace(directory)))
}

// RoutedAdapter limits Feishu-visible projects and realtime events while
// leaving the raw OpenCode adapter available to the desktop read model.
type RoutedAdapter struct {
	opencode.Adapter
	routes *RouteRegistry
}

func NewRoutedAdapter(adapter opencode.Adapter, routes *RouteRegistry) *RoutedAdapter {
	return &RoutedAdapter{Adapter: adapter, routes: routes}
}

func (a *RoutedAdapter) ListDirectories(ctx context.Context) ([]string, error) {
	directories, err := a.Adapter.ListDirectories(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(directories))
	for _, directory := range directories {
		if a.routes.Enabled(directory) {
			result = append(result, directory)
		}
	}
	return result, nil
}

func (a *RoutedAdapter) ListProjects(ctx context.Context) ([]opencode.Project, error) {
	projects, err := a.Adapter.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]opencode.Project, 0, len(projects))
	for _, project := range projects {
		filtered := project
		filtered.Sandboxes = nil
		worktreeEnabled := a.routes.Enabled(project.Worktree)
		for _, sandbox := range project.Sandboxes {
			if a.routes.Enabled(sandbox) {
				filtered.Sandboxes = append(filtered.Sandboxes, sandbox)
			}
		}
		if worktreeEnabled || len(filtered.Sandboxes) > 0 {
			if !worktreeEnabled {
				filtered.Worktree = ""
			}
			result = append(result, filtered)
		}
	}
	return result, nil
}

func (a *RoutedAdapter) CreateSession(ctx context.Context, directory, title string) (opencode.Session, error) {
	if !a.routes.Enabled(directory) {
		return opencode.Session{}, fmt.Errorf("project is not connected to the Feishu channel")
	}
	return a.Adapter.CreateSession(ctx, directory, title)
}

func (a *RoutedAdapter) ListSessions(ctx context.Context, directory string) ([]opencode.Session, error) {
	sessions, err := a.Adapter.ListSessions(ctx, directory)
	if err != nil {
		return nil, err
	}
	result := make([]opencode.Session, 0, len(sessions))
	for _, session := range sessions {
		if !a.routes.GoalSession(directory, session.ID) {
			result = append(result, session)
		}
	}
	return result, nil
}

func (a *RoutedAdapter) ListQuestions(ctx context.Context, directory string) ([]opencode.QuestionRequest, error) {
	items, err := a.Adapter.ListQuestions(ctx, directory)
	if err != nil {
		return nil, err
	}
	result := make([]opencode.QuestionRequest, 0, len(items))
	for _, item := range items {
		if !a.routes.AutonomousGoalSession(directory, item.SessionID) {
			result = append(result, item)
		}
	}
	return result, nil
}

func (a *RoutedAdapter) ListPermissions(ctx context.Context, directory string) ([]opencode.PermissionRequest, error) {
	items, err := a.Adapter.ListPermissions(ctx, directory)
	if err != nil {
		return nil, err
	}
	result := make([]opencode.PermissionRequest, 0, len(items))
	for _, item := range items {
		if !a.routes.AutonomousGoalSession(directory, item.SessionID) {
			result = append(result, item)
		}
	}
	return result, nil
}

func (a *RoutedAdapter) WatchEvents(ctx context.Context, handler func(opencode.Event)) error {
	return a.Adapter.WatchEvents(ctx, func(event opencode.Event) {
		if a.routes.Enabled(event.Directory) && !a.isGoalLifecycleEvent(event) {
			handler(event)
		}
	})
}

func (a *RoutedAdapter) isGoalLifecycleEvent(event opencode.Event) bool {
	var sessionID string
	switch event.Type {
	case "session.idle":
		var payload opencode.SessionEvent
		if json.Unmarshal(event.Properties, &payload) == nil {
			sessionID = payload.SessionID
		}
	case "session.status":
		var payload opencode.StatusEvent
		if json.Unmarshal(event.Properties, &payload) == nil {
			sessionID = payload.SessionID
		}
	case "question.asked":
		var payload opencode.QuestionRequest
		if json.Unmarshal(event.Properties, &payload) == nil && a.routes.AutonomousGoalSession(event.Directory, payload.SessionID) {
			return true
		}
		return false
	case "permission.asked":
		var payload opencode.PermissionRequest
		if json.Unmarshal(event.Properties, &payload) == nil && a.routes.AutonomousGoalSession(event.Directory, payload.SessionID) {
			return true
		}
		return false
	default:
		return false
	}
	return sessionID != "" && a.routes.GoalSession(event.Directory, sessionID)
}
