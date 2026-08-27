package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaohang2/opencode-handoff/internal/domain"
)

func TestClaimOnlyOpenByChat(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	for index, messageID := range []string{"msg_old", "msg_new"} {
		handoff := domain.Handoff{
			ID:                     fmt.Sprintf("hof_%d", index),
			SessionID:              "ses_1",
			Directory:              "/work/a",
			ProjectName:            "a",
			Type:                   domain.HandoffFinished,
			LastAssistantMessageID: messageID,
			CreatedAt:              time.Now().Add(time.Duration(index) * time.Second),
		}
		if err := database.Create(ctx, handoff); err != nil {
			t.Fatal(err)
		}
		if err := database.BindMessage(ctx, handoff.ID, domain.MessageRef{ChatID: "oc_1", MessageID: "om_" + messageID}); err != nil {
			t.Fatal(err)
		}
	}

	claimed, err := database.ClaimOnlyOpenByChat(ctx, "oc_1", "om_direct")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.LastAssistantMessageID != "msg_new" {
		t.Fatalf("claimed message = %s", claimed.LastAssistantMessageID)
	}
}

func TestClaimOnlyOpenByChatRejectsMultipleSessions(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	for index, sessionID := range []string{"ses_1", "ses_2"} {
		handoff := domain.Handoff{
			ID:                     fmt.Sprintf("hof_%d", index),
			SessionID:              sessionID,
			Directory:              "/work/a",
			ProjectName:            "a",
			Type:                   domain.HandoffFinished,
			LastAssistantMessageID: fmt.Sprintf("msg_%d", index),
			CreatedAt:              time.Now(),
		}
		if err := database.Create(ctx, handoff); err != nil {
			t.Fatal(err)
		}
		if err := database.BindMessage(ctx, handoff.ID, domain.MessageRef{ChatID: "oc_1", MessageID: fmt.Sprintf("om_%d", index)}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := database.ClaimOnlyOpenByChat(ctx, "oc_1", "om_direct"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("ClaimOnlyOpenByChat() error = %v", err)
	}
}
