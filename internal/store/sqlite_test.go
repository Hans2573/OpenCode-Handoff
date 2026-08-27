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
