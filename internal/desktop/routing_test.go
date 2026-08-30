package desktop

import (
	"context"
	"testing"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
)

type routeAdapterStub struct {
	opencode.Adapter
	projects []opencode.Project
	events   []opencode.Event
}

func (s routeAdapterStub) ListProjects(context.Context) ([]opencode.Project, error) {
	return s.projects, nil
}

func (s routeAdapterStub) ListDirectories(context.Context) ([]string, error) {
	var result []string
	for _, project := range s.projects {
		result = append(result, project.Worktree)
		result = append(result, project.Sandboxes...)
	}
	return result, nil
}

func (s routeAdapterStub) WatchEvents(_ context.Context, handler func(opencode.Event)) error {
	for _, event := range s.events {
		handler(event)
	}
	return nil
}

func TestRoutedAdapterFiltersProjectsDirectoriesAndEvents(t *testing.T) {
	registry := NewRouteRegistry()
	registry.Replace([]domain.ProjectRoute{{Directory: `D:\work\enabled`, RouteEnabled: true}})
	stub := routeAdapterStub{
		projects: []opencode.Project{{ID: "p1", Worktree: `D:\work\disabled`, Sandboxes: []string{`D:\work\enabled`}}},
		events: []opencode.Event{
			{Directory: `D:\work\disabled`, Type: "session.idle"},
			{Directory: `D:\work\enabled`, Type: "session.idle"},
		},
	}
	adapter := NewRoutedAdapter(stub, registry)
	directories, err := adapter.ListDirectories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(directories) != 1 || directories[0] != `D:\work\enabled` {
		t.Fatalf("directories=%v", directories)
	}
	projects, err := adapter.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Worktree != "" || len(projects[0].Sandboxes) != 1 {
		t.Fatalf("projects=%+v", projects)
	}
	var events []opencode.Event
	if err := adapter.WatchEvents(context.Background(), func(event opencode.Event) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Directory != `D:\work\enabled` {
		t.Fatalf("events=%+v", events)
	}
}
