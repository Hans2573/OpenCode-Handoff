package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func TestGoalBlockedPattern(t *testing.T) {
	value := "```goal-status\n<<<{\"completed\":false,\"blocked\":true,\"reason\":\"unsafe\",\"attempts\":[\"safe option\"],\"required_capability\":\"system access\"}>>>\n```"
	if !goalBlockedPattern.MatchString(value) {
		t.Fatalf("expected blocked marker: %q", value)
	}
}

func TestSupervisorQuestionDecisionIsStructuredAndValidated(t *testing.T) {
	custom := false
	request := opencode.QuestionRequest{ID: "que_1", Questions: []opencode.QuestionInfo{{Question: "Choose", Options: []opencode.QuestionOption{{Label: "safe"}, {Label: "unsafe"}}, Custom: &custom}}}
	text := "```goal-supervisor\n<<<{\"kind\":\"question\",\"request_id\":\"que_1\",\"decision\":\"answer\",\"answers\":[[\"safe\"]],\"risk\":\"low\",\"reason\":\"best safe path\",\"suggestion\":\"\"}>>>\n```"
	decision, err := parseSupervisorDecision(text, "que_1", "question")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateQuestionAnswers(request, decision.Answers); err != nil {
		t.Fatal(err)
	}
	decision.Answers = [][]string{{"invented"}}
	if err := validateQuestionAnswers(request, decision.Answers); err == nil {
		t.Fatal("expected unknown option to be rejected")
	}
	dangerous := request
	dangerous.Questions[0].Options[0].Description = "Delete all data before rebuilding"
	if !questionAnswersHardBlocked(dangerous, [][]string{{"safe"}}) {
		t.Fatal("expected dangerous option description to be hard blocked")
	}
}

func TestHardBlockedPermissionRejectsUnapprovedExternalDirectory(t *testing.T) {
	project := t.TempDir()
	external := t.TempDir()
	loop := domain.GoalLoop{Directory: project, AllowedDirectories: []string{filepath.Join(project, "shared")}}
	request := opencode.PermissionRequest{Permission: "external_directory", Patterns: []string{filepath.Join(external, "*")}}
	if reason := hardBlockedPermission(loop, request); !strings.Contains(reason, "不在 Goal 配置") {
		t.Fatalf("reason=%q", reason)
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

func TestGoalLoopAttachesExistingIdleSessionWithoutInterruptingIt(t *testing.T) {
	var prompts []string
	createdSessions := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/session/ses_existing":
			_, _ = io.WriteString(response, `{"id":"ses_existing","directory":"/work/project","title":"existing"}`)
		case request.Method == http.MethodPost && request.URL.Path == "/session":
			createdSessions++
			_, _ = io.WriteString(response, `{"id":"unexpected"}`)
		case request.Method == http.MethodPost && request.URL.Path == "/session/ses_existing/prompt_async":
			var body struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			if len(body.Parts) > 0 {
				prompts = append(prompts, body.Parts[0].Text)
			}
			response.WriteHeader(http.StatusNoContent)
		case request.URL.Path == "/config/providers":
			_, _ = io.WriteString(response, `{"providers":[{"id":"openai","name":"OpenAI","models":{"gpt-test":{"id":"gpt-test","name":"GPT Test","variants":{"high":{}}}}}]}`)
		case request.URL.Path == "/question" || request.URL.Path == "/permission":
			_, _ = io.WriteString(response, `[]`)
		case request.URL.Path == "/session/status":
			_, _ = io.WriteString(response, `{}`)
		case request.URL.Path == "/session/ses_existing/message":
			_, _ = io.WriteString(response, `[{"info":{"id":"old_assistant","sessionID":"ses_existing","role":"assistant"},"parts":[{"type":"text","text":"previous work"}]}]`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	manager, database, project := newGoalLoopTestManager(t, server.URL)
	page, err := manager.CreateGoalLoop(GoalLoopInput{Goal: "finish attached work", ProjectID: project.ID, AgentID: store.DefaultAgentID, SessionID: "ses_existing", AutomationMode: domain.GoalLoopAutonomous, ModelProviderID: "openai", ModelID: "gpt-test", ModelVariant: "high", FailureLimit: 3, GoalCommandConfirmed: true, StartNow: true})
	if err != nil {
		t.Fatal(err)
	}
	if createdSessions != 0 || len(prompts) != 1 {
		t.Fatalf("created=%d prompts=%q", createdSessions, prompts)
	}
	if !strings.HasPrefix(prompts[0], "/goal finish attached work") || !strings.Contains(prompts[0], "保留有效成果") {
		t.Fatalf("attach prompt=%q", prompts[0])
	}
	loop, err := database.GetGoalLoop(context.Background(), page.Loops[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loop.AttachedSession || loop.SessionID != "ses_existing" || loop.CycleCount != 1 || loop.LastAssistantMessageID != "old_assistant" || loop.SupervisorModelVariant != "high" {
		t.Fatalf("loop=%+v", loop)
	}
}

func TestGoalLoopWaitsForBusyAttachedSessionBeforeSendingGoal(t *testing.T) {
	busy := true
	var prompts []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/session/ses_busy":
			_, _ = io.WriteString(response, `{"id":"ses_busy","directory":"/work/project","title":"busy"}`)
		case request.Method == http.MethodPost && request.URL.Path == "/session/ses_busy/prompt_async":
			var body struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			if len(body.Parts) > 0 {
				prompts = append(prompts, body.Parts[0].Text)
			}
			response.WriteHeader(http.StatusNoContent)
		case request.URL.Path == "/config/providers":
			_, _ = io.WriteString(response, `{"providers":[{"id":"openai","name":"OpenAI","models":{"gpt-test":{"id":"gpt-test","name":"GPT Test"}}}]}`)
		case request.URL.Path == "/question" || request.URL.Path == "/permission":
			_, _ = io.WriteString(response, `[]`)
		case request.URL.Path == "/session/status":
			if busy {
				_, _ = io.WriteString(response, `{"ses_busy":{"type":"busy"}}`)
			} else {
				_, _ = io.WriteString(response, `{}`)
			}
		case request.URL.Path == "/session/ses_busy/message":
			_, _ = io.WriteString(response, `[{"info":{"id":"old_assistant","sessionID":"ses_busy","role":"assistant"},"parts":[{"type":"text","text":"still working"}]}]`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	manager, database, project := newGoalLoopTestManager(t, server.URL)
	page, err := manager.CreateGoalLoop(GoalLoopInput{Goal: "wait then continue", ProjectID: project.ID, AgentID: store.DefaultAgentID, SessionID: "ses_busy", AutomationMode: domain.GoalLoopAutonomous, ModelProviderID: "openai", ModelID: "gpt-test", FailureLimit: 3, GoalCommandConfirmed: true, StartNow: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 0 || page.Loops[0].Status != domain.GoalLoopWaitingTakeover {
		t.Fatalf("prompts=%q loop=%+v", prompts, page.Loops[0])
	}
	busy = false
	loop, err := database.GetGoalLoop(context.Background(), page.Loops[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	manager.processGoalLoop(context.Background(), loop)
	if len(prompts) != 1 || !strings.HasPrefix(prompts[0], "/goal wait then continue") {
		t.Fatalf("prompts=%q", prompts)
	}
}

func TestGoalLoopRejectsSecondActiveBindingForSameSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet && request.URL.Path == "/session/ses_shared" {
			_, _ = io.WriteString(response, `{"id":"ses_shared","directory":"/work/project","title":"shared"}`)
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()

	manager, database, project := newGoalLoopTestManager(t, server.URL)
	now := time.Now().UTC()
	loop := domain.GoalLoop{ID: "goal_bound", Name: "bound", Goal: "finish", ProjectID: project.ID, ProjectName: project.Name, Directory: project.Directory, SessionID: "ses_shared", AttachedSession: true, AutomationMode: domain.GoalLoopAutonomous, Status: domain.GoalLoopRunning, FailureLimit: 3, CreatedAt: now, UpdatedAt: now}
	if err := database.CreateGoalLoop(context.Background(), loop); err != nil {
		t.Fatal(err)
	}
	if err := manager.validateGoalSessionBinding(context.Background(), "ses_shared", project.Directory, ""); err == nil || !strings.Contains(err.Error(), "另一个活动 Goal") {
		t.Fatalf("expected active binding error, got %v", err)
	}
}

func TestSupervisorFailureThresholdRebuildsWithFallbackModel(t *testing.T) {
	manager, database, project := newGoalLoopTestManager(t, "http://127.0.0.1:1")
	now := time.Now().UTC()
	loop := domain.GoalLoop{ID: "goal_recovery", Name: "recovery", Goal: "finish", ProjectID: project.ID, ProjectName: project.Name, Directory: project.Directory, ModelProviderID: "primary", ModelID: "executor", SupervisorModelProviderID: "fast", SupervisorModelID: "judge", SupervisorSessionID: "ses_supervisor", PendingRequestID: "per_1", PendingRequestType: "permission", AutomationMode: domain.GoalLoopAutonomous, Status: domain.GoalLoopDeciding, FailureLimit: 2, CreatedAt: now, UpdatedAt: now}
	if err := database.CreateGoalLoop(context.Background(), loop); err != nil {
		t.Fatal(err)
	}
	manager.recordSupervisorFailure(context.Background(), &loop, errors.New("temporary model error"))
	manager.recordSupervisorFailure(context.Background(), &loop, errors.New("temporary model error"))
	stored, err := database.GetGoalLoop(context.Background(), loop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SupervisorModelProviderID != "primary" || stored.SupervisorModelID != "executor" || stored.SupervisorSessionID != "" || stored.PendingRequestID != "" {
		t.Fatalf("fallback loop=%+v", stored)
	}
}

func TestAutonomousGoalManualOverrideCannotBypassHardSafety(t *testing.T) {
	handled := false
	replies := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/permission":
			if handled {
				_, _ = io.WriteString(response, `[]`)
				return
			}
			_, _ = io.WriteString(response, `[{"id":"per_unsafe","sessionID":"ses_exec","permission":"external_directory","patterns":["/outside/*"]}]`)
		case request.Method == http.MethodPost && request.URL.Path == "/permission/per_unsafe/reply":
			handled = true
			replies++
			response.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodGet && request.URL.Path == "/question":
			_, _ = io.WriteString(response, `[]`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	manager, database, project := newGoalLoopTestManager(t, server.URL)
	now := time.Now().UTC()
	loop := domain.GoalLoop{ID: "goal_override", Name: "override", Goal: "finish safely", ProjectID: project.ID, ProjectName: project.Name, Directory: project.Directory, SessionID: "ses_exec", AttachedSession: true, AutomationMode: domain.GoalLoopAutonomous, Status: domain.GoalLoopDeciding, PendingRequestID: "per_unsafe", PendingRequestType: "permission", FailureLimit: 3, CycleCount: 1, CreatedAt: now, UpdatedAt: now}
	if err := database.CreateGoalLoop(context.Background(), loop); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReplyLoopPermission("per_unsafe", project.Directory, "always"); err == nil || !strings.Contains(err.Error(), "不允许永久授权") {
		t.Fatalf("expected permanent grant rejection, got %v", err)
	}
	if _, err := manager.ReplyLoopPermission("per_unsafe", project.Directory, "once"); err == nil || !strings.Contains(err.Error(), "安全边界") {
		t.Fatalf("expected hard-safety rejection, got %v", err)
	}
	if replies != 0 {
		t.Fatalf("unsafe override reached OpenCode: replies=%d", replies)
	}
	if _, err := manager.ReplyLoopPermission("per_unsafe", project.Directory, "reject"); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetGoalLoop(context.Background(), loop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replies != 1 || stored.PendingRequestID != "" || !strings.Contains(stored.PendingFeedback, "用户在桌面应用中手动拒绝") {
		t.Fatalf("replies=%d loop=%+v", replies, stored)
	}
}

func TestGoalLoopAutonomouslyAnswersValidatedQuestion(t *testing.T) {
	answered := false
	var submitted [][]string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/question":
			if answered {
				_, _ = io.WriteString(response, `[]`)
				return
			}
			_, _ = io.WriteString(response, `[{"id":"que_1","sessionID":"ses_exec","questions":[{"question":"Choose","header":"Plan","options":[{"label":"safe"},{"label":"unsafe"}],"multiple":false,"custom":false}]}]`)
		case request.Method == http.MethodPost && request.URL.Path == "/question/que_1/reply":
			var body struct {
				Answers [][]string `json:"answers"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			submitted, answered = body.Answers, true
			response.WriteHeader(http.StatusNoContent)
		case request.URL.Path == "/permission":
			_, _ = io.WriteString(response, `[]`)
		case request.URL.Path == "/session/status":
			_, _ = io.WriteString(response, `{}`)
		case request.URL.Path == "/session/ses_supervisor/message":
			_, _ = io.WriteString(response, `[{"info":{"id":"decision_1","sessionID":"ses_supervisor","role":"assistant"},"parts":[{"type":"text","text":"`+"```goal-supervisor\\n<<<{\\\"kind\\\":\\\"question\\\",\\\"request_id\\\":\\\"que_1\\\",\\\"decision\\\":\\\"answer\\\",\\\"answers\\\":[[\\\"safe\\\"]],\\\"risk\\\":\\\"low\\\",\\\"reason\\\":\\\"safe choice\\\",\\\"suggestion\\\":\\\"\\\"}>>>\\n```"+`"}]}]`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	manager, database, project := newGoalLoopTestManager(t, server.URL)
	now := time.Now().UTC()
	loop := domain.GoalLoop{ID: "goal_auto", Name: "auto", Goal: "finish", ProjectID: project.ID, ProjectName: project.Name, Directory: project.Directory, AgentID: store.DefaultAgentID, AgentName: "OpenCode", ModelProviderID: "openai", ModelID: "gpt-test", SupervisorModelProviderID: "openai", SupervisorModelID: "gpt-test", SessionID: "ses_exec", SupervisorSessionID: "ses_supervisor", PendingRequestID: "que_1", PendingRequestType: "question", SupervisorLastMessageID: "baseline", AutomationMode: domain.GoalLoopAutonomous, Status: domain.GoalLoopDeciding, FailureLimit: 3, CycleCount: 1, CreatedAt: now, UpdatedAt: now}
	if err := database.CreateGoalLoop(context.Background(), loop); err != nil {
		t.Fatal(err)
	}
	manager.processGoalLoop(context.Background(), loop)
	if !answered || len(submitted) != 1 || len(submitted[0]) != 1 || submitted[0][0] != "safe" {
		t.Fatalf("answered=%v submitted=%v", answered, submitted)
	}
	events, err := database.ListGoalLoopEvents(context.Background(), loop.ID, 10)
	if err != nil || len(events) != 1 || events[0].Metadata["decision"] != "answer" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func newGoalLoopTestManager(t *testing.T, serverURL string) (*Manager, *store.SQLite, domain.AgentProject) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goal-loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureDesktopDefaults(ctx, serverURL); err != nil {
		t.Fatal(err)
	}
	project := domain.AgentProject{ID: "project_1", AgentID: store.DefaultAgentID, Name: "project", Directory: "/work/project", LastSeen: time.Now().UTC()}
	if err := database.SyncProjects(ctx, []domain.AgentProject{project}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetProjectRoute(ctx, project.ID, store.DefaultChannelID, true); err != nil {
		t.Fatal(err)
	}
	client, err := opencode.NewClient(opencode.ClientOptions{BaseURL: serverURL})
	if err != nil {
		t.Fatal(err)
	}
	routes := NewRouteRegistry()
	items, err := database.ListProjectRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	routes.Replace(items)
	manager := &Manager{ctx: ctx, cancel: cancel, store: database, raw: client, routes: routes, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Cleanup(func() { _ = manager.Close() })
	return manager, database, project
}
