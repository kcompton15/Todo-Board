package scripts_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kcompton15/Todo-Board/internal/httpapi"
	"github.com/kcompton15/Todo-Board/internal/store"
)

func TestCLITrustWorkflow(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("CLI integration requires %s", tool)
		}
	}
	dir := t.TempDir()
	legacy := `{"version":1,"tasks":[{"id":"tlegacy","title":"Hidden outcome","lane":"done","priority":2,"source":"web","subtasks":[{"id":"sold","title":"Open step","lane":"today"}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(httpapi.New(s, httpapi.NewHub(), nil, nil).Handler())
	defer api.Close()
	run := func(wantError bool, args ...string) string {
		t.Helper()
		cmd := exec.Command("bash", append([]string{"./todo"}, args...)...)
		cmd.Env = append(os.Environ(), "TODO_URL="+api.URL, "TODO_ACTOR=codex")
		data, err := cmd.CombinedOutput()
		if (err != nil) != wantError {
			t.Fatalf("todo %v: %v\n%s", args, err, data)
		}
		return string(data)
	}
	if got := run(false, "context"); !strings.Contains(got, "tlegacy") || !strings.Contains(got, "Open step") {
		t.Fatalf("hidden context: %s", got)
	}
	createdOutput := run(false, "add", "Investigate kind", "--kind", "review", "--lane", "doing", "--project", "Internal Tools")
	if !strings.Contains(createdOutput, "created ") {
		t.Fatalf("add output: %s", createdOutput)
	}
	reviewTasks := s.List(store.Filter{Kind: "review"})
	if len(reviewTasks) != 1 || reviewTasks[0].Title != "Investigate kind" {
		t.Fatalf("added review: %#v", reviewTasks)
	}
	if got := run(false, "ls", "--kind", "review"); !strings.Contains(got, "KIND") || !strings.Contains(got, "review") {
		t.Fatalf("kind list: %s", got)
	}
	if got := run(false, "find", "Investigate kind"); !strings.Contains(got, "KIND") || !strings.Contains(got, "review") {
		t.Fatalf("kind find: %s", got)
	}
	if got := run(false, "show", reviewTasks[0].ID); !strings.Contains(got, "kind: review") {
		t.Fatalf("kind show: %s", got)
	}
	if got := run(false, "kind", reviewTasks[0].ID, "followup"); !strings.Contains(got, "-> followup") {
		t.Fatalf("kind update: %s", got)
	}
	if got := run(true, "kind", reviewTasks[0].ID, "nonsense"); !strings.Contains(got, "invalid kind") {
		t.Fatalf("invalid kind: %s", got)
	}
	kindTask, err := s.Get(reviewTasks[0].ID)
	if err != nil || kindTask.Kind != "followup" {
		t.Fatalf("kind command state = %#v, err = %v", kindTask, err)
	}
	run(false, "move", "tlegacy", "today")
	if got := run(true, "done", "tlegacy"); !strings.Contains(got, "unfinished subtasks") {
		t.Fatal(got)
	}
	run(false, "block", "tlegacy", "Waiting for review")
	run(false, "check", "tlegacy", "sold")
	run(false, "done", "tlegacy")
	var history struct {
		Activity []store.Activity `json:"activity"`
	}
	if err := json.Unmarshal([]byte(run(false, "history", "tlegacy", "--json")), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Activity) != 4 || history.Activity[0].Actor != "codex" {
		t.Fatalf("history: %#v", history)
	}
	task, _ := s.Get("tlegacy")
	if task.Lane != "done" || task.Notes != "Blocked: Waiting for review" || task.Revision != 5 {
		t.Fatalf("CLI state: %#v", task)
	}
	if got := run(false, "context"); strings.Contains(got, "tlegacy") {
		t.Fatal("finished task still in active context")
	}
}

func TestCLIKindUsesCurrentRevision(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("CLI integration requires %s", tool)
		}
	}
	task := store.Task{Revision: 7, ID: "tkind", Title: "Kind guard", Lane: "today", Kind: "work", Priority: 2, Source: "test", Subtasks: []store.Subtask{}}
	patched := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks":
			_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []store.Task{task}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/tkind":
			_ = json.NewEncoder(w).Encode(task)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/tasks/tkind":
			if got := r.Header.Get("If-Match"); got != "7" {
				t.Errorf("If-Match = %q; want 7", got)
			}
			var body struct {
				Kind string `json:"kind"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode patch: %v", err)
			}
			if body.Kind != "review" {
				t.Errorf("patch kind = %q; want review", body.Kind)
			}
			task.Kind = body.Kind
			task.Revision++
			patched = true
			_ = json.NewEncoder(w).Encode(task)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	cmd := exec.Command("bash", "./todo", "kind", "tkind", "review")
	cmd.Env = append(os.Environ(), "TODO_URL="+api.URL, "TODO_ACTOR=codex")
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("todo kind: %v\n%s", err, data)
	}
	if !patched || !strings.Contains(string(data), "kind tkind -> review") {
		t.Fatalf("kind command output = %q, patched = %v", data, patched)
	}
}

func TestCLIContextIsQuietAndByteBounded(t *testing.T) {
	tasks := make([]store.Task, 12)
	for i := range tasks {
		tasks[i] = store.Task{
			ID:       fmt.Sprintf("tcontext%02d", i),
			Title:    fmt.Sprintf("Context card %02d %s", i, strings.Repeat("detail-", 18)),
			Lane:     "doing",
			Kind:     "work",
			Priority: 2,
			Source:   "test",
		}
		subtaskCount := 5
		if i == 0 {
			subtaskCount = 10
		}
		if i == 1 {
			subtaskCount = 0
		}
		for j := 0; j < subtaskCount; j++ {
			lane := "doing"
			done := false
			title := fmt.Sprintf("Open step %02d-%02d", i, j)
			if i == 0 && j >= 6 {
				lane = "done"
				done = true
				title = fmt.Sprintf("COMPLETED step %02d", j)
			}
			tasks[i].Subtasks = append(tasks[i].Subtasks, store.Subtask{
				ID:    fmt.Sprintf("s%02d%02d", i, j),
				Title: title,
				Lane:  lane,
				Done:  done,
			})
		}
	}
	tasks[0].Title = "Follow-up owner"
	tasks[0].Lane = "today"
	tasks[0].Kind = "followup"
	tasks[0].Project = "Internal Tools"
	tasks[1].Title = "PROBE-SHOULD-NOT-APPEAR"
	tasks[1].Kind = "probe"
	tasks[2].Title = "Review multilingual résumé 界"
	tasks[2].Lane = "blocked"
	tasks[2].Kind = "review"
	tasks[3].Title = "Default work card"

	var subtaskTotal int
	for _, task := range tasks {
		subtaskTotal += len(task.Subtasks)
	}
	if subtaskTotal != 60 {
		t.Fatalf("fixture has %d subtasks; want 60", subtaskTotal)
	}

	got := runCLIContext(t, tasks)
	if !utf8.ValidString(got) {
		t.Fatalf("context output is not valid UTF-8: %q", got)
	}
	if size := len([]byte(got)); size > 1900 {
		t.Fatalf("context output is %d bytes; want at most 1900\n%s", size, got)
	}
	for _, forbidden := range []string{"[x]", "COMPLETED step", "PROBE-SHOULD-NOT-APPEAR", "[work]"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("context output contains %q:\n%s", forbidden, got)
		}
	}
	for _, required := range []string{
		"- [today] [followup] tcontext00  Follow-up owner  {Internal Tools}",
		"- [blocked] [review] tcontext02  Review multilingual résumé 界",
		"- [doing] tcontext03  Default work card",
		"    - +1 more open steps",
		"more active cards (todo ls)",
	} {
		if !strings.Contains(got, required) {
			t.Fatalf("context output does not contain %q:\n%s", required, got)
		}
	}
	if strings.Contains(got, "s0005") {
		t.Fatalf("context output included a sixth open step:\n%s", got)
	}
}

func TestCLIContextOmitsWholeMultibyteCardAtBudgetEdge(t *testing.T) {
	hugeSubtasks := make([]store.Subtask, 5)
	for i := range hugeSubtasks {
		hugeSubtasks[i] = store.Subtask{
			ID:    fmt.Sprintf("shuge%d", i),
			Title: strings.Repeat("界", 300),
			Lane:  "doing",
		}
	}
	tasks := []store.Task{
		{ID: "tsmall", Title: "Small card", Lane: "today", Kind: "work", Priority: 2, Source: "test"},
		{
			ID:       "thuge",
			Title:    "OVERSIZED " + strings.Repeat("界", 389),
			Lane:     "doing",
			Kind:     "review",
			Priority: 2,
			Source:   "test",
			Subtasks: hugeSubtasks,
		},
		{ID: "tlater", Title: "Later card", Lane: "blocked", Kind: "followup", Priority: 2, Source: "test"},
	}

	got := runCLIContext(t, tasks)
	if !utf8.ValidString(got) {
		t.Fatalf("context output is not valid UTF-8: %q", got)
	}
	if size := len([]byte(got)); size > 1900 {
		t.Fatalf("context output is %d bytes; want at most 1900\n%s", size, got)
	}
	if !strings.Contains(got, "tsmall  Small card") || !strings.Contains(got, "- +2 more active cards (todo ls)") {
		t.Fatalf("context output did not retain the small card and summarize the remainder:\n%s", got)
	}
	if strings.Contains(got, "OVERSIZED") || strings.Contains(got, "tlater") {
		t.Fatalf("context output emitted a partial or later card after the oversized block:\n%s", got)
	}
}

func runCLIContext(t *testing.T, tasks []store.Task) string {
	t.Helper()
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("CLI integration requires %s", tool)
		}
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tasks" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"tasks": tasks}); err != nil {
			t.Errorf("encode tasks: %v", err)
		}
	}))
	t.Cleanup(api.Close)

	cmd := exec.Command("bash", "./todo", "context")
	cmd.Env = append(os.Environ(), "TODO_URL="+api.URL, "TODO_ACTOR=codex")
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("todo context: %v\n%s", err, data)
	}
	return string(data)
}
