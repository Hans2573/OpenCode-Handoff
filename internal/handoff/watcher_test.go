package handoff

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/xiaohang2/opencode-handoff/internal/opencode"
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
