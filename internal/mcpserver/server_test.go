package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kcompton15/Todo-Board/internal/store"
)

func TestServerProtocolAndToolCalls(t *testing.T) {
	t.Parallel()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tasks" {
			t.Fatalf("request = %s %s; want GET /api/tasks", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("kind"); got != "review" {
			t.Fatalf("kind = %q; want review", got)
		}
		writeTestJSON(t, w, http.StatusOK, map[string]any{"tasks": []any{
			map[string]any{
				"id": "task-123", "title": "Review task", "lane": "today", "kind": "review", "priority": 2,
				"project": "Web", "tag": "Review", "notes": "", "source": "codex", "order": 1000,
				"createdAt": "2026-09-17T12:00:00Z", "updatedAt": "2026-09-17T12:00:00Z",
				"subtasks": []any{},
			},
		}})
	}))
	t.Cleanup(api.Close)
	client, err := NewBoardClient(api.URL, api.Client())
	if err != nil {
		t.Fatalf("NewBoardClient: %v", err)
	}
	server := New(client)

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"ping-1","method":"ping"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_tasks","arguments":{"query":"Review","kind":"review"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	responses := decodeResponseLines(t, output.String())
	if len(responses) != 4 {
		t.Fatalf("got %d responses; want 4 (notification must be silent):\n%s", len(responses), output.String())
	}
	initialize := responses[0]
	resultMap := requireResultMap(t, initialize)
	if resultMap["protocolVersion"] != "2025-11-25" {
		t.Fatalf("initialize protocolVersion = %v", resultMap["protocolVersion"])
	}
	if initialize["id"].(float64) != 1 {
		t.Fatalf("initialize id = %v", initialize["id"])
	}

	if got := requireResultMap(t, responses[1]); len(got) != 0 {
		t.Fatalf("ping result = %v; want empty object", got)
	}

	toolsValue := requireResultMap(t, responses[2])["tools"]
	tools, ok := toolsValue.([]any)
	if !ok || len(tools) != 10 {
		t.Fatalf("tools = %#v; want 10 tools", toolsValue)
	}
	toolNames := make(map[string]bool)
	for _, rawTool := range tools {
		tool := rawTool.(map[string]any)
		toolNames[tool["name"].(string)] = true
	}
	for _, name := range []string{"get_activity", "list_tasks", "get_task", "create_task", "update_task", "add_subtask", "update_subtask"} {
		if !toolNames[name] {
			t.Errorf("tools/list missing %q", name)
		}
	}

	callResult := requireResultMap(t, responses[3])
	if callResult["isError"] == true {
		t.Fatalf("list_tasks returned error: %v", callResult)
	}
	structured := callResult["structuredContent"].(map[string]any)
	if got := len(structured["tasks"].([]any)); got != 1 {
		t.Fatalf("structured task count = %d; want 1", got)
	}
}

func TestServerReturnsToolErrorsWithoutStopping(t *testing.T) {
	t.Parallel()

	client, err := NewBoardClient("http://127.0.0.1:1", &http.Client{})
	if err != nil {
		t.Fatalf("NewBoardClient: %v", err)
	}
	server := New(client)
	input := strings.Join([]string{
		`not-json`,
		`{"jsonrpc":"2.0","id":1,"method":"unknown"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"unknown_tool","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_task","arguments":{"title":""}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_tasks","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"list_tasks","arguments":{"unexpected":true}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"ping"}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	responses := decodeResponseLines(t, output.String())
	if len(responses) != 7 {
		t.Fatalf("got %d responses; want 7:\n%s", len(responses), output.String())
	}
	requireRPCErrorCode(t, responses[0], -32700)
	requireRPCErrorCode(t, responses[1], -32601)
	for i := 2; i <= 5; i++ {
		result := requireResultMap(t, responses[i])
		if result["isError"] != true {
			t.Errorf("response %d isError = %v; want true", i, result["isError"])
		}
	}
	if got := requireResultMap(t, responses[6]); len(got) != 0 {
		t.Fatalf("final ping result = %v; server did not continue cleanly", got)
	}
}

func TestServerMutationToolCalls(t *testing.T) {
	t.Parallel()

	var created CreateTaskInput
	var patched UpdateTaskInput
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/task-1":
			writeTestJSON(t, w, http.StatusOK, testTask("task-1", "today"))
		case r.Method == http.MethodPost && r.URL.Path == "/api/tasks":
			decodeTestJSON(t, r, &created)
			if created.Source != claudeChatSource {
				t.Errorf("create source = %q; want %q", created.Source, claudeChatSource)
			}
			task := testTask("task-2", "backlog")
			task["kind"] = created.Kind
			writeTestJSON(t, w, http.StatusCreated, map[string]any{"tasks": []any{task}})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/tasks/task-1":
			decodeTestJSON(t, r, &patched)
			task := testTask("task-1", "doing")
			task["kind"] = valueOrZero(patched.Kind)
			writeTestJSON(t, w, http.StatusOK, task)
		case r.Method == http.MethodPost && r.URL.Path == "/api/tasks/task-1/subtasks":
			writeTestJSON(t, w, http.StatusCreated, map[string]any{"id": "sub-1", "title": "Inspect API", "lane": "doing", "done": false})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/tasks/task-1/subtasks/sub-1":
			writeTestJSON(t, w, http.StatusOK, map[string]any{"id": "sub-1", "title": "Inspect API", "lane": "done", "done": true})
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	t.Cleanup(api.Close)
	client, err := NewBoardClient(api.URL, api.Client())
	if err != nil {
		t.Fatalf("NewBoardClient: %v", err)
	}
	server := New(client)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_task","arguments":{"id":"task-1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_task","arguments":{"title":"New outcome","kind":"probe"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"update_task","arguments":{"id":"task-1","lane":"doing","kind":"followup"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"add_subtask","arguments":{"id":"task-1","title":"Inspect API","lane":"doing"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"update_subtask","arguments":{"id":"task-1","subtaskId":"sub-1","lane":"done"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	responses := decodeResponseLines(t, output.String())
	if len(responses) != 5 {
		t.Fatalf("got %d responses; want 5", len(responses))
	}
	for i, response := range responses {
		result := requireResultMap(t, response)
		if result["isError"] == true {
			t.Errorf("response %d returned tool error: %v", i, result)
		}
	}
	if created.Kind != "probe" || valueOrZero(patched.Kind) != "followup" {
		t.Fatalf("kind inputs were not forwarded: create=%+v patch=%+v", created, patched)
	}
}

func TestServerKindSchemasAndVisibleValidation(t *testing.T) {
	t.Parallel()

	definitions := toolDefinitions()
	for _, name := range []string{"list_tasks", "create_task", "update_task"} {
		var definition *toolDefinition
		for i := range definitions {
			if definitions[i].Name == name {
				definition = &definitions[i]
				break
			}
		}
		if definition == nil {
			t.Fatalf("missing tool definition %q", name)
		}
		properties := definition.InputSchema["properties"].(map[string]any)
		kind := properties["kind"].(map[string]any)
		if got := kind["enum"]; !reflect.DeepEqual(got, store.KindOrder) {
			t.Fatalf("%s kind enum = %#v; want %#v", name, got, store.KindOrder)
		}
	}

	client, err := NewBoardClient("http://127.0.0.1:1", &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	server := New(client)
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_task","arguments":{"title":"bad","kind":"nonsense"}}}` + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	result := requireResultMap(t, decodeResponseLines(t, output.String())[0])
	if result["isError"] != true {
		t.Fatalf("invalid kind result = %#v; want visible tool error", result)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(content, `invalid kind "nonsense"`) {
		t.Fatalf("invalid kind error = %q", content)
	}
}

func TestLifecycleGuidanceParity(t *testing.T) {
	t.Parallel()

	descriptions := map[string]string{}
	for _, definition := range toolDefinitions() {
		descriptions[definition.Name] = definition.Description
	}
	for _, name := range []string{"list_tasks", "create_task", "update_task"} {
		if descriptions[name] == "" {
			t.Fatalf("missing description for %q", name)
		}
	}

	sources := map[string]string{
		"MCP instructions and task tools": strings.Join([]string{
			mcpInstructions,
			descriptions["list_tasks"],
			descriptions["create_task"],
			descriptions["update_task"],
		}, "\n"),
	}
	for name, path := range map[string]string{
		"README":                  filepath.Join("..", "..", "README.md"),
		"agent integration guide": filepath.Join("..", "..", "docs", "AGENT-INTEGRATION.md"),
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sources[name] = string(contents)
	}

	for name, source := range sources {
		normalized := strings.ToLower(source)
		for _, phrase := range []string{
			"plain answer",
			"work",
			"review",
			"merge request url",
			"followup",
			"human must do",
			"probe",
			"real investigation",
			"verified",
			"next_step",
			"never move",
			"do not ask",
		} {
			if !strings.Contains(normalized, phrase) {
				t.Errorf("%s is missing lifecycle phrase %q", name, phrase)
			}
		}
	}
}

func TestUpdateTaskRequiresAChange(t *testing.T) {
	t.Parallel()

	client, err := NewBoardClient("http://127.0.0.1:1", &http.Client{})
	if err != nil {
		t.Fatalf("NewBoardClient: %v", err)
	}
	server := New(client)
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"update_task","arguments":{"id":"task-1"}}}` + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	responses := decodeResponseLines(t, output.String())
	result := requireResultMap(t, responses[0])
	if result["isError"] != true {
		t.Fatalf("result = %v; want tool error", result)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(content, "at least one") {
		t.Fatalf("error text = %q; want actionable message", content)
	}
}

func decodeResponseLines(t *testing.T, output string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	responses := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		responses = append(responses, response)
	}
	return responses
}

func requireResultMap(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("response has no object result: %v", response)
	}
	return result
}

func requireRPCErrorCode(t *testing.T, response map[string]any, want float64) {
	t.Helper()
	errValue, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error: %v", response)
	}
	if errValue["code"] != want {
		t.Fatalf("error code = %v; want %v", errValue["code"], want)
	}
}

func testTask(id, lane string) map[string]any {
	return map[string]any{
		"id": id, "title": "Task", "lane": lane, "kind": "work", "priority": 2, "project": "Web",
		"tag": "Review", "notes": "", "source": claudeChatSource, "order": 1000,
		"createdAt": "2026-09-17T12:00:00Z", "updatedAt": "2026-09-17T12:00:00Z",
		"subtasks": []any{},
	}
}
