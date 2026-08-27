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
