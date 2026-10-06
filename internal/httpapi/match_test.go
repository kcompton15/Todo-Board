package httpapi

import (
	"net/http"
	"testing"

	"github.com/kcompton15/Todo-Board/internal/store"
)

func TestCreateOnMatchOverHTTP(t *testing.T) {
	server, taskStore := newTestServer(t)
	seed, err := taskStore.CreateMany([]store.TaskInput{{Title: "Review ABC-942 plan", Lane: "doing"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	card := seed[0]
	body := `{"title":"Re-review the plan","links":[{"ref":"ABC-942"}]}`

	response := request(t, server.URL+"/api/tasks", http.MethodPost, body, "application/json")
	var rejected struct {
		Code  string      `json:"code"`
		Error string      `json:"error"`
		Match store.Match `json:"match"`
	}
	decodeResponse(t, response, &rejected)
	if response.StatusCode != http.StatusConflict || rejected.Code != "open_match" || rejected.Error == "" ||
		rejected.Match != (store.Match{ID: card.ID, Number: card.Number, Title: card.Title, Ref: "ABC-942"}) {
		t.Fatalf("reject = %d %#v", response.StatusCode, rejected)
	}

	response = request(t, server.URL+"/api/tasks?onMatch=nope", http.MethodPost, body, "application/json")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid onMatch = %d", response.StatusCode)
	}
	closeResponse(t, response)

	response = request(t, server.URL+"/api/tasks?onMatch=attach", http.MethodPost, body, "application/json")
	var attached struct {
		Attached bool          `json:"attached"`
		Task     store.Task    `json:"task"`
		Subtask  store.Subtask `json:"subtask"`
	}
	decodeResponse(t, response, &attached)
	if response.StatusCode != http.StatusOK || !attached.Attached || attached.Task.ID != card.ID ||
		attached.Subtask.Number != 1 || attached.Task.Revision != card.Revision+1 {
		t.Fatalf("attach = %d %#v", response.StatusCode, attached)
	}

	response = request(t, server.URL+"/api/tasks?onMatch=attach", http.MethodPost,
		`{"tasks":[{"title":"ABC-942 again"},{"title":"unrelated"}]}`, "application/json")
	var batch struct {
		Tasks       []store.Task `json:"tasks"`
		Attachments []struct {
			Task    store.Task    `json:"task"`
			Subtask store.Subtask `json:"subtask"`
		} `json:"attachments"`
	}
	decodeResponse(t, response, &batch)
	if response.StatusCode != http.StatusCreated || len(batch.Tasks) != 1 || len(batch.Attachments) != 1 ||
		batch.Attachments[0].Subtask.Number != 2 {
		t.Fatalf("batch = %d %#v", response.StatusCode, batch)
	}

	response = request(t, server.URL+"/api/import", http.MethodPost, `[{"title":"ABC-942 imported"}]`, "application/json")
	var imported struct {
		Created     int              `json:"created"`
		Attachments []map[string]any `json:"attachments"`
	}
	decodeResponse(t, response, &imported)
	if response.StatusCode != http.StatusCreated || imported.Created != 0 || len(imported.Attachments) != 1 {
		t.Fatalf("import = %d %#v", response.StatusCode, imported)
	}

	response = request(t, server.URL+"/api/ops", http.MethodPost,
		`{"ops":[{"op":"create","title":"ABC-942 by op","onMatch":"reject"}]}`, "application/json")
	decodeResponse(t, response, &rejected)
	if response.StatusCode != http.StatusConflict || rejected.Match.ID != card.ID {
		t.Fatalf("ops reject = %d %#v", response.StatusCode, rejected)
	}
}
