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
