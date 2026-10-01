package httpapi

import (
	"encoding/json"
	"github.com/kcompton15/Todo-Board/internal/store"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestActionHTTPReplayAndHistoryConflict(t *testing.T) {
	s, _ := store.New(t.TempDir())
	tasks, _ := s.CreateMany([]store.TaskInput{{Title: "guard", Kind: "work"}}, "")
	task := tasks[0]
	hub := NewHub()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	handler := New(s, hub, nil, nil).Handler()
	head := s.History(store.HistoryFilter{TaskID: task.ID}).Records[0].ID
	lane := "done"
	envelope := store.OpsEnvelope{Source: "board-reconciler", ExpectedHistoryHeads: map[string]string{task.ID: head}, Ops: []store.Operation{
		{Op: "patch", ID: task.ID, Lane: &lane, ExpectedRevision: &task.Revision},
		{Op: "work_log", ID: task.ID, ExpectedRevision: &task.Revision, WorkLog: &store.WorkLogInput{Kind: store.WorkLogProgress, Text: "proof", ActionID: "http-action"}},
	}}
	call := func(want, broadcasts int) string {
		t.Helper()
		data, _ := json.Marshal(envelope)
		req := httptest.NewRequest("POST", "/api/ops", strings.NewReader(string(data)))
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want || len(events) != broadcasts {
			t.Fatalf("status=%d events=%d body=%s", res.Code, len(events), res.Body.String())
		}
		for len(events) > 0 {
			<-events
		}
		return res.Body.String()
	}
	s.AddWorkLog(store.WorkLogInput{TaskID: task.ID, Kind: store.WorkLogBlocker, Text: "pause"}, store.Mutation{})
	if !strings.Contains(call(409, 0), "history_conflict") {
		t.Fatal("missing conflict code")
	}
	envelope.ExpectedHistoryHeads[task.ID] = s.History(store.HistoryFilter{TaskID: task.ID}).Records[0].ID
	call(200, 1)
	if !strings.Contains(call(200, 0), `"alreadyApplied":true`) {
		t.Fatal("missing replay flag")
	}
	envelope.Ops[1].WorkLog.Text = "different"
	if !strings.Contains(call(409, 0), "action_conflict") {
		t.Fatal("missing action conflict code")
	}
}
