package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kcompton15/Todo-Board/internal/store"
)

func TestRoutesAcceptCardAndStepNumbers(t *testing.T) {
	server, taskStore := newTestServer(t)
	created, err := taskStore.CreateMany([]store.TaskInput{
		{Title: "first", Lane: "today"},
		{Title: "second", Lane: "today", Kind: "review", Subtasks: store.SubtaskInputs{{Title: "a"}, {Title: "b"}, {Title: "c"}}},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	card := created[1]
	if card.Number != 2 {
		t.Fatalf("card number = %d", card.Number)
	}

	for _, path := range []string{"/api/tasks/2", "/api/tasks/%232", "/api/tasks/" + card.ID} {
		response := request(t, server.URL+path, http.MethodGet, "", "")
		var got store.Task
		decodeResponse(t, response, &got)
		if response.StatusCode != http.StatusOK || got.ID != card.ID || got.Number != 2 || got.Subtasks[2].Number != 3 {
			t.Fatalf("GET %s = %d %#v", path, response.StatusCode, got)
		}
	}

	req, err := http.NewRequest(http.MethodPatch, server.URL+"/api/tasks/%232", strings.NewReader(`{"lane":"doing","number":77}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("If-Match", `"1"`)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var patched store.Task
	decodeResponse(t, response, &patched)
	if response.StatusCode != http.StatusOK || patched.Lane != "doing" || patched.Number != 2 || response.Header.Get("ETag") != `"2"` {
		t.Fatalf("PATCH by number = %d %#v", response.StatusCode, patched)
	}

	req, err = http.NewRequest(http.MethodPatch, server.URL+"/api/tasks/2", strings.NewReader(`{"lane":"today"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("If-Match", "1")
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("stale If-Match by number = %d", response.StatusCode)
	}
	closeResponse(t, response)

	for _, path := range []string{"/api/tasks/2/subtasks/2", "/api/tasks/2/subtasks/%232", "/api/tasks/2/subtasks/2.2"} {
		response = request(t, server.URL+path, http.MethodPatch, `{"done":true}`, "application/json")
		var sub store.Subtask
		decodeResponse(t, response, &sub)
		if response.StatusCode != http.StatusOK || sub.ID != card.Subtasks[1].ID || sub.Number != 2 {
			t.Fatalf("PATCH %s = %d %#v", path, response.StatusCode, sub)
		}
	}

	response = request(t, server.URL+"/api/tasks/2/subtasks/1", http.MethodDelete, "", "")
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE step by number = %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	closeResponse(t, response)

	response = request(t, server.URL+"/api/tasks/2/subtasks", http.MethodPost, `{"title":"d"}`, "application/json")
	var added store.Subtask
	decodeResponse(t, response, &added)
	if response.StatusCode != http.StatusCreated || added.Number != 4 {
		t.Fatalf("POST step by card number = %d %#v", response.StatusCode, added)
	}

	response = request(t, server.URL+"/api/tasks/2/links", http.MethodPost, `{"ref":"ABC-1"}`, "application/json")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("link by number = %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	closeResponse(t, response)

	response = request(t, server.URL+"/api/tasks/2/work-log", http.MethodPost, `{"kind":"progress","text":"by number"}`, "application/json")
	var entry store.WorkLogEntry
	decodeResponse(t, response, &entry)
	if response.StatusCode != http.StatusCreated || entry.TaskID != card.ID {
		t.Fatalf("work log by number = %d %#v", response.StatusCode, entry)
	}

	for _, path := range []string{"/api/tasks/2/history", "/api/history?taskId=%232", "/api/activity?taskId=2"} {
		response = request(t, server.URL+path, http.MethodGet, "", "")
		body := readResponseBody(t, response)
		if response.StatusCode != http.StatusOK || !strings.Contains(body, card.ID) || strings.Contains(body, created[0].ID) {
			t.Fatalf("GET %s = %d %s", path, response.StatusCode, body)
		}
	}

	response = request(t, server.URL+"/api/tasks/2.3", http.MethodGet, "", "")
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(readResponseBody(t, response), "subtask route") {
		t.Fatalf("step ref on task route = %d", response.StatusCode)
	}
	for _, path := range []string{"/api/tasks/99", "/api/tasks/%2399", "/api/tasks/2/subtasks/9"} {
		response = request(t, server.URL+path, http.MethodPatch, `{"done":true}`, "application/json")
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("PATCH %s = %d", path, response.StatusCode)
		}
		closeResponse(t, response)
	}

	response = request(t, server.URL+"/api/tasks/1", http.MethodDelete, "", "")
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE by number = %d", response.StatusCode)
	}
	closeResponse(t, response)
	if _, err := taskStore.Get(created[0].ID); err == nil {
		t.Fatal("card #1 still exists")
	}
}
