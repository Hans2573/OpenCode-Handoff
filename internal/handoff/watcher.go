package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/xiaohang2/opencode-handoff/internal/opencode"
)

type SignalKind string

const (
	SignalStopped  SignalKind = "stopped"
	SignalError    SignalKind = "error"
	SignalQuestion SignalKind = "question"
)

type Signal struct {
	SessionID string
	Directory string
	Kind      SignalKind
	Error     string
	Question  opencode.QuestionRequest
}

type WatcherOptions struct {
	SSE             bool
	PollingFallback bool
	PollingInterval time.Duration
	NotifyQuestions bool
}

type Watcher struct {
	client    opencode.Adapter
	options   WatcherOptions
	logger    *slog.Logger
	signals   chan Signal
	mu        sync.Mutex
	statuses  map[string]observedStatus
	questions map[string]struct{}
}

type observedStatus struct {
	Type      string
	Directory string
}

func NewWatcher(client opencode.Adapter, options WatcherOptions, logger *slog.Logger) *Watcher {
	return &Watcher{
		client:    client,
		options:   options,
		logger:    logger,
		signals:   make(chan Signal, 128),
		statuses:  make(map[string]observedStatus),
		questions: make(map[string]struct{}),
	}
}

func (w *Watcher) Run(ctx context.Context) <-chan Signal {
	var wg sync.WaitGroup
	if w.options.PollingFallback {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.runPolling(ctx)
		}()
	}
	if w.options.SSE {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.runSSE(ctx)
		}()
	}
	go func() {
		wg.Wait()
		close(w.signals)
	}()
	return w.signals
}

func (w *Watcher) runPolling(ctx context.Context) {
	w.poll(ctx)
	ticker := time.NewTicker(w.options.PollingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

func (w *Watcher) poll(ctx context.Context) {
	directories, err := w.client.ListDirectories(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Warn("list OpenCode project directories", "error", err)
		}
		return
	}
	for _, directory := range directories {
		statuses, err := w.client.GetSessionStatuses(ctx, directory)
		if err != nil {
			if ctx.Err() == nil {
				w.logger.Warn("poll OpenCode session statuses", "directory", directory, "error", err)
			}
		} else {
			w.reconcileStatuses(ctx, directory, statuses)
		}
		if w.options.NotifyQuestions {
			questions, err := w.client.ListQuestions(ctx, directory)
			if err != nil {
				if ctx.Err() == nil {
					w.logger.Warn("poll OpenCode questions", "directory", directory, "error", err)
				}
				continue
			}
			for _, question := range questions {
				w.observeQuestion(ctx, directory, question)
			}
		}
	}
}

func (w *Watcher) reconcileStatuses(ctx context.Context, directory string, current map[string]opencode.SessionStatus) {
	w.mu.Lock()
	var stopped []string
	for sessionID, status := range current {
		previous, known := w.statuses[sessionID]
		w.statuses[sessionID] = observedStatus{Type: status.Type, Directory: directory}
		if known && previous.Type != "idle" && status.Type == "idle" {
			stopped = append(stopped, sessionID)
		}
	}
	for sessionID, previous := range w.statuses {
		if previous.Directory != directory || previous.Type == "idle" {
			continue
		}
		if _, exists := current[sessionID]; !exists {
			stopped = append(stopped, sessionID)
			w.statuses[sessionID] = observedStatus{Type: "idle", Directory: directory}
		}
	}
	w.mu.Unlock()
	for _, sessionID := range stopped {
		w.emit(ctx, Signal{SessionID: sessionID, Directory: directory, Kind: SignalStopped})
	}
}

func (w *Watcher) runSSE(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := w.client.WatchEvents(ctx, func(event opencode.Event) {
			w.handleEvent(ctx, event)
		})
		if ctx.Err() != nil {
			return
		}
		if !errors.Is(err, io.EOF) {
			w.logger.Warn("OpenCode event stream disconnected", "error", err, "retry_in", backoff)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (w *Watcher) handleEvent(ctx context.Context, event opencode.Event) {
	switch event.Type {
	case "session.status":
		var statusEvent opencode.StatusEvent
		if json.Unmarshal(event.Properties, &statusEvent) == nil {
			w.updateStatus(ctx, statusEvent.SessionID, event.Directory, statusEvent.Status.Type)
		}
	case "session.idle":
		var sessionEvent opencode.SessionEvent
		if json.Unmarshal(event.Properties, &sessionEvent) == nil && sessionEvent.SessionID != "" {
			w.updateStatus(ctx, sessionEvent.SessionID, event.Directory, "idle")
		}
	case "session.error":
		var sessionEvent opencode.SessionEvent
		if json.Unmarshal(event.Properties, &sessionEvent) == nil && sessionEvent.SessionID != "" {
			w.emit(ctx, Signal{
				SessionID: sessionEvent.SessionID,
				Directory: event.Directory,
				Kind:      SignalError,
				Error:     opencode.ErrorSummary(sessionEvent.Error),
			})
		}
	case "question.asked":
		var question opencode.QuestionRequest
		if json.Unmarshal(event.Properties, &question) == nil {
			w.observeQuestion(ctx, event.Directory, question)
		}
	}
}

func (w *Watcher) observeQuestion(ctx context.Context, directory string, question opencode.QuestionRequest) {
	if question.ID == "" || question.SessionID == "" {
		return
	}
	w.mu.Lock()
	_, known := w.questions[question.ID]
	if !known {
		w.questions[question.ID] = struct{}{}
	}
	w.mu.Unlock()
	if !known {
		w.emit(ctx, Signal{SessionID: question.SessionID, Directory: directory, Kind: SignalQuestion, Question: question})
	}
}

func (w *Watcher) updateStatus(ctx context.Context, sessionID, directory, current string) {
	if sessionID == "" || current == "" {
		return
	}
	w.mu.Lock()
	previous, known := w.statuses[sessionID]
	w.statuses[sessionID] = observedStatus{Type: current, Directory: directory}
	w.mu.Unlock()

	if known && previous.Type != "idle" && current == "idle" {
		w.emit(ctx, Signal{SessionID: sessionID, Directory: directory, Kind: SignalStopped})
	}
}

func (w *Watcher) emit(ctx context.Context, signal Signal) {
	select {
	case <-ctx.Done():
	case w.signals <- signal:
	}
}
