package scripts_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
	if got := run(false, "context"); !strings.Contains(got, "#1  Hidden outcome") || !strings.Contains(got, "#1.1  Open step") {
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
	if got := run(false, "context"); strings.Contains(got, "Hidden outcome") {
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
	tasks[1].Title = "PROBE-ONLY-IN-ALSO"
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
	for _, forbidden := range []string{"[x]", "COMPLETED step", "[probe]", "[work]"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("context output contains %q:\n%s", forbidden, got)
		}
	}
	for _, required := range []string{
		"- [today] [followup] tcontext00  Follow-up owner  {Internal Tools}",
		"- [blocked] [review] tcontext02  Review multilingual résumé 界",
		"- [doing] tcontext03  Default work card",
		"    - +2 more open steps",
		"Also open (backlog/probe):\n- tcontext01 PROBE-ONLY-IN-ALSO",
		"more active cards (todo ls)",
	} {
		if !strings.Contains(got, required) {
			t.Fatalf("context output does not contain %q:\n%s", required, got)
		}
	}
	if strings.Contains(got, "s0003") || strings.Contains(got, "s0203") {
		t.Fatalf("context output included a fourth open step:\n%s", got)
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
	return runCLIIn(t, tasks, "", nil, "context")
}

// runCLIIn serves tasks read-only and runs todo from dir (the scripts directory when empty).
func runCLIIn(t *testing.T, tasks []store.Task, dir string, env []string, args ...string) string {
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

	script, err := filepath.Abs("todo")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "TODO_URL="+api.URL, "TODO_ACTOR=codex", "TODO_PROJECT="), env...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("todo %v: %v\n%s", args, err, data)
	}
	return string(data)
}

func TestCLIResolvesAndPrintsNumbers(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("CLI integration requires %s", tool)
		}
	}
	s, err := store.New(t.TempDir())
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
	expect := func(got string, want ...string) {
		t.Helper()
		for _, fragment := range want {
			if !strings.Contains(got, fragment) {
				t.Fatalf("output does not contain %q:\n%s", fragment, got)
			}
		}
	}

	expect(run(false, "add", "First card", "--lane", "today"), "created #1 [today] First card")
	expect(run(false, "add", "Second card", "--lane", "today", "--sub", "alpha", "--sub", "beta"), "created #2 [today] Second card")
	second, _, err := s.Resolve("2")
	if err != nil {
		t.Fatal(err)
	}
	expect(run(false, "ls"), "#       LANE", "#1      today", "#2      today")
	for _, ref := range []string{"1", "#1"} {
		expect(run(false, "show", ref), "#1  t", "First card")
	}
	expect(run(false, "show", "2"), "#2  "+second.ID, "#2.1  "+second.Subtasks[0].ID+"  alpha", "#2.2  "+second.Subtasks[1].ID+"  beta")
	expect(run(false, "sub", "2", "gamma"), "created #2.3 [today] gamma")
	expect(run(false, "check", "2", "2"), "updated #2.2  beta")
	expect(run(false, "check", "#2", "#2.3"), "updated #2.3  gamma")
	expect(run(false, "uncheck", "2", second.Subtasks[1].ID[:4]), "updated #2.2  beta")
	expect(run(false, "submove", "2", "1", "doing"), "moved #2.1 to doing")
	expect(run(false, "move", "#2", "doing"), "moved #2 to doing")
	expect(run(false, "context"), "- [doing] #2  Second card", "    - [ ] [doing] #2.1  alpha")
	expect(run(false, "log", "2", "progress", "numbered"), "logged progress on #2")
	expect(run(true, "check", "2", "#1.1"), "not on card #2")
	expect(run(true, "check", "2", "9"), "no step 9 on card #2")
	expect(run(true, "show", "99"), "no task #99")
	expect(run(true, "show", "2.1"), "is a step")

	task, err := s.Get(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Lane != "doing" || task.Subtasks[0].Lane != "doing" || task.Subtasks[1].Done || !task.Subtasks[2].Done {
		t.Fatalf("CLI state: %#v", task)
	}
	first, _, err := s.Resolve("1")
	if err != nil {
		t.Fatal(err)
	}
	expect(run(false, "rm", "1"), "deleted #1 ("+first.ID+")")
	expect(run(false, "add", "Third card"), "created #3 [backlog] Third card")
	var listed struct {
		Tasks []store.Task `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(run(false, "ls", "--json")), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Tasks) != 2 || listed.Tasks[0].ID == "" || listed.Tasks[0].Number == 0 {
		t.Fatalf("ls --json lost IDs or numbers: %#v", listed.Tasks)
	}
}

func TestCLIAddAttachesToOpenCard(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("CLI integration requires %s", tool)
		}
	}
	if err := store.SetJiraSite("example.atlassian.net"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetIssuePrefixes([]string{"ABC", "XYZ"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.SetIssuePrefixes(nil); err != nil {
			t.Error(err)
		}
		if err := store.SetJiraSite(""); err != nil {
			t.Error(err)
		}
	})
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateMany([]store.TaskInput{{Title: "Review ABC-942 plan", Lane: "done"}}, "test"); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(httpapi.New(s, httpapi.NewHub(), nil, nil).Handler())
	defer api.Close()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("bash", append([]string{"./todo"}, args...)...)
		cmd.Env = append(os.Environ(), "TODO_URL="+api.URL, "TODO_ACTOR=codex")
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("todo %v: %v\n%s", args, err, data)
		}
		return strings.TrimSpace(string(data))
	}
	if got := run("add", "Re-review ABC-942 plan", "--link", "ABC-942"); got != "created #2 [backlog] Re-review ABC-942 plan" {
		t.Fatalf("first add: %s", got)
	}
	followOn, _, err := s.Resolve("2")
	if err != nil || !strings.HasPrefix(followOn.Notes, "Follow-on to #1.") {
		t.Fatalf("follow-on notes = %q %v", followOn.Notes, err)
	}
	if got := run("add", "Re-review ABC-942 plan", "--link", "ABC-942"); got != "attached to #2 (Re-review ABC-942 plan) as #2.1" {
		t.Fatalf("second add: %s", got)
	}
	if got := run("add", "Third pass on ABC-942", "--new"); got != "created #3 [backlog] Third pass on ABC-942" {
		t.Fatalf("--new add: %s", got)
	}
	_, step, err := s.Resolve("2.1")
	if err != nil || step == nil || step.Lane != "today" {
		t.Fatalf("attached step = %#v %v", step, err)
	}
	batch := filepath.Join(t.TempDir(), "batch.json")
	if err := os.WriteFile(batch, []byte(`[{"title":"XYZ-1 imported"},{"title":"XYZ-1 again"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := run("import", batch); got != "created 1 task(s), attached 1 to existing cards" {
		t.Fatalf("import: %s", got)
	}
}

func TestCLIContextListsBacklogAndProbeCards(t *testing.T) {
	base := time.Now().UTC().Add(-5 * 24 * time.Hour)
	var tasks []store.Task
	for i := 0; i < 20; i++ {
		tasks = append(tasks, store.Task{
			ID: fmt.Sprintf("tactive%02d", i), Number: i + 1, Title: fmt.Sprintf("Active card %02d %s", i, strings.Repeat("x", 120)),
			Lane: "doing", Kind: "work", Priority: 2, Source: "test", UpdatedAt: base,
		})
	}
	for i := 0; i < 40; i++ {
		tasks = append(tasks, store.Task{
			ID: fmt.Sprintf("tbacklog%02d", i), Number: 100 + i, Title: fmt.Sprintf("Backlog idea %02d", i), Project: "API",
			Lane: "backlog", Kind: "work", Priority: 2, Source: "test", UpdatedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}
	tasks = append(tasks,
		store.Task{ID: "tprobe", Number: 200, Title: "Open probe", Lane: "today", Kind: "probe", Priority: 2, Source: "test", UpdatedAt: base.Add(48 * time.Hour)},
		store.Task{ID: "tdoneprobe", Number: 201, Title: "Finished probe", Lane: "done", Kind: "probe", Priority: 2, Source: "test", UpdatedAt: base.Add(72 * time.Hour)},
		store.Task{ID: "tbusy", Number: 202, Title: "Backlog with active step", Lane: "backlog", Kind: "work", Priority: 2, Source: "test", UpdatedAt: base.Add(96 * time.Hour),
			Subtasks: []store.Subtask{{ID: "sbusy", Number: 1, Title: "busy step", Lane: "doing"}}},
	)
	got := runCLIContext(t, tasks)
	if size := len([]byte(got)); size > 1900 {
		t.Fatalf("context output is %d bytes; want at most 1900\n%s", size, got)
	}
	header := "## Task board: update these instead of creating duplicates\n"
	if !strings.HasPrefix(got, header) || !strings.Contains(got, "more active cards (todo ls)") {
		t.Fatalf("active section changed:\n%s", got)
	}
	_, also, ok := strings.Cut(got, "Also open (backlog/probe):\n")
	if !ok {
		t.Fatalf("no backlog/probe section:\n%s", got)
	}
	lines := strings.Split(strings.TrimSpace(also), "\n")
	if len(lines) < 4 || lines[0] != "- #200 Open probe" || lines[1] != "- #139 Backlog idea 39 {API}" || lines[2] != "- #138 Backlog idea 38 {API}" {
		t.Fatalf("backlog/probe order:\n%s", also)
	}
	if !strings.HasPrefix(lines[len(lines)-1], "- +") || !strings.Contains(lines[len(lines)-1], "more backlog/probe cards") {
		t.Fatalf("no +K more tail:\n%s", also)
	}
	if strings.Contains(also, "Finished probe") || strings.Contains(also, "Backlog with active step") {
		t.Fatalf("done probe or active backlog card in section:\n%s", also)
	}
	if !strings.Contains(got, "#202  Backlog with active step") && !strings.Contains(got, "more active cards") {
		t.Fatalf("active backlog card lost:\n%s", got)
	}
}

func TestCLIFindShowsTopTen(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("CLI integration requires %s", tool)
		}
	}
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inputs := []store.TaskInput{{Title: "lint gate for web-api", Lane: "done"}}
	for i := 0; i < 12; i++ {
		inputs = append(inputs, store.TaskInput{Title: fmt.Sprintf("lint card %02d", i), Lane: "today"})
	}
	if _, err := s.CreateMany(inputs, ""); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(httpapi.New(s, httpapi.NewHub(), nil, nil).Handler())
	defer api.Close()
	cmd := exec.Command("bash", "./todo", "find", "lint gate")
	cmd.Env = append(os.Environ(), "TODO_URL="+api.URL)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("todo find: %v\n%s", err, data)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 12 || !strings.Contains(lines[1], "#1 ") || !strings.Contains(lines[1], "lint gate for web-api") {
		t.Fatalf("find output:\n%s", data)
	}
	if lines[11] != `+3 more matches (todo find "lint gate" --json)` {
		t.Fatalf("find tail = %q", lines[11])
	}
}

type cliResult struct {
	stdout, stderr string
	code           int
}

func runCLIReport(t *testing.T, url string, env []string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"./todo", "report"}, args...)...)
	cmd.Env = append(append(os.Environ(), "TODO_URL="+url), env...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := cliResult{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		result.code = exitErr.ExitCode()
	default:
		t.Fatalf("todo report %v: %v", args, err)
	}
	return result
}

func requireCLITools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("CLI integration requires %s", tool)
		}
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

type fakeReportBoard struct {
	t          *testing.T
	mu         sync.Mutex
	lastClaim  any
	states     []map[string]any
	polls      int
	requests   []map[string]any
	missingJob bool
}

func (f *fakeReportBoard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/reports/worker":
		writeTestJSON(f.t, w, map[string]any{"lastClaimAt": f.lastClaim})
	case r.Method == http.MethodPost && (r.URL.Path == "/api/reports/jobs" || r.URL.Path == "/api/reports/preview"):
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("decode %s: %v", r.URL.Path, err)
		}
		body["path"] = r.URL.Path
		f.requests = append(f.requests, body)
		if r.URL.Path == "/api/reports/preview" {
			writeTestJSON(f.t, w, map[string]any{"markdown": "# Preview digest"})
			return
		}
		w.WriteHeader(http.StatusAccepted)
		writeTestJSON(f.t, w, map[string]any{"id": "rjob", "state": "queued", "digestMarkdown": "# Job digest", "cardCount": 2})
	case r.Method == http.MethodGet && r.URL.Path == "/api/reports/jobs/rjob":
		if f.missingJob {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			if _, err := w.Write([]byte(`{"error":"report job not found"}`)); err != nil {
				f.t.Errorf("write 404: %v", err)
			}
			return
		}
		state := f.states[min(f.polls, len(f.states)-1)]
		f.polls++
		writeTestJSON(f.t, w, state)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeReportBoard) takeRequests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	requests := f.requests
	f.requests = nil
	return requests
}

func TestCLIReportPrintsDraftWhenWorkerFinishes(t *testing.T) {
	requireCLITools(t)
	board := &fakeReportBoard{t: t, lastClaim: time.Now().UTC().Format(time.RFC3339Nano), states: []map[string]any{
		{"id": "rjob", "state": "running"},
		{"id": "rjob", "state": "done", "markdown": "- Shipped the thing (#4)", "cardCount": 1},
	}}
	api := httptest.NewServer(board)
	defer api.Close()
	got := runCLIReport(t, api.URL, nil, "weekly", "--from", "2026-10-05", "--to", "2026-10-11", "--project", "Billing", "--project", "mobile-app")
	if got.code != 0 || got.stdout != "- Shipped the thing (#4)\n" || got.stderr != "" {
		t.Fatalf("report = %#v", got)
	}
	want := map[string]any{"path": "/api/reports/jobs", "kind": "weekly", "start": "2026-10-05", "end": "2026-10-11", "projects": []any{"Billing", "mobile-app"}}
	if requests := board.takeRequests(); len(requests) != 1 || fmt.Sprint(requests[0]) != fmt.Sprint(want) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestCLIReportDefaultsMatchBoardDialog(t *testing.T) {
	requireCLITools(t)
	board := &fakeReportBoard{t: t, lastClaim: time.Now().UTC().Format(time.RFC3339Nano), states: []map[string]any{{"id": "rjob", "state": "done", "markdown": "- ok (#1)"}}}
	api := httptest.NewServer(board)
	defer api.Close()
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	now := time.Now().In(chicago)
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, chicago)
	back := map[time.Weekday]int{time.Monday: 3, time.Sunday: 2}[today.Weekday()]
	if back == 0 {
		back = 1
	}
	weekStart := today.AddDate(0, 0, -((int(today.Weekday())+6)%7 + 7))
	for kind, dates := range map[string][2]string{
		"standup": {today.AddDate(0, 0, -back).Format(time.DateOnly), today.Format(time.DateOnly)},
		"weekly":  {weekStart.Format(time.DateOnly), weekStart.AddDate(0, 0, 6).Format(time.DateOnly)},
	} {
		if got := runCLIReport(t, api.URL, nil, kind); got.code != 0 {
			t.Fatalf("%s report = %#v", kind, got)
		}
		if requests := board.takeRequests(); len(requests) != 1 || requests[0]["start"] != dates[0] || requests[0]["end"] != dates[1] {
			t.Fatalf("%s requests = %#v; want %v", kind, requests, dates)
		}
	}
}

func TestCLIReportFallsBackToDigest(t *testing.T) {
	requireCLITools(t)
	fresh := time.Now().UTC().Format(time.RFC3339Nano)
	cases := []struct {
		name    string
		board   *fakeReportBoard
		env     []string
		digest  string
		message string
	}{
		{"no worker", &fakeReportBoard{lastClaim: nil}, nil, "# Preview digest", "has not checked in since the board started (start it with scripts/install-todo-reporter --enable)"},
		{"stale worker", &fakeReportBoard{lastClaim: time.Now().Add(-2 * time.Minute).Format(time.RFC3339)}, nil, "# Preview digest", "last checked in 1"},
		{"failed job", &fakeReportBoard{lastClaim: fresh, states: []map[string]any{{"id": "rjob", "state": "failed", "error": "Claude version has not been isolation-verified"}}}, nil, "# Job digest", "the report writer failed: Claude version has not been isolation-verified"},
		{"timeout", &fakeReportBoard{lastClaim: fresh, states: []map[string]any{{"id": "rjob", "state": "running"}}}, []string{"TODO_REPORT_TIMEOUT=1"}, "# Job digest", "no report after 1 s"},
		{"lost job", &fakeReportBoard{lastClaim: fresh, missingJob: true}, nil, "# Job digest", "the board lost report job rjob"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.board.t = t
			api := httptest.NewServer(tc.board)
			defer api.Close()
			got := runCLIReport(t, api.URL, tc.env, "standup", "--from", "2026-10-09", "--to", "2026-10-12")
			if got.code != 4 || got.stdout != tc.digest+"\n" || !strings.Contains(got.stderr, tc.message) || !strings.Contains(got.stderr, "printed the evidence digest instead") {
				t.Fatalf("report = %#v", got)
			}
		})
	}
}

func TestCLIReportBoardUnreachableAndBadInput(t *testing.T) {
	requireCLITools(t)
	api := httptest.NewServer(http.NotFoundHandler())
	url := api.URL
	api.Close()
	if got := runCLIReport(t, url, nil, "weekly"); got.code != 3 || !strings.Contains(got.stderr, "board unreachable") || got.stdout != "" {
		t.Fatalf("unreachable = %#v", got)
	}
	if got := runCLIReport(t, url, nil, "monthly"); got.code != 1 || !strings.Contains(got.stderr, "invalid report kind 'monthly'") {
		t.Fatalf("bad kind = %#v", got)
	}
	if got := runCLIReport(t, url, []string{"TODO_REPORT_TIMEOUT=soon"}, "weekly"); got.code != 1 || !strings.Contains(got.stderr, "TODO_REPORT_TIMEOUT") {
		t.Fatalf("bad timeout = %#v", got)
	}
}

func TestCLIReportUsesRealJobQueue(t *testing.T) {
	requireCLITools(t)
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(httpapi.New(s, httpapi.NewHub(), nil, nil).Handler())
	defer api.Close()
	missing := runCLIReport(t, api.URL, nil, "weekly", "--from", "2026-10-05", "--to", "2026-10-11")
	if missing.code != 4 || !strings.HasPrefix(missing.stdout, "# Weekly report (2026-10-05 through 2026-10-11)") {
		t.Fatalf("missing worker = %#v", missing)
	}
	post := func(path, body string) (int, map[string]any) {
		t.Helper()
		response, err := http.Post(api.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("close %s: %v", path, err)
			}
		}()
		var decoded map[string]any
		if response.StatusCode == http.StatusOK {
			if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
		}
		return response.StatusCode, decoded
	}
	if status, _ := post("/api/reports/jobs/claim", "{}"); status != http.StatusNoContent {
		t.Fatalf("empty claim = %d", status)
	}
	worker := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			status, job := post("/api/reports/jobs/claim", "{}")
			if status == http.StatusOK {
				status, _ = post(fmt.Sprintf("/api/reports/jobs/%v/complete", job["id"]), `{"markdown":"- Quiet week (#1)","cardCount":1}`)
				if status != http.StatusOK {
					worker <- fmt.Errorf("complete = %d", status)
					return
				}
				worker <- nil
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		worker <- errors.New("no job claimed")
	}()
	got := runCLIReport(t, api.URL, nil, "weekly", "--from", "2026-10-05", "--to", "2026-10-11")
	if err := <-worker; err != nil {
		t.Fatal(err)
	}
	if got.code != 0 || got.stdout != "- Quiet week (#1)\n" {
		t.Fatalf("report = %#v", got)
	}
}
