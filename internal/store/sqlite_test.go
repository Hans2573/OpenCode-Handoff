package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
)

func TestSQLiteHandoffLifecycle(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	handoff := domain.Handoff{
		ID:                     "hof_1",
		SessionID:              "ses_1",
		Directory:              "/work/a",
		ProjectName:            "a",
		Type:                   domain.HandoffFinished,
		LastAssistantMessageID: "msg_1",
		LastAssistantText:      "done",
		CreatedAt:              time.Now(),
	}
	if err := database.Create(ctx, handoff); err != nil {
		t.Fatal(err)
	}
	handoff.ID = "hof_duplicate"
	if err := database.Create(ctx, handoff); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate Create() error = %v", err)
	}
	if err := database.BindMessage(ctx, "hof_1", domain.MessageRef{ChatID: "oc_1", MessageID: "om_1"}); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetByID(ctx, "hof_1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastAssistantText != "done" || stored.FeishuMessageID != "om_1" || stored.Status != domain.StatusOpen {
		t.Fatalf("stored handoff = %+v", stored)
	}
	if _, err := database.GetByID(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing GetByID() error = %v", err)
	}

	claimed, err := database.ClaimByMessage(ctx, "om_1", "om_reply")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.SessionID != "ses_1" || claimed.Status != domain.StatusResumed {
		t.Fatalf("claimed handoff = %+v", claimed)
	}
	if _, err := database.ClaimByMessage(ctx, "om_1", "om_reply"); !errors.Is(err, ErrDuplicateReply) {
		t.Fatalf("duplicate reply error = %v", err)
	}
	if _, err := database.ClaimByMessage(ctx, "om_1", "om_reply_again"); err != nil {
		t.Fatalf("historical handoff claim error = %v", err)
	}
	if err := database.Reopen(ctx, "hof_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimByMessage(ctx, "om_1", "om_retry"); err != nil {
		t.Fatalf("claim after reopen: %v", err)
	}
}

func TestSQLiteChannelBinding(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	if _, err := database.GetChannelBinding(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("initial GetChannelBinding() error = %v", err)
	}
	binding := domain.ChannelBinding{ChatID: "oc_bound", UserIDs: []string{"ou_user", "user_id"}}
	if err := database.BindChannel(ctx, binding); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetChannelBinding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ChatID != "oc_bound" || len(stored.UserIDs) != 2 {
		t.Fatalf("binding = %+v", stored)
	}
	if err := database.BindChannel(ctx, binding); err != nil {
		t.Fatalf("idempotent BindChannel() error = %v", err)
	}
	if err := database.BindChannel(ctx, domain.ChannelBinding{ChatID: "oc_other", UserIDs: []string{"ou_other"}}); !errors.Is(err, ErrAlreadyBound) {
		t.Fatalf("rebind error = %v", err)
	}
}

func TestSQLiteDesktopRoutesAndEvents(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	if err := database.EnsureDesktopDefaults(ctx, "http://127.0.0.1:4096"); err != nil {
		t.Fatal(err)
	}
	projects := []domain.AgentProject{
		{ID: "project-a", AgentID: DefaultAgentID, Name: "Alpha", Directory: `D:\work\alpha`, LastSeen: time.Now()},
		{ID: "project-b", AgentID: DefaultAgentID, Name: "Beta", Directory: `D:\work\beta`, LastSeen: time.Now()},
	}
	if err := database.SyncProjects(ctx, projects); err != nil {
		t.Fatal(err)
	}
	routes, err := database.ListProjectRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 || routes[0].RouteEnabled || routes[1].RouteEnabled {
		t.Fatalf("initial routes = %+v", routes)
	}
	if err := database.SetProjectRoute(ctx, "project-a", DefaultChannelID, true); err != nil {
		t.Fatal(err)
	}
	routes, err = database.ListProjectRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !routes[0].RouteEnabled || routes[1].RouteEnabled {
		t.Fatalf("updated routes = %+v", routes)
	}

	old := time.Now().Add(-48 * time.Hour)
	for _, event := range []domain.EventLog{
		{Level: "info", Type: "old", Source: "test", Message: "expired", CreatedAt: old},
		{Level: "warn", Type: "new", Source: "test", Message: "keep this", Metadata: map[string]any{"attempt": float64(2)}},
	} {
		if err := database.AppendEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.CleanupEvents(ctx, 24*time.Hour, 10); err != nil {
		t.Fatal(err)
	}
	events, err := database.ListEvents(ctx, "keep", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "new" || events[0].Metadata["attempt"] != float64(2) {
		t.Fatalf("events = %+v", events)
	}
	if err := database.ClearEvents(ctx); err != nil {
		t.Fatal(err)
	}
	events, err = database.ListEvents(ctx, "", 20)
	if err != nil || len(events) != 0 {
		t.Fatalf("events after clear = %+v, err = %v", events, err)
	}
}

func TestSQLiteSessionExecutionLifecycleAndRetention(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	now := time.Now().UTC().Truncate(time.Millisecond)
	first := domain.SessionExecutionRun{
		SessionID: "ses_1", Directory: "/work/a", ProjectName: "Alpha",
		SessionTitle: "First session", StartedAt: now.Add(-10 * time.Minute),
	}
	if _, err := database.StartSessionExecution(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOpenSessionExecutions(ctx, first.SessionID, first.Directory, now.Add(-5*time.Minute), "completed"); err != nil {
		t.Fatal(err)
	}
	second := first
	second.StartedAt = now.Add(-time.Minute)
	if _, err := database.StartSessionExecution(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOpenSessionExecutions(ctx, second.SessionID, second.Directory, now, "human_intervention"); err != nil {
		t.Fatal(err)
	}

	runs, err := database.ListSessionExecutionRuns(ctx, 30*24*time.Hour, 20)
	if err != nil || len(runs) != 2 {
		t.Fatalf("execution runs = %+v, err = %v", runs, err)
	}
	if runs[0].DurationSeconds != 300 || runs[1].DurationSeconds != 60 {
		t.Fatalf("execution durations = %d, %d", runs[0].DurationSeconds, runs[1].DurationSeconds)
	}
	stats, err := database.ListSessionExecutionStats(ctx, 30*24*time.Hour)
	if err != nil || len(stats) != 1 {
		t.Fatalf("execution stats = %+v, err = %v", stats, err)
	}
	if stats[0].ExecutionCount != 2 || stats[0].TotalExecutionSeconds != 360 || stats[0].LatestExecutionSeconds != 60 {
		t.Fatalf("execution stats = %+v", stats[0])
	}

	old := first
	old.SessionID = "ses_old"
	old.StartedAt = now.Add(-40 * 24 * time.Hour)
	if _, err := database.StartSessionExecution(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOpenSessionExecutions(ctx, old.SessionID, old.Directory, now.Add(-39*24*time.Hour), "completed"); err != nil {
		t.Fatal(err)
	}
	if err := database.CleanupSessionExecutions(ctx, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	stats, err = database.ListSessionExecutionStats(ctx, 365*24*time.Hour)
	if err != nil || len(stats) != 1 || stats[0].SessionID != "ses_1" {
		t.Fatalf("stats after cleanup = %+v, err = %v", stats, err)
	}
}

func TestSQLiteProjectRoutesRequireExplicitOptIn(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.EnsureDesktopDefaults(ctx, "http://127.0.0.1:4096"); err != nil {
		t.Fatal(err)
	}
	project := domain.AgentProject{ID: "project-opt-in", AgentID: DefaultAgentID, Name: "Opt In", Directory: `D:\work\opt-in`}
	if err := database.SyncProjects(ctx, []domain.AgentProject{project}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetProjectRoute(ctx, project.ID, DefaultChannelID, true); err != nil {
		t.Fatal(err)
	}
	reset, err := database.EnsureProjectRoutesOptIn(ctx)
	if err != nil || !reset {
		t.Fatalf("first EnsureProjectRoutesOptIn() = %v, %v", reset, err)
	}
	routes, err := database.ListProjectRoutes(ctx)
	if err != nil || len(routes) != 1 || routes[0].RouteEnabled {
		t.Fatalf("routes after opt-in migration = %+v, err = %v", routes, err)
	}
	if err := database.SetProjectRoute(ctx, project.ID, DefaultChannelID, true); err != nil {
		t.Fatal(err)
	}
	reset, err = database.EnsureProjectRoutesOptIn(ctx)
	if err != nil || reset {
		t.Fatalf("second EnsureProjectRoutesOptIn() = %v, %v", reset, err)
	}
	routes, err = database.ListProjectRoutes(ctx)
	if err != nil || !routes[0].RouteEnabled {
		t.Fatalf("explicit user choice was not preserved: %+v, err = %v", routes, err)
	}
}

func TestSQLiteSessionCreateReceipt(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	if err := database.ClaimSessionCreate(ctx, "evt_1"); err != nil {
		t.Fatal(err)
	}
	if err := database.ClaimSessionCreate(ctx, "evt_1"); !errors.Is(err, ErrDuplicateReply) {
		t.Fatalf("duplicate session create claim error = %v", err)
	}
	if err := database.CompleteSessionCreate(ctx, "evt_1", "ses_1"); err != nil {
		t.Fatal(err)
	}
	if err := database.ReleaseSessionCreate(ctx, "evt_1"); err != nil {
		t.Fatal(err)
	}
	if err := database.ClaimSessionCreate(ctx, "evt_1"); !errors.Is(err, ErrDuplicateReply) {
		t.Fatalf("completed receipt was released: %v", err)
	}

	if err := database.ClaimSessionCreate(ctx, "evt_retry"); err != nil {
		t.Fatal(err)
	}
	if err := database.ReleaseSessionCreate(ctx, "evt_retry"); err != nil {
		t.Fatal(err)
	}
	if err := database.ClaimSessionCreate(ctx, "evt_retry"); err != nil {
		t.Fatalf("released receipt cannot be retried: %v", err)
	}
}

func TestSQLitePendingSessionModelLifecycle(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	if _, err := database.GetPendingSessionModel(ctx, "ses_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing pending model error = %v", err)
	}
	want := domain.SessionModel{ProviderID: "openai", ModelID: "gpt-test", ModelName: "GPT Test", Variant: "high"}
	if err := database.SetPendingSessionModel(ctx, "ses_1", want); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetPendingSessionModel(ctx, "ses_1")
	if err != nil || got != want {
		t.Fatalf("pending model = %+v, %v", got, err)
	}
	if err := database.ClearPendingSessionModel(ctx, "ses_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetPendingSessionModel(ctx, "ses_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleared pending model error = %v", err)
	}
}

func TestSQLiteRecentModelsAreOrderedAndUpdated(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	first := domain.SessionModel{ProviderID: "openai", ModelID: "gpt-test", ModelName: "GPT Test", Variant: "high"}
	second := domain.SessionModel{ProviderID: "anthropic", ModelID: "claude-test", ModelName: "Claude Test"}
	if err := database.RecordRecentModel(ctx, first); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := database.RecordRecentModel(ctx, second); err != nil {
		t.Fatal(err)
	}
	recent, err := database.ListRecentModels(ctx, 5)
	if err != nil || len(recent) != 2 || recent[0] != second || recent[1] != first {
		t.Fatalf("recent models = %+v, %v", recent, err)
	}
	first.ModelName = "GPT Test Renamed"
	time.Sleep(time.Millisecond)
	if err := database.RecordRecentModel(ctx, first); err != nil {
		t.Fatal(err)
	}
	recent, err = database.ListRecentModels(ctx, 1)
	if err != nil || len(recent) != 1 || recent[0] != first {
		t.Fatalf("updated recent models = %+v, %v", recent, err)
	}
}

func TestSQLitePersistsQuestionAndPreventsSecondAnswer(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	handoff := domain.Handoff{
		ID: "hof_question", SessionID: "ses_1", Directory: "/work/a", ProjectName: "a",
		Type: domain.HandoffQuestion, LastAssistantMessageID: "question:que_1", QuestionID: "que_1",
		Questions: []domain.Question{{Text: "Choose", Options: []domain.QuestionOption{{Label: "A"}}}},
		CreatedAt: time.Now(),
	}
	if err := database.Create(ctx, handoff); err != nil {
		t.Fatal(err)
	}
	if err := database.BindMessage(ctx, handoff.ID, domain.MessageRef{ChatID: "oc_1", MessageID: "om_question"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimByMessage(ctx, "om_question", "evt_1")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.QuestionID != "que_1" || len(claimed.Questions) != 1 || claimed.Questions[0].Options[0].Label != "A" {
		t.Fatalf("claimed question = %+v", claimed)
	}
	if _, err := database.ClaimByMessage(ctx, "om_question", "evt_2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second question claim error = %v", err)
	}
}

func TestSQLitePersistsPermissionAndPreventsSecondDecision(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	handoff := domain.Handoff{
		ID: "hof_permission", SessionID: "ses_1", Directory: "/work/a", ProjectName: "a",
		Type: domain.HandoffPermission, LastAssistantMessageID: "permission:per_1", PermissionID: "per_1",
		Permission: domain.Permission{
			Name: "external_directory", Patterns: []string{`C:\Users\test\Desktop\preview.html`},
			Always: []string{`C:\Users\test\Desktop\*`}, Metadata: map[string]any{"source": "read"},
		},
		CreatedAt: time.Now(),
	}
	if err := database.Create(ctx, handoff); err != nil {
		t.Fatal(err)
	}
	if err := database.BindMessage(ctx, handoff.ID, domain.MessageRef{ChatID: "oc_1", MessageID: "om_permission"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimByMessage(ctx, "om_permission", "evt_1")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.PermissionID != "per_1" || claimed.Permission.Name != "external_directory" || claimed.Permission.Always[0] != `C:\Users\test\Desktop\*` {
		t.Fatalf("claimed permission = %+v", claimed)
	}
	if _, err := database.ClaimByMessage(ctx, "om_permission", "evt_2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second permission claim error = %v", err)
	}
	handoff.ID = "hof_permission_2"
	handoff.PermissionID = "per_2"
	handoff.LastAssistantMessageID = "permission:per_2"
	if err := database.Create(ctx, handoff); err != nil {
		t.Fatal(err)
	}
	if err := database.BindMessage(ctx, handoff.ID, domain.MessageRef{ChatID: "oc_1", MessageID: "om_permission_2"}); err != nil {
		t.Fatal(err)
	}
	if err := database.CloseResolvedPermissions(ctx, "ses_1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimByMessage(ctx, "om_permission_2", "evt_3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed related permission claim error = %v", err)
	}
	handoff.ID = "hof_permission_3"
	handoff.PermissionID = "per_3"
	handoff.LastAssistantMessageID = "permission:per_3"
	if err := database.Create(ctx, handoff); err != nil {
		t.Fatal(err)
	}
	if err := database.BindMessage(ctx, handoff.ID, domain.MessageRef{ChatID: "oc_1", MessageID: "om_permission_3"}); err != nil {
		t.Fatal(err)
	}
	if err := database.ClosePermission(ctx, "per_3"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimByMessage(ctx, "om_permission_3", "evt_4"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("locally resolved permission claim error = %v", err)
	}
}
