package desktop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
)

type routeAdapterStub struct {
	opencode.Adapter
	projects    []opencode.Project
	events      []opencode.Event
	sessions    []opencode.Session
	questions   []opencode.QuestionRequest
	permissions []opencode.PermissionRequest
}

func (s routeAdapterStub) ListQuestions(context.Context, string) ([]opencode.QuestionRequest, error) {
	return s.questions, nil
}
func (s routeAdapterStub) ListPermissions(context.Context, string) ([]opencode.PermissionRequest, error) {
	return s.permissions, nil
}

func (s routeAdapterStub) ListSessions(context.Context, string) ([]opencode.Session, error) {
	return s.sessions, nil
}

func (s routeAdapterStub) ListProjects(context.Context) ([]opencode.Project, error) {
	return s.projects, nil
}

func TestRoutedAdapterHidesGoalSessionLifecycleButKeepsApprovals(t *testing.T) {
	registry := NewRouteRegistry()
	directory := `D:\work\enabled`
	registry.Replace([]domain.ProjectRoute{{Directory: directory, RouteEnabled: true}})
	registry.ReplaceGoalSessions(map[string]struct{}{routeKey(directory) + "\x00ses_goal": {}})
	idle, _ := json.Marshal(opencode.SessionEvent{SessionID: "ses_goal"})
	question, _ := json.Marshal(map[string]any{"id": "que_1", "sessionID": "ses_goal"})
	stub := routeAdapterStub{
		sessions: []opencode.Session{{ID: "ses_goal"}, {ID: "ses_normal"}},
		events: []opencode.Event{
			{Directory: directory, Type: "session.idle", Properties: idle},
			{Directory: directory, Type: "question.asked", Properties: question},
		},
	}
	adapter := NewRoutedAdapter(stub, registry)
	sessions, err := adapter.ListSessions(context.Background(), directory)
	if err != nil || len(sessions) != 1 || sessions[0].ID != "ses_normal" {
		t.Fatalf("sessions=%+v err=%v", sessions, err)
	}
	var events []opencode.Event
	if err := adapter.WatchEvents(context.Background(), func(event opencode.Event) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "question.asked" {
		t.Fatalf("events=%+v", events)
	}
}

func TestRoutedAdapterHidesAutonomousGoalApprovalsFromFeishu(t *testing.T) {
	registry := NewRouteRegistry()
	directory := `D:\work\enabled`
	registry.Replace([]domain.ProjectRoute{{Directory: directory, RouteEnabled: true}})
	registry.ReplaceAutonomousGoalSessions(map[string]struct{}{routeKey(directory) + "\x00ses_goal": {}})
	questionEvent, _ := json.Marshal(opencode.QuestionRequest{ID: "que_goal", SessionID: "ses_goal"})
	permissionEvent, _ := json.Marshal(opencode.PermissionRequest{ID: "per_goal", SessionID: "ses_goal"})
	stub := routeAdapterStub{
		questions:   []opencode.QuestionRequest{{ID: "que_goal", SessionID: "ses_goal"}, {ID: "que_normal", SessionID: "ses_normal"}},
		permissions: []opencode.PermissionRequest{{ID: "per_goal", SessionID: "ses_goal"}, {ID: "per_normal", SessionID: "ses_normal"}},
		events:      []opencode.Event{{Directory: directory, Type: "question.asked", Properties: questionEvent}, {Directory: directory, Type: "permission.asked", Properties: permissionEvent}},
	}
	adapter := NewRoutedAdapter(stub, registry)
	questions, err := adapter.ListQuestions(context.Background(), directory)
	if err != nil || len(questions) != 1 || questions[0].ID != "que_normal" {
		t.Fatalf("questions=%+v err=%v", questions, err)
	}
	permissions, err := adapter.ListPermissions(context.Background(), directory)
	if err != nil || len(permissions) != 1 || permissions[0].ID != "per_normal" {
		t.Fatalf("permissions=%+v err=%v", permissions, err)
	}
	var events []opencode.Event
	if err := adapter.WatchEvents(context.Background(), func(event opencode.Event) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events=%+v", events)
	}
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
