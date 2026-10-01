package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewBoardClientRejectsNonLoopbackURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		wantErr string
	}{
		{name: "public host", baseURL: "http://example.com", wantErr: "loopback"},
		{name: "userinfo", baseURL: "http://user@127.0.0.1:7337", wantErr: "user information"},
		{name: "path", baseURL: "http://127.0.0.1:7337/api", wantErr: "path"},
		{name: "unsupported scheme", baseURL: "file:///tmp/tasks", wantErr: "absolute HTTP"},
		{name: "missing port host", baseURL: "127.0.0.1:7337", wantErr: "absolute"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewBoardClient(test.baseURL, &http.Client{Timeout: time.Second})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("NewBoardClient(%q) error = %v; want substring %q", test.baseURL, err, test.wantErr)
			}
		})
	}
}

func TestValidateID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "generated id", value: "tmu5snid5hzavy"},
		{name: "prefix", value: "tmu5"},
		{name: "dash and underscore", value: "task-1_sub"},
		{name: "empty", value: "", wantErr: true},
		{name: "path separator", value: "task/1", wantErr: true},
		{name: "too long", value: strings.Repeat("x", 129), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateID("task id", test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateID(%q) error = %v; wantErr %v", test.value, err, test.wantErr)
			}
		})
	}
}

func TestBoardClientTaskWorkflow(t *testing.T) {
	t.Parallel()

	var created CreateTaskInput
	var patched UpdateTaskInput
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "rate confirmation" {
			t.Errorf("q = %q; want rate confirmation", got)
		}
		if got := r.URL.Query().Get("lane"); got != "doing" {
			t.Errorf("lane = %q; want doing", got)
		}
		if got := r.URL.Query().Get("kind"); got != "review" {
			t.Errorf("kind = %q; want review", got)
		}
		writeTestJSON(t, w, http.StatusOK, map[string]any{"tasks": []any{
			map[string]any{
				"id": "task-123", "title": "Review rate confirmation", "lane": "doing", "kind": "review",
				"priority": 1, "project": "API", "tag": "Review", "notes": "",
				"source": "codex", "order": 1000, "createdAt": "2026-09-17T12:00:00Z",
				"updatedAt": "2026-09-17T12:00:00Z", "subtasks": []any{},
			},
		}})
	})
	mux.HandleFunc("GET /api/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{
			"id": r.PathValue("id"), "title": "Review rate confirmation", "lane": "doing", "kind": "review",
			"priority": 1, "project": "API", "tag": "Review", "notes": "",
			"source": "codex", "order": 1000, "createdAt": "2026-09-17T12:00:00Z",
			"updatedAt": "2026-09-17T12:00:00Z", "subtasks": []any{},
		})
	})
	mux.HandleFunc("POST /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		decodeTestJSON(t, r, &created)
		if created.Source != claudeChatSource {
			t.Errorf("source = %q; want %q", created.Source, claudeChatSource)
		}
		writeTestJSON(t, w, http.StatusCreated, map[string]any{"tasks": []any{
			map[string]any{
				"id": "task-456", "title": created.Title, "lane": created.Lane, "kind": created.Kind,
				"priority": created.Priority, "project": created.Project, "tag": created.Tag,
				"notes": created.Notes, "source": created.Source, "order": 1000,
				"createdAt": "2026-09-17T12:00:00Z", "updatedAt": "2026-09-17T12:00:00Z",
				"subtasks": []any{},
			},
		}})
	})
	mux.HandleFunc("PATCH /api/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "task-456" {
			t.Errorf("task id = %q; want task-456", r.PathValue("id"))
		}
		decodeTestJSON(t, r, &patched)
		writeTestJSON(t, w, http.StatusOK, map[string]any{
			"id": "task-456", "title": "Write handoff", "lane": valueOrZero(patched.Lane), "kind": valueOrZero(patched.Kind),
			"priority": 2, "project": "Internal Tools", "tag": "Planning", "notes": "",
			"source": claudeChatSource, "order": 1000, "createdAt": "2026-09-17T12:00:00Z",
			"updatedAt": "2026-09-17T12:05:00Z", "subtasks": []any{},
		})
	})
	mux.HandleFunc("POST /api/tasks/{id}/subtasks", func(w http.ResponseWriter, r *http.Request) {
		var input AddSubtaskInput
		decodeTestJSON(t, r, &input)
		writeTestJSON(t, w, http.StatusCreated, map[string]any{
			"id": "sub-1", "title": input.Title, "lane": input.Lane, "done": false,
		})
	})
	mux.HandleFunc("PATCH /api/tasks/{id}/subtasks/{subtaskID}", func(w http.ResponseWriter, r *http.Request) {
		var input UpdateSubtaskInput
		decodeTestJSON(t, r, &input)
		writeTestJSON(t, w, http.StatusOK, map[string]any{
			"id": r.PathValue("subtaskID"), "title": "Inspect API",
			"lane": valueOrZero(input.Lane), "done": valueOrZero(input.Done),
		})
	})

	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)
	client, err := NewBoardClient(api.URL, api.Client())
	if err != nil {
		t.Fatalf("NewBoardClient: %v", err)
	}
	ctx := context.Background()

	tasks, err := client.ListTasks(ctx, ListTasksInput{Query: "rate confirmation", Lane: "doing", Kind: "review"})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "task-123" {
		t.Fatalf("ListTasks = %+v; want task-123", tasks)
	}
	task, err := client.GetTask(ctx, GetTaskInput{ID: "task-123"})
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.ID != "task-123" {
		t.Fatalf("GetTask = %+v; want task-123", task)
	}

	createdTask, err := client.CreateTask(ctx, CreateTaskInput{
		Title: "Write handoff", Lane: "today", Kind: "probe", Priority: 2, Project: "Internal Tools", Tag: "Planning",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if createdTask.ID != "task-456" || createdTask.Kind != "probe" || created.Source != claudeChatSource {
		t.Fatalf("CreateTask = %+v, request source = %q", createdTask, created.Source)
	}

	lane := "doing"
	kind := "followup"
	updatedTask, err := client.UpdateTask(ctx, UpdateTaskInput{ID: "task-456", Lane: &lane, Kind: &kind})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if updatedTask.Lane != "doing" || updatedTask.Kind != "followup" || valueOrZero(patched.Kind) != "followup" || patched.ID != "" {
		t.Fatalf("UpdateTask = %+v, request = %+v", updatedTask, patched)
	}

	subtask, err := client.AddSubtask(ctx, AddSubtaskInput{ID: "task-456", Title: "Inspect API", Lane: "doing"})
	if err != nil {
		t.Fatalf("AddSubtask: %v", err)
	}
	if subtask.ID != "sub-1" || subtask.Lane != "doing" || subtask.Done {
		t.Fatalf("AddSubtask = %+v", subtask)
	}

	blocked := "blocked"
	subtask, err = client.UpdateSubtask(ctx, UpdateSubtaskInput{
		ID: "task-456", SubtaskID: "sub-1", Lane: &blocked,
	})
	if err != nil {
		t.Fatalf("UpdateSubtask: %v", err)
	}
	if subtask.Lane != "blocked" || subtask.Done {
		t.Fatalf("UpdateSubtask = %+v; want blocked", subtask)
	}
}

func TestBoardClientSurfacesAPIError(t *testing.T) {
	t.Parallel()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, w, http.StatusBadRequest, map[string]string{"error": "invalid lane"})
	}))
	t.Cleanup(api.Close)
	client, err := NewBoardClient(api.URL, api.Client())
	if err != nil {
		t.Fatalf("NewBoardClient: %v", err)
	}

	_, err = client.ListTasks(context.Background(), ListTasksInput{})
	if err == nil || !strings.Contains(err.Error(), "invalid lane") {
		t.Fatalf("ListTasks error = %v; want API message", err)
	}
}

func TestBoardClientRejectsInvalidResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr string
	}{
		{
			name: "malformed JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"tasks":`))
			},
			wantErr: "decode board response",
		},
		{
			name: "oversized body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(bytes.Repeat([]byte("x"), maxAPIResponse+1))
			},
			wantErr: "exceeds",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			api := httptest.NewServer(test.handler)
			t.Cleanup(api.Close)
			client, err := NewBoardClient(api.URL, api.Client())
			if err != nil {
				t.Fatalf("NewBoardClient: %v", err)
			}
			_, err = client.ListTasks(context.Background(), ListTasksInput{})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ListTasks error = %v; want substring %q", err, test.wantErr)
			}
		})
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func decodeTestJSON(t *testing.T, r *http.Request, value any) {
	t.Helper()
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(value); err != nil {
		t.Fatalf("decode request: %v", err)
	}
}

func valueOrZero[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}
