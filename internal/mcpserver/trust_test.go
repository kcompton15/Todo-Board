package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kcompton15/Todo-Board/internal/httpapi"
	"github.com/kcompton15/Todo-Board/internal/store"
)

func TestMCPTrustFeaturesEndToEnd(t *testing.T) {
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(httpapi.New(s, httpapi.NewHub(), nil, nil).Handler())
	defer api.Close()
	client, err := NewBoardClient(api.URL, api.Client())
	if err != nil {
		t.Fatal(err)
	}
	server := New(client)
	ctx := context.Background()
	task, err := client.CreateTask(ctx, CreateTaskInput{Title: "MCP task", Lane: "today", Kind: "review", Subtasks: []string{"Step"}})
	if err != nil {
		t.Fatal(err)
	}
	if task.Revision != 1 || task.Kind != "review" {
		t.Fatalf("create revision %d", task.Revision)
	}
	args, _ := json.Marshal(map[string]any{"id": task.ID, "lane": "done", "expectedRevision": 1})
	if _, err := server.dispatchTool(ctx, "update_task", args); err == nil || !strings.Contains(err.Error(), "unfinished subtasks") {
		t.Fatalf("completion: %v", err)
	}
	args, _ = json.Marshal(map[string]any{"id": task.ID, "subtaskId": task.Subtasks[0].ID, "done": true, "expectedRevision": 1})
	if _, err := server.dispatchTool(ctx, "update_subtask", args); err != nil {
		t.Fatal(err)
	}
	args, _ = json.Marshal(map[string]any{"id": task.ID, "notes": "stale", "expectedRevision": 1})
	if _, err := server.dispatchTool(ctx, "update_task", args); err == nil || !strings.Contains(err.Error(), "revision conflict") {
		t.Fatalf("stale: %v", err)
	}
	args, _ = json.Marshal(map[string]any{"id": task.ID, "title": "another", "expectedRevision": 1})
	if _, err := server.dispatchTool(ctx, "add_subtask", args); err == nil || !strings.Contains(err.Error(), "revision conflict") {
		t.Fatalf("stale add: %v", err)
	}
	args, _ = json.Marshal(map[string]any{"id": task.ID})
	result, err := server.dispatchTool(ctx, "get_activity", args)
	if err != nil {
		t.Fatal(err)
	}
	if len(result["activity"].([]any)) != 2 {
		t.Fatalf("history: %#v", result)
	}
	for _, event := range s.Activity(task.ID) {
		if event.Actor != claudeChatSource {
			t.Fatalf("actor: %#v", event)
		}
	}
	got, _ := s.Get(task.ID)
	if got.Notes != "" || got.Revision != 2 || !got.Subtasks[0].Done {
		t.Fatalf("state: %#v", got)
	}
}

type handlerTransport struct {
	handler http.Handler
}

func (transport handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	transport.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

func TestMCPUpdateTaskEmptyKindResetsToWork(t *testing.T) {
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(s, httpapi.NewHub(), nil, nil).Handler()
	client, err := NewBoardClient("http://127.0.0.1", &http.Client{Transport: handlerTransport{handler: handler}})
	if err != nil {
		t.Fatal(err)
	}
	server := New(client)
	task, err := client.CreateTask(context.Background(), CreateTaskInput{Title: "MCP review", Kind: "review"})
	if err != nil {
		t.Fatal(err)
	}

	args, err := json.Marshal(map[string]any{"id": task.ID, "kind": "", "expectedRevision": task.Revision})
	if err != nil {
		t.Fatal(err)
	}
	result, err := server.dispatchTool(context.Background(), "update_task", args)
	if err != nil {
		t.Fatalf("update_task with empty kind: %v", err)
	}
	updated, ok := result["task"].(store.Task)
	if !ok {
		t.Fatalf("update_task result = %#v; want store.Task", result["task"])
	}
	if updated.Kind != "work" {
		t.Fatalf("update_task kind = %q; want work", updated.Kind)
	}
	persisted, err := s.Get(task.ID)
	if err != nil || persisted.Kind != "work" {
		t.Fatalf("persisted task = %#v, error = %v; want kind work", persisted, err)
	}
}
