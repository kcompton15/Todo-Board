package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/kcompton15/Todo-Board/internal/store"
)

const (
	claudeChatSource = "claude-chat"
	maxAPIResponse   = 16 << 20 // Accommodate 50 bounded activity entries with Unicode field previews.
)

// Task is the board's task representation returned by MCP tools.
type Task = store.Task

// Subtask is the board's subtask representation returned by MCP tools.
type Subtask = store.Subtask

// ListTasksInput contains optional filters for a board query.
type ListTasksInput struct {
	Query    string `json:"query,omitempty"`
	Lane     string `json:"lane,omitempty"`
	Project  string `json:"project,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

// GetTaskInput identifies a task by its full ID.
type GetTaskInput struct {
	ID string `json:"id"`
}

// CreateTaskInput contains editable values for a new task. Source is always
// overwritten with claude-chat before the request reaches the board.
type CreateTaskInput struct {
	Links         *[]store.LinkInput `json:"links,omitempty"`
	AgentContext  *string            `json:"agentContext,omitempty"`
	ReconcileMode *string            `json:"reconcileMode,omitempty"`
	Title         string             `json:"title"`
	Lane          string             `json:"lane,omitempty"`
	Kind          string             `json:"kind,omitempty"`
	Priority      int                `json:"priority,omitempty"`
	Project       string             `json:"project,omitempty"`
	Tag           string             `json:"tag,omitempty"`
	Notes         string             `json:"notes,omitempty"`
	Subtasks      []string           `json:"subtasks,omitempty"`
	Source        string             `json:"source,omitempty"`
}

// UpdateTaskInput identifies a task and contains only fields Claude may edit.
// Pointer fields distinguish a missing value from an intentional empty value.
type UpdateTaskInput struct {
	Links            *[]store.LinkInput `json:"links,omitempty"`
	AgentContext     *string            `json:"agentContext,omitempty"`
	ReconcileMode    *string            `json:"reconcileMode,omitempty"`
	ExpectedRevision *uint64            `json:"expectedRevision,omitempty"`
	ID               string             `json:"-"`
	Title            *string            `json:"title,omitempty"`
	Lane             *string            `json:"lane,omitempty"`
	Kind             *string            `json:"kind,omitempty"`
	Priority         *int               `json:"priority,omitempty"`
	Project          *string            `json:"project,omitempty"`
	Tag              *string            `json:"tag,omitempty"`
	Notes            *string            `json:"notes,omitempty"`
}

// AddSubtaskInput identifies a task and supplies the new step's title.
type AddSubtaskInput struct {
	ExpectedRevision *uint64 `json:"expectedRevision,omitempty"`
	ID               string  `json:"id"`
	Title            string  `json:"title"`
	Lane             string  `json:"lane,omitempty"`
}

// UpdateSubtaskInput identifies a subtask and contains fields to change.
type UpdateSubtaskInput struct {
	ExpectedRevision *uint64 `json:"expectedRevision,omitempty"`
	ID               string  `json:"-"`
	SubtaskID        string  `json:"-"`
	Title            *string `json:"title,omitempty"`
	Lane             *string `json:"lane,omitempty"`
	Done             *bool   `json:"done,omitempty"`
}

// BoardClient calls the existing loopback-only task board API.
type BoardClient struct {
	baseURL    *url.URL
	httpClient *http.Client
}

// NewBoardClient constructs a client for a loopback HTTP board URL.
func NewBoardClient(baseURL string, httpClient *http.Client) (*BoardClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("board URL must be an absolute HTTP URL: %w", err)
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return nil, errors.New("board URL must be an absolute HTTP URL")
	}
	if parsed.Scheme != "http" {
		return nil, errors.New("board URL must use http on the local device")
	}
	if parsed.User != nil {
		return nil, errors.New("board URL cannot contain user information")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("board URL cannot contain a path")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("board URL cannot contain a query or fragment")
	}
	if !isLoopbackHost(parsed.Hostname()) {
		return nil, errors.New("board URL must use a loopback host")
	}
	parsed.Path = ""

	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	} else if httpClient.Timeout <= 0 {
		clientCopy := *httpClient
		clientCopy.Timeout = 5 * time.Second
		httpClient = &clientCopy
	}
	return &BoardClient{baseURL: parsed, httpClient: httpClient}, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ListTasks returns tasks matching every supplied filter.
func (c *BoardClient) ListTasks(ctx context.Context, input ListTasksInput) ([]Task, error) {
	query := make(url.Values)
	if input.Query != "" {
		query.Set("q", input.Query)
	}
	if input.Lane != "" {
		query.Set("lane", input.Lane)
	}
	if input.Project != "" {
		query.Set("project", input.Project)
	}
	if input.Tag != "" {
		query.Set("tag", input.Tag)
	}
	if input.Kind != "" {
		query.Set("kind", input.Kind)
	}
	if input.Priority != 0 {
		query.Set("priority", strconv.Itoa(input.Priority))
	}
	var response struct {
		Tasks []Task `json:"tasks"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/tasks", query, nil, &response); err != nil {
		return nil, err
	}
	if response.Tasks == nil {
		response.Tasks = []Task{}
	}
	return response.Tasks, nil
}

// GetTask returns one task by full ID.
func (c *BoardClient) GetTask(ctx context.Context, input GetTaskInput) (Task, error) {
	if err := validateID("task id", input.ID); err != nil {
		return Task{}, err
	}
	var task Task
	err := c.do(ctx, http.MethodGet, "/api/tasks/"+input.ID, nil, nil, &task)
	return task, err
}

// CreateTask creates a card whose provenance is always claude-chat.
func (c *BoardClient) CreateTask(ctx context.Context, input CreateTaskInput) (Task, error) {
	input.Source = claudeChatSource
	var response struct {
		Tasks []Task `json:"tasks"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/tasks", nil, input, &response); err != nil {
		return Task{}, err
	}
	if len(response.Tasks) != 1 {
		return Task{}, fmt.Errorf("board returned %d created tasks; want 1", len(response.Tasks))
	}
	return response.Tasks[0], nil
}

// UpdateTask changes editable fields on an existing task.
func (c *BoardClient) UpdateTask(ctx context.Context, input UpdateTaskInput) (Task, error) {
	if err := validateID("task id", input.ID); err != nil {
		return Task{}, err
	}
	if !input.hasChanges() {
		return Task{}, errors.New("update_task requires at least one field to change")
	}
	var task Task
	err := c.do(ctx, http.MethodPatch, "/api/tasks/"+input.ID, nil, input, &task, input.ExpectedRevision)
	return task, err
}

func (input UpdateTaskInput) hasChanges() bool {
	if input.Links != nil || input.AgentContext != nil || input.ReconcileMode != nil {
		return true
	}
	return input.Title != nil || input.Lane != nil || input.Priority != nil ||
		input.Project != nil || input.Tag != nil || input.Kind != nil || input.Notes != nil
}

// AddSubtask appends one step to an existing task.
func (c *BoardClient) AddSubtask(ctx context.Context, input AddSubtaskInput) (Subtask, error) {
	if err := validateID("task id", input.ID); err != nil {
		return Subtask{}, err
	}
	body := struct {
		Title string `json:"title"`
		Lane  string `json:"lane,omitempty"`
	}{Title: input.Title, Lane: input.Lane}
	var subtask Subtask
	err := c.do(ctx, http.MethodPost, "/api/tasks/"+input.ID+"/subtasks", nil, body, &subtask, input.ExpectedRevision)
	return subtask, err
}

// UpdateSubtask changes a subtask title, completion state, or both.
func (c *BoardClient) UpdateSubtask(ctx context.Context, input UpdateSubtaskInput) (Subtask, error) {
	if err := validateID("task id", input.ID); err != nil {
		return Subtask{}, err
	}
	if err := validateID("subtask id", input.SubtaskID); err != nil {
		return Subtask{}, err
	}
	if input.Title == nil && input.Lane == nil && input.Done == nil {
		return Subtask{}, errors.New("update_subtask requires at least one field to change")
	}
	var subtask Subtask
	path := "/api/tasks/" + input.ID + "/subtasks/" + input.SubtaskID
	err := c.do(ctx, http.MethodPatch, path, nil, input, &subtask, input.ExpectedRevision)
	return subtask, err
}

// GetActivity returns bounded, newest-first activity for one task or the whole board.
func (c *BoardClient) GetActivity(ctx context.Context, input GetTaskInput) (map[string]any, error) {
	if input.ID != "" {
		if err := validateID("task id", input.ID); err != nil {
			return nil, err
		}
	}
	var response map[string]any
	err := c.do(ctx, http.MethodGet, "/api/activity", url.Values{"taskId": {input.ID}, "limit": {"50"}}, nil, &response)
	return response, err
}

func validateID(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > 128 {
		return fmt.Errorf("%s is too long", field)
	}
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '-' || char == '_' {
			continue
		}
		return fmt.Errorf("%s contains an invalid character", field)
	}
	return nil
}

func (c *BoardClient) do(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	requestBody any,
	responseBody any,
	revisions ...*uint64,
) error {
	endpoint := *c.baseURL
	endpoint.Path = path
	if query != nil {
		endpoint.RawQuery = query.Encode()
	}

	var body io.Reader
	if requestBody != nil {
		payload, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encode board request: %w", err)
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("create board request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Actor", claudeChatSource)
	if len(revisions) > 0 && revisions[0] != nil {
		request.Header.Set("If-Match", strconv.FormatUint(*revisions[0], 10))
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("board is unreachable at %s: %w", c.baseURL.String(), err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponse+1))
	if err != nil {
		return fmt.Errorf("read board response: %w", err)
	}
	if len(payload) > maxAPIResponse {
		return fmt.Errorf("board response exceeds %d bytes", maxAPIResponse)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var apiError struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(payload, &apiError) == nil && apiError.Error != "" {
			return fmt.Errorf("board rejected request: %s", apiError.Error)
		}
		return fmt.Errorf("board request failed with HTTP %d", response.StatusCode)
	}
	if responseBody == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(responseBody); err != nil {
		return fmt.Errorf("decode board response: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("decode board response: unexpected trailing JSON")
	}
	return nil
}
