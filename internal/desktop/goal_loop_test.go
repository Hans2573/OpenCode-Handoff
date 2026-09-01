package desktop

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
	"github.com/Hans2573/OpenCode-Handoff/internal/store"
)

func TestGoalCompletionPattern(t *testing.T) {
	valid := []string{
		"```goal-status\n<<<{\"completed\":true}>>>\n```",
		"Done.\n\n```goal-status\n<<< { \"completed\" : true } >>>\n```\n",
	}
	for _, value := range valid {
		if !goalCompletionPattern.MatchString(value) {
			t.Fatalf("expected completion marker: %q", value)
		}
	}
	invalid := []string{
		"<<<{\"completed\":true}>>>",
		"```json\n<<<{\"completed\":true}>>>\n```",
		"```goal-status\n<<<{\"completed\":false}>>>\n```",
		"```goal-status\n<<<{\"completed\":true}>>>\n```\nmore",
	}
	for _, value := range invalid {
		if goalCompletionPattern.MatchString(value) {
			t.Fatalf("unexpected completion marker: %q", value)
		}
	}
}

func TestGoalNameUsesFirstLineAndTruncates(t *testing.T) {
	if got := goalName("  first line\nsecond line"); got != "first line" {
		t.Fatalf("goalName=%q", got)
	}
	if got := goalName("这是一个需要被截断的非常长的目标名称，因为它明显超过四十个字符并且还在继续继续继续继续继续"); len([]rune(got)) != 41 {
		t.Fatalf("truncated rune count=%d, value=%q", len([]rune(got)), got)
	}
}

func TestGoalLoopStartsContinuesAndCompletesInOneSession(t *testing.T) {
	var prompts []string
	var promptModels []opencode.ModelRef
	assistantMessage := "still working"
	assistantID := "msg_1"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/session":
			_, _ = io.WriteString(response, `{"id":"ses_goal","directory":"/work/project","title":"goal"}`)
		case request.Method == http.MethodPost && request.URL.Path == "/session/ses_goal/prompt_async":
			var body struct {
				Model *struct {
					ProviderID string `json:"providerID"`
					ModelID    string `json:"modelID"`
				} `json:"model"`
				Variant string `json:"variant"`
				Parts   []struct {
					Text string `json:"text"`
				} `json:"parts"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode prompt: %v", err)
			}
			if len(body.Parts) > 0 {
				prompts = append(prompts, body.Parts[0].Text)
			}
			if body.Model != nil {
				promptModels = append(promptModels, opencode.ModelRef{ProviderID: body.Model.ProviderID, ModelID: body.Model.ModelID, Variant: body.Variant})
			}
			response.WriteHeader(http.StatusNoContent)
		case request.URL.Path == "/config/providers":
			_, _ = io.WriteString(response, `{"providers":[{"id":"openai","name":"OpenAI","models":{"gpt-test":{"id":"gpt-test","name":"GPT Test","variants":{"high":{}}}}}]}`)
		case request.URL.Path == "/question" || request.URL.Path == "/permission":
			_, _ = io.WriteString(response, `[]`)
		case request.URL.Path == "/session/status":
			_, _ = io.WriteString(response, `{}`)
		case request.URL.Path == "/session/ses_goal/message":
			_ = json.NewEncoder(response).Encode([]map[string]any{{
				"info":  map[string]any{"id": assistantID, "sessionID": "ses_goal", "role": "assistant"},
				"parts": []map[string]any{{"type": "text", "text": assistantMessage}},
			}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goal-loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureDesktopDefaults(ctx, server.URL); err != nil {
		t.Fatal(err)
	}
	project := domain.AgentProject{ID: "project_1", AgentID: store.DefaultAgentID, Name: "project", Directory: "/work/project", LastSeen: time.Now().UTC()}
	if err := database.SyncProjects(ctx, []domain.AgentProject{project}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetProjectRoute(ctx, project.ID, store.DefaultChannelID, true); err != nil {
		t.Fatal(err)
	}
	client, err := opencode.NewClient(opencode.ClientOptions{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	routes := NewRouteRegistry()
	routeItems, err := database.ListProjectRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	routes.Replace(routeItems)
	manager := &Manager{ctx: ctx, cancel: cancel, store: database, raw: client, routes: routes, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Cleanup(func() { _ = manager.Close() })

	page, err := manager.CreateGoalLoop(GoalLoopInput{Goal: "finish the task", ProjectID: project.ID, AgentID: store.DefaultAgentID, ModelProviderID: "openai", ModelID: "gpt-test", ModelVariant: "high", FailureLimit: 3, GoalCommandConfirmed: true, StartNow: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Loops) != 1 || page.Loops[0].SessionID != "ses_goal" || page.Loops[0].ModelID != "gpt-test" || len(prompts) != 1 || prompts[0] != "/goal finish the task" || len(promptModels) != 1 || promptModels[0].Variant != "high" {
		t.Fatalf("page=%+v prompts=%q models=%+v", page, prompts, promptModels)
	}

	loop, err := database.GetGoalLoop(ctx, page.Loops[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	manager.processGoalLoop(ctx, loop)
	if len(prompts) != 2 || prompts[1] != goalContinuationPrompt || len(promptModels) != 2 || promptModels[1].ModelID != "gpt-test" {
		t.Fatalf("continuation prompts=%q models=%+v", prompts, promptModels)
	}

	assistantID = "msg_2"
	assistantMessage = "done\n\n```goal-status\n<<<{\"completed\":true}>>>\n```"
	loop, err = database.GetGoalLoop(ctx, loop.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager.processGoalLoop(ctx, loop)
	loop, err = database.GetGoalLoop(ctx, loop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loop.Status != domain.GoalLoopCompleted || loop.SessionID != "ses_goal" || len(prompts) != 2 {
		t.Fatalf("completed loop=%+v prompts=%q", loop, prompts)
	}
}
