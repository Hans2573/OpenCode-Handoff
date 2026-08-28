package feishu

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
	"github.com/Hans2573/OpenCode-Handoff/internal/store"
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

func TestFormatQuestionHandoffWithOptionButtons(t *testing.T) {
	content, err := formatHandoffCard(domain.Handoff{
		SessionID: "ses_question", SessionName: "Choose next", ProjectName: "handoff", Type: domain.HandoffQuestion,
		QuestionID: "que_1", Questions: []domain.Question{{
			Header: "选择一个答案", Text: "你希望接下来做什么？",
			Options: []domain.QuestionOption{{Label: "分析产品设计文档", Description: "解读规范和架构"}, {Label: "审查 GitLab MR"}},
		}},
	}, 3000)
	if err != nil {
		t.Fatal(err)
	}
	card := decodeHandoffCard(t, content)
	message := cardContents(card.Body.Elements)
	for _, expected := range []string{"ses_question", "Choose next", "等待选择", "你希望接下来做什么？", "分析产品设计文档", "忽略", "输入自己的答案", "提交自定义答案", "回调不可用时可引用回复"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("question card %q does not contain %q", message, expected)
		}
	}
	buttons := findCardElements(card.Body.Elements, "button")
	if len(buttons) != 4 {
		t.Fatalf("question buttons = %d, want 4", len(buttons))
	}
	for _, button := range buttons {
		if len(button.Behaviors) != 1 || button.Behaviors[0].Type != "callback" {
			t.Fatalf("button does not use a V2 callback behavior: %+v", button)
		}
	}
	form := findCardElement(card.Body.Elements, "form")
	input := findCardElement(card.Body.Elements, "input")
	if form == nil || form.Name != "custom_answer_form" || input == nil || input.Name != "custom_answer" || input.InputType != "multiline_text" || input.Rows != 3 || input.MaxLength != 1000 || input.Required == nil || !*input.Required {
		t.Fatalf("custom answer form is incomplete: form=%+v input=%+v", form, input)
	}
	if findCardElement(card.Body.Elements, "input_text") != nil {
		t.Fatal("question card still contains the unsupported input_text tag")
	}
	submit := findNamedCardElement(card.Body.Elements, "custom_submit")
	if submit == nil || submit.ActionType != "form_submit" || len(submit.Behaviors) != 1 || submit.Behaviors[0].Value["action"] != "question_custom_reply" {
		t.Fatalf("custom answer submit button is incomplete: %+v", submit)
	}
}

func TestSendHandoffFallsBackWhenFeishuRejectsQuestionForm(t *testing.T) {
	messageID := "om_fallback"
	var contents []string
	client := &Client{
		chatID: "oc_chat",
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		sendCard: func(_ context.Context, _, _, content string) (*larkim.CreateMessageResp, error) {
			contents = append(contents, content)
			if len(contents) == 1 {
				return &larkim.CreateMessageResp{CodeError: larkcore.CodeError{
					Code: 230099,
					Msg:  "Failed to create card content, ext=ErrCode: 200621; ErrMsg: parse card json err",
				}}, nil
			}
			return &larkim.CreateMessageResp{
				CodeError: larkcore.CodeError{Code: 0},
				Data:      &larkim.CreateMessageRespData{MessageId: &messageID},
			}, nil
		},
	}
	handoff := domain.Handoff{
		ID: "handoff_1", SessionID: "ses_1", ProjectName: "handoff", Type: domain.HandoffQuestion,
		Questions: []domain.Question{{Text: "下一步？", Options: []domain.QuestionOption{{Label: "继续"}}}},
	}
	ref, err := client.SendHandoff(context.Background(), handoff)
	if err != nil {
		t.Fatal(err)
	}
	if ref.MessageID != messageID || len(contents) != 2 {
		t.Fatalf("fallback result = %+v, sends = %d", ref, len(contents))
	}
	first := decodeHandoffCard(t, contents[0])
	second := decodeHandoffCard(t, contents[1])
	if findCardElement(first.Body.Elements, "input") == nil {
		t.Fatal("initial question card omitted the custom-answer input")
	}
	if findCardElement(second.Body.Elements, "form") != nil || findCardElement(second.Body.Elements, "input") != nil {
		t.Fatal("fallback question card still contains the rejected form")
	}
	if !strings.Contains(cardContents(second.Body.Elements), "直接输入自定义答案") {
		t.Fatal("fallback question card omitted the quoted-reply instruction")
	}
}

func TestFormatPermissionHandoff(t *testing.T) {
	content, err := formatHandoffCard(domain.Handoff{
		SessionID: "ses_permission", SessionName: "Inspect preview", ProjectName: "handoff", Type: domain.HandoffPermission,
		PermissionID: "per_1", Permission: domain.Permission{
			Name:     "external_directory",
			Patterns: []string{`C:\Users\test\Desktop\preview.html`},
			Always:   []string{`C:\Users\test\Desktop\*`},
		},
	}, 3000)
	if err != nil {
		t.Fatal(err)
	}
	card := decodeHandoffCard(t, content)
	message := cardContents(card.Body.Elements)
	for _, expected := range []string{
		"ses_permission", "Inspect preview", "等待授权", "访问项目目录外文件",
		`C:\Users\test\Desktop\preview.html`, `C:\Users\test\Desktop\*`,
		"允许一次", "始终允许", "拒绝", "其他待处理权限",
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("permission card %q does not contain %q", message, expected)
		}
	}
	buttons := findCardElements(card.Body.Elements, "button")
	if len(buttons) != 3 {
		t.Fatalf("permission buttons = %d, want 3", len(buttons))
	}
	want := map[string]bool{"once": false, "always": false, "reject": false}
	for _, button := range buttons {
		if len(button.Behaviors) != 1 || button.Behaviors[0].Value["action"] != "permission_reply" {
			t.Fatalf("permission button callback = %+v", button)
		}
		decision, _ := button.Behaviors[0].Value["decision"].(string)
		if _, ok := want[decision]; !ok {
			t.Fatalf("unexpected permission decision %q", decision)
		}
		want[decision] = true
	}
	for decision, found := range want {
		if !found {
			t.Fatalf("missing permission decision %q", decision)
		}
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
		if element.Text != nil {
			contents = append(contents, element.Text.Content)
		}
		if element.Label != nil {
			contents = append(contents, element.Label.Content)
		}
		if element.Placeholder != nil {
			contents = append(contents, element.Placeholder.Content)
		}
		if len(element.Elements) > 0 {
			contents = append(contents, cardContents(element.Elements))
		}
		if len(element.Columns) > 0 {
			contents = append(contents, cardContents(element.Columns))
		}
	}
	return strings.Join(contents, "\n")
}

func TestOnCardActionRoutesCustomQuestionAnswer(t *testing.T) {
	client := &Client{replies: make(chan domain.UserReply, 1)}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_1"},
		Context:  &callback.Context{OpenMessageID: "om_question", OpenChatID: "oc_chat"},
		Action: &callback.CallBackAction{
			Value:     map[string]any{"action": "question_custom_reply"},
			FormValue: map[string]any{"custom_answer": "按 TDD 开始"},
		},
	}}
	go func() {
		reply := <-client.replies
		if len(reply.QuestionAnswers) != 1 || reply.QuestionAnswers[0][0] != "按 TDD 开始" {
			t.Errorf("custom card reply = %+v", reply)
		}
		reply.Result <- nil
	}()
	response, err := client.onCardAction(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if response.Toast == nil || response.Toast.Type != "success" {
		t.Fatalf("custom card response = %+v", response)
	}
}

func TestOnCardActionRoutesQuestionReject(t *testing.T) {
	client := &Client{replies: make(chan domain.UserReply, 1)}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_1"},
		Context:  &callback.Context{OpenMessageID: "om_question", OpenChatID: "oc_chat"},
		Action:   &callback.CallBackAction{Value: map[string]any{"action": "question_reject"}},
	}}
	go func() {
		reply := <-client.replies
		if !reply.RejectQuestion {
			t.Errorf("reject card reply = %+v", reply)
		}
		reply.Result <- nil
	}()
	response, err := client.onCardAction(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if response.Toast == nil || response.Toast.Type != "success" || !strings.Contains(response.Toast.Content, "忽略") {
		t.Fatalf("reject card response = %+v", response)
	}
}

func TestOnCardActionRoutesPermissionDecision(t *testing.T) {
	client := &Client{replies: make(chan domain.UserReply, 1)}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_1"},
		Context:  &callback.Context{OpenMessageID: "om_permission", OpenChatID: "oc_chat"},
		Action: &callback.CallBackAction{Value: map[string]any{
			"action": "permission_reply", "decision": "always",
		}},
	}}
	go func() {
		reply := <-client.replies
		if reply.PermissionReply != "always" || reply.ParentMessageID != "om_permission" || !reply.CardAction {
			t.Errorf("permission card reply = %+v", reply)
		}
		reply.Result <- nil
	}()
	response, err := client.onCardAction(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if response.Toast == nil || response.Toast.Type != "success" || !strings.Contains(response.Toast.Content, "始终允许") {
		t.Fatalf("permission card response = %+v", response)
	}
}

func TestIsAbortCommand(t *testing.T) {
	for _, input := range []string{"/stop", " /stop ", "/STOP"} {
		if !isAbortCommand(input) {
			t.Fatalf("isAbortCommand(%q) = false", input)
		}
	}
	for _, input := range []string{"/abort", "/cancel", "中断", "停止", "终止", "abort", "stop", "cancel", "继续", "中断一下然后继续", ""} {
		if isAbortCommand(input) {
			t.Fatalf("isAbortCommand(%q) = true", input)
		}
	}
}

func TestIsHelpCommand(t *testing.T) {
	for _, input := range []string{"/help", " /HELP "} {
		if !isHelpCommand(input) {
			t.Fatalf("isHelpCommand(%q) = false", input)
		}
	}
	for _, input := range []string{"help", "/help now", ""} {
		if isHelpCommand(input) {
			t.Fatalf("isHelpCommand(%q) = true", input)
		}
	}
}

func TestOnMessageRepliesHelpWithoutRouting(t *testing.T) {
	var response string
	client := &Client{
		replies: make(chan domain.UserReply, 1),
		replyText: func(_ context.Context, _ string, text string) error {
			response = text
			return nil
		},
	}
	messageID := "om_help"
	chatID := "oc_chat"
	messageType := "text"
	content := `{"text":"/help"}`
	openID := "ou_open"
	event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender: &larkim.EventSender{SenderId: &larkim.UserId{OpenId: &openID}},
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
	if !strings.Contains(response, "回复 /stop") {
		t.Fatalf("help response = %q", response)
	}
	select {
	case reply := <-client.replies:
		t.Fatalf("help command was routed as reply: %+v", reply)
	default:
	}
}

func TestOnCardActionRoutesQuestionAnswer(t *testing.T) {
	client := &Client{replies: make(chan domain.UserReply, 1)}
	userID := "user_1"
	event := &callback.CardActionTriggerEvent{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_1"}},
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_1", UserID: &userID},
			Context:  &callback.Context{OpenMessageID: "om_question", OpenChatID: "oc_chat"},
			Action: &callback.CallBackAction{Value: map[string]any{
				"action": "question_reply", "answers": []any{[]any{"Analyze PRD"}},
			}},
		},
	}
	go func() {
		reply := <-client.replies
		if reply.MessageID != "evt_1" || reply.ParentMessageID != "om_question" || reply.ChatID != "oc_chat" || reply.QuestionAnswers[0][0] != "Analyze PRD" {
			t.Errorf("card reply = %+v", reply)
		}
		reply.Result <- nil
	}()
	response, err := client.onCardAction(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if response.Toast == nil || response.Toast.Type != "success" {
		t.Fatalf("card response = %+v", response)
	}
}

func findCardElement(elements []handoffCardElement, tag string) *handoffCardElement {
	for index := range elements {
		if elements[index].Tag == tag {
			return &elements[index]
		}
		if nested := findCardElement(elements[index].Elements, tag); nested != nil {
			return nested
		}
		if nested := findCardElement(elements[index].Columns, tag); nested != nil {
			return nested
		}
	}
	return nil
}

func findCardElements(elements []handoffCardElement, tag string) []*handoffCardElement {
	var result []*handoffCardElement
	for index := range elements {
		if elements[index].Tag == tag {
			result = append(result, &elements[index])
		}
		result = append(result, findCardElements(elements[index].Elements, tag)...)
		result = append(result, findCardElements(elements[index].Columns, tag)...)
	}
	return result
}

func findNamedCardElement(elements []handoffCardElement, name string) *handoffCardElement {
	for index := range elements {
		if elements[index].Name == name {
			return &elements[index]
		}
		if nested := findNamedCardElement(elements[index].Elements, name); nested != nil {
			return nested
		}
		if nested := findNamedCardElement(elements[index].Columns, name); nested != nil {
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
