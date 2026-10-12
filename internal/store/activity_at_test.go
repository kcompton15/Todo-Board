package store

import (
	"testing"
	"time"
)

func TestActivityAtIgnoresReordersAndLinkRefreshes(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMany([]TaskInput{{Title: "Ship it", Lane: "doing"}}, "cli")
	if err != nil {
		t.Fatal(err)
	}
	id := created[0].ID
	start := created[0].ActivityAt
	if start.IsZero() || !start.Equal(created[0].CreatedAt) {
		t.Fatalf("activityAt %v, createdAt %v", start, created[0].CreatedAt)
	}
	unchanged := func(step string) {
		t.Helper()
		task, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if !task.ActivityAt.Equal(start) {
			t.Fatalf("%s moved activityAt from %v to %v", step, start, task.ActivityAt)
		}
	}

	order := 42.0
	if _, err := s.Patch(id, TaskPatch{Order: &order}, Mutation{Actor: "web"}); err != nil {
		t.Fatal(err)
	}
	unchanged("reorder")

	if _, _, err := s.LinkTask(id, LinkInput{Ref: "team/api!1"}, Mutation{Actor: reconcilerActor}); err != nil {
		t.Fatal(err)
	}
	unchanged("reconciler link discovery")

	state := "opened"
	if _, _, err := s.LinkTask(id, LinkInput{Ref: "team/api!1", State: &state}, Mutation{Actor: "cli"}); err != nil {
		t.Fatal(err)
	}
	unchanged("link status refresh")

	notes := "picked back up"
	patched, err := s.Patch(id, TaskPatch{Notes: &notes}, Mutation{Actor: "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	if !patched.ActivityAt.After(start) {
		t.Fatalf("notes edit left activityAt at %v", patched.ActivityAt)
	}
}

func TestActivityAtMovesOnStepsAndLaneChanges(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMany([]TaskInput{{Title: "Ship it", Lane: "doing", Subtasks: SubtaskInputs{{Title: "step"}}}}, "cli")
	if err != nil {
		t.Fatal(err)
	}
	task := created[0]
	done := true
	if _, _, err := s.PatchSubtask(task.ID, task.Subtasks[0].ID, SubtaskPatch{Done: &done}, Mutation{Actor: reconcilerActor}); err != nil {
		t.Fatal(err)
	}
	checked, err := s.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !checked.ActivityAt.After(task.ActivityAt) {
		t.Fatal("checking a step did not move activityAt")
	}
	lane := "blocked"
	moved, err := s.Patch(task.ID, TaskPatch{Lane: &lane}, Mutation{Actor: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if !moved.ActivityAt.After(checked.ActivityAt) {
		t.Fatal("a lane change did not move activityAt")
	}
}

func TestBackfillActivityUsesMeaningfulHistory(t *testing.T) {
	at := func(day int) time.Time { return time.Date(2026, time.October, day, 12, 0, 0, 0, time.UTC) }
	tasks := map[string]Task{
		"edited":  {ID: "edited", CreatedAt: at(1), UpdatedAt: at(12)},
		"new":     {ID: "new", CreatedAt: at(5), UpdatedAt: at(12)},
		"old":     {ID: "old", CreatedAt: at(1), UpdatedAt: at(9)},
		"current": {ID: "current", CreatedAt: at(1), UpdatedAt: at(12), ActivityAt: at(11)},
	}
	history := []HistoryRecord{
		{TaskID: "edited", At: at(3), Actor: "cli", Kind: "task_updated", Changes: []FieldChange{{Field: "notes"}}},
		{TaskID: "edited", At: at(12), Actor: "web", Kind: "task_updated", Changes: []FieldChange{{Field: "rank"}}},
		{TaskID: "edited", At: at(11), Actor: reconcilerActor, Kind: "task_updated", Changes: []FieldChange{{Field: "links"}}},
		{TaskID: "edited", At: at(11), Actor: "codex", Kind: "work_log"},
		{TaskID: "new", At: at(12), Actor: "web", Kind: "task_updated", Changes: []FieldChange{{Field: "rank"}}},
	}
	backfillActivity(tasks, history, at(2))
	want := map[string]time.Time{"edited": at(3), "new": at(5), "old": at(9), "current": at(11)}
	for id, expected := range want {
		if got := tasks[id].ActivityAt; !got.Equal(expected) {
			t.Fatalf("%s activityAt %v; want %v", id, got, expected)
		}
	}
}
