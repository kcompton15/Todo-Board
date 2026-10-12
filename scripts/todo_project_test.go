package scripts_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kcompton15/Todo-Board/internal/httpapi"
	"github.com/kcompton15/Todo-Board/internal/store"
)

func projectFixture(now time.Time) []store.Task {
	day := 24 * time.Hour
	card := func(id string, number int, title, lane, project string, idle time.Duration) store.Task {
		at := now.Add(-idle)
		return store.Task{ID: id, Number: number, Title: title, Lane: lane, Kind: "work", Priority: 2, Source: "test",
			Project: project, CreatedAt: at, UpdatedAt: now, ActivityAt: at}
	}
	return []store.Task{
		card("tother", 1, "Fresh other work", "doing", "web-app", time.Hour),
		card("tapi", 2, "API work", "doing", "API", 2*day),
		card("tapiold", 3, "Old API work", "blocked", "API", 12*day),
		card("tstale", 4, "Forgotten other work", "doing", "Mobile", 20*day),
		card("tportal", 5, "Portal work", "today", "Customer Portal", 30*time.Minute),
		card("tdone", 6, "Finished", "done", "Mobile", 40*day),
	}
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	dir := filepath.Join(parts...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCLIProjectResolution(t *testing.T) {
	tasks := projectFixture(time.Now().UTC())
	root := t.TempDir()
	cases := []struct {
		dir  string
		env  []string
		want string
	}{
		{mkdir(t, root, "api", "cmd", "src"), nil, "API"},
		{mkdir(t, root, "acmecustomerportal"), nil, "Customer Portal"},
		{mkdir(t, root, "web-app-worktrees", "m18"), nil, "web-app"},
		{mkdir(t, root, "api", "cmd"), []string{"TODO_PROJECT=Mobile"}, "Mobile"},
		{mkdir(t, root, "pinned", "deep"), nil, "Brand New"},
		{mkdir(t, root, "-dash-leading", "api"), nil, "API"},
	}
	if err := os.WriteFile(filepath.Join(root, "pinned", ".todo-project"), []byte("  Brand New \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := strings.TrimSpace(runCLIIn(t, tasks, c.dir, c.env, "project")); got != c.want {
			t.Fatalf("project in %s %v = %q; want %q", c.dir, c.env, got, c.want)
		}
	}

	script, err := filepath.Abs("todo")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, "project")
	cmd.Dir = mkdir(t, root, "unrelated")
	cmd.Env = append(os.Environ(), "TODO_URL=http://127.0.0.1:1", "TODO_PROJECT=")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("project without a match succeeded: %s", out)
	}
}

func TestCLIContextPutsThisDirectoryFirstAndFoldsIdleCards(t *testing.T) {
	tasks := projectFixture(time.Now().UTC())
	dir := mkdir(t, t.TempDir(), "api", "server")
	got := runCLIIn(t, tasks, "", nil, "context", "--cwd", dir)
	lines := strings.Split(got, "\n")
	if lines[0] != "## Task board for {API} (this directory): update these instead of creating duplicates" {
		t.Fatalf("header:\n%s", got)
	}
	if !strings.HasPrefix(lines[1], "- [doing] #2  API work  {API}") || !strings.HasPrefix(lines[2], "- [blocked] #3  Old API work  {API}  (idle 12d)") {
		t.Fatalf("this directory's cards are not first, or idle is unmarked:\n%s", got)
	}
	if !strings.Contains(got, "#5  Portal work") || !strings.Contains(got, "#1  Fresh other work") {
		t.Fatalf("recent cards elsewhere missing:\n%s", got)
	}
	if strings.Contains(got, "Forgotten other work") || !strings.Contains(got, "- 1 card idle 7+ days in other projects (todo stale)") {
		t.Fatalf("idle card elsewhere was not folded into a count:\n%s", got)
	}
	if strings.Index(got, "#5  Portal work") > strings.Index(got, "#1  Fresh other work") {
		t.Fatalf("other projects are not most recent first:\n%s", got)
	}

	plain := runCLIIn(t, tasks, mkdir(t, t.TempDir(), "elsewhere"), nil, "context")
	if !strings.HasPrefix(plain, "## Task board: update these") || !strings.Contains(plain, "- 2 cards idle 7+ days (todo stale)") {
		t.Fatalf("context without a project:\n%s", plain)
	}
}

func TestCLIStaleListsIdleOpenCards(t *testing.T) {
	tasks := projectFixture(time.Now().UTC())
	got := runCLIIn(t, tasks, "", nil, "stale")
	for _, want := range []string{"#4      doing     20d", "#3      blocked   12d"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stale output missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "#4 ") > strings.Index(got, "#3 ") || strings.Contains(got, "Finished") || strings.Contains(got, "API work  ") {
		t.Fatalf("stale order or filter:\n%s", got)
	}
	var parsed struct {
		Tasks []struct {
			Number   int `json:"number"`
			IdleDays int `json:"idleDays"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(runCLIIn(t, tasks, "", nil, "stale", "--days", "15", "--json")), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tasks) != 1 || parsed.Tasks[0].Number != 4 || parsed.Tasks[0].IdleDays != 20 {
		t.Fatalf("stale --days 15 --json = %+v", parsed.Tasks)
	}
}

func TestCLIAddFilesUnderThisDirectorysProject(t *testing.T) {
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
	script, err := filepath.Abs("todo")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "TODO_URL="+api.URL, "TODO_ACTOR=codex", "TODO_PROJECT=")
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("todo %v: %v\n%s", args, err, data)
		}
		return strings.TrimSpace(string(data))
	}
	apiDir := mkdir(t, root, "api", "cmd")
	if got := run(apiDir, "add", "Unfiled before any project exists"); got != "created #1 [backlog] Unfiled before any project exists" {
		t.Fatalf("add with no board projects: %s", got)
	}
	run(root, "add", "Seed", "--project", "API")
	if got := run(apiDir, "add", "Defaulted"); got != "created #3 [backlog] Defaulted  {API}" {
		t.Fatalf("add without --project: %s", got)
	}
	if got := run(apiDir, "add", "Explicit", "--project", "Mobile"); got != "created #4 [backlog] Explicit  {Mobile}" {
		t.Fatalf("add with --project: %s", got)
	}
	if got := run(apiDir, "add", "Opted out", "--project", ""); got != "created #5 [backlog] Opted out" {
		t.Fatalf("add with an empty --project: %s", got)
	}
	dashed := mkdir(t, root, "-dash-leading", "api")
	if got := run(dashed, "add", "Dashed", "--sub", "-v flag", "--sub", "--force"); got != "created #6 [backlog] Dashed  {API}" {
		t.Fatalf("add under a dash-leading directory: %s", got)
	}
	task, _, err := s.Resolve("6")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Subtasks) != 2 || task.Subtasks[0].Title != "-v flag" || task.Subtasks[1].Title != "--force" {
		t.Fatalf("dash-leading steps = %+v", task.Subtasks)
	}
}

func TestCLIDetectsTheCallingAgent(t *testing.T) {
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
	cases := []struct {
		env  []string
		want string
	}{
		{[]string{"CODEX_THREAD_ID=t1", "CLAUDECODE="}, "codex"},
		{[]string{"CODEX_THREAD_ID=", "CLAUDECODE=1"}, "claude-code"},
		{[]string{"CODEX_THREAD_ID=", "CLAUDECODE="}, "cli"},
		{[]string{"CODEX_THREAD_ID=", "CLAUDECODE=1", "TODO_ACTOR=reviewer"}, "reviewer"},
	}
	for i, c := range cases {
		cmd := exec.Command("bash", "./todo", "add", "Card "+c.want)
		cmd.Env = append(append(os.Environ(), "TODO_URL="+api.URL, "TODO_ACTOR=", "TODO_PROJECT="), c.env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("todo add: %v\n%s", err, out)
		}
		task, _, err := s.Resolve(strconv.Itoa(i + 1))
		if err != nil {
			t.Fatal(err)
		}
		records := s.History(store.HistoryFilter{TaskID: task.ID}).Records
		if len(records) == 0 || records[0].Actor != c.want {
			t.Fatalf("%v: history %+v; want actor %q", c.env, records, c.want)
		}
		if c.want != "reviewer" && task.Source != c.want {
			t.Fatalf("%v: source %q; want %q", c.env, task.Source, c.want)
		}
	}
}
