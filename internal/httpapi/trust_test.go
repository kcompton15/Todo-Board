package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kcompton15/Todo-Board/internal/store"
)

func TestTrustContractsAndSingleBroadcast(t *testing.T) {
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	handler := New(s, hub, nil, nil).Handler()
	call := func(method, path, body, revision string, want int, broadcasts bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Actor", "test-client")
		if revision != "" {
			req.Header.Set("If-Match", revision)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		count := 0
		for len(events) > 0 {
			<-events
			count++
		}
		if (broadcasts && count != 1) || (!broadcasts && count != 0) {
			t.Fatalf("broadcasts %d, want success=%v", count, broadcasts)
		}
		return w
	}
	w := call("POST", "/api/tasks", `{"title":"Outcome","lane":"today","subtasks":["Step"]}`, "", 201, true)
	var created struct {
		Tasks []store.Task `json:"tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	task := created.Tasks[0]
	if task.Revision != 1 {
		t.Fatalf("creation revision %d", task.Revision)
	}
	path := "/api/tasks/" + task.ID
	w = call("PATCH", path, `{"lane":"done"}`, "1", 409, false)
	if !strings.Contains(w.Body.String(), `"code":"open_subtasks"`) {
		t.Fatal(w.Body.String())
	}
	w = call("PATCH", path, `{"notes":"from another session"}`, `"1"`, 200, true)
	if w.Header().Get("ETag") != `"2"` {
		t.Fatal("PATCH ETag missing or stale")
	}
	w = call("PUT", path, `{"title":"stale"}`, "1", 409, false)
	if !strings.Contains(w.Body.String(), `"code":"revision_conflict"`) {
		t.Fatal(w.Body.String())
	}
	call("DELETE", path, "", "1", 409, false)
	call("PATCH", path+"/subtasks/"+task.Subtasks[0].ID, `{"done":true}`, "1", 409, false)
	call("POST", path+"/subtasks", `{"title":"Stale"}`, "1", 409, false)
	w = call("PATCH", path+"/subtasks/"+task.Subtasks[0].ID, `{"done":true}`, "2", 200, true)
	if w.Header().Get("ETag") != `"3"` {
		t.Fatalf("subtask PATCH ETag: %s", w.Header().Get("ETag"))
	}
	w = call("GET", path, "", "", 200, false)
	if w.Header().Get("ETag") != `"3"` {
		t.Fatalf("ETag: %s", w.Header().Get("ETag"))
	}
	call("PATCH", path, `{"lane":"done"}`, "3", 200, true)
	w = call("GET", "/api/activity?taskId="+task.ID+"&limit=1", "", "", 200, false)
	var history struct {
		Activity []store.Activity `json:"activity"`
		More     bool             `json:"more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	call("POST", path+"/work-log", `{"kind":"progress","text":"verified the task"}`, "4", 201, true)
	w = call("GET", path+"/history?limit=10", "", "", 200, false)
	if !strings.Contains(w.Body.String(), "verified the task") || !strings.Contains(w.Body.String(), "partialLegacyHistory") {
		t.Fatal(w.Body.String())
	}
	if !history.More || len(history.Activity) != 1 || history.Activity[0].Actor != "test-client" || history.Activity[0].Revision != 4 {
		t.Fatalf("activity: %#v", history)
	}
	for _, raw := range []string{"0", "*", "abc", `"1`, `W/"1"`} {
		call("PATCH", path, `{"notes":"bad"}`, raw, 400, false)
	}
	call("GET", "/api/activity?limit=101", "", "", 400, false)
	call("POST", "/api/ops", fmt.Sprintf(`{"ops":[{"op":"patch","id":%q,"notes":"bad","expectedRevision":1}]}`, task.ID), "", 409, false)
	if len(s.Activity(task.ID)) != 4 {
		t.Fatal("failed requests created history")
	}
	w = call("PUT", path, `{"title":"Updated complete outcome","lane":"done"}`, "4", 200, true)
	if w.Header().Get("ETag") != `"5"` {
		t.Fatal("PUT ETag missing or stale")
	}
}

func TestSubtaskCreateReturnsTaskRevision(t *testing.T) {
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateMany([]store.TaskInput{{Title: "Outcome", Lane: "today"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(s, NewHub(), nil, nil).Handler()
	req := httptest.NewRequest("POST", "/api/tasks/"+created[0].ID+"/subtasks", strings.NewReader(`{"title":"Next"}`))
	req.Header.Set("If-Match", "1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated || w.Header().Get("ETag") != `"2"` {
		t.Fatalf("status %d ETag %q: %s", w.Code, w.Header().Get("ETag"), w.Body.String())
	}
}

func TestActivityFallbackActorAndLegacyAPICompatibility(t *testing.T) {
	server, s := newTestServer(t)
	response := request(t, server.URL+"/api/ops", http.MethodPost, `{"source":"codex","ops":[{"op":"create","title":"Agent"}]}`, "application/json")
	if response.StatusCode != 200 {
		t.Fatal(readResponseBody(t, response))
	}
	closeResponse(t, response)
	events := s.Activity("")
	if len(events) != 1 || events[0].Actor != "codex" {
		t.Fatalf("batch actor: %#v", events)
	}
	response = request(t, server.URL+"/api/tasks/"+events[0].TaskID, "PATCH", `{"notes":"legacy unconditional client"}`, "application/json")
	if response.StatusCode != 200 {
		t.Fatal(readResponseBody(t, response))
	}
	closeResponse(t, response)
	if s.Activity("")[0].Actor != "api" {
		t.Fatal("unattributed write incorrectly attributed to creator")
	}
}
