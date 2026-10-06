package inbox

import (
	"fmt"
	"github.com/kcompton15/Todo-Board/internal/store"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if err := store.SetJiraSite("example.atlassian.net"); err != nil {
		panic(err)
	}
	if err := store.SetIssuePrefixes([]string{"ABC", "XYZ"}); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestInboxLinkAndReceiptAtomicity(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.New(dir)
	tasks, _ := s.CreateMany([]store.TaskInput{{Title: "review", Kind: "review"}}, "")
	id := tasks[0].ID
	broadcasts := 0
	w := New(filepath.Join(dir, "inbox"), time.Second, s, func() { broadcasts++ }, nil)
	writeAtomic(t, w.directory, "bad.json", fmt.Sprintf(`{"ops":[{"op":"link","id":%q,"link":{"ref":"PROJ-1"}},{"op":"work_log","id":%q,"workLog":{"kind":"review_delivered","text":""}}]}`, id, id))
	w.scan()
	got, _ := s.Get(id)
	if broadcasts != 0 || len(got.Links) != 0 {
		t.Fatal("partial inbox commit")
	}
	writeAtomic(t, w.directory, "good.json", fmt.Sprintf(`{"ops":[{"op":"link","id":%q,"link":{"ref":"PROJ-1"},"expectedRevision":1},{"op":"patch","id":%q,"agentContext":"memo","reconcileMode":"manual","expectedRevision":1},{"op":"work_log","id":%q,"workLog":{"kind":"review_delivered","text":"Findings finalized"},"expectedRevision":1}]}`, id, id, id))
	w.scan()
	got, _ = s.Get(id)
	if broadcasts != 1 || len(got.Links) != 1 || got.AgentContext != "memo" || got.ReconcileMode != "manual" || got.Revision != 2 {
		t.Fatal(got, broadcasts)
	}
	history := s.History(store.HistoryFilter{TaskID: id})
	found := false
	for _, r := range history.Records {
		if r.WorkLog != nil && r.WorkLog.Kind == store.WorkLogReviewDelivered {
			found = true
		}
	}
	if !found {
		t.Fatal("missing receipt")
	}
}

func TestInboxArraysAttachToOpenCards(t *testing.T) {
	dir := t.TempDir()
	s, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := s.CreateMany([]store.TaskInput{{Title: "XYZ-180 rollout", Lane: "doing"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	w := New(filepath.Join(dir, "inbox"), time.Second, s, nil, nil)
	writeAtomic(t, w.directory, "attach.json", `[{"title":"XYZ-180 follow-up"},{"title":"Separate outcome"}]`)
	w.scan()
	got, err := s.Get(seed[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Subtasks) != 1 || got.Subtasks[0].Title != "XYZ-180 follow-up" || len(s.List(store.Filter{})) != 2 {
		t.Fatalf("inbox attach = %#v", got)
	}
}
