package desktop

import (
	"testing"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
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

func TestLastUserMessageReturnsLatestCompleteText(t *testing.T) {
	older := opencode.Message{Info: opencode.MessageInfo{Role: "user"}, Parts: []opencode.Part{{Type: "text", Text: "older"}}}
	older.Info.Time.Created = time.Now().Add(-time.Minute).UnixMilli()
	assistant := opencode.Message{Info: opencode.MessageInfo{Role: "assistant"}, Parts: []opencode.Part{{Type: "text", Text: "ignore me"}}}
	latest := opencode.Message{Info: opencode.MessageInfo{Role: "user"}, Parts: []opencode.Part{
		{Type: "text", Text: "first complete paragraph"},
		{Type: "tool", Text: "not user text"},
		{Type: "text", Text: "second complete paragraph"},
	}}
	created := time.Now().Add(-5 * time.Second).Truncate(time.Millisecond)
	latest.Info.Time.Created = created.UnixMilli()

	at, text, ok := lastUserMessage([]opencode.Message{older, assistant, latest})
	if !ok {
		t.Fatal("lastUserMessage did not find the latest user message")
	}
	if !at.Equal(created) {
		t.Fatalf("created at = %v, want %v", at, created)
	}
	if text != "first complete paragraph\n\nsecond complete paragraph" {
		t.Fatalf("text = %q", text)
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
