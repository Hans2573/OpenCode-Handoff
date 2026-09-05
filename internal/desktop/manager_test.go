package desktop

import (
	"testing"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
	"github.com/Hans2573/OpenCode-Handoff/internal/store"
)

func TestMapSessionStatus(t *testing.T) {
	tests := []struct {
		enabled bool
		input   opencode.SessionStatus
		want    string
	}{
		{false, opencode.SessionStatus{Type: "busy"}, "unmonitored"},
		{true, opencode.SessionStatus{Type: "busy"}, "running"},
		{true, opencode.SessionStatus{Type: "retry"}, "retrying"},
		{true, opencode.SessionStatus{Type: "idle"}, "idle"},
		{true, opencode.SessionStatus{Type: "future-state"}, "unknown"},
	}
	for _, test := range tests {
		got, _, _ := mapSessionStatus(test.enabled, test.input)
		if got != test.want {
			t.Errorf("mapSessionStatus(%v, %q) = %q, want %q", test.enabled, test.input.Type, got, test.want)
		}
	}
}

func TestCountCompletedSessionsUsesAllTopLevelMonitoredSessions(t *testing.T) {
	sessions := []opencode.Session{
		{ID: "idle"},
		{ID: "explicit-idle"},
		{ID: "running"},
		{ID: "retrying"},
		{ID: "question"},
		{ID: "permission"},
		{ID: "child", ParentID: "idle"},
	}
	statuses := map[string]opencode.SessionStatus{
		"explicit-idle": {Type: "idle"},
		"running":       {Type: "busy"},
		"retrying":      {Type: "retry"},
	}
	questions := map[string]struct{}{"question": {}}
	permissions := map[string]struct{}{"permission": {}}

	if got := countCompletedSessions(true, sessions, statuses, questions, permissions); got != 2 {
		t.Fatalf("completed sessions = %d, want 2", got)
	}
	if got := countCompletedSessions(false, sessions, statuses, questions, permissions); got != 0 {
		t.Fatalf("unmonitored completed sessions = %d, want 0", got)
	}
}

func TestLastUserMessageReturnsLatestCompleteText(t *testing.T) {
	older := opencode.Message{Info: opencode.MessageInfo{Role: "user"}, Parts: []opencode.Part{{Type: "text", Text: "older"}}}
	older.Info.Time.Created = time.Now().Add(-time.Minute).UnixMilli()
	assistant := opencode.Message{Info: opencode.MessageInfo{Role: "assistant"}, Parts: []opencode.Part{{Type: "text", Text: "ignore me"}}}
	latest := opencode.Message{Info: opencode.MessageInfo{Role: "user"}, Parts: []opencode.Part{
		{Type: "text", Text: "first complete paragraph"},
		{Type: "tool", Text: "not user text"},
		{Type: "text", Text: "second complete paragraph"},
	}}
	latest.Info.Model = &opencode.ModelRef{ProviderID: "openai", ModelID: "gpt-test", Variant: "high"}
	created := time.Now().Add(-5 * time.Second).Truncate(time.Millisecond)
	latest.Info.Time.Created = created.UnixMilli()

	at, text, model, ok := lastUserMessage([]opencode.Message{older, assistant, latest})
	if !ok {
		t.Fatal("lastUserMessage did not find the latest user message")
	}
	if !at.Equal(created) {
		t.Fatalf("created at = %v, want %v", at, created)
	}
	if text != "first complete paragraph\n\nsecond complete paragraph" {
		t.Fatalf("text = %q", text)
	}
	if model == nil || model.ProviderID != "openai" || model.ModelID != "gpt-test" || model.Variant != "high" {
		t.Fatalf("model = %+v", model)
	}
}

func TestRedactMetadataRecursivelyRemovesSensitiveValues(t *testing.T) {
	redacted := redactMetadata(map[string]any{
		"project": "alpha",
		"nested": map[string]any{
			"accessToken": "must-not-survive",
			"items":       []any{map[string]any{"promptText": "private prompt"}},
		},
	})
	if redacted["project"] != "alpha" {
		t.Fatalf("non-sensitive value changed: %+v", redacted)
	}
	nested := redacted["nested"].(map[string]any)
	if nested["accessToken"] != "***" {
		t.Fatalf("nested token was not redacted: %+v", redacted)
	}
	items := nested["items"].([]any)
	if items[0].(map[string]any)["promptText"] != "***" {
		t.Fatalf("prompt in slice was not redacted: %+v", redacted)
	}
}

func TestSortProjectsByRecentConversation(t *testing.T) {
	older := time.Now().Add(-2 * time.Hour).UTC()
	newer := time.Now().Add(-10 * time.Minute).UTC()
	projects := []ProjectView{
		{ID: "none-b", Name: "Beta", Directory: `D:\work\beta`},
		{ID: "old", Name: "Old", Directory: `D:\work\old`},
		{ID: "new", Name: "New", Directory: `D:\work\new`},
		{ID: "none-a", Name: "Alpha", Directory: `D:\work\alpha`},
	}
	sessions := []SessionView{
		{Directory: `d:\WORK\old`, UpdatedAt: older},
		{Directory: `D:\work\new`, UpdatedAt: older},
		{Directory: `D:\work\new`, UpdatedAt: newer},
	}

	sortProjectsByRecentConversation(projects, sessions)
	want := []string{"new", "old", "none-a", "none-b"}
	for index, id := range want {
		if projects[index].ID != id {
			t.Fatalf("projects[%d] = %q, want %q; all=%+v", index, projects[index].ID, id, projects)
		}
	}
	if !projects[0].LastConversationAt.Equal(newer) || !projects[1].LastConversationAt.Equal(older) {
		t.Fatalf("conversation timestamps were not assigned: %+v", projects)
	}
}

func TestFlattenProjectsIncludesGlobalSessionDirectories(t *testing.T) {
	now := time.Now().UTC()
	projects := []opencode.Project{
		{ID: "global", Worktree: "/"},
		{ID: "git-project", Worktree: `D:\work\git-project`, Sandboxes: []string{`D:\work\git-sandbox`}},
	}
	sessions := []opencode.Session{
		{ID: "ses_global", Directory: `D:\work\tokenhub`},
		{ID: "ses_duplicate", Directory: `d:\WORK\git-project`},
	}

	items := flattenProjects(projects, sessions, now)
	byDirectory := make(map[string]domain.AgentProject, len(items))
	for _, item := range items {
		byDirectory[routeKey(item.Directory)] = item
	}

	if len(items) != 4 {
		t.Fatalf("flattenProjects() returned %d items, want 4: %+v", len(items), items)
	}
	tokenhub, ok := byDirectory[routeKey(`D:\work\tokenhub`)]
	if !ok {
		t.Fatalf("global session directory was not discovered: %+v", items)
	}
	if tokenhub.Name != "tokenhub" || tokenhub.ID != stableProjectID(store.DefaultAgentID, tokenhub.Directory) {
		t.Fatalf("discovered session project = %+v", tokenhub)
	}
}

func TestChannelViewsShowsPairingRequired(t *testing.T) {
	items := (&Manager{}).channelViews(ServiceStatus{
		FeishuState:           "connected",
		FeishuPairingRequired: true,
	})
	if len(items) == 0 || items[0].Status != "pairing" || items[0].StatusLabel != "待配对" {
		t.Fatalf("channel views = %+v", items)
	}
}
