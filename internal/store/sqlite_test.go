package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaohang2/opencode-handoff/internal/domain"
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
