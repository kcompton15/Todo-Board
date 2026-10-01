package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if err := SetJiraSite("example.atlassian.net"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func resetJiraSite(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if err := SetJiraSite("example.atlassian.net"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSetJiraSite(t *testing.T) {
	resetJiraSite(t)
	for _, bad := range []string{"https://example.atlassian.net", "example.atlassian.net:443", "example.atlassian.net/browse", "Example.atlassian.net", "-a.net", "a..net", "a b.net", "a.net."} {
		if err := SetJiraSite(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if JiraSite() != "example.atlassian.net" {
		t.Fatalf("failed set changed site to %q", JiraSite())
	}
	if err := SetJiraSite("other.example.com"); err != nil || JiraSite() != "other.example.com" {
		t.Fatal(JiraSite(), err)
	}
	if _, _, destination, err := CanonicalLink("https://other.example.com/browse/PROJ-7"); err != nil || destination != "https://other.example.com/browse/PROJ-7" {
		t.Fatal(destination, err)
	}
	if _, _, _, err := CanonicalLink("https://example.atlassian.net/browse/PROJ-7"); err == nil {
		t.Fatal("previous site accepted")
	}
}

func TestJiraRefsRequireSite(t *testing.T) {
	resetJiraSite(t)
	if err := SetJiraSite(""); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"PROJ-1", "https://example.atlassian.net/browse/PROJ-1"} {
		if _, _, _, err := CanonicalLink(value); err == nil {
			t.Errorf("accepted %q without a site", value)
		}
	}
	if _, _, _, err := CanonicalLink("PROJ-1"); err == nil || !strings.Contains(err.Error(), "TODO_JIRA_SITE") {
		t.Fatal(err)
	}
	if _, _, _, err := CanonicalLink("team/api!1"); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalLinks(t *testing.T) {
	for _, value := range []string{"PROJ-931", "OPS-1", "team/sub/api!175", "https://gitlab.com/team/api/-/merge_requests/175?x=1#note", "https://example.atlassian.net/browse/PROJ-931?x=1"} {
		if _, _, _, err := CanonicalLink(value); err != nil {
			t.Errorf("%s: %v", value, err)
		}
	}
	for _, value := range []string{"PROJ-0", "PROJ-01", "!1", "api!1", "https://evil.test/browse/PROJ-1", "http://gitlab.com/a/b/-/merge_requests/1", "https://user@gitlab.com/a/b/-/merge_requests/1", "https://gitlab.com:443/a/b/-/merge_requests/1", "https://gitlab.com/a%2fb/c/-/merge_requests/1", "https://gitlab.com/a/%2e%2e/-/merge_requests/1", "https://gitlab.com/a//b/-/merge_requests/1", "https://gitlab.com/a/b/-/merge_requests/0", "PROJ-1\n", "https://gitlab.com/a/b/-/merge_requests/1/", "https://gitlab.com/a/b/-/merge_requests/1%00"} {
		if _, _, _, err := CanonicalLink(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}

func TestLinkMetadataLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	memo := strings.Repeat("界", 4000)
	mode := "manual"
	inputs := []LinkInput{{Ref: "group/api!1"}}
	created, err := s.CreateMany([]TaskInput{{Title: "Review", Kind: "review", Links: &inputs, AgentContext: &memo, ReconcileMode: &mode, Subtasks: SubtaskInputs{{Title: "API"}}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	task := created[0]
	if task.Links[0].Role != "required" || task.ReconcileMode != "manual" || task.AgentContext != memo {
		t.Fatal(task)
	}
	raw, _ := json.Marshal(task.Links[0])
	if strings.Contains(string(raw), "stateChangedAt") {
		t.Fatal(string(raw))
	}
	before := s.History(HistoryFilter{})
	link, revision, err := s.LinkTask(task.ID, LinkInput{Ref: "group/api!1"})
	if err != nil || revision != task.Revision || link.ID != task.Links[0].ID || !reflect.DeepEqual(before, s.History(HistoryFilter{})) {
		t.Fatalf("no-op: %v %d", err, revision)
	}
	state := "merged"
	link, revision, err = s.LinkTask(task.ID, LinkInput{Ref: "group/api!1", State: &state})
	if err != nil || revision != task.Revision+1 || link.StateChangedAt == nil {
		t.Fatal(link, revision, err)
	}
	observed := *link.StateChangedAt
	*link.StateChangedAt = time.Time{}
	read, _ := s.Get(task.ID)
	if !read.Links[0].StateChangedAt.Equal(observed) {
		t.Fatal("aliased timestamp")
	}
	scoped, _, err := s.LinkTask(task.ID, LinkInput{Ref: "PROJ-931", SubtaskID: task.Subtasks[0].ID, Role: "reference"})
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := s.Replace(task.ID, TaskInput{Title: "Old PUT", Kind: "review", Subtasks: SubtaskInputs{{ID: task.Subtasks[0].ID, Title: "API"}}})
	if err != nil || len(replaced.Links) != 2 || replaced.AgentContext != memo || replaced.ReconcileMode != mode {
		t.Fatal(replaced, err)
	}
	round, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	read, _ = round.Get(task.ID)
	if len(read.Links) != 2 || read.Links[0].State != "merged" {
		t.Fatal(read)
	}
	if _, _, err = s.LinkTask(task.ID, LinkInput{Ref: "PROJ-932", ID: scoped.ID}); err == nil {
		t.Fatal("foreign identity accepted")
	}
	if _, _, err = s.LinkTask(task.ID, LinkInput{Ref: "PROJ-931", URL: "https://example.atlassian.net/browse/PROJ-932"}); err == nil {
		t.Fatal("mismatch accepted")
	}
	empty := ""
	clear := []LinkInput{}
	read, err = s.Patch(task.ID, TaskPatch{Links: &clear, AgentContext: &empty})
	if err != nil || len(read.Links) != 0 || read.AgentContext != "" {
		t.Fatal(read, err)
	}
	memo += "界"
	if _, err = s.Patch(task.ID, TaskPatch{AgentContext: &memo}); err == nil {
		t.Fatal("Unicode limit ignored")
	}
	history := s.History(HistoryFilter{TaskID: task.ID})
	found := false
	for _, r := range history.Records {
		for _, c := range r.Changes {
			if c.Field == "links" && strings.Contains(c.Before, "reference") {
				var links []Link
				if json.Unmarshal([]byte(c.Before), &links) != nil {
					t.Fatal(c)
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("missing structured history")
	}
}

func TestLinkRemovalAndAtomicity(t *testing.T) {
	for _, path := range []string{"delete", "put", "patch", "ops"} {
		t.Run(path, func(t *testing.T) {
			s, _ := New(t.TempDir())
			tasks, err := s.CreateMany([]TaskInput{{Title: "task", Subtasks: SubtaskInputs{{Title: "child"}}}}, "")
			if err != nil {
				t.Fatal(err)
			}
			task := tasks[0]
			_, _, err = s.LinkTask(task.ID, LinkInput{Ref: "PROJ-1", SubtaskID: task.Subtasks[0].ID})
			if err != nil {
				t.Fatal(err)
			}
			subs := SubtaskInputs{}
			switch path {
			case "delete":
				err = s.DeleteSubtask(task.ID, task.Subtasks[0].ID)
			case "put":
				_, err = s.Replace(task.ID, TaskInput{Title: "task"})
			case "patch":
				_, err = s.Patch(task.ID, TaskPatch{Subtasks: &subs})
			case "ops":
				_, err = s.ApplyOps([]Operation{{Op: "patch", ID: task.ID, Subtasks: subs}}, "test")
			}
			got, _ := s.Get(task.ID)
			if err != nil || len(got.Links) != 0 {
				t.Fatal(got, err)
			}
		})
	}
	s, _ := New(t.TempDir())
	created, _ := s.CreateMany([]TaskInput{{Title: "atomic"}}, "")
	task := created[0]
	before := s.History(HistoryFilter{})
	_, err := s.ApplyOps([]Operation{{Op: "link", ID: task.ID, Link: &LinkInput{Ref: "PROJ-1"}}, {Op: "link", ID: task.ID, Link: &LinkInput{Ref: "PROJ-2", SubtaskID: "missing"}}}, "test")
	if err == nil {
		t.Fatal("invalid batch succeeded")
	}
	got, _ := s.Get(task.ID)
	if len(got.Links) != 0 || !reflect.DeepEqual(before, s.History(HistoryFilter{})) {
		t.Fatal("partial commit")
	}
	bad := []LinkInput{{Ref: "PROJ-1", SubtaskID: "missing"}}
	if _, err = s.Replace(task.ID, TaskInput{Title: "atomic", Links: &bad}); err == nil {
		t.Fatal("dangling explicit link")
	}
	stale := task.Revision + 1
	if _, _, err = s.LinkTask(task.ID, LinkInput{Ref: "PROJ-1"}, Mutation{TaskID: task.ID, ExpectedRevision: &stale}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	s.path = filepath.Join(t.TempDir(), "absent", "tasks.json")
	if _, _, err = s.LinkTask(task.ID, LinkInput{Ref: "PROJ-1"}); !errors.Is(err, ErrPersistence) {
		t.Fatal(err)
	}
	got, _ = s.Get(task.ID)
	if len(got.Links) != 0 {
		t.Fatal("failed persistence committed")
	}
}

func TestReviewDeliveryReceipt(t *testing.T) {
	s, _ := New(t.TempDir())
	created, _ := s.CreateMany([]TaskInput{{Title: "review", Kind: "review", Subtasks: SubtaskInputs{{Title: "API"}}}, {Title: "work"}}, "")
	review := created[0]
	for _, input := range []WorkLogInput{{TaskID: created[1].ID, Kind: WorkLogReviewDelivered, Text: "done"}, {TaskID: review.ID, Kind: WorkLogReviewDelivered, Text: " "}, {TaskID: review.ID, Kind: WorkLogReviewDelivered, Text: "done", NeedsReview: true}, {TaskID: review.ID, Kind: WorkLogReviewDelivered, Text: "done", SubtaskID: "missing"}} {
		if _, err := s.AddWorkLog(input, Mutation{}); err == nil {
			t.Fatal("invalid receipt accepted", input)
		}
	}
	receipt, err := s.AddWorkLog(WorkLogInput{TaskID: review.ID, Kind: WorkLogReviewDelivered, Text: "Finalized API findings", SubtaskID: review.Subtasks[0].ID}, Mutation{Actor: "codex"})
	if err != nil || receipt.ID == "" || receipt.RecordedAt.IsZero() {
		t.Fatal(receipt, err)
	}
	reloaded, err := New(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	page := reloaded.History(HistoryFilter{TaskID: review.ID})
	if page.Records[0].ID != receipt.ID || page.Records[0].WorkLog.Text != receipt.Text {
		t.Fatal(page)
	}
	got, _ := s.Get(review.ID)
	if got.Lane == "done" || got.Revision != review.Revision {
		t.Fatal("receipt mutated completion")
	}
}

func TestLegacyMetadataDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(`{"version":1,"tasks":[{"id":"old","title":"Old","lane":"today","priority":2,"source":"web","subtasks":[]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := s.Get("old")
	raw, _ := json.Marshal(task)
	if task.ReconcileMode != "automatic" || !strings.Contains(string(raw), `"links":[]`) {
		t.Fatal(string(raw))
	}
}

func TestLinkCacheCapAndReceiptReport(t *testing.T) {
	s, _ := New(t.TempDir())
	tasks, _ := s.CreateMany([]TaskInput{{Title: "review", Kind: "review"}}, "")
	id := tasks[0].ID
	state, category := "Done", "done"
	link, revision, err := s.LinkTask(id, LinkInput{Ref: "PROJ-1", State: &state, StateCategory: &category})
	if err != nil {
		t.Fatal(err)
	}
	observed := *link.StateChangedAt
	before := s.History(HistoryFilter{})
	repeated, rev, err := s.LinkTask(id, LinkInput{Ref: "PROJ-1", State: &state, StateCategory: &category})
	if err != nil || rev != revision || !repeated.StateChangedAt.Equal(observed) || !reflect.DeepEqual(before, s.History(HistoryFilter{})) {
		t.Fatal("repeated observation changed history")
	}
	for _, input := range []LinkInput{{Ref: "group/api!1", StateCategory: &category}, {Ref: "group/api!1", State: &state}, {Ref: "PROJ-2", State: &state}, {Ref: "PROJ-2", StateCategory: &category}, {Ref: "PROJ-2", Role: "invalid"}} {
		if _, _, err = s.LinkTask(id, input); err == nil {
			t.Fatal("bad state/role", input)
		}
	}
	for i := 2; i <= MaxLinks; i++ {
		if _, _, err = s.LinkTask(id, LinkInput{Ref: fmt.Sprintf("PROJ-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err = s.LinkTask(id, LinkInput{Ref: "PROJ-100"}); err == nil {
		t.Fatal("cap ignored")
	}
	receipt, err := s.AddWorkLog(WorkLogInput{TaskID: id, Kind: WorkLogReviewDelivered, Text: "Delivered final findings"}, Mutation{Actor: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	location, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	day := receipt.RecordedAt.In(location).Format("2006-01-02")
	report, err := s.PreviewReport(ReportRequest{Kind: "standup", Start: day, End: day})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	if !strings.Contains(string(raw), "Delivered final findings") || !strings.Contains(string(raw), "Progress") {
		t.Fatal(string(raw))
	}
}
