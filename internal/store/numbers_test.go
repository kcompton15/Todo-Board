package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const versionTwoFixture = `{"version":2,"tasks":[
{"revision":1,"id":"tlate","title":"late","lane":"today","priority":2,"source":"test","order":1000,"createdAt":"2026-09-03T00:00:00Z","updatedAt":"2026-09-03T00:00:00Z","subtasks":[{"id":"sone","title":"one","lane":"today"},{"id":"stwo","title":"two","lane":"done","done":true}]},
{"revision":1,"id":"tbeta","title":"tie b","lane":"backlog","priority":2,"source":"test","order":1000,"createdAt":"2026-09-02T00:00:00Z","updatedAt":"2026-09-02T00:00:00Z","subtasks":[]},
{"revision":1,"id":"talpha","title":"tie a","lane":"done","priority":2,"source":"test","order":2000,"createdAt":"2026-09-02T00:00:00Z","updatedAt":"2026-09-02T00:00:00Z","subtasks":[]},
{"revision":1,"id":"tearly","title":"early","lane":"doing","priority":2,"source":"test","order":1000,"createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z","subtasks":[]}
]}`

func loadFixture(t *testing.T, data string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func numbersByID(s *Store) map[string]int {
	result := make(map[string]int)
	for _, task := range s.List(Filter{}) {
		result[task.ID] = task.Number
	}
	return result
}

func TestVersionTwoBackfillIsDeterministicAndBacksUp(t *testing.T) {
	first, dir := loadFixture(t, versionTwoFixture)
	second, _ := loadFixture(t, versionTwoFixture)
	want := map[string]int{"tearly": 1, "talpha": 2, "tbeta": 3, "tlate": 4}
	for _, s := range []*Store{first, second} {
		got := numbersByID(s)
		for id, number := range want {
			if got[id] != number {
				t.Fatalf("numbers = %v; want %v", got, want)
			}
		}
	}
	late, err := first.Get("tlate")
	if err != nil {
		t.Fatal(err)
	}
	if late.Subtasks[0].Number != 1 || late.Subtasks[1].Number != 2 || late.NextSubtaskNumber != 3 {
		t.Fatalf("subtask numbers = %#v next %d", late.Subtasks, late.NextSubtaskNumber)
	}
	unchanged, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != versionTwoFixture {
		t.Fatal("loading rewrote the state file")
	}

	created, err := first.CreateMany([]TaskInput{{Title: "after migration"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if created[0].Number != 5 {
		t.Fatalf("new card number = %d; want 5", created[0].Number)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "tasks.json.v2-*.backup"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("v2 backups = %v; want one", backups)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != versionTwoFixture {
		t.Fatal("backup does not hold the pre-migration state")
	}
	persisted, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state persistedState
	if err := json.Unmarshal(persisted, &state); err != nil {
		t.Fatal(err)
	}
	if state.Version != 3 || state.NextNumber != 6 {
		t.Fatalf("persisted version %d nextNumber %d; want 3 and 6", state.Version, state.NextNumber)
	}

	reloaded, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := numbersByID(reloaded); got["tlate"] != 4 || got[created[0].ID] != 5 {
		t.Fatalf("reloaded numbers = %v", got)
	}
	again, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(persisted) {
		t.Fatal("restart changed the persisted state")
	}
	if _, err := reloaded.CreateMany([]TaskInput{{Title: "no new backup"}}, "test"); err != nil {
		t.Fatal(err)
	}
	if backups, _ := filepath.Glob(filepath.Join(dir, "tasks.json.v*.backup")); len(backups) != 1 {
		t.Fatalf("a v3 write created another backup: %v", backups)
	}
}

func TestMissingNumbersFollowExistingOnes(t *testing.T) {
	s, _ := loadFixture(t, `{"version":3,"nextNumber":9,"tasks":[
{"revision":1,"id":"tnumbered","number":7,"title":"numbered","lane":"today","priority":2,"source":"test","createdAt":"2026-09-05T00:00:00Z","updatedAt":"2026-09-05T00:00:00Z","nextSubtaskNumber":6,"subtasks":[{"id":"sa","number":4,"title":"a","lane":"today"},{"id":"sb","title":"b","lane":"today"}]},
{"revision":1,"id":"tlegacy","title":"legacy","lane":"today","priority":2,"source":"test","createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-01T00:00:00Z","subtasks":[]}
]}`)
	got := numbersByID(s)
	if got["tnumbered"] != 7 || got["tlegacy"] != 9 {
		t.Fatalf("numbers = %v", got)
	}
	task, err := s.Get("tnumbered")
	if err != nil {
		t.Fatal(err)
	}
	if task.Subtasks[1].Number != 6 || task.NextSubtaskNumber != 7 {
		t.Fatalf("legacy step = %#v next %d", task.Subtasks[1], task.NextSubtaskNumber)
	}
}

func TestDuplicateNumbersRefuseToLoad(t *testing.T) {
	for name, data := range map[string]string{
		"cards": `{"version":3,"tasks":[{"id":"ta","number":1,"title":"a","lane":"today","priority":2,"source":"t","subtasks":[]},{"id":"tb","number":1,"title":"b","lane":"today","priority":2,"source":"t","subtasks":[]}]}`,
		"steps": `{"version":3,"tasks":[{"id":"ta","number":1,"title":"a","lane":"today","priority":2,"source":"t","subtasks":[{"id":"sa","number":2,"title":"a"},{"id":"sb","number":2,"title":"b"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := New(dir); err == nil || !strings.Contains(err.Error(), "share number") {
				t.Fatalf("New error = %v; want duplicate number", err)
			}
		})
	}
}

func TestNumbersPersistAndAreNeverReused(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMany([]TaskInput{{Title: "one"}, {Title: "two"}, {Title: "three", Subtasks: SubtaskInputs{{Title: "a"}, {Title: "b"}}}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	for i, task := range created {
		if task.Number != i+1 {
			t.Fatalf("created[%d].Number = %d", i, task.Number)
		}
	}
	if err := s.Delete(created[2].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSubtask(created[0].ID, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing subtask = %v", err)
	}
	reloaded, err := New(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	next, err := reloaded.CreateMany([]TaskInput{{Title: "four"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if next[0].Number != 4 {
		t.Fatalf("number after delete and reload = %d; want 4", next[0].Number)
	}

	added, _, err := reloaded.AddSubtasks(next[0].ID, SubtaskInputs{{Title: "a"}, {Title: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if added[0].Number != 1 || added[1].Number != 2 {
		t.Fatalf("added steps = %#v", added)
	}
	if err := reloaded.DeleteSubtask(next[0].ID, added[1].ID); err != nil {
		t.Fatal(err)
	}
	again, err := New(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	third, _, err := again.AddSubtasks(next[0].ID, SubtaskInputs{{Title: "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if third[0].Number != 3 {
		t.Fatalf("step number after deleting #2 = %d; want 3", third[0].Number)
	}
}

func TestFailedTransactionDoesNotConsumeNumber(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateMany([]TaskInput{{Title: "valid"}, {Title: ""}}, "test"); err == nil {
		t.Fatal("expected validation failure")
	}
	_, err := s.ApplyOps([]Operation{{Op: "create", Title: ptr("op card")}, {Op: "patch", ID: "tmissing", Title: ptr("x")}}, "test")
	if err == nil {
		t.Fatal("expected ops failure")
	}
	created, err := s.CreateMany([]TaskInput{{Title: "first real"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if created[0].Number != 1 {
		t.Fatalf("number after failed transactions = %d; want 1", created[0].Number)
	}
}

func TestEverySubtaskPathAssignsNumbers(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMany([]TaskInput{{Title: "card", Lane: "today", Subtasks: SubtaskInputs{{Title: "a"}, {Title: "b"}}}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	id := created[0].ID
	if _, err := s.ApplyOps([]Operation{{Op: "subtask", ID: id, Title: ptr("c")}}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyOps([]Operation{{Op: "create", Title: ptr("op card"), Subtasks: SubtaskInputs{{Title: "x"}}}}, "test"); err != nil {
		t.Fatal(err)
	}
	task, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	keep := task.Subtasks[2]
	patched, err := s.Patch(id, TaskPatch{Subtasks: &SubtaskInputs{{ID: keep.ID, Title: keep.Title}, {Title: "d"}}})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Subtasks[0].Number != 3 || patched.Subtasks[1].Number != 4 || patched.NextSubtaskNumber != 5 {
		t.Fatalf("patched steps = %#v next %d", patched.Subtasks, patched.NextSubtaskNumber)
	}
	opCard, _, err := s.Resolve("2")
	if err != nil {
		t.Fatal(err)
	}
	if opCard.Title != "op card" || opCard.Subtasks[0].Number != 1 {
		t.Fatalf("op card = %#v", opCard)
	}
}

func TestReplaceRoundTripKeepsNumbers(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateMany([]TaskInput{{Title: "filler"}}, "test"); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateMany([]TaskInput{{Title: "card", Lane: "today", Subtasks: SubtaskInputs{{Title: "a"}, {Title: "b"}, {Title: "c"}}}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	current := created[0]
	current.Number = 99
	current.Subtasks[0].Number = 42
	current.Subtasks = current.Subtasks[:2]
	data, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	var input TaskInput
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	input.Subtasks = append(input.Subtasks, SubtaskInput{Title: "new"})
	replaced, err := s.Replace(current.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Number != 2 {
		t.Fatalf("replace changed card number to %d", replaced.Number)
	}
	got := []int{replaced.Subtasks[0].Number, replaced.Subtasks[1].Number, replaced.Subtasks[2].Number}
	if got[0] != 1 || got[1] != 2 || got[2] != 4 {
		t.Fatalf("step numbers after replace = %v; want [1 2 4]", got)
	}
}

func TestResolveAcceptsEveryForm(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateMany([]TaskInput{{Title: "one", Subtasks: SubtaskInputs{{Title: "a"}, {Title: "b"}}}, {Title: "two"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	one := created[0]
	for _, ref := range []string{one.ID, "1", "#1", " 1 "} {
		task, step, err := s.Resolve(ref)
		if err != nil || task.ID != one.ID || step != nil {
			t.Fatalf("Resolve(%q) = %s %v %v", ref, task.ID, step, err)
		}
	}
	for _, ref := range []string{"1.2", "#1.2"} {
		task, step, err := s.Resolve(ref)
		if err != nil || task.ID != one.ID || step == nil || step.ID != one.Subtasks[1].ID {
			t.Fatalf("Resolve(%q) = %s %v %v", ref, task.ID, step, err)
		}
	}
	for _, ref := range []string{"", "0", "#", "-1", "+1", "1.", ".1", "1.0", "1.3", "3", "1.2.3", "##1", "t", "1e2", "99999999999"} {
		if _, _, err := s.Resolve(ref); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Resolve(%q) error = %v; want not found", ref, err)
		}
	}
	for ref, want := range map[string]string{one.Subtasks[0].ID: one.Subtasks[0].ID, "2": one.Subtasks[1].ID, "#2": one.Subtasks[1].ID, "1.1": one.Subtasks[0].ID, "#1.2": one.Subtasks[1].ID} {
		sub, ok := FindSubtask(one, ref)
		if !ok || sub.ID != want {
			t.Fatalf("FindSubtask(%q) = %v %v; want %s", ref, sub.ID, ok, want)
		}
	}
	for _, ref := range []string{"3", "2.1", "0", "x"} {
		if _, ok := FindSubtask(one, ref); ok {
			t.Fatalf("FindSubtask(%q) matched", ref)
		}
	}
}

func ptr[T any](value T) *T { return &value }
