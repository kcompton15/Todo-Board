package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOperationOmissionAndExplicitEmptySubtasks(t *testing.T) {
	for _, tc := range []struct {
		input SubtaskInputs
		want  bool
	}{{nil, false}, {SubtaskInputs{}, true}} {
		data, err := json.Marshal(Operation{Op: "patch", ID: "card", Subtasks: tc.input})
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateNoNullFields(data); err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		_, has := fields["subtasks"]
		if has != tc.want {
			t.Fatal(string(data))
		}
	}
}

func actionFixture(t *testing.T) (*Store, Task, OpsEnvelope) {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := s.CreateMany([]TaskInput{{Title: "action", Kind: "work"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	task := tasks[0]
	head := s.History(HistoryFilter{TaskID: task.ID}).Records[0].ID
	lane := "done"
	return s, task, OpsEnvelope{Source: "board-reconciler", ExpectedHistoryHeads: map[string]string{task.ID: head}, Ops: []Operation{{Op: "patch", ID: task.ID, Lane: &lane, ExpectedRevision: &task.Revision}, {Op: "work_log", ID: task.ID, ExpectedRevision: &task.Revision, WorkLog: &WorkLogInput{Kind: WorkLogProgress, Text: "merged exact required MR", ActionID: "test-action"}}}}
}

func TestReconcileHistoryRaceAndReplay(t *testing.T) {
	s, task, envelope := actionFixture(t)
	original := s.History(HistoryFilter{})
	if _, err := s.AddWorkLog(WorkLogInput{TaskID: task.ID, Kind: WorkLogBlocker, Text: "human pause"}, Mutation{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ApplyEnvelope(envelope); !errors.Is(err, ErrHistoryConflict) {
		t.Fatalf("history race accepted: %v", err)
	}
	got, _ := s.Get(task.ID)
	if got.Revision != task.Revision || got.Lane == "done" {
		t.Fatal("race partially applied")
	}
	envelope.ExpectedHistoryHeads[task.ID] = s.History(HistoryFilter{TaskID: task.ID}).Records[0].ID
	if _, replayed, err := s.ApplyEnvelope(envelope); err != nil || replayed {
		t.Fatal(replayed, err)
	}
	before := s.History(HistoryFilter{})
	persisted := 0
	s.afterPersist = func() { persisted++ }
	if _, replayed, err := s.ApplyEnvelope(envelope); err != nil || !replayed {
		t.Fatal(replayed, err)
	}
	if persisted != 0 || !reflect.DeepEqual(before, s.History(HistoryFilter{})) {
		t.Fatal("replay persisted or duplicated history")
	}
	envelope.Ops[1].WorkLog.Text = "different action"
	if _, _, err := s.ApplyEnvelope(envelope); !errors.Is(err, ErrActionConflict) {
		t.Fatal(err)
	}
	if len(before.Records) != len(original.Records)+3 {
		t.Fatal(before)
	}
	reloaded, err := New(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	envelope.Ops[1].WorkLog.Text = "merged exact required MR"
	if _, replayed, err := reloaded.ApplyEnvelope(envelope); err != nil || !replayed {
		t.Fatal("reload replay", replayed, err)
	}
}
func TestReconcileActionAtomicFailure(t *testing.T) {
	s, task, envelope := actionFixture(t)
	before := s.History(HistoryFilter{})
	envelope.Ops = append(envelope.Ops, Operation{Op: "unlink", ID: task.ID, LinkID: "missing", ExpectedRevision: &task.Revision})
	if _, _, err := s.ApplyEnvelope(envelope); err == nil {
		t.Fatal("invalid action accepted")
	}
	if !reflect.DeepEqual(before, s.History(HistoryFilter{})) {
		t.Fatal("partial receipt")
	}
	envelope.Ops = envelope.Ops[:2]
	s.path = filepath.Join(t.TempDir(), "absent", "tasks.json")
	if _, _, err := s.ApplyEnvelope(envelope); !errors.Is(err, ErrPersistence) {
		t.Fatal(err)
	}
	got, _ := s.Get(task.ID)
	if got.Lane == "done" || !reflect.DeepEqual(before, s.History(HistoryFilter{})) {
		t.Fatal("persist failure committed")
	}
}
func TestStandaloneActionLogReplay(t *testing.T) {
	s, task, _ := actionFixture(t)
	input := WorkLogInput{TaskID: task.ID, Kind: WorkLogProgress, Text: "note", ActionID: "standalone"}
	first, replay, err := s.AddWorkLogResult(input, Mutation{TaskID: task.ID, ExpectedRevision: &task.Revision})
	if err != nil || replay || first.ActionHash == "" {
		t.Fatal(first, err)
	}
	lane := "doing"
	if _, err := s.Patch(task.ID, TaskPatch{Lane: &lane}); err != nil {
		t.Fatal(err)
	}
	second, replay, err := s.AddWorkLogResult(input, Mutation{TaskID: task.ID, ExpectedRevision: &task.Revision})
	if err != nil || !replay || second.ID != first.ID {
		t.Fatal(second, replay, err)
	}
	input.Text = "changed"
	if _, err := s.AddWorkLog(input, Mutation{}); !errors.Is(err, ErrActionConflict) {
		t.Fatal(err)
	}
}

func TestActionRejectsImplicitCreate(t *testing.T) {
	s, task, envelope := actionFixture(t)
	title := "must not create"
	envelope.Ops = append(envelope.Ops, Operation{ID: task.ID, Title: &title, ExpectedRevision: &task.Revision})
	before := s.History(HistoryFilter{})
	if _, _, err := s.ApplyEnvelope(envelope); err == nil {
		t.Fatal("implicit create accepted")
	}
	if !reflect.DeepEqual(before, s.History(HistoryFilter{})) || len(s.List(Filter{})) != 1 {
		t.Fatal("rejected action changed board")
	}
}
