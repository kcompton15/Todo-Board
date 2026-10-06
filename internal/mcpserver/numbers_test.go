package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kcompton15/Todo-Board/internal/httpapi"
	"github.com/kcompton15/Todo-Board/internal/store"
)

func TestToolsAcceptCardAndStepNumbers(t *testing.T) {
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
	if _, err := client.CreateTask(ctx, CreateTaskInput{Title: "filler"}); err != nil {
		t.Fatal(err)
	}
	result, err := client.CreateTask(ctx, CreateTaskInput{Title: "numbered", Lane: "today", Kind: "review", Subtasks: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	created := result.Task
	if created.Number != 2 || created.Subtasks[1].Number != 2 {
		t.Fatalf("created = %#v", created)
	}

	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		data, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		result, err := server.dispatchTool(ctx, name, data)
		if err != nil {
			t.Fatalf("%s %v: %v", name, args, err)
		}
		return result
	}

	for _, ref := range []string{"2", "#2", created.ID} {
		task := call("get_task", map[string]any{"id": ref})["task"].(Task)
		if task.ID != created.ID || task.Number != 2 {
			t.Fatalf("get_task %q = %#v", ref, task)
		}
	}
	payload := mustJSON(call("get_task", map[string]any{"id": "#2"}))
	if !strings.Contains(payload, `"number":2`) {
		t.Fatalf("tool output lacks number: %s", payload)
	}
	if task := call("update_task", map[string]any{"id": "#2", "lane": "doing"})["task"].(Task); task.Lane != "doing" {
		t.Fatalf("update_task by number = %#v", task)
	}
	if sub := call("add_subtask", map[string]any{"id": "2", "title": "c"})["subtask"].(Subtask); sub.Number != 3 {
		t.Fatalf("add_subtask by number = %#v", sub)
	}
	for _, step := range []string{"1", "#2"} {
		if sub := call("update_subtask", map[string]any{"id": "#2", "subtaskId": step, "done": true})["subtask"].(Subtask); !sub.Done {
			t.Fatalf("update_subtask %q = %#v", step, sub)
		}
	}
	if link := call("link_task", map[string]any{"id": "#2", "link": map[string]any{"ref": "ABC-7"}})["link"].(store.Link); link.Ref != "ABC-7" {
		t.Fatalf("link_task by number = %#v", link)
	}
	if receipt := call("log_review_delivered", map[string]any{"id": "2", "text": "done"})["receipt"].(store.WorkLogEntry); receipt.TaskID != created.ID {
		t.Fatalf("log_review_delivered by number = %#v", receipt)
	}
	activity := call("get_activity", map[string]any{"id": "#2"})["activity"].([]any)
	if len(activity) == 0 {
		t.Fatal("get_activity by number returned nothing")
	}
	for _, event := range activity {
		if event.(map[string]any)["taskId"] != created.ID {
			t.Fatalf("get_activity by number leaked %v", event)
		}
	}

	for _, bad := range []string{"#", "##2", "2/..", "2.x"} {
		data, err := json.Marshal(map[string]any{"id": bad})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := server.dispatchTool(ctx, "get_task", data); err == nil {
			t.Fatalf("get_task %q succeeded", bad)
		}
	}
}
