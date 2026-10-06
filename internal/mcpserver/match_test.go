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

func TestCreateTaskAttachesByDefault(t *testing.T) {
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seed, err := s.CreateMany([]store.TaskInput{{Title: "Review ABC-778", Lane: "today"}}, "test")
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
	call := func(args map[string]any) (map[string]any, error) {
		data, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		return server.dispatchTool(context.Background(), "create_task", data)
	}

	result, err := call(map[string]any{"title": "Re-review ABC-778"})
	if err != nil {
		t.Fatal(err)
	}
	if result["attached"] != true || result["task"].(Task).ID != seed[0].ID ||
		result["message"] != "attached to #1 (Review ABC-778) as #1.1" {
		t.Fatalf("attach result = %#v", result)
	}
	if _, err := call(map[string]any{"title": "ABC-778 again", "onMatch": "reject"}); err == nil || !strings.Contains(err.Error(), "#1 already covers ABC-778") {
		t.Fatalf("reject = %v", err)
	}
	result, err = call(map[string]any{"title": "ABC-778 separate", "onMatch": "create"})
	if err != nil || result["attached"] != nil || result["task"].(Task).Number != 2 {
		t.Fatalf("create = %#v %v", result, err)
	}
	if _, err := call(map[string]any{"title": "x", "onMatch": "merge"}); err == nil {
		t.Fatal("invalid onMatch accepted")
	}
}
