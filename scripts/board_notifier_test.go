package scripts_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const notifierNow = "1790164800" // 2026-09-23T12:00:00Z

func TestBoardNotifierDryRunPlansAllRulesWithoutMutation(t *testing.T) {
	tasks := `{"tasks":[
		{"id":"tblocked","title":"Blocked card","lane":"blocked","kind":"work","revision":2,"notes":"Blocked: waiting on sales\nremind: daily","updatedAt":"2026-09-23T11:00:00Z","subtasks":[]},
		{"id":"tdate","title":"Dated reminder","lane":"today","kind":"work","revision":1,"notes":"remind: 2026-09-22","updatedAt":"2026-09-23T11:00:00Z","subtasks":[]},
		{"id":"treview","title":"Merged review","lane":"doing","kind":"review","revision":7,"notes":"https://gitlab.com/team/api/-/merge_requests/42","updatedAt":"2026-09-23T11:00:00Z","subtasks":[{"id":"sopen","lane":"doing","done":false}]},
		{"id":"tprobe-old","title":"Expired probe","lane":"today","kind":"probe","revision":4,"notes":"","updatedAt":"2026-09-22T11:59:59Z","subtasks":[]},
		{"id":"tprobe-boundary","title":"Boundary probe","lane":"today","kind":"probe","revision":4,"notes":"","updatedAt":"2026-09-22T12:00:00Z","subtasks":[]},
		{"id":"tstale-old","title":"Stale work","lane":"doing","kind":"work","revision":5,"notes":"","updatedAt":"2026-09-18T11:59:59Z","subtasks":[]},
		{"id":"tstale-boundary","title":"Boundary work","lane":"doing","kind":"work","revision":5,"notes":"","updatedAt":"2026-09-18T12:00:00Z","subtasks":[]}
	]}`
	var mutations int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/tasks" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tasks))
			return
		}
		mutations++
		http.Error(w, "dry run mutated fixture", http.StatusInternalServerError)
	}))
	defer server.Close()

	bin := notifierFakeBin(t)
	stdout, stderr, err := runNotifier(t, server.URL, bin, "--dry-run", "--now")
	if err != nil {
		t.Fatalf("dry run: %v\nstderr: %s", err, stderr)
	}
	if mutations != 0 {
		t.Fatalf("dry run made %d mutations", mutations)
	}
	for _, expected := range []string{"Blocked", "waiting on sales", "Dated reminder", "Stale in progress", "Stale work", "tprobe-old"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("stdout missing %q:\n%s", expected, stdout)
		}
	}
	// Review closure belongs to board-reconciler; a merged-MR review card is left alone here.
	for _, unexpected := range []string{"tprobe-boundary", "Boundary work", "Reviews closed", "treview"} {
		if strings.Contains(stdout, unexpected) {
			t.Fatalf("stdout unexpectedly contains boundary item %q:\n%s", unexpected, stdout)
		}
	}

	events := decodeNotifierEvents(t, stdout)
	mutationsByID := map[string]map[string]any{}
	for _, event := range events {
		if event["type"] == "mutation" {
			mutationsByID[event["id"].(string)] = event
		}
	}
	if len(mutationsByID) != 1 {
		t.Fatalf("planned mutations = %#v", mutationsByID)
	}
	ops := mutationsByID["tprobe-old"]["envelope"].(map[string]any)["ops"].([]any)
	if len(ops) != 2 || ops[0].(map[string]any)["op"] != "work_log" || ops[1].(map[string]any)["op"] != "patch" {
		t.Fatalf("probe atomic op shape = %#v", ops)
	}
	if got := ops[0].(map[string]any)["workLog"].(map[string]any)["text"]; got != "auto-closed: idle probe" {
		t.Fatalf("work-log text = %v", got)
	}
}

func TestBoardNotifierUsesOneAtomicOpsRequest(t *testing.T) {
	tasks := `{"tasks":[{"id":"tprobe","title":"Idle probe","lane":"doing","kind":"probe","revision":7,"notes":"","updatedAt":"2026-09-22T11:00:00Z","subtasks":[{"id":"sopen","lane":"doing","done":false}]}]}`
	var mu sync.Mutex
	var posts int
	var posted []byte
	var actor string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks":
			_, _ = w.Write([]byte(tasks))
		case r.Method == http.MethodPost && r.URL.Path == "/api/ops":
			mu.Lock()
			defer mu.Unlock()
			posts++
			actor = r.Header.Get("X-Actor")
			posted, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"applied":3,"tasks":[{"id":"tprobe","lane":"done"}]}`))
		default:
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	bin := notifierFakeBin(t)
	_, stderr, err := runNotifier(t, server.URL, bin, "--now")
	if err != nil {
		t.Fatalf("notifier: %v\nstderr: %s", err, stderr)
	}
	if posts != 1 || actor != "board-notifier" {
		t.Fatalf("posts = %d, actor = %q", posts, actor)
	}
	var envelope struct {
		Ops []struct {
			Op      string `json:"op"`
			WorkLog *struct {
				Text string `json:"text"`
			} `json:"workLog"`
		} `json:"ops"`
	}
	if err := json.Unmarshal(posted, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Ops) != 3 || envelope.Ops[0].Op != "work_log" || envelope.Ops[0].WorkLog == nil || envelope.Ops[2].Op != "patch" {
		t.Fatalf("posted envelope = %s", posted)
	}
}

func TestBoardNotifierRejectsUnsafeTaskResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tasks":[{"id":"bad","revision":"not-a-number"}]}`))
	}))
	defer server.Close()
	stdout, stderr, err := runNotifier(t, server.URL, notifierFakeBin(t), "--dry-run", "--now")
	if err == nil || strings.TrimSpace(stdout) != "" || !strings.Contains(stderr, "invalid or unsafe task data") {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
}

func notifierFakeBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "osascript"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runNotifier(t *testing.T, todoURL, fakeBin string, args ...string) (string, string, error) {
	t.Helper()
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("board notifier test requires %s", tool)
		}
	}
	cmd := exec.Command("bash", append([]string{"./board-notifier"}, args...)...)
	cmd.Env = append(os.Environ(),
		"TODO_URL="+todoURL,
		"BOARD_NOTIFIER_NOW_EPOCH="+notifierNow,
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func decodeNotifierEvents(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for lineNumber, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line %d is not JSON: %v\n%s", lineNumber+1, err, line)
		}
		events = append(events, event)
	}
	return events
}
