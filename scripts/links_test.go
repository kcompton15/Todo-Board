package scripts_test

import (
	"encoding/json"
	"github.com/kcompton15/Todo-Board/internal/httpapi"
	"github.com/kcompton15/Todo-Board/internal/store"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCLILinksAndDelivery(t *testing.T) {
	if err := store.SetJiraSite("example.atlassian.net"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.SetJiraSite(""); err != nil {
			t.Error(err)
		}
	})
	s, _ := store.New(t.TempDir())
	server := httptest.NewServer(httpapi.New(s, httpapi.NewHub(), nil, nil).Handler())
	defer server.Close()
	run := func(wantError bool, args ...string) string {
		t.Helper()
		cmd := exec.Command("bash", append([]string{"./todo"}, args...)...)
		cmd.Env = append(os.Environ(), "TODO_URL="+server.URL)
		data, err := cmd.CombinedOutput()
		if (err != nil) != wantError {
			t.Fatalf("%v: %v %s", args, err, data)
		}
		return string(data)
	}
	run(false, "add", "Link review", "--kind", "review", "--link", "PROJ-1", "--agent-context", "agent memo", "--sub", "API")
	task := s.List(store.Filter{})[0]
	if len(task.Links) != 1 || task.AgentContext != "agent memo" {
		t.Fatal(task)
	}
	run(false, "link", task.ID, "group/api!1", "--sub", task.Subtasks[0].ID, "--role", "reference")
	task, _ = s.Get(task.ID)
	revision := task.Revision
	run(false, "link", task.ID, "group/api!1", "--sub", task.Subtasks[0].ID)
	got, _ := s.Get(task.ID)
	if got.Revision != revision || got.Links[1].Role != "reference" {
		t.Fatal("no-op lost role")
	}
	if output := run(false, "show", task.ID); !strings.Contains(output, "scope="+task.Subtasks[0].ID) || !strings.Contains(output, "[reference]") {
		t.Fatal(output)
	}
	var decoded store.Task
	if err := json.Unmarshal([]byte(run(false, "show", task.ID, "--json")), &decoded); err != nil || len(decoded.Links) != 2 {
		t.Fatal(decoded, err)
	}
	run(true, "unlink", task.ID, "l")
	run(false, "reconcile", task.ID, "manual")
	run(false, "memo", task.ID, "")
	run(false, "log", task.ID, "review_delivered", "Final API findings", "--sub", task.Subtasks[0].ID)
	run(false, "unlink", task.ID, task.Links[0].ID)
	got, _ = s.Get(task.ID)
	if got.ReconcileMode != "manual" || got.AgentContext != "" || len(got.Links) != 1 {
		t.Fatal(got)
	}
	page := s.History(store.HistoryFilter{TaskID: task.ID})
	found := false
	for _, r := range page.Records {
		if r.WorkLog != nil && r.WorkLog.Kind == store.WorkLogReviewDelivered && r.WorkLog.SubtaskID == task.Subtasks[0].ID {
			found = true
		}
	}
	if !found {
		t.Fatal("scoped receipt missing")
	}
}
