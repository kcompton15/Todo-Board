package httpapi

import (
	"encoding/json"
	"fmt"
	"github.com/kcompton15/Todo-Board/internal/store"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if err := store.SetJiraSite("example.atlassian.net"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestLinksHTTPAtomicBroadcastAndETag(t *testing.T) {
	s, _ := store.New(t.TempDir())
	tasks, _ := s.CreateMany([]store.TaskInput{{Title: "review", Kind: "review"}}, "")
	task := tasks[0]
	hub := NewHub()
	events, closeEvents := hub.Subscribe()
	defer closeEvents()
	handler := New(s, hub, nil, nil).Handler()
	call := func(method, path, body, revision string, status, count int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("If-Match", revision)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		if len(events) != count {
			t.Fatalf("SSE count %d want %d", len(events), count)
		}
		for len(events) > 0 {
			<-events
		}
		return w
	}
	path := "/api/tasks/" + task.ID
	w := call("POST", path+"/links", `{"ref":"PROJ-1"}`, "1", 200, 1)
	var link store.Link
	if err := json.Unmarshal(w.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}
	if w.Header().Get("ETag") != `"2"` {
		t.Fatal(w.Header())
	}
	w = call("POST", path+"/links", `{"ref":"PROJ-1"}`, "2", 200, 1)
	if w.Header().Get("ETag") != `"2"` {
		t.Fatal("duplicate changed revision")
	}
	call("POST", path+"/links", `{"ref":"PROJ-2"}`, "1", 409, 0)
	call("POST", path+"/links", `{"ref":"PROJ-2","stateChangedAt":"2026-01-01T00:00:00Z"}`, "2", 400, 0)
	call("PATCH", path, `{"links":null}`, "2", 400, 0)
	call("PUT", path, `{"title":"old client","kind":"review"}`, "2", 200, 1)
	got, _ := s.Get(task.ID)
	if len(got.Links) != 1 {
		t.Fatal("old PUT lost links")
	}
	call("POST", "/api/ops", fmt.Sprintf(`{"ops":[{"op":"link","id":%q,"link":{"ref":"PROJ-2"},"expectedRevision":3},{"op":"unlink","id":%q,"linkId":"foreign","expectedRevision":3}]}`, task.ID, task.ID), "", 400, 0)
	got, _ = s.Get(task.ID)
	if len(got.Links) != 1 || got.Revision != 3 {
		t.Fatal("partial ops")
	}
	call("POST", path+"/work-log", `{"kind":"review_delivered","text":"Final findings"}`, "3", 201, 1)
	w = call("DELETE", path+"/links/"+link.ID, "", "3", 200, 1)
	if w.Header().Get("ETag") != `"4"` || !strings.Contains(w.Body.String(), link.ID) {
		t.Fatal(w.Body.String(), w.Header())
	}
	call("DELETE", path+"/links/"+link.ID, "", "4", 404, 0)
}

func TestMetadataJSONImportAndCreateOps(t *testing.T) {
	s, _ := store.New(t.TempDir())
	handler := New(s, NewHub(), nil, nil).Handler()
	for _, test := range []struct{ path, body string }{
		{"/api/import", `[{"title":"import","links":[{"ref":"PROJ-11"}],"agentContext":"import memo","reconcileMode":"manual"}]`},
		{"/api/ops", `{"ops":[{"op":"create","title":"ops","links":[{"ref":"group/web!12","role":"reference"}],"agentContext":"ops memo","reconcileMode":"manual"}]}`},
	} {
		r := httptest.NewRequest("POST", test.path, strings.NewReader(test.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code < 200 || w.Code >= 300 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	tasks := s.List(store.Filter{})
	if len(tasks) != 2 {
		t.Fatal(tasks)
	}
	for _, task := range tasks {
		if len(task.Links) != 1 || task.ReconcileMode != "manual" || task.AgentContext != task.Title+" memo" {
			t.Fatal(task)
		}
	}
}
