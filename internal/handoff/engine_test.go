package handoff

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaohang2/opencode-handoff/internal/domain"
	"github.com/xiaohang2/opencode-handoff/internal/opencode"
	"github.com/xiaohang2/opencode-handoff/internal/store"
)

func TestEngineSendsOnceAndRoutesAuthorizedReply(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	adapter := &fakeAdapter{
		session: opencode.Session{ID: "ses_1", Directory: "/work/project", Title: "session"},
		messages: []opencode.Message{{
			Info:  opencode.MessageInfo{ID: "msg_1", Role: "assistant"},
			Parts: []opencode.Part{{Type: "text", Text: "finished"}},
		}},
	}
	channel := &fakeChannel{}
	engine := newTestEngine(adapter, channel, database, true, true)
	signal := Signal{SessionID: "ses_1", Directory: "/work/project", Kind: SignalStopped}

	if err := engine.handleSignal(ctx, signal); err != nil {
		t.Fatal(err)
	}
	if err := engine.handleSignal(ctx, signal); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("sent handoffs = %d, want 1", len(channel.sent))
	}
	if channel.sent[0].ProjectName != "project" || channel.sent[0].SessionName != "session" || channel.sent[0].LastAssistantText != "finished" {
		t.Fatalf("handoff = %+v", channel.sent[0])
	}

	reply := domain.UserReply{
		MessageID:       "om_reply",
		ParentMessageID: "om_handoff",
		ChatID:          "oc_allowed",
		SenderID:        "on_other_identifier",
		SenderIDs:       []string{"ou_allowed"},
		Text:            "continue",
	}
	if err := engine.handleReply(ctx, reply); err != nil {
		t.Fatal(err)
	}
	if len(adapter.prompts) != 1 || adapter.prompts[0].Text != "continue" || adapter.prompts[0].Directory != "/work/project" {
		t.Fatalf("prompts = %+v", adapter.prompts)
	}
	if err := engine.handleReply(ctx, reply); err != nil {
		t.Fatal(err)
	}
	if len(adapter.prompts) != 1 {
		t.Fatalf("duplicate reply injected %d prompts", len(adapter.prompts))
	}
	reply.MessageID = "om_reply_later"
	reply.Text = "one more task"
	if err := engine.handleReply(ctx, reply); err != nil {
		t.Fatal(err)
	}
	if len(adapter.prompts) != 2 || adapter.prompts[1].Text != "one more task" {
		t.Fatalf("historical handoff prompts = %+v", adapter.prompts)
	}
}

func TestEngineIgnoresSubagentSession(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	adapter := &fakeAdapter{
		session: opencode.Session{ID: "ses_child", ParentID: "ses_parent", Directory: "/work/project"},
		messages: []opencode.Message{{
			Info:  opencode.MessageInfo{ID: "msg_child", Role: "assistant"},
			Parts: []opencode.Part{{Type: "text", Text: "child finished"}},
		}},
	}
	channel := &fakeChannel{}
	engine := newTestEngine(adapter, channel, database, true, true)

	for _, signal := range []Signal{
		{SessionID: "ses_child", Directory: "/work/project", Kind: SignalError, Error: "child interrupted"},
		{SessionID: "ses_child", Directory: "/work/project", Kind: SignalStopped},
	} {
		if err := engine.handleSignal(ctx, signal); err != nil {
			t.Fatal(err)
		}
	}
	if len(channel.sent) != 0 {
		t.Fatalf("subagent generated %d notifications", len(channel.sent))
	}
	if adapter.messageCalls != 0 {
		t.Fatalf("subagent messages were fetched %d times", adapter.messageCalls)
	}
}

func TestEngineDetectsMessageErrorWhenIdleNotificationsDisabled(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	adapter := &fakeAdapter{
		session: opencode.Session{ID: "ses_1", Directory: "/work/project"},
		messages: []opencode.Message{{
			Info: opencode.MessageInfo{ID: "msg_error", Role: "assistant", Error: []byte(`{"data":{"message":"timeout"}}`)},
		}},
	}
	channel := &fakeChannel{}
	engine := newTestEngine(adapter, channel, database, false, true)
	if err := engine.handleSignal(ctx, Signal{SessionID: "ses_1", Directory: "/work/project", Kind: SignalStopped}); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Type != domain.HandoffError {
		t.Fatalf("sent handoffs = %+v", channel.sent)
	}
}

func TestEngineAuthorizesPersistedPairingWithoutManualAllowlist(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.BindChannel(ctx, domain.ChannelBinding{ChatID: "oc_paired", UserIDs: []string{"ou_owner"}}); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{
		session: opencode.Session{ID: "ses_1", Directory: "/work/project"},
		messages: []opencode.Message{{
			Info:  opencode.MessageInfo{ID: "msg_1", Role: "assistant"},
			Parts: []opencode.Part{{Type: "text", Text: "finished"}},
		}},
	}
	channel := &fakeChannel{chatID: "oc_paired"}
	engine := NewEngine(adapter, channel, database, EngineOptions{
		MaxOutputChars: 3000,
		NotifyIdle:     true,
		NotifyError:    true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := engine.handleSignal(ctx, Signal{SessionID: "ses_1", Directory: "/work/project", Kind: SignalStopped}); err != nil {
		t.Fatal(err)
	}
	if err := engine.handleReply(ctx, domain.UserReply{
		MessageID: "om_reply",
		ChatID:    "oc_paired",
		SenderID:  "ou_owner",
		Text:      "continue",
	}); err != nil {
		t.Fatal(err)
	}
	if len(adapter.prompts) != 1 || len(channel.notices) != 1 {
		t.Fatalf("prompts=%v notices=%v", adapter.prompts, channel.notices)
	}
}

func TestEngineExplainsAmbiguousUnquotedReply(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.BindChannel(ctx, domain.ChannelBinding{ChatID: "oc_paired", UserIDs: []string{"ou_owner"}}); err != nil {
		t.Fatal(err)
	}
	for index, sessionID := range []string{"ses_1", "ses_2"} {
		handoff := domain.Handoff{
			ID:                     fmt.Sprintf("hof_%d", index),
			SessionID:              sessionID,
			Directory:              "/work/project",
			ProjectName:            "project",
			Type:                   domain.HandoffFinished,
			LastAssistantMessageID: fmt.Sprintf("msg_%d", index),
			CreatedAt:              time.Now().Add(time.Duration(index) * time.Second),
		}
		if err := database.Create(ctx, handoff); err != nil {
			t.Fatal(err)
		}
		if err := database.BindMessage(ctx, handoff.ID, domain.MessageRef{ChatID: "oc_paired", MessageID: fmt.Sprintf("om_%d", index)}); err != nil {
			t.Fatal(err)
		}
	}
	adapter := &fakeAdapter{}
	channel := &fakeChannel{}
	engine := NewEngine(adapter, channel, database, EngineOptions{MaxOutputChars: 3000}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := engine.handleReply(ctx, domain.UserReply{
		MessageID: "om_direct",
		ChatID:    "oc_paired",
		SenderID:  "ou_owner",
		Text:      "continue",
	}); err != nil {
		t.Fatal(err)
	}
	if len(adapter.prompts) != 0 || len(channel.notices) != 1 {
		t.Fatalf("prompts=%v notices=%v", adapter.prompts, channel.notices)
	}
}

func newTestEngine(adapter opencode.Adapter, channel *fakeChannel, database store.Store, notifyIdle, notifyError bool) *Engine {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewEngine(adapter, channel, database, EngineOptions{
		MaxOutputChars: 3000,
		NotifyIdle:     notifyIdle,
		NotifyError:    notifyError,
		AllowedUsers:   []string{"ou_allowed"},
		ChatID:         "oc_allowed",
	}, logger)
}

type promptCall struct {
	SessionID string
	Directory string
	Text      string
}

type fakeAdapter struct {
	session      opencode.Session
	messages     []opencode.Message
	prompts      []promptCall
	messageCalls int
}

func (f *fakeAdapter) ListDirectories(context.Context) ([]string, error) {
	return []string{f.session.Directory}, nil
}

func (f *fakeAdapter) ListSessions(context.Context, string) ([]opencode.Session, error) {
	return []opencode.Session{f.session}, nil
}

func (f *fakeAdapter) GetSession(context.Context, string, string) (opencode.Session, error) {
	return f.session, nil
}

func (f *fakeAdapter) GetSessionStatuses(context.Context, string) (map[string]opencode.SessionStatus, error) {
	return nil, nil
}

func (f *fakeAdapter) GetMessages(context.Context, string, string, int) ([]opencode.Message, error) {
	f.messageCalls++
	return f.messages, nil
}

func (f *fakeAdapter) SendPrompt(_ context.Context, sessionID, directory, text string) error {
	f.prompts = append(f.prompts, promptCall{SessionID: sessionID, Directory: directory, Text: text})
	return nil
}

func (f *fakeAdapter) WatchEvents(context.Context, func(opencode.Event)) error {
	return context.Canceled
}

type fakeChannel struct {
	sent    []domain.Handoff
	replies chan domain.UserReply
	notices []string
	chatID  string
}

func (f *fakeChannel) SendHandoff(_ context.Context, handoff domain.Handoff) (domain.MessageRef, error) {
	f.sent = append(f.sent, handoff)
	chatID := f.chatID
	if chatID == "" {
		chatID = "oc_allowed"
	}
	return domain.MessageRef{ChatID: chatID, MessageID: "om_handoff"}, nil
}

func (f *fakeChannel) Reply(_ context.Context, _ string, text string) error {
	f.notices = append(f.notices, text)
	return nil
}

func (f *fakeChannel) Receive(context.Context) (<-chan domain.UserReply, error) {
	if f.replies == nil {
		f.replies = make(chan domain.UserReply)
	}
	return f.replies, nil
}
