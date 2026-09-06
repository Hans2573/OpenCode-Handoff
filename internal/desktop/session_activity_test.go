package desktop

import (
	"testing"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
)

func TestLastObservedOperationReportsRunningTool(t *testing.T) {
	started := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Millisecond)
	message := opencode.Message{}
	message.Info.ID = "msg_1"
	message.Info.Agent = "general"
	message.Info.Time.Created = started.Add(-time.Second).UnixMilli()
	part := opencode.Part{ID: "prt_1", Type: "tool", Tool: "bash"}
	part.State.Status = "running"
	part.State.Title = "npm test"
	part.State.Time.Start = started.UnixMilli()
	message.Parts = []opencode.Part{part}

	operation, ok := lastObservedOperation([]opencode.Message{message})
	if !ok || operation.typeName != "bash" || operation.summary != "npm test" || operation.status != "running" || operation.agent != "general" || !operation.startedAt.Equal(started) {
		t.Fatalf("operation = %+v, ok = %v", operation, ok)
	}
}

func TestRunningToolOutputChangesActivityFingerprint(t *testing.T) {
	message := opencode.Message{}
	message.Info.ID = "msg_1"
	part := opencode.Part{ID: "prt_1", Type: "tool", Tool: "bash"}
	part.State.Status = "running"
	part.State.Output = "first line"

	first, ok := operationFromPart(message, part)
	if !ok {
		t.Fatal("expected tool operation")
	}
	part.State.Output = "first line\nsecond line"
	second, ok := operationFromPart(message, part)
	if !ok {
		t.Fatal("expected updated tool operation")
	}
	if first.fingerprint == second.fingerprint {
		t.Fatal("tool output progress must change the activity fingerprint")
	}
}

func TestObserveDirectoryTreatsSubagentWaitingAsRootWaiting(t *testing.T) {
	root := opencode.Session{ID: "root"}
	child := opencode.Session{ID: "child", ParentID: "root"}
	group := []opencode.Session{root, child}
	waiting := map[string]struct{}{child.ID: {}}

	if !sessionGroupWaiting(group, waiting) {
		t.Fatal("subagent waiting state must apply to the root session")
	}
}

func TestActivityLevelUsesThresholdsAndWaitingState(t *testing.T) {
	now := time.Now().UTC()
	item := domain.SessionActivitySnapshot{SessionStatus: "busy", LastActivityAt: now.Add(-31 * time.Minute)}
	thresholds := [2]time.Duration{10 * time.Minute, 30 * time.Minute}
	if got := activityLevel(now, item, false, thresholds); got != domain.SessionActivityStalled {
		t.Fatalf("stalled level = %q", got)
	}
	if got := activityLevel(now, item, true, thresholds); got != domain.SessionActivityWaiting {
		t.Fatalf("waiting level = %q", got)
	}
	item.SnoozedUntil = now.Add(time.Minute)
	if got := activityLevel(now, item, false, thresholds); got != domain.SessionActivityNormal {
		t.Fatalf("snoozed level = %q", got)
	}
}
