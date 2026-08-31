package opencode

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Session struct {
	ID        string `json:"id"`
	ParentID  string `json:"parentID,omitempty"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
	Time      struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

type Project struct {
	ID        string   `json:"id"`
	Worktree  string   `json:"worktree"`
	Sandboxes []string `json:"sandboxes"`
	Name      string   `json:"name,omitempty"`
}

type SessionStatus struct {
	Type    string `json:"type"`
	Attempt int    `json:"attempt,omitempty"`
	Message string `json:"message,omitempty"`
}

type ModelRef struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
	Variant    string `json:"variant,omitempty"`
}

type Model struct {
	ProviderID   string
	ProviderName string
	ID           string
	Name         string
	Status       string
	Variants     []string
	Reasoning    bool
	Attachment   bool
	ContextLimit int64
}

type Message struct {
	Info  MessageInfo `json:"info"`
	Parts []Part      `json:"parts"`
}

type MessageInfo struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionID"`
	Role      string          `json:"role"`
	Model     *ModelRef       `json:"model,omitempty"`
	Error     json.RawMessage `json:"error,omitempty"`
	Time      struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed,omitempty"`
	} `json:"time"`
}

type Part struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type AssistantOutput struct {
	MessageID string
	Text      string
	Error     string
}

type Event struct {
	Directory  string
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

type StatusEvent struct {
	SessionID string        `json:"sessionID"`
	Status    SessionStatus `json:"status"`
}

type SessionEvent struct {
	SessionID string          `json:"sessionID"`
	Error     json.RawMessage `json:"error,omitempty"`
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type QuestionInfo struct {
	Question string           `json:"question"`
	Header   string           `json:"header"`
	Options  []QuestionOption `json:"options"`
	Multiple bool             `json:"multiple"`
	Custom   *bool            `json:"custom,omitempty"`
}

func (q QuestionInfo) AllowsCustom() bool {
	return q.Custom == nil || *q.Custom
}

type QuestionRequest struct {
	ID        string         `json:"id"`
	SessionID string         `json:"sessionID"`
	Questions []QuestionInfo `json:"questions"`
	Tool      struct {
		MessageID string `json:"messageID"`
		CallID    string `json:"callID"`
	} `json:"tool"`
}

type PermissionRequest struct {
	ID         string         `json:"id"`
	SessionID  string         `json:"sessionID"`
	Permission string         `json:"permission"`
	Patterns   []string       `json:"patterns"`
	Metadata   map[string]any `json:"metadata"`
	Always     []string       `json:"always"`
	Tool       *struct {
		MessageID string `json:"messageID"`
		CallID    string `json:"callID"`
	} `json:"tool,omitempty"`
}

type PermissionReply string

const (
	PermissionOnce   PermissionReply = "once"
	PermissionAlways PermissionReply = "always"
	PermissionReject PermissionReply = "reject"
)

func (r PermissionReply) Valid() bool {
	return r == PermissionOnce || r == PermissionAlways || r == PermissionReject
}

func LastAssistantOutput(messages []Message) (AssistantOutput, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Info.Role != "assistant" {
			continue
		}
		start := 0
		for index, part := range message.Parts {
			if part.Type == "tool" {
				start = index + 1
			}
		}
		var chunks []string
		for _, part := range message.Parts[start:] {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				chunks = append(chunks, part.Text)
			}
		}
		return AssistantOutput{
			MessageID: message.Info.ID,
			Text:      strings.TrimSpace(strings.Join(chunks, "\n\n")),
			Error:     ErrorSummary(message.Info.Error),
		}, true
	}
	return AssistantOutput{}, false
}

func ErrorSummary(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return strings.TrimSpace(string(raw))
	}
	if message := findString(value, "message"); message != "" {
		if name := findString(value, "name"); name != "" && !strings.Contains(message, name) {
			return fmt.Sprintf("%s: %s", name, message)
		}
		return message
	}
	if name := findString(value, "name"); name != "" {
		return name
	}
	compact, _ := json.Marshal(value)
	return string(compact)
}

func findString(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if raw, ok := typed[key]; ok {
			if text, ok := raw.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
		for _, child := range typed {
			if found := findString(child, key); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findString(child, key); found != "" {
				return found
			}
		}
	}
	return ""
}
