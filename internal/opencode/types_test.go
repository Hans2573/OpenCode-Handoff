package opencode

import (
	"encoding/json"
	"testing"
)

func TestLastAssistantOutput(t *testing.T) {
	messages := []Message{
		{Info: MessageInfo{ID: "msg_user", Role: "user"}},
		{
			Info: MessageInfo{
				ID:    "msg_assistant",
				Role:  "assistant",
				Error: json.RawMessage(`{"name":"APIError","data":{"message":"request timeout"}}`),
			},
			Parts: []Part{
				{Type: "reasoning", Text: "hidden"},
				{Type: "text", Text: "I will run the tests"},
				{Type: "tool", Text: "command and output"},
				{Type: "reasoning", Text: "also hidden"},
				{Type: "text", Text: "first"},
				{Type: "text", Text: "second"},
			},
		},
	}

	output, ok := LastAssistantOutput(messages)
	if !ok {
		t.Fatal("LastAssistantOutput() found no assistant message")
	}
	if output.MessageID != "msg_assistant" || output.Text != "first\n\nsecond" {
		t.Fatalf("unexpected output: %+v", output)
	}
	if output.Error != "APIError: request timeout" {
		t.Fatalf("error = %q", output.Error)
	}
}

func TestQuestionCustomDefaultsToAllowed(t *testing.T) {
	var omitted QuestionInfo
	if err := json.Unmarshal([]byte(`{"question":"Choose","header":"Next","options":[]}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if !omitted.AllowsCustom() {
		t.Fatal("omitted custom field should allow a custom answer")
	}

	var disabled QuestionInfo
	if err := json.Unmarshal([]byte(`{"question":"Choose","header":"Next","options":[],"custom":false}`), &disabled); err != nil {
		t.Fatal(err)
	}
	if disabled.Custom == nil || disabled.AllowsCustom() {
		t.Fatal("explicit custom=false should disable custom answers")
	}
}
