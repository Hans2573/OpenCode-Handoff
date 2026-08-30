package desktop

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
)

type RouteRegistry struct {
	mu          sync.RWMutex
	directories map[string]struct{}
}

func NewRouteRegistry() *RouteRegistry {
	return &RouteRegistry{directories: make(map[string]struct{})}
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

func (a *RoutedAdapter) WatchEvents(ctx context.Context, handler func(opencode.Event)) error {
	return a.Adapter.WatchEvents(ctx, func(event opencode.Event) {
		if a.routes.Enabled(event.Directory) {
			handler(event)
		}
	})
}
