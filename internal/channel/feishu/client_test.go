package feishu

import (
	"context"
	"encoding/json"
	"errors"
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
		"💬 最后输出（末尾 1000 字）",
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
		"💬 最后输出（末尾 3000 字）",
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

func TestFormatGoalCompletionHandoff(t *testing.T) {
	content, err := formatHandoffCard(domain.Handoff{
		ID: "hof_goal", SessionID: "ses_goal", SessionName: "Ship feature", ProjectName: "handoff",
		Directory: "/work/project", Type: domain.HandoffGoalCompletion, LastAssistantText: "目标已经完成",
	}, 3000)
	if err != nil {
		t.Fatal(err)
	}
	card := decodeHandoffCard(t, content)
	message := cardContents(card.Body.Elements)
	for _, expected := range []string{"Goal Loop · 等待完成确认", "目标已经完成", "确认完成", "继续 Goal", "直接 @机器人 回复"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("Goal completion card %q does not contain %q", message, expected)
		}
	}
	buttons := findCardElements(card.Body.Elements, "button")
	if len(buttons) != 2 || buttons[0].Behaviors[0].Value["action"] != "goal_complete" || buttons[1].Behaviors[0].Value["action"] != "goal_continue" {
		t.Fatalf("Goal completion buttons = %+v", buttons)
	}
}

func TestFormatGoalStatusHandoffHasNoInteractiveReplyActions(t *testing.T) {
	content, err := formatHandoffCard(domain.Handoff{
		ID: "hof_goal_status", SessionID: "ses_goal", SessionName: "Ship feature", ProjectName: "handoff",
		Directory: "/work/project", Type: domain.HandoffGoalStatus, LastAssistantText: "Goal 已启动",
	}, 3000)
	if err != nil {
		t.Fatal(err)
	}
	card := decodeHandoffCard(t, content)
	message := cardContents(card.Body.Elements)
	if !strings.Contains(message, "Goal Loop · 状态更新") || !strings.Contains(message, "Goal 已启动") {
		t.Fatalf("Goal status card = %q", message)
	}
	if buttons := findCardElements(card.Body.Elements, "button"); len(buttons) != 0 {
		t.Fatalf("Goal status must not create reply actions: %+v", buttons)
	}
}

func TestOnCardActionRoutesGoalContinuation(t *testing.T) {
	client := &Client{replies: make(chan domain.UserReply, 1)}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_1"},
		Context:  &callback.Context{OpenMessageID: "om_goal", OpenChatID: "oc_chat"},
		Action:   &callback.CallBackAction{Value: map[string]any{"action": "goal_continue"}},
	}}
	go func() {
		reply := <-client.replies
		if !reply.GoalContinue || reply.GoalComplete || reply.ParentMessageID != "om_goal" {
			t.Errorf("Goal card reply = %+v", reply)
		}
		reply.Result <- nil
	}()
	response, err := client.onCardAction(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if response == nil {
		t.Fatal("expected Goal continuation toast")
	}
}

func TestFormatHandoffUsesTailPreviewAndOffersDetailedOutput(t *testing.T) {
	content, err := formatHandoffCard(domain.Handoff{
		ID: "hof_long", SessionID: "ses_long", ProjectName: "handoff", Type: domain.HandoffFinished,
		LastAssistantText: "开头结论" + strings.Repeat("中", 20) + "末尾结论",
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	card := decodeHandoffCard(t, content)
	message := cardContents(card.Body.Elements)
	if strings.Contains(message, "开头结论") || !strings.Contains(message, "末尾结论") || !strings.Contains(message, "已省略前") {
		t.Fatalf("tail preview = %q", message)
	}
	buttons := findCardElements(card.Body.Elements, "button")
	if len(buttons) != 2 || buttons[0].Behaviors[0].Value["action"] != "assistant_output" || buttons[0].Behaviors[0].Value["handoff_id"] != "hof_long" {
		t.Fatalf("handoff buttons = %+v", buttons)
	}
}

func TestFormatAssistantOutputCardUsesCollapsedFullText(t *testing.T) {
	content, err := formatAssistantOutputCard(domain.AssistantOutputDetail{
		SessionID: "ses_1", SessionName: "Final answer",
		Content: "仅包含最终答复正文",
	})
	if err != nil {
		t.Fatal(err)
	}
	card := decodeHandoffCard(t, content)
	message := cardContents(card.Body.Elements)
	for _, expected := range []string{"详细答复", "Final answer", "ses_1", "展开查看全部最终答复", "仅包含最终答复正文"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("detail card %q does not contain %q", message, expected)
		}
	}
	panel := findCardElement(card.Body.Elements, "collapsible_panel")
	if panel == nil || panel.Expanded == nil || *panel.Expanded || len(findCardElements(card.Body.Elements, "button")) != 0 {
		t.Fatalf("detail panel = %+v", panel)
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

func TestSendHandoffAddsTimestampSeparatorBeforeCard(t *testing.T) {
	messageID := "om_message"
	var calls []string
	client := &Client{
		chatID: "oc_chat",
		now: func() time.Time {
			return time.Date(2026, time.September, 2, 15, 6, 30, 0, time.Local)
		},
		sendText: func(_ context.Context, chatID, id, text string) error {
			calls = append(calls, "text:"+chatID+":"+id+":"+text)
			return nil
		},
		sendCard: func(_ context.Context, chatID, id, _ string) (*larkim.CreateMessageResp, error) {
			calls = append(calls, "card:"+chatID+":"+id)
			return &larkim.CreateMessageResp{
				CodeError: larkcore.CodeError{Code: 0},
				Data:      &larkim.CreateMessageRespData{MessageId: &messageID},
			}, nil
		},
	}

	ref, err := client.SendHandoff(context.Background(), domain.Handoff{
		ID: "handoff_1", SessionID: "ses_1", ProjectName: "handoff", Type: domain.HandoffFinished,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.MessageID != messageID {
		t.Fatalf("message ref = %+v", ref)
	}
	want := []string{
		"text:oc_chat:handoff_1:separator:=== 2026-09-02 15:06:30 ===",
		"card:oc_chat:handoff_1",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	for index := range want {
		if calls[index] != want[index] {
			t.Fatalf("calls = %#v, want %#v", calls, want)
		}
	}
}

func TestSendHandoffContinuesWhenSeparatorFails(t *testing.T) {
	messageID := "om_message"
	cardSent := false
	client := &Client{
		chatID: "oc_chat",
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		sendText: func(context.Context, string, string, string) error {
			return errors.New("separator unavailable")
		},
		sendCard: func(context.Context, string, string, string) (*larkim.CreateMessageResp, error) {
			cardSent = true
			return &larkim.CreateMessageResp{
				CodeError: larkcore.CodeError{Code: 0},
				Data:      &larkim.CreateMessageRespData{MessageId: &messageID},
			}, nil
		},
	}

	if _, err := client.SendHandoff(context.Background(), domain.Handoff{
		ID: "handoff_1", SessionID: "ses_1", ProjectName: "handoff", Type: domain.HandoffFinished,
	}); err != nil {
		t.Fatal(err)
	}
	if !cardSent {
		t.Fatal("handoff card was not sent after separator failure")
	}
}

func TestSendHandoffGroupsSameSessionWithinQuietPeriod(t *testing.T) {
	messageID := "om_message"
	now := time.Date(2026, time.September, 2, 15, 6, 30, 0, time.Local)
	var separators []string
	client := &Client{
		chatID: "oc_chat",
		now:    func() time.Time { return now },
		sendText: func(_ context.Context, _, _, text string) error {
			separators = append(separators, text)
			return nil
		},
		sendCard: func(context.Context, string, string, string) (*larkim.CreateMessageResp, error) {
			return &larkim.CreateMessageResp{
				CodeError: larkcore.CodeError{Code: 0},
				Data:      &larkim.CreateMessageRespData{MessageId: &messageID},
			}, nil
		},
	}

	send := func(id, sessionID string) {
		t.Helper()
		if _, err := client.SendHandoff(context.Background(), domain.Handoff{
			ID: id, SessionID: sessionID, ProjectName: "handoff", Type: domain.HandoffFinished,
		}); err != nil {
			t.Fatal(err)
		}
	}

	send("handoff_1", "ses_1")
	now = now.Add(2 * time.Second)
	send("handoff_2", "ses_1")
	if len(separators) != 1 {
		t.Fatalf("same-session separators = %#v, want one", separators)
	}

	send("handoff_3", "ses_2")
	if len(separators) != 2 {
		t.Fatalf("different-session separators = %#v, want two", separators)
	}

	now = now.Add(handoffSeparatorQuietPeriod)
	send("handoff_4", "ses_1")
	if len(separators) != 3 {
		t.Fatalf("new-batch separators = %#v, want three", separators)
	}
}

func TestFormatPermissionHandoff(t *testing.T) {
	content, err := formatHandoffCard(domain.Handoff{
		SessionID: "ses_permission", SessionName: "Inspect preview", ProjectName: "handoff", Type: domain.HandoffPermission,
		PermissionID: "per_1", Permission: domain.Permission{
			Name:     "external_directory",
			Patterns: []string{`C:\Users\test\Desktop\preview.html`},
			Always:   []string{`C:\Users\test\Desktop\*`},
			Metadata: map[string]any{"filepath": `C:\Users\test\Desktop\README.md`},
		},
	}, 3000)
	if err != nil {
		t.Fatal(err)
	}
	card := decodeHandoffCard(t, content)
	message := cardContents(card.Body.Elements)
	for _, expected := range []string{
		"ses_permission", "Inspect preview", "等待授权", "访问项目目录外文件",
		`C:\Users\test\Desktop\preview.html`, `C:\Users\test\Desktop\*`, `C:\Users\test\Desktop\README.md`,
		"允许一次", "仅处理当前这条请求", "始终允许", "拒绝", "其他待处理权限",
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

func TestFormatProjectAndCreatedSessionCards(t *testing.T) {
	content, err := formatProjectCard(domain.ProjectPage{
		Projects: []domain.Project{{ID: "project_1", Name: "opsloop-sdd", Directory: `D:\work\opsloop-sdd`}},
		Page:     1, TotalPages: 2, Total: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	card := decodeHandoffCard(t, content)
	message := cardContents(card.Body.Elements)
	for _, expected := range []string{"OpenCode Projects", "第 1/2 页", "opsloop-sdd", `D:\work\opsloop-sdd`, "选择模型并创建", "下一页"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("project card %q does not contain %q", message, expected)
		}
	}
	buttons := findCardElements(card.Body.Elements, "button")
	if len(buttons) != 2 || buttons[0].Behaviors[0].Value["action"] != "project_create" || buttons[1].Behaviors[0].Value["action"] != "project_page" {
		t.Fatalf("project buttons = %+v", buttons)
	}

	content, err = formatHandoffCard(domain.Handoff{
		SessionID: "ses_created", SessionName: "Feishu · opsloop-sdd", ProjectName: "opsloop-sdd",
		Directory: `D:\work\opsloop-sdd`, Type: domain.HandoffSession,
	}, 3000)
	if err != nil {
		t.Fatal(err)
	}
	message = cardContents(decodeHandoffCard(t, content).Body.Elements)
	for _, expected := range []string{"Session Created", "ses_created", "opsloop-sdd", "引用回复本消息"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("created session card %q does not contain %q", message, expected)
		}
	}
}

func TestFormatModelCardsExplainWhenSelectionTakesEffect(t *testing.T) {
	model := domain.Model{
		ProviderID: "openai", ProviderName: "OpenAI", ID: "gpt-test", Name: "GPT Test",
		Variants: []string{"low", "high"}, Reasoning: true, ContextLimit: 200000,
	}
	homeContent, err := formatModelCard(domain.ModelPage{
		Home: true, Total: 12,
		Recent:    []domain.RecentModel{{Model: model, Variant: "high"}},
		Providers: []domain.ModelProvider{{ID: "openai", Name: "OpenAI", Count: 12}},
		Context:   domain.ModelContext{Target: domain.ModelTargetSwitch, ProjectDirectory: `D:\work\project`, SessionID: "ses_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	homeCard := decodeHandoffCard(t, homeContent)
	homeMessage := cardContents(homeCard.Body.Elements)
	for _, expected := range []string{"最近使用", "GPT Test · high", "按 Provider 查看", "OpenAI · 12", "/models <关键词>"} {
		if !strings.Contains(homeMessage, expected) {
			t.Fatalf("model home %q does not contain %q", homeMessage, expected)
		}
	}
	homeButtons := findCardElements(homeCard.Body.Elements, "button")
	if len(homeButtons) != 4 || homeButtons[0].Behaviors[0].Value["action"] != "model_apply" || homeButtons[1].Behaviors[0].Value["action"] != "model_provider" || homeButtons[2].Behaviors[0].Value["action"] != "model_all" || homeButtons[3].Behaviors[0].Value["action"] != "model_search" {
		t.Fatalf("model home buttons = %+v", homeButtons)
	}
	fallbackContent, err := formatModelCardWithoutSearch(domain.ModelPage{
		Home: true, Total: 12, Providers: []domain.ModelProvider{{ID: "openai", Name: "OpenAI", Count: 12}},
		Context: domain.ModelContext{Target: domain.ModelTargetSwitch, ProjectDirectory: `D:\work\project`, SessionID: "ses_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fallbackCard := decodeHandoffCard(t, fallbackContent)
	if findCardElement(fallbackCard.Body.Elements, "form") != nil || !strings.Contains(cardContents(fallbackCard.Body.Elements), "不支持卡片搜索框") {
		t.Fatalf("fallback model card = %+v", fallbackCard)
	}

	content, err := formatModelCard(domain.ModelPage{
		Models: []domain.Model{model}, Page: 1, TotalPages: 1, Total: 1,
		Context: domain.ModelContext{Target: domain.ModelTargetCreate, ProjectDirectory: `D:\work\project`},
	})
	if err != nil {
		t.Fatal(err)
	}
	card := decodeHandoffCard(t, content)
	message := cardContents(card.Body.Elements)
	for _, expected := range []string{"脱敏", "第一次引用回复任务时才真正生效", "GPT Test", "200K", "选择模型与档位"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("model card %q does not contain %q", message, expected)
		}
	}
	buttons := findCardElements(card.Body.Elements, "button")
	if len(buttons) != 3 || buttons[0].Behaviors[0].Value["action"] != "model_apply" || buttons[1].Behaviors[0].Value["action"] != "model_variants" || buttons[2].Behaviors[0].Value["action"] != "model_home" {
		t.Fatalf("model buttons = %+v", buttons)
	}

	content, err = formatModelVariantCard(domain.ModelVariantPage{
		Model:   model,
		Context: domain.ModelContext{Target: domain.ModelTargetSwitch, ProjectDirectory: `D:\work\project`, SessionID: "ses_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	message = cardContents(decodeHandoffCard(t, content).Body.Elements)
	for _, expected := range []string{"不会中断当前执行", "下一条飞书任务起生效", "使用 low 档位", "使用 high 档位"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("variant card %q does not contain %q", message, expected)
		}
	}
}

func TestInvalidCardReplyErrorDetection(t *testing.T) {
	for _, message := range []string{
		"reply card to Feishu message: code=230099 message=failed to create card content",
		"card error 200621: parse card json failed",
	} {
		if !isInvalidCardReplyError(errors.New(message)) {
			t.Fatalf("invalid card error not detected: %s", message)
		}
	}
	if isInvalidCardReplyError(errors.New("network timeout")) {
		t.Fatal("network error was treated as invalid card")
	}
}

func TestFormatRunningSessionsCard(t *testing.T) {
	lastInput := strings.Repeat("检查部署状态", 40) + "完整输入结尾"
	content, err := formatRunningSessionsCard(domain.RunningSessions{
		Items: []domain.RunningSession{{
			SessionID: "ses_run", SessionName: "Deploy checks", ProjectName: "opsloop",
			State: "waiting_permission", LastUserText: lastInput, RunningFor: 95 * time.Second, HasLastUserInput: true,
		}},
		Total: 1, ScannedProjects: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	message := cardContents(decodeHandoffCard(t, content).Body.Elements)
	for _, expected := range []string{"Running Sessions", "等待授权", "Deploy checks", "opsloop", "ses_run", "1 分 35 秒", "完整输入结尾"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("running sessions card %q does not contain %q", message, expected)
		}
	}
	panel := findCardElement(decodeHandoffCard(t, content).Body.Elements, "collapsible_panel")
	if panel == nil || panel.Expanded == nil || *panel.Expanded || panel.Header == nil || panel.Header.Title.Content != "💬 最后一次用户输入" {
		t.Fatalf("last input panel = %+v", panel)
	}
	if !strings.Contains(cardContents(panel.Elements), lastInput) {
		t.Fatal("last input panel truncated the user input")
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

func TestOnCardActionRoutesAssistantOutputDetail(t *testing.T) {
	client := &Client{replies: make(chan domain.UserReply, 1)}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_1"},
		Context:  &callback.Context{OpenMessageID: "om_handoff", OpenChatID: "oc_chat"},
		Action:   &callback.CallBackAction{Value: map[string]any{"action": "assistant_output", "handoff_id": "hof_1"}},
	}}
	go func() {
		reply := <-client.replies
		if !reply.ViewOutput || reply.HandoffID != "hof_1" || reply.ParentMessageID != "om_handoff" {
			t.Errorf("assistant output card reply = %+v", reply)
		}
		reply.Result <- nil
	}()
	response, err := client.onCardAction(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if response.Toast == nil || response.Toast.Type != "success" || !strings.Contains(response.Toast.Content, "详细答复") {
		t.Fatalf("assistant output response = %+v", response)
	}
}

func TestOnCardActionRoutesProjectCreate(t *testing.T) {
	client := &Client{replies: make(chan domain.UserReply, 1)}
	event := &callback.CardActionTriggerEvent{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_create"}},
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_1"},
			Context:  &callback.Context{OpenMessageID: "om_projects", OpenChatID: "oc_chat"},
			Action: &callback.CallBackAction{Value: map[string]any{
				"action": "project_create", "directory": `D:\work\project`,
			}},
		},
	}
	go func() {
		reply := <-client.replies
		if !reply.ListModels || reply.ModelContext.Target != domain.ModelTargetCreate || reply.ProjectDirectory != `D:\work\project` || reply.MessageID != "evt_create" {
			t.Errorf("project create reply = %+v", reply)
		}
		reply.Result <- nil
	}()
	response, err := client.onCardAction(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if response.Toast == nil || response.Toast.Type != "success" || !strings.Contains(response.Toast.Content, "模型") {
		t.Fatalf("project create response = %+v", response)
	}
}

func TestOnCardActionRoutesModelSelectionContext(t *testing.T) {
	client := &Client{replies: make(chan domain.UserReply, 1)}
	event := &callback.CardActionTriggerEvent{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_model"}},
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_1"},
			Context:  &callback.Context{OpenMessageID: "om_models", OpenChatID: "oc_chat"},
			Action: &callback.CallBackAction{Value: map[string]any{
				"action": "model_apply", "target": "switch", "directory": `D:\work\project`,
				"session_id": "ses_1", "session_name": "Existing", "provider_id": "openai",
				"model_id": "gpt-test", "variant": "high",
			}},
		},
	}
	go func() {
		reply := <-client.replies
		if !reply.ApplyModel || reply.ModelContext.Target != domain.ModelTargetSwitch || reply.ModelContext.SessionID != "ses_1" || reply.ProviderID != "openai" || reply.ModelID != "gpt-test" || reply.ModelVariant != "high" {
			t.Errorf("model selection reply = %+v", reply)
		}
		reply.Result <- nil
	}()
	response, err := client.onCardAction(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if response.Toast == nil || response.Toast.Type != "success" || !strings.Contains(response.Toast.Content, "下一条") {
		t.Fatalf("model selection response = %+v", response)
	}
}

func TestOnCardActionRoutesModelProviderFilter(t *testing.T) {
	client := &Client{replies: make(chan domain.UserReply, 1)}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_1"},
		Context:  &callback.Context{OpenMessageID: "om_models", OpenChatID: "oc_chat"},
		Action: &callback.CallBackAction{Value: map[string]any{
			"action": "model_provider", "filter_provider": "openai", "target": "switch",
			"directory": `D:\work\project`, "session_id": "ses_1",
		}},
	}}
	go func() {
		reply := <-client.replies
		if !reply.ListModels || reply.ModelPage != 1 || reply.ModelProviderID != "openai" || reply.ModelContext.SessionID != "ses_1" {
			t.Errorf("model provider reply = %+v", reply)
		}
		reply.Result <- nil
	}()
	response, err := client.onCardAction(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if response.Toast == nil || response.Toast.Type != "success" || !strings.Contains(response.Toast.Content, "模型") {
		t.Fatalf("model provider response = %+v", response)
	}
}

func TestOnCardActionRoutesModelSearchWithSessionContext(t *testing.T) {
	client := &Client{replies: make(chan domain.UserReply, 1)}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_1"},
		Context:  &callback.Context{OpenMessageID: "om_models", OpenChatID: "oc_chat"},
		Action: &callback.CallBackAction{
			Value: map[string]any{
				"action": "model_search", "target": "switch", "directory": `D:\work\project`, "session_id": "ses_1",
			},
			FormValue: map[string]any{"model_query": " claude code "},
		},
	}}
	go func() {
		reply := <-client.replies
		if !reply.ListModels || reply.ModelPage != 1 || reply.ModelQuery != "claude code" || reply.ModelContext.SessionID != "ses_1" {
			t.Errorf("model search reply = %+v", reply)
		}
		reply.Result <- nil
	}()
	response, err := client.onCardAction(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if response.Toast == nil || response.Toast.Type != "success" || !strings.Contains(response.Toast.Content, "模型") {
		t.Fatalf("model search response = %+v", response)
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

func TestParseProjectCommand(t *testing.T) {
	for input, expectedPage := range map[string]int{"/project": 1, " /PROJECT 2 ": 2, "/project invalid": 1} {
		page, ok := parseProjectCommand(input)
		if !ok || page != expectedPage {
			t.Fatalf("parseProjectCommand(%q) = %d, %v", input, page, ok)
		}
	}
	if _, ok := parseProjectCommand("project"); ok {
		t.Fatal("plain project text was parsed as a command")
	}
}

func TestParseModelsCommand(t *testing.T) {
	tests := []struct {
		input string
		query string
		page  int
	}{{"/models", "", 0}, {"models", "", 0}, {" /MODELS 3 ", "", 3}, {"models gpt", "gpt", 1}, {"/models claude code 2", "claude code", 2}}
	for _, test := range tests {
		query, page, ok := parseModelsCommand(test.input)
		if !ok || query != test.query || page != test.page {
			t.Fatalf("parseModelsCommand(%q) = %q, %d, %v", test.input, query, page, ok)
		}
	}
	if _, _, ok := parseModelsCommand("model"); ok {
		t.Fatal("unrecognised model text was parsed as a command")
	}
}

func TestIsRunningCommand(t *testing.T) {
	for _, input := range []string{"/running", " /RUNNING ", "/r"} {
		if !isRunningCommand(input) {
			t.Fatalf("isRunningCommand(%q) = false", input)
		}
	}
	for _, input := range []string{"running", "/run", "/r now", ""} {
		if isRunningCommand(input) {
			t.Fatalf("isRunningCommand(%q) = true", input)
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

func TestOnMessageAcceptsBareModelsOnlyWhenUnquoted(t *testing.T) {
	messageID := "om_models"
	chatID := "oc_chat"
	messageType := "text"
	content := `{"text":"models gpt"}`
	openID := "ou_open"
	client := &Client{replies: make(chan domain.UserReply, 2), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender:  &larkim.EventSender{SenderId: &larkim.UserId{OpenId: &openID}},
		Message: &larkim.EventMessage{MessageId: &messageID, ChatId: &chatID, MessageType: &messageType, Content: &content},
	}}
	if err := client.onMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	reply := <-client.replies
	if !reply.ListModels || reply.ModelQuery != "gpt" {
		t.Fatalf("bare models reply = %+v", reply)
	}
	parentID := "om_parent"
	event.Event.Message.ParentId = &parentID
	if err := client.onMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	reply = <-client.replies
	if reply.ListModels || reply.Text != "models gpt" {
		t.Fatalf("quoted bare models reply = %+v", reply)
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

func TestOnMessageRoutesProjectCommand(t *testing.T) {
	client := &Client{replies: make(chan domain.UserReply, 1), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	messageID := "om_project"
	chatID := "oc_chat"
	messageType := "text"
	content := `{"text":"/project 2"}`
	openID := "ou_open"
	event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender:  &larkim.EventSender{SenderId: &larkim.UserId{OpenId: &openID}},
		Message: &larkim.EventMessage{MessageId: &messageID, ChatId: &chatID, MessageType: &messageType, Content: &content},
	}}
	if err := client.onMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	reply := <-client.replies
	if !reply.ListProjects || reply.ProjectPage != 2 {
		t.Fatalf("project command reply = %+v", reply)
	}
}

func TestOnMessageRoutesRunningCommand(t *testing.T) {
	client := &Client{replies: make(chan domain.UserReply, 1), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	messageID := "om_running"
	chatID := "oc_chat"
	messageType := "text"
	content := `{"text":"/r"}`
	openID := "ou_open"
	event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender:  &larkim.EventSender{SenderId: &larkim.UserId{OpenId: &openID}},
		Message: &larkim.EventMessage{MessageId: &messageID, ChatId: &chatID, MessageType: &messageType, Content: &content},
	}}
	if err := client.onMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	reply := <-client.replies
	if !reply.ListRunning {
		t.Fatalf("running command reply = %+v", reply)
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
