package handoff

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
)

func TestWatcherEmitsOnlyOnObservedTransition(t *testing.T) {
	watcher := NewWatcher(&fakeAdapter{}, WatcherOptions{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher.updateStatus(ctx, "ses_1", "/work/a", "idle")
	select {
	case signal := <-watcher.signals:
		t.Fatalf("initial idle emitted signal: %+v", signal)
	default:
	}
	watcher.updateStatus(ctx, "ses_1", "/work/a", "busy")
	watcher.updateStatus(ctx, "ses_1", "/work/a", "idle")
	select {
	case signal := <-watcher.signals:
		if signal.SessionID != "ses_1" || signal.Directory != "/work/a" || signal.Kind != SignalStopped {
			t.Fatalf("signal = %+v", signal)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for transition signal")
	}
}

func TestWatcherPreservesErrorDirectory(t *testing.T) {
	watcher := NewWatcher(&fakeAdapter{}, WatcherOptions{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	properties, _ := json.Marshal(map[string]any{
		"sessionID": "ses_error",
		"error":     map[string]any{"data": map[string]any{"message": "timeout"}},
	})
	watcher.handleEvent(context.Background(), opencode.Event{
		Directory:  "/work/error",
		Type:       "session.error",
		Properties: properties,
	})
	signal := <-watcher.signals
	if signal.Directory != "/work/error" || signal.Error != "timeout" || signal.Kind != SignalError {
		t.Fatalf("signal = %+v", signal)
	}
}

func TestWatcherTreatsMissingBusyStatusAsIdle(t *testing.T) {
	watcher := NewWatcher(&fakeAdapter{}, WatcherOptions{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	watcher.updateStatus(ctx, "ses_busy", "/work/a", "busy")
	watcher.reconcileStatuses(ctx, "/work/a", map[string]opencode.SessionStatus{})

	select {
	case signal := <-watcher.signals:
		if signal.SessionID != "ses_busy" || signal.Kind != SignalStopped {
			t.Fatalf("signal = %+v", signal)
		}
	case <-time.After(time.Second):
		t.Fatal("missing busy status did not emit stopped signal")
	}
}

func TestWatcherEmitsQuestionOnceAcrossSSEAndPolling(t *testing.T) {
	watcher := NewWatcher(&fakeAdapter{}, WatcherOptions{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	question := opencode.QuestionRequest{ID: "que_1", SessionID: "ses_1"}
	question.Questions = []opencode.QuestionInfo{{Question: "Choose"}}
	properties, _ := json.Marshal(question)
	watcher.handleEvent(context.Background(), opencode.Event{
		Directory: "/work/a", Type: "question.asked", Properties: properties,
	})
	watcher.observeQuestion(context.Background(), "/work/a", question)

	select {
	case signal := <-watcher.signals:
		if signal.Kind != SignalQuestion || signal.Question.ID != "que_1" || signal.Directory != "/work/a" {
			t.Fatalf("signal = %+v", signal)
		}
	case <-time.After(time.Second):
		t.Fatal("question signal was not emitted")
	}
	select {
	case duplicate := <-watcher.signals:
		t.Fatalf("duplicate question signal = %+v", duplicate)
	default:
	}
}

func TestWatcherEmitsPermissionOnceAcrossSSEAndPolling(t *testing.T) {
	watcher := NewWatcher(&fakeAdapter{}, WatcherOptions{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	permission := opencode.PermissionRequest{
		ID: "per_1", SessionID: "ses_1", Permission: "external_directory",
		Patterns: []string{`C:\Users\test\Desktop\*`},
	}
	properties, _ := json.Marshal(permission)
	watcher.handleEvent(context.Background(), opencode.Event{
		Directory: "/work/a", Type: "permission.asked", Properties: properties,
	})
	watcher.observePermission(context.Background(), "/work/a", permission)

	select {
	case signal := <-watcher.signals:
		if signal.Kind != SignalPermission || signal.Permission.ID != "per_1" || signal.Directory != "/work/a" {
			t.Fatalf("signal = %+v", signal)
		}
	case <-time.After(time.Second):
		t.Fatal("permission signal was not emitted")
	}
	select {
	case duplicate := <-watcher.signals:
		t.Fatalf("duplicate permission signal = %+v", duplicate)
	default:
	}
}

func TestWatcherEmitsPermissionResolved(t *testing.T) {
	watcher := NewWatcher(&fakeAdapter{}, WatcherOptions{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	properties, _ := json.Marshal(map[string]any{
		"sessionID": "ses_1", "requestID": "per_1", "reply": "once",
	})
	watcher.handleEvent(context.Background(), opencode.Event{
		Directory: "/work/a", Type: "permission.replied", Properties: properties,
	})
	select {
	case signal := <-watcher.signals:
		if signal.Kind != SignalPermissionResolved || signal.PermissionID != "per_1" || signal.SessionID != "ses_1" {
			t.Fatalf("signal = %+v", signal)
		}
	case <-time.After(time.Second):
		t.Fatal("permission resolved signal was not emitted")
	}
}
