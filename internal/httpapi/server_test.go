package httpapi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kcompton15/Todo-Board/internal/store"
)

func TestTaskLifecycleAndValidation(t *testing.T) {
	server, taskStore := newTestServer(t)

	response := request(t, server.URL+"/api/tasks", http.MethodPost, `{"title":"probe","lane":"today","project":"Tools","subtasks":["a","b"]}`, "application/json")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	var created struct {
		Tasks []store.Task `json:"tasks"`
	}
	decodeResponse(t, response, &created)
	if len(created.Tasks) != 1 || created.Tasks[0].Project != "Tools" {
		t.Fatalf("unexpected created response: %#v", created)
	}
	task := created.Tasks[0]

	response = request(t, server.URL+"/api/tasks/"+task.ID, http.MethodPatch, `{"lane":"doing"}`, "application/json")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("patch returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	closeResponse(t, response)

	subtaskID := task.Subtasks[0].ID
	response = request(t, server.URL+"/api/tasks/"+task.ID+"/subtasks/"+subtaskID, http.MethodPatch, `{"lane":"blocked"}`, "application/json")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("subtask patch returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	closeResponse(t, response)

	got, err := taskStore.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lane != "doing" || got.Subtasks[0].Lane != "blocked" || got.Subtasks[0].Done {
		t.Fatalf("task was not updated: %#v", got)
	}

	response = request(t, server.URL+"/api/tasks/"+task.ID, http.MethodDelete, "", "")
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete returned %d", response.StatusCode)
	}
	closeResponse(t, response)

	badInputs := []struct {
		name string
		body string
	}{
		{name: "empty title", body: `{"title":""}`},
		{name: "invalid lane", body: `{"title":"x","lane":"later"}`},
		{name: "null", body: `{"title":"x","project":null}`},
	}
	for _, bad := range badInputs {
		t.Run(bad.name, func(t *testing.T) {
			response := request(t, server.URL+"/api/tasks", http.MethodPost, bad.body, "application/json")
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", response.StatusCode, readResponseBody(t, response))
			}
			closeResponse(t, response)
		})
	}
}

func TestSubtaskLaneCreateAndValidation(t *testing.T) {
	server, taskStore := newTestServer(t)

	response := request(t, server.URL+"/api/tasks", http.MethodPost, `{"title":"parent","lane":"today"}`, "application/json")
	var created struct {
		Tasks []store.Task `json:"tasks"`
	}
	decodeResponse(t, response, &created)
	task := created.Tasks[0]

	response = request(
		t, server.URL+"/api/tasks/"+task.ID+"/subtasks", http.MethodPost,
		`{"title":"independent","lane":"blocked"}`, "application/json",
	)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("subtask create returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	var subtask store.Subtask
	decodeResponse(t, response, &subtask)
	if subtask.Lane != "blocked" || subtask.Done {
		t.Fatalf("unexpected created subtask: %#v", subtask)
	}

	response = request(
		t, server.URL+"/api/tasks/"+task.ID+"/subtasks/"+subtask.ID, http.MethodPatch,
		`{"lane":"doing","done":true}`, "application/json",
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("subtask patch returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	decodeResponse(t, response, &subtask)
	if subtask.Lane != "doing" || subtask.Done {
		t.Fatalf("lane was not authoritative over done: %#v", subtask)
	}

	response = request(
		t, server.URL+"/api/tasks/"+task.ID+"/subtasks", http.MethodPost,
		`{"title":"invalid","lane":"later"}`, "application/json",
	)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid subtask lane returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	closeResponse(t, response)

	got, err := taskStore.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Subtasks) != 1 {
		t.Fatalf("invalid create mutated task: %#v", got.Subtasks)
	}
}

func TestImportAndAtomicOps(t *testing.T) {
	server, taskStore := newTestServer(t)
	dsl := "Review webhook %API #Review !1 @today\n  - inspect retry behavior\n"
	response := request(t, server.URL+"/api/import", http.MethodPost, dsl, "text/plain")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("import returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	closeResponse(t, response)
	before := len(taskStore.List(store.Filter{}))

	body := `{"source":"codex","ops":[{"op":"create","title":"temporary"},{"op":"patch","id":"missing","lane":"doing"}]}`
	response = request(t, server.URL+"/api/ops", http.MethodPost, body, "application/json")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected invalid ops to return 400, got %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	errorBody := readResponseBody(t, response)
	if !strings.Contains(errorBody, "ops[1]") {
		t.Fatalf("failure does not name op index: %s", errorBody)
	}
	if len(taskStore.List(store.Filter{})) != before {
		t.Fatal("invalid ops batch mutated state")
	}
}

func TestTaskKindRESTAndOps(t *testing.T) {
	server, taskStore := newTestServer(t)

	response := request(t, server.URL+"/api/tasks", http.MethodPost, `{"title":"default"}`, "application/json")
	var defaults struct {
		Tasks []store.Task `json:"tasks"`
	}
	decodeResponse(t, response, &defaults)
	if len(defaults.Tasks) != 1 || defaults.Tasks[0].Kind != "work" {
		t.Fatalf("default kind response: %#v", defaults)
	}

	response = request(t, server.URL+"/api/tasks", http.MethodPost, `{"title":"review","kind":"review"}`, "application/json")
	var reviews struct {
		Tasks []store.Task `json:"tasks"`
	}
	decodeResponse(t, response, &reviews)
	if len(reviews.Tasks) != 1 || reviews.Tasks[0].Kind != "review" {
		t.Fatalf("explicit kind response: %#v", reviews)
	}

	response = request(t, server.URL+"/api/tasks?kind=review", http.MethodGet, "", "")
	var filtered struct {
		Tasks []store.Task `json:"tasks"`
	}
	decodeResponse(t, response, &filtered)
	if len(filtered.Tasks) != 1 || filtered.Tasks[0].ID != reviews.Tasks[0].ID {
		t.Fatalf("kind filter response: %#v", filtered)
	}
	response = request(t, server.URL+"/api/tasks/"+reviews.Tasks[0].ID, http.MethodPatch, `{"kind":"probe"}`, "application/json")
	var patched store.Task
	decodeResponse(t, response, &patched)
	if patched.Kind != "probe" {
		t.Fatalf("REST patch kind = %q; want probe", patched.Kind)
	}

	response = request(t, server.URL+"/api/tasks?kind=nonsense", http.MethodGet, "", "")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid kind filter returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	if body := readResponseBody(t, response); strings.TrimSpace(body) != `{"error":"invalid kind"}` {
		t.Fatalf("invalid kind response = %q", body)
	}

	response = request(t, server.URL+"/api/tasks", http.MethodPost, `{"title":"bad","kind":"nonsense"}`, "application/json")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid create kind returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	if body := readResponseBody(t, response); !strings.Contains(body, "kind must be one of work, followup, review, probe") {
		t.Fatalf("invalid create kind response = %q", body)
	}

	body := fmt.Sprintf(`{"source":"test","ops":[{"op":"patch","id":%q,"kind":"work"},{"op":"create","title":"follow up","kind":"followup"}]}`, reviews.Tasks[0].ID)
	response = request(t, server.URL+"/api/ops", http.MethodPost, body, "application/json")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("kind ops returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	closeResponse(t, response)
	patched, err := taskStore.Get(reviews.Tasks[0].ID)
	if err != nil || patched.Kind != "work" {
		t.Fatalf("ops-patched kind = %q, err = %v", patched.Kind, err)
	}
	if got := taskStore.List(store.Filter{Kind: "followup"}); len(got) != 1 || got[0].Title != "follow up" {
		t.Fatalf("created kind operation = %#v", got)
	}
}

func TestSSEInitialAndMutationEvents(t *testing.T) {
	server, _ := newTestServer(t)
	response, err := http.Get(server.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer closeResponse(t, response)
	reader := bufio.NewReader(response.Body)
	initial := readSSEFrame(t, reader)
	if !strings.Contains(initial, `"tasks":[]`) {
		t.Fatalf("unexpected initial frame: %s", initial)
	}

	response = request(t, server.URL+"/api/tasks", http.MethodPost, `{"title":"live"}`, "application/json")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	closeResponse(t, response)
	frame := readSSEFrame(t, reader)
	if !strings.Contains(frame, `"title":"live"`) {
		t.Fatalf("mutation was not broadcast: %s", frame)
	}
}

func TestAtomicOpsWorkLogClosePublishesOneSSEUpdate(t *testing.T) {
	taskStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := taskStore.CreateMany([]store.TaskInput{{
		Title: "merged review", Lane: "doing", Kind: "review",
		Subtasks: store.SubtaskInputs{{Title: "review API"}},
	}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	task := created[0]
	hub := NewHub()
	updates, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	server := httptest.NewServer(New(taskStore, hub, nil, []byte("<html>board</html>")).Handler())
	defer server.Close()

	body := fmt.Sprintf(`{"source":"board-notifier","ops":[{"op":"work_log","id":%q,"expectedRevision":%d,"workLog":{"kind":"progress","text":"MR !42 merged; closed by board-notifier"}},{"op":"check","id":%q,"subtaskId":%q,"done":true,"expectedRevision":%d},{"op":"patch","id":%q,"lane":"done","expectedRevision":%d}]}`,
		task.ID, task.Revision, task.ID, task.Subtasks[0].ID, task.Revision, task.ID, task.Revision)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/ops", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Actor", "board-notifier")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("atomic close returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	closeResponse(t, response)

	select {
	case payload := <-updates:
		if !strings.Contains(string(payload), `"lane":"done"`) {
			t.Fatalf("SSE payload did not contain closed task: %s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("atomic close did not publish an SSE update")
	}
	select {
	case payload := <-updates:
		t.Fatalf("atomic close published more than once: %s", payload)
	case <-time.After(100 * time.Millisecond):
	}

	history := taskStore.History(store.HistoryFilter{TaskID: task.ID, Limit: 100})
	var logCount int
	for _, record := range history.Records {
		if record.WorkLog != nil && record.WorkLog.Text == "MR !42 merged; closed by board-notifier" {
			logCount++
			if record.Actor != "board-notifier" {
				t.Fatalf("work log actor = %q", record.Actor)
			}
		}
	}
	if logCount != 1 {
		t.Fatalf("work log count = %d; history = %#v", logCount, history.Records)
	}
}

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	taskStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	api := New(taskStore, NewHub(), nil, []byte("<html>board</html>"))
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	return server, taskStore
}

func request(t *testing.T, url, method, body, contentType string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeResponse(t *testing.T, response *http.Response, destination any) {
	t.Helper()
	defer closeResponse(t, response)
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatal(err)
	}
}

func readResponseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer closeResponse(t, response)
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func closeResponse(t *testing.T, response *http.Response) {
	t.Helper()
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
}

func readSSEFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			return strings.Join(lines, "")
		}
		lines = append(lines, line)
	}
}
