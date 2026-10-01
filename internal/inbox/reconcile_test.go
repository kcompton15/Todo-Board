package inbox

import (
	"encoding/json"
	"github.com/kcompton15/Todo-Board/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func TestActionInboxAtomicReplay(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.New(dir)
	tasks, _ := s.CreateMany([]store.TaskInput{{Title: "guard", Kind: "work"}}, "")
	task := tasks[0]
	head := s.History(store.HistoryFilter{TaskID: task.ID}).Records[0].ID
	count := 0
	w := New(filepath.Join(dir, "inbox"), time.Second, s, func() { count++ }, nil)
	lane := "done"
	e := store.OpsEnvelope{Source: "board-reconciler", ExpectedHistoryHeads: map[string]string{task.ID: head}, Ops: []store.Operation{
		{Op: "patch", ID: task.ID, Lane: &lane, ExpectedRevision: &task.Revision},
		{Op: "work_log", ID: task.ID, ExpectedRevision: &task.Revision, WorkLog: &store.WorkLogInput{Kind: store.WorkLogProgress, Text: "proof", ActionID: "inbox-action"}},
	}}
	send := func(name string) {
		t.Helper()
		data, _ := json.Marshal(e)
		writeAtomic(t, w.directory, name, string(data))
		w.scan()
	}
	e.Ops = append(e.Ops, store.Operation{Op: "unlink", ID: task.ID, LinkID: "missing", ExpectedRevision: &task.Revision})
	send("bad.json")
	got, _ := s.Get(task.ID)
	if count != 0 || got.Revision != task.Revision {
		t.Fatal("partial transaction")
	}
	e.Ops = e.Ops[:2]
	send("good.json")
	send("replay.json")
	if count != 1 {
		t.Fatal("replay broadcast", count)
	}
	logs := 0
	for _, r := range s.History(store.HistoryFilter{TaskID: task.ID}).Records {
		if r.WorkLog != nil && r.WorkLog.ActionID == "inbox-action" {
			logs++
		}
	}
	if logs != 1 {
		t.Fatal("duplicate receipt", logs)
	}
}
