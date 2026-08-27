package feishu

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/xiaohang2/opencode-handoff/internal/domain"
	"github.com/xiaohang2/opencode-handoff/internal/store"
)

func TestFormatHandoff(t *testing.T) {
	content, err := formatHandoffCard(domain.Handoff{
		SessionID:         "ses_123",
		SessionName:       "Fix login timeout",
		ProjectName:       "opsloop",
		Type:              domain.HandoffError,
		ErrorText:         "request timeout",
		LastAssistantText: "running tests",
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	card := decodeHandoffCard(t, content)
	message := cardContents(card.Body.Elements)
	for _, expected := range []string{
		"🆔 Session ID: ses_123",
		"🏷️ Session Name: Fix login timeout",
		"🚨 OpenCode · Interrupted",
		"📁 Project: opsloop",
		"⚠️ 执行异常",
		"request timeout",
		"💬 最后输出（1000）",
		"running tests",
		"↩️ 引用回复本消息可继续原 Session",
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message %q does not contain %q", message, expected)
		}
	}
	if strings.Index(message, "🆔 Session ID:") > strings.Index(message, "🚨 OpenCode · Interrupted") {
		t.Fatalf("session metadata is not at the beginning: %q", message)
	}
	panel := findCardElement(card.Body.Elements, "collapsible_panel")
	if panel == nil || panel.Expanded == nil || *panel.Expanded {
		t.Fatalf("last output panel is not collapsed: %+v", panel)
	}
}

func TestFormatFinishedHandoff(t *testing.T) {
	content, err := formatHandoffCard(domain.Handoff{
		SessionID:         "ses_456",
		ProjectName:       "handoff",
		Type:              domain.HandoffFinished,
		LastAssistantText: "all tests passed",
	}, 3000)
	if err != nil {
		t.Fatal(err)
	}
	card := decodeHandoffCard(t, content)
	message := cardContents(card.Body.Elements)
	for _, expected := range []string{
		"🆔 Session ID: ses_456",
		"🏷️ Session Name: Untitled Session",
		"✅ OpenCode · Task Finished",
		"📁 Project: handoff",
		"💬 最后输出（3000）",
		"all tests passed",
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message %q does not contain %q", message, expected)
		}
	}
	if strings.Contains(message, "执行异常") || strings.Contains(message, "引用回复") {
		t.Fatalf("finished message contains error-only content: %q", message)
	}
	if card.Schema != "2.0" || !card.Config.UpdateMulti || card.Config.WidthMode != "fill" {
		t.Fatalf("unexpected card config: %+v", card)
	}
}

func decodeHandoffCard(t *testing.T, content string) handoffCard {
	t.Helper()
	var card handoffCard
	if err := json.Unmarshal([]byte(content), &card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	return card
}

func cardContents(elements []handoffCardElement) string {
	var contents []string
	for _, element := range elements {
		if element.Content != "" {
			contents = append(contents, element.Content)
		}
		if element.Header != nil {
			contents = append(contents, element.Header.Title.Content)
		}
		if len(element.Elements) > 0 {
			contents = append(contents, cardContents(element.Elements))
		}
	}
	return strings.Join(contents, "\n")
}

func findCardElement(elements []handoffCardElement, tag string) *handoffCardElement {
	for index := range elements {
		if elements[index].Tag == tag {
			return &elements[index]
		}
		if nested := findCardElement(elements[index].Elements, tag); nested != nil {
			return nested
		}
	}
	return nil
}

func TestOnMessageExtractsReplyRouteAndAllUserIDs(t *testing.T) {
	client := &Client{
		replies: make(chan domain.UserReply, 1),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	messageID := "om_reply"
	chatID := "oc_chat"
	messageType := "text"
	content := `{"text":" continue "}`
	openID := "ou_open"
	userID := "user_id"
	event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender: &larkim.EventSender{SenderId: &larkim.UserId{OpenId: &openID, UserId: &userID}},
		Message: &larkim.EventMessage{
			MessageId:   &messageID,
			ChatId:      &chatID,
			MessageType: &messageType,
			Content:     &content,
		},
	}}
	if err := client.onMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	reply := <-client.replies
	if reply.ParentMessageID != "" || reply.Text != "continue" || len(reply.SenderIDs) != 2 {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestOnMessagePairsFromCommand(t *testing.T) {
	bindingStore := &fakeBindingStore{}
	var confirmation string
	client := &Client{
		pairingCode:  "ABC123",
		bindingStore: bindingStore,
		paired:       make(chan struct{}),
		allowed:      map[string]struct{}{},
		replies:      make(chan domain.UserReply, 1),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		replyText: func(_ context.Context, _ string, text string) error {
			confirmation = text
			return nil
		},
	}
	messageID := "om_bind"
	chatID := "oc_bound"
	messageType := "text"
	mentionKey := "@_user_1"
	content := `{"text":"@_user_1 /bind abc123"}`
	openID := "ou_owner"
	event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender: &larkim.EventSender{SenderId: &larkim.UserId{OpenId: &openID}},
		Message: &larkim.EventMessage{
			MessageId:   &messageID,
			ChatId:      &chatID,
			MessageType: &messageType,
			Content:     &content,
			Mentions:    []*larkim.MentionEvent{{Key: &mentionKey}},
		},
	}}
	if err := client.onMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if bindingStore.binding.ChatID != chatID || bindingStore.binding.UserIDs[0] != openID {
		t.Fatalf("binding = %+v", bindingStore.binding)
	}
	if !strings.Contains(confirmation, "配对成功") {
		t.Fatalf("confirmation = %q", confirmation)
	}
	select {
	case <-client.paired:
	default:
		t.Fatal("pairing did not unblock senders")
	}
}

type fakeBindingStore struct {
	binding domain.ChannelBinding
}

func (f *fakeBindingStore) GetChannelBinding(context.Context) (domain.ChannelBinding, error) {
	if f.binding.ChatID == "" {
		return domain.ChannelBinding{}, store.ErrNotFound
	}
	return f.binding, nil
}

func (f *fakeBindingStore) BindChannel(_ context.Context, binding domain.ChannelBinding) error {
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = time.Now()
	}
	f.binding = binding
	return nil
}
