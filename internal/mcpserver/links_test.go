package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/kcompton15/Todo-Board/internal/httpapi"
	"github.com/kcompton15/Todo-Board/internal/store"
	"net/http/httptest"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if err := store.SetJiraSite("example.atlassian.net"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestMCPMetadataDispatch(t *testing.T) {
	s, _ := store.New(t.TempDir())
	api := httptest.NewServer(httpapi.New(s, httpapi.NewHub(), nil, nil).Handler())
	defer api.Close()
	client, _ := NewBoardClient(api.URL, api.Client())
	server := New(client)
	call := func(name string, args any, wantError bool) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(args)
		result, err := server.dispatchTool(context.Background(), name, raw)
		if (err != nil) != wantError {
			t.Fatalf("%s: %v", name, err)
		}
		return result
	}
	result := call("create_task", map[string]any{"title": "review", "kind": "review", "links": []any{map[string]any{"ref": "PROJ-1"}}, "agentContext": "memo", "reconcileMode": "manual"}, false)
	task := result["task"].(Task)
	if len(task.Links) != 1 || task.AgentContext != "memo" || task.ReconcileMode != "manual" {
		t.Fatal(task)
	}
	call("update_task", map[string]any{"id": task.ID, "agentContext": "", "expectedRevision": task.Revision}, false)
	result = call("link_task", map[string]any{"id": task.ID, "link": map[string]any{"ref": "group/api!1"}, "expectedRevision": 2}, false)
	link := result["link"].(store.Link)
	call("unlink_task", map[string]any{"id": task.ID, "linkId": link.ID, "expectedRevision": 2}, true)
	result = call("log_review_delivered", map[string]any{"id": task.ID, "text": "Finalized findings", "expectedRevision": 3}, false)
	receipt := result["receipt"].(store.WorkLogEntry)
	if receipt.Kind != store.WorkLogReviewDelivered || receipt.Actor != "claude-chat" {
		t.Fatal(receipt)
	}
	call("unlink_task", map[string]any{"id": task.ID, "linkId": link.ID, "expectedRevision": 3}, false)
	call("update_task", map[string]any{"id": task.ID, "links": []any{}, "reconcileMode": "automatic", "expectedRevision": 4}, false)
	got, _ := s.Get(task.ID)
	if len(got.Links) != 0 || got.ReconcileMode != "automatic" || got.AgentContext != "" {
		t.Fatal(got)
	}
	call("link_task", map[string]any{"id": task.ID, "link": map[string]any{"ref": "PROJ-2", "stateChangedAt": "forged"}}, true)
}
