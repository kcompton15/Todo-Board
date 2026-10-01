package reconcile

import (
	"github.com/kcompton15/Todo-Board/internal/store"
	"testing"
	"time"
)

var epoch = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func testTask(kind string) store.Task {
	return store.Task{ID: "t1", Revision: 1, Title: "Review task", Kind: kind, Lane: "doing", ReconcileMode: "automatic", CreatedAt: epoch}
}
func mrLink(ref, scope string) store.Link {
	return store.Link{Kind: "mr", Ref: ref, Role: "required", SubtaskID: scope}
}
func mrObservation(state string) Observation { return Observation{MR: &MRState{State: state}} }
func reviewObservation(event ReviewEvent) Observation {
	return Observation{MR: &MRState{State: "opened"}, Review: &ReviewActivity{ReviewerID: 7, Complete: true, Events: []ReviewEvent{event}}}
}
func TestScopeCompletionRules(t *testing.T) {
	after := epoch.Add(time.Hour)
	before := epoch.Add(-time.Hour)
	for _, test := range []struct {
		name, kind string
		links      []store.Link
		ev         Evidence
		want       bool
	}{
		{"all merged", "work", []store.Link{mrLink("a/api!1", ""), mrLink("a/web!2", "")}, Evidence{"a/api!1": mrObservation("merged"), "a/web!2": mrObservation("merged")}, true},
		{"mixed open", "work", []store.Link{mrLink("a/api!1", ""), mrLink("a/web!2", "")}, Evidence{"a/api!1": mrObservation("merged"), "a/web!2": mrObservation("opened")}, false},
		{"closed work", "work", []store.Link{mrLink("a/api!1", "")}, Evidence{"a/api!1": mrObservation("closed")}, false},
		{"terminal review", "review", []store.Link{mrLink("a/api!1", "")}, Evidence{"a/api!1": mrObservation("closed")}, true},
		{"Jira fallback", "work", []store.Link{{Kind: "jira", Ref: "PROJ-1", Role: "required"}}, Evidence{"PROJ-1": {Issue: &IssueState{Category: "done"}}}, true},
		{"Jira review insufficient", "review", []store.Link{{Kind: "jira", Ref: "PROJ-1", Role: "required"}}, Evidence{"PROJ-1": {Issue: &IssueState{Category: "done"}}}, false},
		{"reference only", "work", []store.Link{{Kind: "mr", Ref: "a/api!1", Role: "reference"}}, Evidence{"a/api!1": mrObservation("merged")}, false},
		{"missing", "work", []store.Link{mrLink("a/api!1", "")}, Evidence{}, false},
		{"approval", "review", []store.Link{mrLink("a/api!1", "")}, Evidence{"a/api!1": reviewObservation(ReviewEvent{Kind: "approval", AuthorID: 7, CreatedAt: &after})}, true},
		{"mixed review evidence", "review", []store.Link{mrLink("a/api!1", ""), mrLink("a/web!2", "")}, Evidence{"a/api!1": reviewObservation(ReviewEvent{Kind: "approval", AuthorID: 7, CreatedAt: &after}), "a/web!2": mrObservation("merged")}, true},
		{"old published finding", "review", []store.Link{mrLink("a/api!1", "")}, Evidence{"a/api!1": reviewObservation(ReviewEvent{Kind: "comment", AuthorID: 7, CreatedAt: &before, Diff: true, Body: "This drops required data when the process fails."})}, false},
		{"other reviewer", "review", []store.Link{mrLink("a/api!1", "")}, Evidence{"a/api!1": reviewObservation(ReviewEvent{Kind: "approval", AuthorID: 8, CreatedAt: &after})}, false},
		{"unknown approval time", "review", []store.Link{mrLink("a/api!1", "")}, Evidence{"a/api!1": reviewObservation(ReviewEvent{Kind: "approval", AuthorID: 7})}, false},
		{"old approval", "review", []store.Link{mrLink("a/api!1", "")}, Evidence{"a/api!1": reviewObservation(ReviewEvent{Kind: "approval", AuthorID: 7, CreatedAt: &before})}, false},
		{"finding", "review", []store.Link{mrLink("a/api!1", "")}, Evidence{"a/api!1": reviewObservation(ReviewEvent{Kind: "comment", AuthorID: 7, CreatedAt: &after, Diff: true, Body: "This drops the signed document when the process fails."})}, true},
		{"logistics", "review", []store.Link{mrLink("a/api!1", "")}, Evidence{"a/api!1": reviewObservation(ReviewEvent{Kind: "comment", AuthorID: 7, CreatedAt: &after, Body: "Will review this tomorrow."})}, false},
		{"bot", "review", []store.Link{mrLink("a/api!1", "")}, Evidence{"a/api!1": reviewObservation(ReviewEvent{Kind: "comment", AuthorID: 7, CreatedAt: &after, Bot: true, Body: "Review complete, no findings."})}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			task := testTask(test.kind)
			task.Links = test.links
			got, reason := scopeDone(task, "", test.ev, CardHistory{})
			if got != test.want {
				t.Fatal(got, reason)
			}
		})
	}
}

func TestExplicitNewReviewRoundInvalidatesOldActivity(t *testing.T) {
	task := testTask("review")
	task.Links = []store.Link{mrLink("a/api!1", "")}
	at := epoch.Add(time.Hour)
	ev := Evidence{"a/api!1": reviewObservation(ReviewEvent{Kind: "approval", AuthorID: 7, CreatedAt: &at})}
	h := CardHistory{Records: []store.HistoryRecord{{At: at.Add(time.Hour), WorkLog: &store.WorkLogEntry{Kind: store.WorkLogNextStep, Text: "Please re-review after the fixes"}}}}
	if done, _ := scopeDone(task, "", ev, h); done {
		t.Fatal("prior activity completed new review round")
	}
	h.Records[0].WorkLog = nil
	h.Records[0].Changes = []store.FieldChange{{Field: "agentContext", After: "new memo"}}
	if done, _ := scopeDone(task, "", ev, h); !done {
		t.Fatal("memo invalidated original review")
	}
}
func TestChildrenReceiptsRoundsAndBlockers(t *testing.T) {
	task := testTask("review")
	task.Subtasks = []store.Subtask{{ID: "s1", Title: "API", Lane: "doing"}, {ID: "s2", Title: "Deploy", Lane: "doing"}}
	task.Links = []store.Link{mrLink("a/api!1", "s1")}
	h := CardHistory{Complete: true}
	plan := Plan([]store.Task{task}, Evidence{"a/api!1": mrObservation("merged")}, HistoryEvidence{task.ID: h})
	if len(plan.Envelopes) != 1 {
		t.Fatal(plan)
	}
	for _, op := range plan.Envelopes[0].Batch.Ops {
		if op.Lane != nil || op.SubtaskID == "s2" {
			t.Fatal("unrelated child or parent completed")
		}
	}
	delivered := epoch.Add(time.Hour)
	h.Records = []store.HistoryRecord{{ID: "receipt", WorkLog: &store.WorkLogEntry{ID: "receipt", Kind: store.WorkLogReviewDelivered, RecordedAt: delivered}}}
	task.Subtasks = nil
	task.Links = nil
	if done, _ := scopeDone(task, "", nil, h); !done {
		t.Fatal("linkless receipt rejected")
	}
	h.Complete = false
	if done, _ := scopeDone(task, "", nil, h); done {
		t.Fatal("incomplete history authorized receipt")
	}
	h.Complete = true
	h.Records = append(h.Records, store.HistoryRecord{At: delivered.Add(time.Minute), Changes: []store.FieldChange{{Field: "title", Before: "old", After: "new"}}})
	if done, _ := scopeDone(task, "", nil, h); done {
		t.Fatal("old receipt covered new round")
	}
	h.Records = h.Records[:1]
	h.Records = append(h.Records, store.HistoryRecord{At: delivered.Add(time.Minute), Changes: []store.FieldChange{{Field: "agentContext", After: "hint"}}})
	if done, _ := scopeDone(task, "", nil, h); !done {
		t.Fatal("memo erased delivery")
	}
	task.Links = []store.Link{mrLink("a/api!1", "")}
	task.Kind = "work"
	h.Records = append(h.Records, store.HistoryRecord{At: delivered, WorkLog: &store.WorkLogEntry{ID: "block", RecordedAt: delivered, Kind: store.WorkLogBlocker, Text: "pause"}})
	if done, _ := scopeDone(task, "", Evidence{"a/api!1": mrObservation("merged")}, h); done {
		t.Fatal("merge bypassed blocker")
	}
	h.Records = append(h.Records, store.HistoryRecord{At: delivered.Add(time.Minute), WorkLog: &store.WorkLogEntry{Kind: store.WorkLogBlockerResolved, ResolvesBlockerID: "block"}})
	if done, _ := scopeDone(task, "", Evidence{"a/api!1": mrObservation("merged")}, h); !done {
		t.Fatal("resolved blocker still blocked")
	}
}
func TestLegacyReviewMigrationAndExclusions(t *testing.T) {
	task := testTask("review")
	task.Notes = "API: https://gitlab.com/a/api/-/merge_requests/1\nWEB: https://gitlab.com/a/web/-/merge_requests/2\nExample: https://gitlab.com/a/example/-/merge_requests/3"
	links, _ := legacyLinks(task)
	if len(links) != 3 || links[0].Role != "required" || links[2].Role != "reference" {
		t.Fatal(links)
	}
	evidence := Evidence{"a/api!1": mrObservation("closed"), "a/web!2": mrObservation("merged")}
	plan := Plan([]store.Task{task}, evidence, HistoryEvidence{task.ID: {Head: "h"}})
	found := false
	for _, op := range plan.Envelopes[0].Batch.Ops {
		if op.Lane != nil && *op.Lane == "done" {
			found = true
		}
	}
	if !found {
		t.Fatal("legacy exact URLs could not close without receipt")
	}
	task.Links = []store.Link{{Ref: "a/api!1", Kind: "mr", Role: "reference"}}
	links, _ = legacyLinks(task)
	for _, l := range links {
		if l.Ref == "a/api!1" {
			t.Fatal("explicit reference overridden")
		}
	}
	for _, kind := range []string{"followup", "probe"} {
		task.Kind = kind
		if len(Plan([]store.Task{task}, evidence, nil).Envelopes) != 0 {
			t.Fatal("excluded kind mutated")
		}
	}
	task.Kind = "review"
	task.ReconcileMode = "manual"
	if len(Plan([]store.Task{task}, evidence, nil).Envelopes) != 0 {
		t.Fatal("manual mutated")
	}
}
