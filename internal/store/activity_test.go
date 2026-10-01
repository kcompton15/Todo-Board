package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func trustStore(t *testing.T) (*Store, Task) {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := s.CreateMany([]TaskInput{{Title: "Outcome", Lane: "today", Source: "codex", Subtasks: SubtaskInputs{{Title: "Verify"}}}}, "", Mutation{Actor: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	return s, tasks[0]
}

func TestCompletionGuardAtomicAndRepair(t *testing.T) {
	s, task := trustStore(t)
	done, title := "done", "must roll back"
	before, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ApplyOps([]Operation{{Op: "patch", ID: task.ID, Title: &title}, {Op: "patch", ID: task.ID, Lane: &done}}, "agent")
	if !errors.Is(err, ErrOpenSubtasks) {
		t.Fatalf("want completion conflict, got %v", err)
	}
	after, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || len(s.Activity(task.ID)) != 1 {
		t.Fatal("rejected batch changed disk or activity")
	}
	_, err = s.ApplyOps([]Operation{{Op: "check", ID: task.ID, SubtaskID: task.Subtasks[0].ID}, {Op: "patch", ID: task.ID, Lane: &done}}, "agent")
	if err != nil {
		t.Fatal(err)
	}
	finished, _ := s.Get(task.ID)
	if finished.Lane != "done" || !finished.Subtasks[0].Done || finished.Revision != 2 {
		t.Fatalf("atomic completion: %#v", finished)
	}
	open := false
	if _, _, err := s.PatchSubtask(task.ID, task.Subtasks[0].ID, SubtaskPatch{Done: &open}); !errors.Is(err, ErrOpenSubtasks) {
		t.Fatalf("reopened child under Done: %v", err)
	}
	if _, _, err := s.AddSubtasks(task.ID, SubtaskInputs{{Title: "New", Lane: "today"}}); !errors.Is(err, ErrOpenSubtasks) {
		t.Fatalf("added open child under Done: %v", err)
	}
}

func TestActivityAttributionRevisionAndRestart(t *testing.T) {
	s, task := trustStore(t)
	if task.Revision != 1 {
		t.Fatalf("created revision %d", task.Revision)
	}
	notes := "human notes"
	changed, err := s.Patch(task.ID, TaskPatch{Notes: &notes}, Mutation{Actor: "web", TaskID: task.ID, ExpectedRevision: &task.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Revision != 2 || changed.Source != "codex" {
		t.Fatalf("revision/source: %#v", changed)
	}
	events := s.Activity(task.ID)
	if len(events) != 2 || events[0].Actor != "web" || events[0].Changes[0] != (FieldChange{"notes", "", notes}) {
		t.Fatalf("activity: %#v", events)
	}
	events[0].Changes[0].After = "tamper"
	if s.Activity(task.ID)[0].Changes[0].After != notes {
		t.Fatal("history escaped by reference")
	}
	_, err = s.Patch(task.ID, TaskPatch{Notes: &notes}, Mutation{TaskID: task.ID, ExpectedRevision: &task.Revision})
	if !errors.Is(err, ErrConflict) || len(s.Activity(task.ID)) != 2 {
		t.Fatalf("stale edit: %v", err)
	}
	reloaded, err := New(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reloaded.Get(task.ID)
	if got.Revision != 2 || !reflect.DeepEqual(s.Activity(task.ID), reloaded.Activity(task.ID)) {
		t.Fatal("history/revision lost on restart")
	}
	if err := reloaded.Delete(task.ID, Mutation{Actor: "cli"}); err != nil {
		t.Fatal(err)
	}
	if event := reloaded.Activity(task.ID)[0]; event.Action != "deleted" || event.Title != task.Title || event.Actor != "cli" {
		t.Fatalf("delete activity: %#v", event)
	}
}

func TestLegacyMigrationDoesNotInventHistory(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"version":1,"tasks":[{"id":"tlegacy","title":"Legacy","lane":"done","priority":2,"source":"web","subtasks":[{"id":"sold","title":"Open","lane":"today"}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := s.Get("tlegacy")
	if task.Revision != 1 || OpenSubtasks(task) != 1 || len(s.Activity("")) != 0 {
		t.Fatalf("legacy defaults: %#v", task)
	}
	title := "Clarified legacy outcome"
	if _, err := s.Patch(task.ID, TaskPatch{Title: &title}); err != nil {
		t.Fatalf("legacy repair blocked: %v", err)
	}
	open := "today"
	if _, err := s.Patch(task.ID, TaskPatch{Lane: &open}); err != nil {
		t.Fatal(err)
	}
}

func TestPersistenceFailureRollsBackHistoryAndRevision(t *testing.T) {
	s, task := trustStore(t)
	s.path = filepath.Join(t.TempDir(), "missing", "tasks.json")
	title := "Never committed"
	if _, err := s.Patch(task.ID, TaskPatch{Title: &title}); !errors.Is(err, ErrPersistence) {
		t.Fatalf("want persistence error: %v", err)
	}
	got, _ := s.Get(task.ID)
	if got.Title != task.Title || got.Revision != 1 || len(s.Activity(task.ID)) != 1 {
		t.Fatal("failed write leaked state")
	}
}

func TestConcurrentRevisionAllowsExactlyOneWriter(t *testing.T) {
	s, task := trustStore(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, title := range []string{"Writer A", "Writer B"} {
		wg.Add(1)
		go func(title string) {
			defer wg.Done()
			_, err := s.Patch(task.ID, TaskPatch{Title: &title}, Mutation{TaskID: task.ID, ExpectedRevision: &task.Revision})
			results <- err
		}(title)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 || len(s.Activity(task.ID)) != 2 {
		t.Fatalf("writers: success=%d conflicts=%d", success, conflicts)
	}
}

func TestActivityRetentionAndBatchStartingRevision(t *testing.T) {
	s, task := trustStore(t)
	s.activity = make([]Activity, MaxActivity)
	for i := range s.activity {
		s.activity[i] = Activity{TaskID: "older"}
	}
	title, notes := "Changed", strings.Repeat("é", 200)
	_, err := s.ApplyOps([]Operation{{Op: "patch", ID: task.ID, Title: &title, ExpectedRevision: &task.Revision}, {Op: "patch", ID: task.ID, Notes: &notes, ExpectedRevision: &task.Revision}}, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Activity("")) != MaxActivity {
		t.Fatal("retention not bounded")
	}
	event := s.Activity(task.ID)[0]
	if event.Actor != "inbox" || event.Revision != 2 || len(event.Changes) != 2 || len([]rune(event.Changes[1].After)) != 161 {
		t.Fatalf("batch event: %#v", event)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted persistedState
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Activity) != MaxActivity {
		t.Fatal("persisted retention wrong")
	}
}

func TestActivityBoundsLongSubtaskIdentifiers(t *testing.T) {
	id := "s" + strings.Repeat("界", 300)
	changes := taskChanges(Task{}, Task{Subtasks: []Subtask{{ID: id, Title: strings.Repeat("界", 300), Lane: "today"}}})
	for _, change := range changes {
		for _, value := range []string{change.Field, change.Before, change.After} {
			if len([]rune(value)) > 161 {
				t.Fatalf("unbounded activity preview: %d characters", len([]rune(value)))
			}
		}
	}
	if len(changes) != 1 || !strings.HasSuffix(changes[0].Field, "…") {
		t.Fatalf("identifier preview missing: %#v", changes)
	}
}
