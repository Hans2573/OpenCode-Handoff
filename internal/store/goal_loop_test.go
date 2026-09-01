package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
)

func TestSQLiteGoalLoopLifecycle(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "goal-loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC().Truncate(time.Millisecond)
	loop := domain.GoalLoop{
		ID: "goal_1", Name: "ship it", Goal: "ship it", ProjectID: "project_1",
		ProjectName: "project", Directory: "/work/project", AgentID: DefaultAgentID,
		AgentName: "OpenCode", ModelProviderID: "openai", ModelID: "gpt-test", ModelName: "GPT Test", ModelVariant: "high",
		PermissionApprovalMode: domain.GoalPermissionAllowAll, Status: domain.GoalLoopDraft, FailureLimit: 7,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateGoalLoop(ctx, loop); err != nil {
		t.Fatal(err)
	}
	loop.SessionID = "ses_1"
	loop.Status = domain.GoalLoopRunning
	loop.CycleCount = 2
	loop.RetryAt = now.Add(time.Minute)
	loop.UpdatedAt = now.Add(time.Second)
	if err := database.SaveGoalLoop(ctx, loop); err != nil {
		t.Fatal(err)
	}
	if err := database.AppendGoalLoopEvent(ctx, loop.ID, "started", "started"); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetGoalLoop(ctx, loop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SessionID != "ses_1" || stored.Status != domain.GoalLoopRunning || stored.CycleCount != 2 || stored.FailureLimit != 7 || stored.ModelProviderID != "openai" || stored.ModelID != "gpt-test" || stored.ModelVariant != "high" || stored.PermissionApprovalMode != domain.GoalPermissionAllowAll {
		t.Fatalf("stored loop = %+v", stored)
	}
	events, err := database.ListGoalLoopEvents(ctx, loop.ID, 10)
	if err != nil || len(events) != 1 || events[0].Message != "started" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if err := database.DeleteGoalLoop(ctx, loop.ID); err != nil {
		t.Fatal(err)
	}
	loops, err := database.ListGoalLoops(ctx)
	if err != nil || len(loops) != 0 {
		t.Fatalf("loops=%+v err=%v", loops, err)
	}
}
