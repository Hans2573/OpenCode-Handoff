package handoff

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/opencode"
	"github.com/Hans2573/OpenCode-Handoff/internal/store"
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
		{SessionID: "ses_child", Directory: "/work/project", Kind: SignalQuestion, Question: testQuestion("que_child", "ses_child")},
		{SessionID: "ses_child", Directory: "/work/project", Kind: SignalPermission, Permission: testPermission("per_child", "ses_child")},
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

func TestEngineRoutesPermissionDecisionWithoutSendingPrompt(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	adapter := &fakeAdapter{session: opencode.Session{ID: "ses_1", Directory: "/work/project", Title: "inspect preview"}}
	channel := &fakeChannel{}
	engine := NewEngine(adapter, channel, database, EngineOptions{
		MaxOutputChars: 3000, NotifyPermission: true, AllowedUsers: []string{"ou_allowed"}, ChatID: "oc_allowed",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	permission := testPermission("per_1", "ses_1")
	if err := engine.handleSignal(ctx, Signal{SessionID: "ses_1", Directory: "/work/project", Kind: SignalPermission, Permission: permission}); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Type != domain.HandoffPermission || channel.sent[0].PermissionID != "per_1" {
		t.Fatalf("permission handoff = %+v", channel.sent)
	}
	if err := engine.handleReply(ctx, domain.UserReply{
		MessageID: "evt_permission", ParentMessageID: "om_handoff", ChatID: "oc_allowed",
		SenderID: "ou_allowed", PermissionReply: "once", CardAction: true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(adapter.permissionReplies) != 1 || adapter.permissionReplies[0].Decision != opencode.PermissionOnce || adapter.permissionReplies[0].ID != "per_1" {
		t.Fatalf("permission replies = %+v", adapter.permissionReplies)
	}
	if len(channel.notices) != 1 || channel.notices[0] != "已允许本次操作，原 OpenCode Session 正在继续。" {
		t.Fatalf("permission confirmation = %v", channel.notices)
	}
	if len(channel.replyIDs) != 1 || channel.replyIDs[0] != "om_handoff" {
		t.Fatalf("permission confirmation target = %v", channel.replyIDs)
	}
	if len(adapter.prompts) != 0 {
		t.Fatalf("permission decision sent as prompt: %v", adapter.prompts)
	}
	if err := engine.handleReply(ctx, domain.UserReply{
		MessageID: "evt_permission_again", ParentMessageID: "om_handoff", ChatID: "oc_allowed",
		SenderID: "ou_allowed", PermissionReply: "always", CardAction: true,
	}); err == nil || !strings.Contains(err.Error(), "已处理") {
		t.Fatalf("second permission decision error = %v", err)
	}
}

func TestEngineReportsRemainingPermissionRequests(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	first := testPermission("per_1", "ses_1")
	second := testPermission("per_2", "ses_1")
	second.Metadata = map[string]any{"filepath": `/work/AGENTS.md`}
	adapter := &fakeAdapter{
		session:     opencode.Session{ID: "ses_1", Directory: "/work/project", Title: "inspect preview"},
		permissions: []opencode.PermissionRequest{first, second},
	}
	channel := &fakeChannel{}
	engine := NewEngine(adapter, channel, database, EngineOptions{
		MaxOutputChars: 3000, NotifyPermission: true, AllowedUsers: []string{"ou_allowed"}, ChatID: "oc_allowed",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := engine.handleSignal(ctx, Signal{SessionID: "ses_1", Directory: "/work/project", Kind: SignalPermission, Permission: first}); err != nil {
		t.Fatal(err)
	}
	if err := engine.handleReply(ctx, domain.UserReply{
		MessageID: "evt_permission", ParentMessageID: "om_handoff", ChatID: "oc_allowed",
		SenderID: "ou_allowed", PermissionReply: "once", CardAction: true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(channel.notices) != 1 || !strings.Contains(channel.notices[0], "仍有 1 条权限请求等待处理") {
		t.Fatalf("permission confirmation = %v", channel.notices)
	}
}

func TestEngineListsProjectsCreatesSessionAndRoutesFirstPrompt(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	adapter := &fakeAdapter{projects: []opencode.Project{
		{ID: "global", Worktree: `/`},
		{ID: "project_1", Worktree: `/work/project`, Name: "Project One"},
	}}
	channel := &fakeChannel{}
	engine := newTestEngine(adapter, channel, database, true, true)

	if err := engine.handleReply(ctx, domain.UserReply{
		MessageID: "om_project", ChatID: "oc_allowed", SenderID: "ou_allowed", ListProjects: true, ProjectPage: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if len(channel.projectPages) != 1 || channel.projectPages[0].Total != 1 || channel.projectPages[0].Projects[0].Name != "Project One" {
		t.Fatalf("project pages = %+v", channel.projectPages)
	}

	create := domain.UserReply{
		MessageID: "evt_create", ParentMessageID: "om_project_card", ChatID: "oc_allowed", SenderID: "ou_allowed",
		CreateSession: true, ProjectDirectory: `/work/project`, CardAction: true,
	}
	if err := engine.handleReply(ctx, create); err != nil {
		t.Fatal(err)
	}
	if len(adapter.createdSessions) != 1 || !sameDirectory(adapter.createdSessions[0].Directory, `/work/project`) {
		t.Fatalf("created sessions = %+v", adapter.createdSessions)
	}
	if len(channel.sent) != 1 || channel.sent[0].Type != domain.HandoffSession || channel.sent[0].SessionID != "ses_created_1" {
		t.Fatalf("created session handoff = %+v", channel.sent)
	}
	if err := engine.handleReply(ctx, create); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("duplicate create error = %v", err)
	}
	if len(adapter.createdSessions) != 1 {
		t.Fatalf("duplicate callback created %d sessions", len(adapter.createdSessions))
	}

	if err := engine.handleReply(ctx, domain.UserReply{
		MessageID: "om_first_prompt", ParentMessageID: "om_handoff", ChatID: "oc_allowed", SenderID: "ou_allowed", Text: "开始分析项目",
	}); err != nil {
		t.Fatal(err)
	}
	if len(adapter.prompts) != 1 || adapter.prompts[0].SessionID != "ses_created_1" || adapter.prompts[0].Text != "开始分析项目" {
		t.Fatalf("created session prompts = %+v", adapter.prompts)
	}
}

func TestEngineRejectsSessionCreationOutsideListedProjects(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	adapter := &fakeAdapter{projects: []opencode.Project{{ID: "project_1", Worktree: `/work/project`}}}
	engine := newTestEngine(adapter, &fakeChannel{}, database, true, true)
	err = engine.handleReply(ctx, domain.UserReply{
		MessageID: "evt_create", ChatID: "oc_allowed", SenderID: "ou_allowed",
		CreateSession: true, ProjectDirectory: `/work/not-listed`, CardAction: true,
	})
	if err == nil || !strings.Contains(err.Error(), "不允许") {
		t.Fatalf("unlisted project error = %v", err)
	}
	if len(adapter.createdSessions) != 0 {
		t.Fatalf("created sessions = %+v", adapter.createdSessions)
	}
}

func TestEngineAbortsSessionFromQuotedHandoff(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	adapter := &fakeAdapter{
		session:  opencode.Session{ID: "ses_1", Directory: "/work/project"},
		messages: []opencode.Message{{Info: opencode.MessageInfo{ID: "msg_1", Role: "assistant"}, Parts: []opencode.Part{{Type: "text", Text: "running"}}}},
	}
	channel := &fakeChannel{}
	engine := newTestEngine(adapter, channel, database, true, true)
	if err := engine.handleSignal(ctx, Signal{SessionID: "ses_1", Directory: "/work/project", Kind: SignalStopped}); err != nil {
		t.Fatal(err)
	}
	if err := engine.handleReply(ctx, domain.UserReply{
		MessageID: "om_abort", ParentMessageID: "om_handoff", ChatID: "oc_allowed",
		SenderID: "ou_allowed", Text: "/stop", AbortSession: true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(adapter.aborts) != 1 || adapter.aborts[0].ID != "ses_1" || adapter.aborts[0].Directory != "/work/project" {
		t.Fatalf("aborts = %+v", adapter.aborts)
	}
	if len(channel.notices) != 1 || !strings.Contains(channel.notices[0], "已请求中断") {
		t.Fatalf("abort confirmation = %v", channel.notices)
	}
}

func TestEngineRequiresQuotedHandoffForAbort(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	adapter := &fakeAdapter{}
	channel := &fakeChannel{}
	engine := newTestEngine(adapter, channel, database, true, true)
	if err := engine.handleReply(ctx, domain.UserReply{MessageID: "om_abort", ChatID: "oc_allowed", SenderID: "ou_allowed", Text: "/stop", AbortSession: true}); err != nil {
		t.Fatal(err)
	}
	if len(adapter.aborts) != 0 || len(channel.notices) != 1 || !strings.Contains(channel.notices[0], "引用对应") {
		t.Fatalf("abort without quote: aborts=%v notices=%v", adapter.aborts, channel.notices)
	}
}

func TestParsePermissionReply(t *testing.T) {
	for input, expected := range map[string]opencode.PermissionReply{
		"允许一次":   opencode.PermissionOnce,
		"2":      opencode.PermissionAlways,
		"reject": opencode.PermissionReject,
	} {
		if actual := parsePermissionReply(input); actual != expected {
			t.Fatalf("parsePermissionReply(%q) = %q, want %q", input, actual, expected)
		}
	}
	if actual := parsePermissionReply("继续"); actual != "" {
		t.Fatalf("invalid permission reply = %q", actual)
	}
}

func TestEngineRoutesQuestionAnswerWithoutSendingPrompt(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	adapter := &fakeAdapter{session: opencode.Session{ID: "ses_1", Directory: "/work/project", Title: "choose next"}}
	channel := &fakeChannel{}
	engine := NewEngine(adapter, channel, database, EngineOptions{
		MaxOutputChars: 3000, NotifyQuestion: true, AllowedUsers: []string{"ou_allowed"}, ChatID: "oc_allowed",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	question := testQuestion("que_1", "ses_1")
	if err := engine.handleSignal(ctx, Signal{SessionID: "ses_1", Directory: "/work/project", Kind: SignalQuestion, Question: question}); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Type != domain.HandoffQuestion || channel.sent[0].QuestionID != "que_1" {
		t.Fatalf("question handoff = %+v", channel.sent)
	}
	if err := engine.handleReply(ctx, domain.UserReply{
		MessageID: "om_answer", ParentMessageID: "om_handoff", ChatID: "oc_allowed",
		SenderID: "ou_allowed", Text: "2",
	}); err != nil {
		t.Fatal(err)
	}
	if len(adapter.questionReplies) != 1 || adapter.questionReplies[0][0][0] != "Review MR" {
		t.Fatalf("question replies = %v", adapter.questionReplies)
	}
	if len(channel.notices) != 1 || channel.notices[0] != "答案已提交到原 OpenCode Session，任务正在继续。" {
		t.Fatalf("question confirmation = %v", channel.notices)
	}
	if len(adapter.prompts) != 0 {
		t.Fatalf("question answer sent as prompt: %v", adapter.prompts)
	}
	if err := engine.handleReply(ctx, domain.UserReply{
		MessageID: "om_answer_again", ParentMessageID: "om_handoff", ChatID: "oc_allowed",
		SenderID: "ou_allowed", Text: "1",
	}); err != nil {
		t.Fatal(err)
	}
	if len(adapter.questionReplies) != 1 {
		t.Fatalf("question was answered twice: %v", adapter.questionReplies)
	}
}

func TestParseQuestionAnswersSupportsMultiSelectAndCustomText(t *testing.T) {
	questions := []domain.Question{
		{Options: []domain.QuestionOption{{Label: "A"}, {Label: "B"}, {Label: "C"}}, Multiple: true},
		{Custom: true},
	}
	answers, err := parseQuestionAnswers(questions, "1，3\nwrite tests first")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(answers) != "[[A C] [write tests first]]" {
		t.Fatalf("answers = %v", answers)
	}
}

func TestParseQuestionAnswersTreatsLegacyMissingCustomAsAllowed(t *testing.T) {
	answers, err := parseQuestionAnswers([]domain.Question{{}}, "我要第四个普通选项")
	if err != nil || len(answers) != 1 || answers[0][0] != "我要第四个普通选项" {
		t.Fatalf("legacy custom answer = %v, %v", answers, err)
	}
	if _, err := parseQuestionAnswers([]domain.Question{{CustomSet: true, Custom: false}}, "not an option"); err == nil {
		t.Fatal("explicit custom=false accepted an unknown answer")
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
		MaxOutputChars:   3000,
		NotifyIdle:       notifyIdle,
		NotifyError:      notifyError,
		NotifyQuestion:   true,
		NotifyPermission: true,
		AllowedUsers:     []string{"ou_allowed"},
		ChatID:           "oc_allowed",
	}, logger)
}

func testQuestion(id, sessionID string) opencode.QuestionRequest {
	question := opencode.QuestionRequest{ID: id, SessionID: sessionID}
	question.Questions = []opencode.QuestionInfo{{
		Question: "What next?", Header: "Choose",
		Options: []opencode.QuestionOption{{Label: "Analyze PRD"}, {Label: "Review MR"}},
	}}
	return question
}

func testPermission(id, sessionID string) opencode.PermissionRequest {
	return opencode.PermissionRequest{
		ID: id, SessionID: sessionID, Permission: "external_directory",
		Patterns: []string{`C:\Users\test\Desktop\preview.html`},
		Always:   []string{`C:\Users\test\Desktop\*`},
	}
}

type promptCall struct {
	SessionID string
	Directory string
	Text      string
}

type permissionReplyCall struct {
	ID        string
	Directory string
	Decision  opencode.PermissionReply
}

type abortCall struct {
	ID        string
	Directory string
}

type fakeAdapter struct {
	session           opencode.Session
	projects          []opencode.Project
	createdSessions   []opencode.Session
	messages          []opencode.Message
	prompts           []promptCall
	messageCalls      int
	questions         []opencode.QuestionRequest
	questionReplies   [][][]string
	rejectedQuestions []string
	permissions       []opencode.PermissionRequest
	permissionReplies []permissionReplyCall
	aborts            []abortCall
}

func (f *fakeAdapter) ListProjects(context.Context) ([]opencode.Project, error) {
	if f.projects != nil {
		return f.projects, nil
	}
	return []opencode.Project{{ID: "project", Worktree: f.session.Directory}}, nil
}

func (f *fakeAdapter) ListDirectories(context.Context) ([]string, error) {
	return []string{f.session.Directory}, nil
}

func (f *fakeAdapter) ListSessions(context.Context, string) ([]opencode.Session, error) {
	return []opencode.Session{f.session}, nil
}

func (f *fakeAdapter) CreateSession(_ context.Context, directory, title string) (opencode.Session, error) {
	session := opencode.Session{ID: fmt.Sprintf("ses_created_%d", len(f.createdSessions)+1), Directory: directory, Title: title}
	f.createdSessions = append(f.createdSessions, session)
	return session, nil
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

func (f *fakeAdapter) AbortSession(_ context.Context, sessionID, directory string) error {
	f.aborts = append(f.aborts, abortCall{ID: sessionID, Directory: directory})
	return nil
}

func (f *fakeAdapter) ListQuestions(context.Context, string) ([]opencode.QuestionRequest, error) {
	return f.questions, nil
}

func (f *fakeAdapter) ReplyQuestion(_ context.Context, _ string, _ string, answers [][]string) error {
	f.questionReplies = append(f.questionReplies, answers)
	return nil
}

func (f *fakeAdapter) RejectQuestion(_ context.Context, requestID, _ string) error {
	f.rejectedQuestions = append(f.rejectedQuestions, requestID)
	return nil
}

func (f *fakeAdapter) ListPermissions(context.Context, string) ([]opencode.PermissionRequest, error) {
	return f.permissions, nil
}

func (f *fakeAdapter) ReplyPermission(_ context.Context, requestID, directory string, decision opencode.PermissionReply) error {
	f.permissionReplies = append(f.permissionReplies, permissionReplyCall{ID: requestID, Directory: directory, Decision: decision})
	remaining := f.permissions[:0]
	for _, permission := range f.permissions {
		if permission.ID != requestID {
			remaining = append(remaining, permission)
		}
	}
	f.permissions = remaining
	return nil
}

func (f *fakeAdapter) WatchEvents(context.Context, func(opencode.Event)) error {
	return context.Canceled
}

type fakeChannel struct {
	sent         []domain.Handoff
	replies      chan domain.UserReply
	notices      []string
	replyIDs     []string
	chatID       string
	projectPages []domain.ProjectPage
}

func (f *fakeChannel) SendHandoff(_ context.Context, handoff domain.Handoff) (domain.MessageRef, error) {
	f.sent = append(f.sent, handoff)
	chatID := f.chatID
	if chatID == "" {
		chatID = "oc_allowed"
	}
	return domain.MessageRef{ChatID: chatID, MessageID: "om_handoff"}, nil
}

func (f *fakeChannel) Reply(_ context.Context, messageID, text string) error {
	f.replyIDs = append(f.replyIDs, messageID)
	f.notices = append(f.notices, text)
	return nil
}

func (f *fakeChannel) ReplyProjects(_ context.Context, _ string, page domain.ProjectPage) error {
	f.projectPages = append(f.projectPages, page)
	return nil
}

func (f *fakeChannel) Receive(context.Context) (<-chan domain.UserReply, error) {
	if f.replies == nil {
		f.replies = make(chan domain.UserReply)
	}
	return f.replies, nil
}
