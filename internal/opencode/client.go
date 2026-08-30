package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Adapter interface {
	ListModels(context.Context) ([]Model, error)
	ListProjects(context.Context) ([]Project, error)
	ListDirectories(context.Context) ([]string, error)
	ListSessions(context.Context, string) ([]Session, error)
	CreateSession(context.Context, string, string) (Session, error)
	GetSession(context.Context, string, string) (Session, error)
	GetSessionStatuses(context.Context, string) (map[string]SessionStatus, error)
	GetMessages(context.Context, string, string, int) ([]Message, error)
	SendPrompt(context.Context, string, string, string, *ModelRef) error
	AbortSession(context.Context, string, string) error
	ListQuestions(context.Context, string) ([]QuestionRequest, error)
	ReplyQuestion(context.Context, string, string, [][]string) error
	RejectQuestion(context.Context, string, string) error
	ListPermissions(context.Context, string) ([]PermissionRequest, error)
	ReplyPermission(context.Context, string, string, PermissionReply) error
	WatchEvents(context.Context, func(Event)) error
}

func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	var response struct {
		Providers []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Models map[string]struct {
				ID           string                     `json:"id"`
				Name         string                     `json:"name"`
				Status       string                     `json:"status"`
				Variants     map[string]json.RawMessage `json:"variants"`
				Capabilities struct {
					Reasoning  bool `json:"reasoning"`
					Attachment bool `json:"attachment"`
				} `json:"capabilities"`
				Limit struct {
					Context int64 `json:"context"`
				} `json:"limit"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := c.getJSON(ctx, "/config/providers", nil, "", &response); err != nil {
		return nil, err
	}
	var result []Model
	for _, provider := range response.Providers {
		models := make([]Model, 0, len(provider.Models))
		for key, item := range provider.Models {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				id = key
			}
			name := strings.TrimSpace(item.Name)
			if name == "" {
				name = id
			}
			variants := make([]string, 0, len(item.Variants))
			for variant := range item.Variants {
				variants = append(variants, variant)
			}
			sort.Strings(variants)
			models = append(models, Model{
				ProviderID: provider.ID, ProviderName: provider.Name, ID: id, Name: name,
				Status: item.Status, Variants: variants, Reasoning: item.Capabilities.Reasoning,
				Attachment: item.Capabilities.Attachment, ContextLimit: item.Limit.Context,
			})
		}
		sort.SliceStable(models, func(i, j int) bool {
			return strings.ToLower(models[i].Name) < strings.ToLower(models[j].Name)
		})
		result = append(result, models...)
	}
	return result, nil
}

type Client struct {
	baseURL   *url.URL
	directory string
	username  string
	password  string
	http      *http.Client
}

type ClientOptions struct {
	BaseURL   string
	Directory string
	Username  string
	Password  string
	HTTP      *http.Client
}

func NewClient(options ClientOptions) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimRight(options.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse OpenCode base URL: %w", err)
	}
	httpClient := options.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL:   baseURL,
		directory: options.Directory,
		username:  options.Username,
		password:  options.Password,
		http:      httpClient,
	}, nil
}

func (c *Client) ListDirectories(ctx context.Context) ([]string, error) {
	if c.directory != "" {
		return []string{c.directory}, nil
	}
	projects, err := c.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var directories []string
	for _, project := range projects {
		items := append([]string{project.Worktree}, project.Sandboxes...)
		for _, directory := range items {
			directory = strings.TrimSpace(directory)
			if directory == "" {
				continue
			}
			if _, ok := seen[directory]; ok {
				continue
			}
			seen[directory] = struct{}{}
			directories = append(directories, directory)
		}
	}
	return directories, nil
}

func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	if c.directory != "" {
		return []Project{{ID: "configured", Worktree: c.directory}}, nil
	}
	var projects []Project
	if err := c.getJSON(ctx, "/project", nil, "", &projects); err != nil {
		return nil, err
	}
	return projects, nil
}

func (c *Client) ListSessions(ctx context.Context, directory string) ([]Session, error) {
	query := url.Values{"limit": []string{"1000"}}
	var sessions []Session
	if err := c.getJSON(ctx, "/session", query, directory, &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

func (c *Client) GetSession(ctx context.Context, sessionID, directory string) (Session, error) {
	var session Session
	if err := c.getJSON(ctx, "/session/"+url.PathEscape(sessionID), nil, directory, &session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (c *Client) CreateSession(ctx context.Context, directory, title string) (Session, error) {
	body := struct {
		Title string `json:"title,omitempty"`
	}{Title: strings.TrimSpace(title)}
	var session Session
	if err := c.doJSON(ctx, http.MethodPost, "/session", nil, directory, body, &session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (c *Client) GetSessionStatuses(ctx context.Context, directory string) (map[string]SessionStatus, error) {
	statuses := make(map[string]SessionStatus)
	if err := c.getJSON(ctx, "/session/status", nil, directory, &statuses); err != nil {
		return nil, err
	}
	return statuses, nil
}

func (c *Client) GetMessages(ctx context.Context, sessionID, directory string, limit int) ([]Message, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	var messages []Message
	path := "/session/" + url.PathEscape(sessionID) + "/message"
	if err := c.getJSON(ctx, path, query, directory, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func (c *Client) SendPrompt(ctx context.Context, sessionID, directory, text string, selected *ModelRef) error {
	body := struct {
		Model *struct {
			ProviderID string `json:"providerID"`
			ModelID    string `json:"modelID"`
		} `json:"model,omitempty"`
		Variant string `json:"variant,omitempty"`
		Parts   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}{}
	if selected != nil && selected.ProviderID != "" && selected.ModelID != "" {
		body.Model = &struct {
			ProviderID string `json:"providerID"`
			ModelID    string `json:"modelID"`
		}{ProviderID: selected.ProviderID, ModelID: selected.ModelID}
		body.Variant = selected.Variant
	}
	body.Parts = append(body.Parts, struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: text})
	path := "/session/" + url.PathEscape(sessionID) + "/prompt_async"
	return c.doJSON(ctx, http.MethodPost, path, nil, directory, body, nil)
}

func (c *Client) AbortSession(ctx context.Context, sessionID, directory string) error {
	path := "/session/" + url.PathEscape(sessionID) + "/abort"
	return c.doJSON(ctx, http.MethodPost, path, nil, directory, nil, nil)
}

func (c *Client) ListQuestions(ctx context.Context, directory string) ([]QuestionRequest, error) {
	var questions []QuestionRequest
	if err := c.getJSON(ctx, "/question", nil, directory, &questions); err != nil {
		return nil, err
	}
	return questions, nil
}

func (c *Client) ReplyQuestion(ctx context.Context, requestID, directory string, answers [][]string) error {
	body := struct {
		Answers [][]string `json:"answers"`
	}{Answers: answers}
	path := "/question/" + url.PathEscape(requestID) + "/reply"
	return c.doJSON(ctx, http.MethodPost, path, nil, directory, body, nil)
}

func (c *Client) RejectQuestion(ctx context.Context, requestID, directory string) error {
	path := "/question/" + url.PathEscape(requestID) + "/reject"
	return c.doJSON(ctx, http.MethodPost, path, nil, directory, nil, nil)
}

func (c *Client) ListPermissions(ctx context.Context, directory string) ([]PermissionRequest, error) {
	var permissions []PermissionRequest
	if err := c.getJSON(ctx, "/permission", nil, directory, &permissions); err != nil {
		return nil, err
	}
	return permissions, nil
}

func (c *Client) ReplyPermission(ctx context.Context, requestID, directory string, reply PermissionReply) error {
	if !reply.Valid() {
		return fmt.Errorf("invalid OpenCode permission reply %q", reply)
	}
	body := struct {
		Reply PermissionReply `json:"reply"`
	}{Reply: reply}
	path := "/permission/" + url.PathEscape(requestID) + "/reply"
	return c.doJSON(ctx, http.MethodPost, path, nil, directory, body, nil)
}

func (c *Client) WatchEvents(ctx context.Context, handler func(Event)) error {
	path := "/event"
	if c.directory == "" {
		path = "/global/event"
	}
	request, err := c.newRequest(ctx, http.MethodGet, path, nil, c.directory, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/event-stream")

	streamClient := *c.http
	streamClient.Timeout = 0
	response, err := streamClient.Do(request)
	if err != nil {
		return fmt.Errorf("subscribe to OpenCode events: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response)
	}

	reader := bufio.NewReader(response.Body)
	var data []string
	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(data) > 0 {
				var event Event
				payload := []byte(strings.Join(data, "\n"))
				if c.directory == "" {
					var global struct {
						Directory string          `json:"directory"`
						Payload   json.RawMessage `json:"payload"`
					}
					if json.Unmarshal(payload, &global) == nil {
						event.Directory = global.Directory
						_ = json.Unmarshal(global.Payload, &event)
					}
				} else {
					event.Directory = c.directory
					_ = json.Unmarshal(payload, &event)
				}
				if event.Type != "" {
					handler(event)
				}
				data = data[:0]
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return ctx.Err()
			}
			if errors.Is(readErr, io.EOF) {
				return io.EOF
			}
			return fmt.Errorf("read OpenCode event stream: %w", readErr)
		}
	}
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, directory string, output any) error {
	return c.doJSON(ctx, http.MethodGet, path, query, directory, nil, output)
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, directory string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode OpenCode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := c.newRequest(ctx, method, path, query, directory, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("OpenCode %s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode OpenCode response: %w", err)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, directory string, body io.Reader) (*http.Request, error) {
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	values := endpoint.Query()
	for key, items := range query {
		for _, item := range items {
			values.Add(key, item)
		}
	}
	if directory == "" {
		directory = c.directory
	}
	if directory != "" {
		values.Set("directory", directory)
	}
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create OpenCode request: %w", err)
	}
	if c.password != "" {
		request.SetBasicAuth(c.username, c.password)
	}
	return request, nil
}

func responseError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		detail = http.StatusText(response.StatusCode)
	}
	return fmt.Errorf("OpenCode returned %s: %s", response.Status, detail)
}
