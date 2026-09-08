package desktop

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/config"
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

func TestSystemInactivityNotificationRepeatsAndResets(t *testing.T) {
	cfg := config.Default()
	cfg.Activity.SystemNotificationInterval.Duration = time.Minute
	manager := &Manager{
		cfg:               cfg,
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		activityReminders: make(map[string]activityReminder),
	}
	var notifications []SystemNotification
	manager.SetSystemNotifier(func(notification SystemNotification) error {
		notifications = append(notifications, notification)
		return nil
	})
	session := opencode.Session{ID: "ses_1"}
	item := domain.SessionActivitySnapshot{
		SessionID: "ses_1", Directory: "/work", Fingerprint: "activity-1",
		SessionStatus: "busy", Level: domain.SessionActivitySuspected, LastActivityAt: time.Now().UTC().Add(-10 * time.Minute),
		OperationType: "bash", OperationSummary: "go test ./...",
	}

	manager.notifySessionInactivity(session, item)
	manager.notifySessionInactivity(session, item)
	if len(notifications) != 1 {
		t.Fatalf("immediate notifications = %d, want 1", len(notifications))
	}
	if notifications[0].SessionID != item.SessionID || notifications[0].Directory != item.Directory {
		t.Fatalf("notification target = %+v", notifications[0])
	}

	key := item.Directory + "\x00" + item.SessionID
	manager.activityReminders[key] = activityReminder{fingerprint: item.Fingerprint, sentAt: time.Now().UTC().Add(-time.Minute)}
	manager.notifySessionInactivity(session, item)
	if len(notifications) != 2 {
		t.Fatalf("repeated notifications = %d, want 2", len(notifications))
	}

	item.LastActivityAt = time.Now().UTC()
	manager.notifySessionInactivity(session, item)
	item.LastActivityAt = time.Now().UTC().Add(-10 * time.Minute)
	manager.notifySessionInactivity(session, item)
	if len(notifications) != 3 {
		t.Fatalf("notifications after reset = %d, want 3", len(notifications))
	}
}

func TestSystemInactivityNotificationUsesIndependentWaitThreshold(t *testing.T) {
	cfg := config.Default()
	cfg.Activity.SuspectedAfter.Duration = 30 * time.Minute
	cfg.Activity.StalledAfter.Duration = time.Hour
	cfg.Activity.SystemNotificationAfter.Duration = 5 * time.Minute
	manager := &Manager{
		cfg:               cfg,
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		activityReminders: make(map[string]activityReminder),
	}
	count := 0
	manager.SetSystemNotifier(func(SystemNotification) error { count++; return nil })
	item := domain.SessionActivitySnapshot{
		SessionID: "ses_1", Directory: "/work", Fingerprint: "activity-1",
		SessionStatus: "busy", Level: domain.SessionActivityNormal,
		LastActivityAt: time.Now().UTC().Add(-6 * time.Minute),
	}

	manager.notifySessionInactivity(opencode.Session{ID: item.SessionID}, item)
	if count != 1 {
		t.Fatalf("notifications = %d, want 1 before suspected threshold", count)
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
