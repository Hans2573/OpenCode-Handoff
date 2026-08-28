package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientDirectoryDiscoveryAndPrompt(t *testing.T) {
	var promptDirectory string
	var promptText string
	var questionAnswers [][]string
	var rejected bool
	var aborted bool
	var permissionReply PermissionReply
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/project":
			writeJSON(t, writer, []Project{{ID: "p1", Worktree: "/work/a", Sandboxes: []string{"/work/a-sandbox"}}})
		case request.Method == http.MethodGet && request.URL.Path == "/session/status":
			if request.URL.Query().Get("directory") != "/work/a" {
				t.Errorf("directory query = %q", request.URL.Query().Get("directory"))
			}
			writeJSON(t, writer, map[string]SessionStatus{"ses_1": {Type: "busy"}})
		case request.Method == http.MethodPost && request.URL.Path == "/session/ses_1/prompt_async":
			promptDirectory = request.URL.Query().Get("directory")
			var body struct {
				Parts []Part `json:"parts"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode prompt: %v", err)
			}
			if len(body.Parts) == 1 {
				promptText = body.Parts[0].Text
			}
			writer.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodPost && request.URL.Path == "/session/ses_1/abort":
			aborted = true
			if request.URL.Query().Get("directory") != "/work/a" {
				t.Errorf("abort directory = %q", request.URL.Query().Get("directory"))
			}
			writer.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodGet && request.URL.Path == "/question":
			writeJSON(t, writer, []QuestionRequest{{ID: "que_1", SessionID: "ses_1"}})
		case request.Method == http.MethodPost && request.URL.Path == "/question/que_1/reply":
			var body struct {
				Answers [][]string `json:"answers"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode question reply: %v", err)
			}
			questionAnswers = body.Answers
			writer.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodPost && request.URL.Path == "/question/que_1/reject":
			rejected = true
			writer.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodGet && request.URL.Path == "/permission":
			writeJSON(t, writer, []PermissionRequest{{
				ID: "per_1", SessionID: "ses_1", Permission: "external_directory",
				Patterns: []string{`C:\Users\test\Desktop\*`}, Always: []string{`C:\Users\test\Desktop\*`},
			}})
		case request.Method == http.MethodPost && request.URL.Path == "/permission/per_1/reply":
			var body struct {
				Reply PermissionReply `json:"reply"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode permission reply: %v", err)
			}
			permissionReply = body.Reply
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	directories, err := client.ListDirectories(context.Background())
	if err != nil || len(directories) != 2 {
		t.Fatalf("ListDirectories() = %v, %v", directories, err)
	}
	statuses, err := client.GetSessionStatuses(context.Background(), "/work/a")
	if err != nil || statuses["ses_1"].Type != "busy" {
		t.Fatalf("GetSessionStatuses() = %v, %v", statuses, err)
	}
	if err := client.SendPrompt(context.Background(), "ses_1", "/work/a", "continue"); err != nil {
		t.Fatal(err)
	}
	if promptDirectory != "/work/a" || promptText != "continue" {
		t.Fatalf("prompt directory=%q text=%q", promptDirectory, promptText)
	}
	if err := client.AbortSession(context.Background(), "ses_1", "/work/a"); err != nil || !aborted {
		t.Fatalf("AbortSession() aborted=%v err=%v", aborted, err)
	}
	questions, err := client.ListQuestions(context.Background(), "/work/a")
	if err != nil || len(questions) != 1 || questions[0].ID != "que_1" {
		t.Fatalf("ListQuestions() = %v, %v", questions, err)
	}
	if err := client.ReplyQuestion(context.Background(), "que_1", "/work/a", [][]string{{"answer"}}); err != nil {
		t.Fatal(err)
	}
	if len(questionAnswers) != 1 || questionAnswers[0][0] != "answer" {
		t.Fatalf("question answers = %v", questionAnswers)
	}
	if err := client.RejectQuestion(context.Background(), "que_1", "/work/a"); err != nil || !rejected {
		t.Fatalf("RejectQuestion() rejected=%v err=%v", rejected, err)
	}
	permissions, err := client.ListPermissions(context.Background(), "/work/a")
	if err != nil || len(permissions) != 1 || permissions[0].Permission != "external_directory" {
		t.Fatalf("ListPermissions() = %v, %v", permissions, err)
	}
	if err := client.ReplyPermission(context.Background(), "per_1", "/work/a", PermissionOnce); err != nil {
		t.Fatal(err)
	}
	if permissionReply != PermissionOnce {
		t.Fatalf("permission reply = %q", permissionReply)
	}
	if err := client.ReplyPermission(context.Background(), "per_1", "/work/a", PermissionReply("invalid")); err == nil {
		t.Fatal("invalid permission reply was accepted")
	}
}

func TestWatchGlobalEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/global/event" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(writer, "data: {\"directory\":\"/work/a\",\"payload\":{\"type\":\"session.status\",\"properties\":{\"sessionID\":\"ses_1\",\"status\":{\"type\":\"idle\"}}}}\n\n")
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, HTTP: &http.Client{Timeout: 2 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	var received Event
	err = client.WatchEvents(context.Background(), func(event Event) { received = event })
	if err != io.EOF {
		t.Fatalf("WatchEvents() error = %v, want EOF", err)
	}
	if received.Directory != "/work/a" || received.Type != "session.status" {
		t.Fatalf("event = %+v", received)
	}
	if !strings.Contains(string(received.Properties), "ses_1") {
		t.Fatalf("event properties = %s", received.Properties)
	}
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
