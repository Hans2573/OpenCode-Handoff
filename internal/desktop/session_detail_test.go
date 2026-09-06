package desktop

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/config"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
)

func TestSessionOperationCapturesDurationInputAndSubagent(t *testing.T) {
	started := time.Now().UTC().Add(-65 * time.Second).Truncate(time.Millisecond)
	root := opencode.Session{ID: "root", Title: "Root"}
	source := opencode.Session{ID: "child", ParentID: "root", Title: "Research"}
	message := opencode.Message{}
	message.Info.ID = "msg_1"
	message.Info.Agent = "explore"
	part := opencode.Part{ID: "part_1", Type: "tool", Tool: "bash"}
	part.State.Status = "completed"
	part.State.Title = "run tests"
	part.State.Input = map[string]any{"command": "curl -H 'Authorization: Bearer abc123' --token raw-token https://user:pass@example.com", "password": "private", "prompt": "inspect performance"}
	part.State.Time.Start = started.UnixMilli()
	part.State.Time.End = started.Add(65 * time.Second).UnixMilli()

	operation, ok := sessionOperationFromPart(root, source, message, part, 0, time.Now().UTC())
	if !ok || operation.DurationSeconds != 65 || !operation.FromSubagent || operation.Agent != "explore" {
		t.Fatalf("operation = %+v, ok = %v", operation, ok)
	}
	if strings.Contains(operation.InputPreview, "private") || strings.Contains(operation.InputPreview, "abc123") || strings.Contains(operation.InputPreview, "raw-token") || strings.Contains(operation.InputPreview, "pass@example") || !strings.Contains(operation.InputPreview, "***") || !strings.Contains(operation.InputPreview, "inspect performance") {
		t.Fatalf("input preview was not selectively redacted: %s", operation.InputPreview)
	}
}

func TestSlowInputPreviewIsLimitedToFourKilobytes(t *testing.T) {
	preview := slowInputPreview(map[string]any{"command": strings.Repeat("界", 5000)})
	if len(preview) > sessionSlowInputLimit || !strings.HasSuffix(preview, "...") {
		t.Fatalf("preview bytes = %d", len(preview))
	}
}

func TestGetSessionDetailIncludesRootAndSubagentSlowOperations(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/session":
			_, _ = fmt.Fprintf(response, `[{"id":"root","directory":"/work/project","title":"Root","time":{"created":%d,"updated":%d}},{"id":"child","parentID":"root","directory":"/work/project","title":"Research","time":{"created":%d,"updated":%d}}]`, now.Add(-10*time.Minute).UnixMilli(), now.UnixMilli(), now.Add(-3*time.Minute).UnixMilli(), now.UnixMilli())
		case "/session/status":
			_, _ = io.WriteString(response, `{}`)
		case "/session/root/message":
			_, _ = fmt.Fprintf(response, `[{"info":{"id":"msg_root","sessionID":"root","role":"assistant","agent":"build","time":{"created":%d,"completed":%d}},"parts":[{"id":"part_root","type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"go test ./..."},"time":{"start":%d,"end":%d}}}]}]`, now.Add(-2*time.Minute).UnixMilli(), now.Add(-75*time.Second).UnixMilli(), now.Add(-2*time.Minute).UnixMilli(), now.Add(-75*time.Second).UnixMilli())
		case "/session/child/message":
			_, _ = fmt.Fprintf(response, `[{"info":{"id":"msg_child","sessionID":"child","role":"assistant","agent":"explore","time":{"created":%d,"completed":%d}},"parts":[{"id":"part_child","type":"tool","tool":"webfetch","state":{"status":"completed","input":{"url":"https://example.com"},"time":{"start":%d,"end":%d}}}]}]`, now.Add(-150*time.Second).UnixMilli(), now.Add(-30*time.Second).UnixMilli(), now.Add(-150*time.Second).UnixMilli(), now.Add(-30*time.Second).UnixMilli())
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	manager, database, project := newGoalLoopTestManager(t, server.URL)
	manager.cfg = config.Default()
	detail, err := manager.GetSessionDetail("root", project.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ToolCallCount != 2 || detail.SubagentCount != 1 || len(detail.Subagents) != 1 || detail.Subagents[0].ID != "child" {
		t.Fatalf("detail = %+v", detail)
	}
	stored, err := database.ListSlowOperations(t.Context(), "root", project.Directory, 10)
	if err != nil || len(stored) != 2 || stored[0].SourceSessionID != "child" || stored[0].DurationSeconds != 120 {
		t.Fatalf("stored = %+v, err = %v", stored, err)
	}
}
