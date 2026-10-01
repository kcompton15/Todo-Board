package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreCreatePatchDeleteAndRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	s, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	created, err := s.CreateMany([]TaskInput{{
		Title: "  Ship task board  ", Lane: "today", Priority: 1,
		Kind: "review", Project: "Internal Tools", Tag: "Board", Source: "codex",
		Subtasks: SubtaskInputs{{Title: "Store"}, {Title: "UI"}},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0].Title != "Ship task board" || created[0].Kind != "review" {
		t.Fatalf("unexpected created tasks: %#v", created)
	}
	if created[0].Order != 1000 || len(created[0].Subtasks) != 2 {
		t.Fatalf("unexpected defaults: %#v", created[0])
	}
	for _, subtask := range created[0].Subtasks {
		if subtask.Lane != "today" || subtask.Done {
			t.Fatalf("new subtask did not inherit parent lane: %#v", subtask)
		}
	}

	doing := "doing"
	patched, err := s.Patch(created[0].ID, TaskPatch{Lane: &doing})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Lane != "doing" || patched.Order != 1000 {
		t.Fatalf("unexpected lane patch: %#v", patched)
	}

	reloaded, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.Get(created[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lane != "doing" || got.Kind != "review" || got.Project != "Internal Tools" || len(got.Subtasks) != 2 {
		t.Fatalf("round-trip mismatch: %#v", got)
	}

	if err := reloaded.Delete(created[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Get(created[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found after delete, got %v", err)
	}
}

func TestLaneMoveAppendsAfterExistingTasks(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMany([]TaskInput{
		{Title: "first", Lane: "doing"},
		{Title: "second", Lane: "today"},
		{Title: "third", Lane: "doing"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	doing := "doing"
	moved, err := s.Patch(created[1].ID, TaskPatch{Lane: &doing})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Order != 3000 {
		t.Fatalf("expected order 3000, got %v", moved.Order)
	}
}

func TestApplyOpsIsAtomic(t *testing.T) {
	s := newTestStore(t)
	title := "created first"
	missingTitle := "will never apply"
	_, err := s.ApplyOps([]Operation{
		{Op: "create", Title: &title},
		{Op: "patch", ID: "missing", Title: &missingTitle},
	}, "codex")
	if err == nil || !strings.Contains(err.Error(), "ops[1]") {
		t.Fatalf("expected indexed failure, got %v", err)
	}
	if got := s.List(Filter{}); len(got) != 0 {
		t.Fatalf("failed batch mutated memory: %#v", got)
	}

	reloaded, err := New(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.List(Filter{}); len(got) != 0 {
		t.Fatalf("failed batch mutated disk: %#v", got)
	}
}

func TestApplyOpsPersistsWorkLogAndCloseOnce(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMany([]TaskInput{{
		Title: "finished review", Lane: "doing", Kind: "review",
		Subtasks: SubtaskInputs{{Title: "first"}, {Title: "second"}},
	}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	task := created[0]
	persists := 0
	s.afterPersist = func() { persists++ }
	done := true
	doneLane := "done"
	operations := []Operation{{
		Op: "work_log", ID: task.ID, ExpectedRevision: &task.Revision,
		WorkLog: &WorkLogInput{Kind: WorkLogProgress, Text: "MR !42 merged; closed by board-notifier"},
	}}
	for _, subtask := range task.Subtasks {
		operations = append(operations, Operation{
			Op: "check", ID: task.ID, SubtaskID: subtask.ID, Done: &done,
			ExpectedRevision: &task.Revision,
		})
	}
	operations = append(operations, Operation{
		Op: "patch", ID: task.ID, Lane: &doneLane, ExpectedRevision: &task.Revision,
	})

	if _, err := s.ApplyOps(operations, "board-notifier", Mutation{Actor: "board-notifier"}); err != nil {
		t.Fatal(err)
	}
	if persists != 1 {
		t.Fatalf("atomic close persisted %d times; want 1", persists)
	}
	closed, err := s.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Lane != "done" || closed.Revision != task.Revision+1 {
		t.Fatalf("closed task = %#v", closed)
	}
	for _, subtask := range closed.Subtasks {
		if !subtask.Done || subtask.Lane != "done" {
			t.Fatalf("subtask was not closed atomically: %#v", subtask)
		}
	}
	history := s.History(HistoryFilter{TaskID: task.ID, Limit: 100})
	var foundLog bool
	for _, record := range history.Records {
		if record.WorkLog != nil && record.WorkLog.Text == "MR !42 merged; closed by board-notifier" {
			foundLog = record.Actor == "board-notifier" && record.WorkLog.Actor == "board-notifier"
		}
	}
	if !foundLog {
		t.Fatalf("atomic close history missing attributed work log: %#v", history.Records)
	}
}

func TestApplyOpsWorkLogRollsBackWithFailedClose(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMany([]TaskInput{{
		Title: "unfinished review", Lane: "doing", Kind: "review",
		Subtasks: SubtaskInputs{{Title: "open step"}},
	}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	task := created[0]
	before := s.History(HistoryFilter{TaskID: task.ID, Limit: 100})
	persists := 0
	s.afterPersist = func() { persists++ }
	doneLane := "done"
	_, err = s.ApplyOps([]Operation{
		{
			Op: "work_log", ID: task.ID, ExpectedRevision: &task.Revision,
			WorkLog: &WorkLogInput{Kind: WorkLogProgress, Text: "must roll back"},
		},
		{Op: "patch", ID: task.ID, Lane: &doneLane, ExpectedRevision: &task.Revision},
	}, "board-notifier", Mutation{Actor: "board-notifier"})
	if err == nil {
		t.Fatal("expected close with an open subtask to fail")
	}
	if persists != 0 {
		t.Fatalf("failed atomic close persisted %d times; want 0", persists)
	}
	after := s.History(HistoryFilter{TaskID: task.ID, Limit: 100})
	if len(after.Records) != len(before.Records) {
		t.Fatalf("failed atomic close retained work log: before=%#v after=%#v", before.Records, after.Records)
	}
}

func TestTaskKindDefaultsFiltersPatchesAndOperations(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMany([]TaskInput{
		{Title: "default kind"},
		{Title: "review card", Kind: "review"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if created[0].Kind != "work" || created[1].Kind != "review" {
		t.Fatalf("unexpected kinds: %#v", created)
	}
	if got := s.List(Filter{Kind: "REVIEW"}); len(got) != 1 || got[0].ID != created[1].ID {
		t.Fatalf("case-insensitive kind filter = %#v", got)
	}

	probe := "probe"
	patched, err := s.Patch(created[0].ID, TaskPatch{Kind: &probe})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Kind != probe {
		t.Fatalf("patched kind = %q; want probe", patched.Kind)
	}
	activity := s.Activity(created[0].ID)
	if len(activity) == 0 || !hasFieldChange(activity[0].Changes, "kind", "work", "probe") {
		t.Fatalf("kind patch missing from activity: %#v", activity)
	}
	history := s.History(HistoryFilter{TaskID: created[0].ID})
	if len(history.Records) == 0 || !hasFieldChange(history.Records[0].Changes, "kind", "work", "probe") {
		t.Fatalf("kind patch missing from history: %#v", history)
	}

	title := "ops card"
	followup := "followup"
	if _, err := s.ApplyOps([]Operation{{Op: "create", Title: &title, Kind: &followup}}, "test"); err != nil {
		t.Fatal(err)
	}
	var opsCard Task
	for _, task := range s.List(Filter{Kind: "followup"}) {
		if task.Title == title {
			opsCard = task
		}
	}
	if opsCard.ID == "" {
		t.Fatal("create operation did not persist followup kind")
	}
	work := "work"
	if _, err := s.ApplyOps([]Operation{{Op: "patch", ID: opsCard.ID, Kind: &work}}, "test"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(opsCard.ID)
	if err != nil || got.Kind != "work" {
		t.Fatalf("patch operation kind = %q, err = %v", got.Kind, err)
	}
}

func hasFieldChange(changes []FieldChange, field, before, after string) bool {
	for _, change := range changes {
		if change.Field == field && change.Before == before && change.After == after {
			return true
		}
	}
	return false
}

func TestSubtaskPatchAndReplacementPreserveIDs(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMany([]TaskInput{{
		Title: "task", Subtasks: SubtaskInputs{{Title: "step"}},
	}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	task := created[0]
	subtaskID := task.Subtasks[0].ID
	done := true
	patched, revision, err := s.PatchSubtask(task.ID, subtaskID, SubtaskPatch{Done: &done})
	if err != nil {
		t.Fatal(err)
	}
	if revision != task.Revision+1 {
		t.Fatalf("revision: got %d, want %d", revision, task.Revision+1)
	}
	if !patched.Done || patched.Lane != "done" {
		t.Fatalf("subtask was not moved to done: %#v", patched)
	}

	replacement := SubtaskInputs{{ID: subtaskID, Title: "renamed", Lane: "blocked"}}
	updated, err := s.Patch(task.ID, TaskPatch{Subtasks: &replacement})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Subtasks[0].ID != subtaskID || updated.Subtasks[0].Title != "renamed" ||
		updated.Subtasks[0].Lane != "blocked" || updated.Subtasks[0].Done {
		t.Fatalf("subtask identity was not preserved: %#v", updated.Subtasks)
	}
}

func TestSubtaskLaneMovesIndependentlyFromParent(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMany([]TaskInput{{
		Title: "parent", Lane: "today", Subtasks: SubtaskInputs{{Title: "independent step"}},
	}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	task := created[0]
	blocked := "blocked"
	subtask, _, err := s.PatchSubtask(task.ID, task.Subtasks[0].ID, SubtaskPatch{Lane: &blocked})
	if err != nil {
		t.Fatal(err)
	}
	if subtask.Lane != "blocked" || subtask.Done {
		t.Fatalf("unexpected blocked subtask: %#v", subtask)
	}

	doing := "doing"
	parent, err := s.Patch(task.ID, TaskPatch{Lane: &doing})
	if err != nil {
		t.Fatal(err)
	}
	if parent.Lane != "doing" || parent.Subtasks[0].Lane != "blocked" {
		t.Fatalf("parent move cascaded to subtask: %#v", parent)
	}

	done := true
	subtask, _, err = s.PatchSubtask(task.ID, task.Subtasks[0].ID, SubtaskPatch{Done: &done})
	if err != nil {
		t.Fatal(err)
	}
	if subtask.Lane != "done" || !subtask.Done {
		t.Fatalf("done compatibility did not update lane: %#v", subtask)
	}

	done = false
	subtask, _, err = s.PatchSubtask(task.ID, task.Subtasks[0].ID, SubtaskPatch{Done: &done})
	if err != nil {
		t.Fatal(err)
	}
	if subtask.Lane != "doing" || subtask.Done {
		t.Fatalf("unchecking did not return to parent lane: %#v", subtask)
	}
}

func TestLegacySubtasksGainLanesOnLoad(t *testing.T) {
	dataDir := t.TempDir()
	state := persistedState{Version: stateVersion, Tasks: []Task{{
		ID: "tlegacy", Title: "legacy", Lane: "today", Priority: 2, Source: "test", Order: 1000,
		CreatedAt: time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC),
		Subtasks: []Subtask{
			{ID: "sopen", Title: "open"},
			{ID: "sdone", Title: "complete", Done: true},
		},
	}}}
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "tasks.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Get("tlegacy")
	if err != nil {
		t.Fatal(err)
	}
	if task.Subtasks[0].Lane != "today" || task.Subtasks[0].Done {
		t.Fatalf("open legacy subtask was not migrated: %#v", task.Subtasks[0])
	}
	if task.Subtasks[1].Lane != "done" || !task.Subtasks[1].Done {
		t.Fatalf("done legacy subtask was not migrated: %#v", task.Subtasks[1])
	}
	if task.Kind != "work" {
		t.Fatalf("legacy task kind = %q; want work", task.Kind)
	}
}

func TestVersionTwoTaskWithoutKindLoadsAndPersistsAsWork(t *testing.T) {
	dataDir := t.TempDir()
	legacy := `{"version":2,"tasks":[{"revision":1,"id":"tlegacy","title":"legacy","lane":"today","priority":2,"project":"","tag":"","notes":"","source":"test","order":1000,"createdAt":"2026-09-17T12:00:00Z","updatedAt":"2026-09-17T12:00:00Z","subtasks":[]}]}`
	if err := os.WriteFile(filepath.Join(dataDir, "tasks.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Get("tlegacy")
	if err != nil {
		t.Fatal(err)
	}
	if task.Kind != "work" {
		t.Fatalf("legacy task kind = %q; want work", task.Kind)
	}
	notes := "force persistence"
	if _, err := s.Patch(task.ID, TaskPatch{Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(filepath.Join(dataDir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), `"version": 2`) || !strings.Contains(string(persisted), `"kind": "work"`) {
		t.Fatalf("legacy task did not persist as v2 work: %s", persisted)
	}
}

func TestManualOrderSortsBeforePriority(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMany([]TaskInput{
		{Title: "critical", Lane: "today", Priority: 1},
		{Title: "normal", Lane: "today", Priority: 2},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	first := float64(500)
	if _, err := s.Patch(created[1].ID, TaskPatch{Order: &first}); err != nil {
		t.Fatal(err)
	}
	got := s.List(Filter{Lane: "today"})
	if got[0].Title != "normal" || got[1].Title != "critical" {
		t.Fatalf("manual order did not outrank priority: %#v", got)
	}
}

func TestTaskSearchIncludesSubtaskTitles(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMany([]TaskInput{{
		Title: "parent", Subtasks: SubtaskInputs{{Title: "SSO Details"}},
	}}, "test")
	if err != nil {
		t.Fatal(err)
	}

	got := s.List(Filter{Query: "sso details"})
	if len(got) != 1 || got[0].ID != created[0].ID {
		t.Fatalf("subtask title did not find its parent task: %#v", got)
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name  string
		input TaskInput
		want  string
	}{
		{name: "empty title", input: TaskInput{Title: "  "}, want: "title is required"},
		{name: "invalid lane", input: TaskInput{Title: "x", Lane: "later"}, want: "invalid lane"},
		{name: "invalid kind", input: TaskInput{Title: "x", Kind: "unknown"}, want: "kind must be one of work, followup, review, probe"},
		{name: "invalid priority", input: TaskInput{Title: "x", Priority: 4}, want: "priority"},
		{name: "tag whitespace", input: TaskInput{Title: "x", Tag: "two words"}, want: "whitespace"},
		{name: "long project", input: TaskInput{Title: "x", Project: strings.Repeat("p", 81)}, want: "project"},
		{name: "invalid subtask lane", input: TaskInput{Title: "x", Subtasks: SubtaskInputs{{Title: "step", Lane: "later"}}}, want: "subtask lane"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := newTestStore(t)
			_, err := s.CreateMany([]TaskInput{test.input}, "test")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestCorruptAndUnknownStateRefuseToLoad(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "corrupt", data: "{", want: "parse"},
		{name: "unknown version", data: `{"version":99,"tasks":[]}`, want: "unsupported state version"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dataDir, "tasks.json"), []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := New(dataDir)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "tasks.json") {
				t.Fatalf("expected path-specific %q error, got %v", test.want, err)
			}
		})
	}
}

func TestPersistenceIsCanonicalAndPrettyPrinted(t *testing.T) {
	s := newTestStore(t)
	_, err := s.CreateMany([]TaskInput{
		{Title: "low", Lane: "today", Priority: 3},
		{Title: "critical", Lane: "today", Priority: 1},
		{Title: "backlog", Lane: "backlog", Priority: 2},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\n  \"version\": 2") {
		t.Fatalf("state is not indented: %s", data)
	}
	if strings.Contains(string(data), `"subtasks": null`) {
		t.Fatalf("empty subtasks must serialize as an array: %s", data)
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	got := []string{state.Tasks[0].Title, state.Tasks[1].Title, state.Tasks[2].Title}
	want := []string{"backlog", "low", "critical"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("canonical order mismatch: got %v want %v", got, want)
		}
	}
}

func TestConcurrentReadsAndWrites(t *testing.T) {
	s := newTestStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = s.CreateMany([]TaskInput{{Title: "concurrent"}}, "test")
		}()
		go func() {
			defer wg.Done()
			_ = s.List(Filter{})
		}()
	}
	wg.Wait()
	if len(s.List(Filter{})) != 10 {
		t.Fatalf("expected 10 tasks, got %d", len(s.List(Filter{})))
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sequence := 0
	s.now = func() time.Time {
		sequence++
		return time.Date(2026, time.September, 17, 12, 0, sequence, 0, time.UTC)
	}
	s.randomID = func(length int) (string, error) {
		sequence++
		return strings.Repeat(string(rune('a'+sequence%20)), length), nil
	}
	return s
}
