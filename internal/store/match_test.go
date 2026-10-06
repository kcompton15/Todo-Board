package store

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestTitleRefs(t *testing.T) {
	cases := map[string][]string{
		"Re-review ABC-942 plan":                                         {"ABC-942"},
		"XYZ-205 paired with ABC-1, ABC-1 again":                         {"XYZ-205", "ABC-1"},
		"Review https://gitlab.com/group/api/-/merge_requests/12/diffs.": {"group/api!12"},
		"Land group/sub/web!7. Then ABC-3":                               {"group/sub/web!7", "ABC-3"},
		"Fix UTF-8 and ISO-8601 handling":                                nil,
		"Support UTF-8, RFC-3339, ISO-8601 check":                        nil,
		"Branch ABC-942-fix and XABC-9 and ABC-1x and ABC-0":             nil,
		"Review web MR !114":                                             nil,
		"DEF-12 is not a configured project":                             nil,
		"https://gitlab.com/group/api/-/issues/5":                        nil,
	}
	for title, want := range cases {
		var got []string
		for _, link := range titleRefs(title) {
			if link.Role != "required" {
				t.Fatalf("%q role = %q", title, link.Role)
			}
			got = append(got, link.Ref)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("titleRefs(%q) = %v; want %v", title, got, want)
		}
	}
}

func TestTitleRefsNeedConfiguredPrefixes(t *testing.T) {
	t.Cleanup(func() {
		if err := SetIssuePrefixes([]string{"ABC", "XYZ"}); err != nil {
			t.Error(err)
		}
	})
	if err := SetIssuePrefixes(nil); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, link := range titleRefs("ABC-1 lands with group/api!3") {
		got = append(got, link.Ref)
	}
	if !reflect.DeepEqual(got, []string{"group/api!3"}) {
		t.Fatalf("unconfigured titleRefs = %v", got)
	}
	s := newMatchStore(t)
	links := []LinkInput{{Ref: "DEF-9"}}
	created, err := s.CreateMany([]TaskInput{{Title: "ABC-1 explicit", Links: &links}}, "test")
	if err != nil || len(created[0].Links) != 1 || created[0].Links[0].Ref != "DEF-9" {
		t.Fatalf("explicit link with no prefixes: %v %+v", err, created)
	}
}

func TestParseIssuePrefixes(t *testing.T) {
	got, err := ParseIssuePrefixes(" ABC, XY2 ,,")
	if err != nil || !reflect.DeepEqual(got, []string{"ABC", "XY2"}) {
		t.Fatalf("ParseIssuePrefixes = %v, %v", got, err)
	}
	if got, err := ParseIssuePrefixes(""); err != nil || got != nil {
		t.Fatalf("empty = %v, %v", got, err)
	}
	for _, bad := range []string{"abc", "A", "2AB", "AB-C", "AB|C"} {
		if _, err := ParseIssuePrefixes(bad); err == nil {
			t.Fatalf("ParseIssuePrefixes(%q) accepted", bad)
		}
		if err := SetIssuePrefixes([]string{bad}); err == nil {
			t.Fatalf("SetIssuePrefixes(%q) accepted", bad)
		}
	}
}

func newMatchStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateMergesTitleRefsAndExplicitRoleWins(t *testing.T) {
	s := newMatchStore(t)
	links := []LinkInput{{Ref: "https://example.atlassian.net/browse/ABC-5", Role: "reference"}}
	created, err := s.CreateMany([]TaskInput{{Title: "ABC-5 and ABC-6 and RFC-82", Links: &links}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, link := range created[0].Links {
		got[link.Ref] = link.Role
	}
	if !reflect.DeepEqual(got, map[string]string{"ABC-5": "reference", "ABC-6": "required"}) {
		t.Fatalf("links = %v", got)
	}
}

func TestOnMatchRejectAttachCreate(t *testing.T) {
	s := newMatchStore(t)
	seed, err := s.CreateMany([]TaskInput{{Title: "Review ABC-1 plan", Lane: "doing"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	card := seed[0]
	links := []LinkInput{{Ref: "ABC-1"}, {Ref: "group/api!3"}}
	input := TaskInput{Title: "Re-review the plan", Links: &links, Notes: "second round"}

	_, err = s.CreateOrAttach([]TaskInput{input}, "", OnMatchReject, Mutation{Actor: "cli"})
	var matched *MatchError
	if !errors.As(err, &matched) || matched.Match != (Match{ID: card.ID, Number: card.Number, Title: card.Title, Ref: "ABC-1"}) {
		t.Fatalf("reject = %v", err)
	}
	if len(s.List(Filter{})) != 1 {
		t.Fatal("reject changed the board")
	}

	outcomes, err := s.CreateOrAttach([]TaskInput{input}, "", OnMatchAttach, Mutation{Actor: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes[0]
	if !got.Attached || got.Task.ID != card.ID || got.Task.Revision != card.Revision+1 || got.Subtask == nil {
		t.Fatalf("attach = %#v", got)
	}
	if got.Subtask.Title != "Re-review the plan" || got.Subtask.Lane != "today" || got.Subtask.Number != 1 {
		t.Fatalf("attached step = %#v", got.Subtask)
	}
	refs := map[string]string{}
	for _, link := range got.Task.Links {
		refs[link.Ref] = link.Role + "/" + link.SubtaskID
	}
	if !reflect.DeepEqual(refs, map[string]string{"ABC-1": "required/", "group/api!3": "required/"}) {
		t.Fatalf("attached links = %v", refs)
	}
	var logged *WorkLogEntry
	for _, record := range s.History(HistoryFilter{TaskID: card.ID}).Records {
		if record.WorkLog != nil {
			logged = record.WorkLog
		}
	}
	if logged == nil || logged.Kind != WorkLogProgress || logged.SubtaskID != got.Subtask.ID || logged.Actor != "cli" ||
		!strings.HasPrefix(logged.Text, "attached from web: Re-review the plan") || !strings.Contains(logged.Text, "second round") {
		t.Fatalf("work log = %#v", logged)
	}
	if len(s.List(Filter{})) != 1 {
		t.Fatal("attach created a card")
	}

	outcomes, err = s.CreateOrAttach([]TaskInput{input}, "", OnMatchCreate)
	if err != nil {
		t.Fatal(err)
	}
	if outcomes[0].Attached || outcomes[0].Task.ID == card.ID || strings.HasPrefix(outcomes[0].Task.Notes, "Follow-on") {
		t.Fatalf("create = %#v", outcomes[0])
	}
}

func TestOnlyDoneMatchesCreateFollowOn(t *testing.T) {
	s := newMatchStore(t)
	if _, err := s.CreateMany([]TaskInput{{Title: "ABC-2 round one", Lane: "done"}, {Title: "ABC-2 round two", Lane: "done"}}, "test"); err != nil {
		t.Fatal(err)
	}
	for i, policy := range []string{OnMatchReject, OnMatchAttach} {
		outcomes, err := s.CreateOrAttach([]TaskInput{{Title: "ABC-2 round " + policy, Notes: "context"}}, "", policy)
		if err != nil {
			t.Fatal(err)
		}
		task := outcomes[0].Task
		if want := fmt.Sprintf("Follow-on to #%d.\n\ncontext", 2+i); outcomes[0].Attached || task.Notes != want {
			t.Fatalf("%s follow-on = %#v", policy, outcomes[0])
		}
		if _, err := s.Patch(task.ID, TaskPatch{Lane: ptr("done")}); err != nil {
			t.Fatal(err)
		}
	}
	long := strings.Repeat("x", MaxNotesLength)
	outcomes, err := s.CreateOrAttach([]TaskInput{{Title: "ABC-2 long", Notes: long}}, "", OnMatchCreate)
	if err != nil {
		t.Fatal(err)
	}
	if notes := outcomes[0].Task.Notes; len([]rune(notes)) != MaxNotesLength || !strings.HasPrefix(notes, "Follow-on to #4.\n\nxxx") {
		t.Fatalf("long notes = %d %q", len(notes), notes[:30])
	}
}

func TestReferenceLinksNeverMatchButStepLinksDo(t *testing.T) {
	s := newMatchStore(t)
	reference := []LinkInput{{Ref: "ABC-3", Role: "reference"}}
	seed, err := s.CreateMany([]TaskInput{
		{Title: "context only", Lane: "today", Links: &reference},
		{Title: "has steps", Lane: "today", Subtasks: SubtaskInputs{{ID: "sstep", Title: "step"}}},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LinkTask(seed[1].ID, LinkInput{Ref: "ABC-4", SubtaskID: "sstep"}); err != nil {
		t.Fatal(err)
	}

	outcomes, err := s.CreateOrAttach([]TaskInput{{Title: "Work on ABC-3"}}, "", OnMatchAttach)
	if err != nil || outcomes[0].Attached {
		t.Fatalf("reference link matched: %#v %v", outcomes, err)
	}
	outcomes, err = s.CreateOrAttach([]TaskInput{{Title: "Ship ABC-4"}}, "", OnMatchAttach)
	if err != nil || !outcomes[0].Attached || outcomes[0].Task.ID != seed[1].ID {
		t.Fatalf("step link did not match: %#v %v", outcomes, err)
	}
}

func TestAttachBatchIsOneTransaction(t *testing.T) {
	s := newMatchStore(t)
	seed, err := s.CreateMany([]TaskInput{{Title: "XYZ-9 outcome", Lane: "today"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	card := seed[0]
	outcomes, err := s.CreateOrAttach([]TaskInput{{Title: "XYZ-9 first"}, {Title: "XYZ-9 second", Lane: "backlog"}, {Title: "XYZ-10 new"}, {Title: "XYZ-10 follow"}}, "", OnMatchAttach)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != card.Revision+1 || len(after.Subtasks) != 2 || after.Subtasks[1].Lane != "backlog" {
		t.Fatalf("batch attach = %#v", after)
	}
	if outcomes[2].Attached || !outcomes[3].Attached || outcomes[3].Task.ID != outcomes[2].Task.ID {
		t.Fatalf("in-batch match = %#v", outcomes)
	}

	before := s.List(Filter{})
	_, err = s.CreateOrAttach([]TaskInput{{Title: "XYZ-9 third"}, {Title: "bad", Lane: "nowhere"}}, "", OnMatchAttach)
	if err == nil {
		t.Fatal("invalid batch succeeded")
	}
	if !reflect.DeepEqual(before, s.List(Filter{})) {
		t.Fatal("failed batch changed the board")
	}
	next, err := s.CreateMany([]TaskInput{{Title: "plain"}}, "test")
	if err != nil || next[0].Number != 3 {
		t.Fatalf("failed batch consumed a number: %#v %v", next, err)
	}
}

func TestOpsCreateDefaultsToAttach(t *testing.T) {
	s := newMatchStore(t)
	seed, err := s.CreateMany([]TaskInput{{Title: "ABC-7 card", Lane: "today"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	title := "ABC-7 via ops"
	if _, err := s.ApplyOps([]Operation{{Op: "create", Title: &title}}, "inbox"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(seed[0].ID)
	if err != nil || len(got.Subtasks) != 1 || len(s.List(Filter{})) != 1 {
		t.Fatalf("ops attach = %#v %v", got, err)
	}
	_, err = s.ApplyOps([]Operation{{Op: "create", Title: &title, OnMatch: OnMatchReject}}, "inbox")
	var matched *MatchError
	if !errors.As(err, &matched) {
		t.Fatalf("ops reject = %v", err)
	}
	if _, err := s.ApplyOps([]Operation{{Op: "patch", ID: seed[0].ID, Title: &title, OnMatch: OnMatchCreate}}, "inbox"); err == nil {
		t.Fatal("onMatch on patch accepted")
	}
}

func TestPickMatchPrefersDedicatedCardOverUmbrella(t *testing.T) {
	s := newMatchStore(t)
	seed, err := s.CreateMany([]TaskInput{
		{Title: "ABC-871 report export", Lane: "doing"},
		{Title: "Sprint 13: land ABC-931, then ABC-871", Lane: "doing"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	dedicated := seed[0]
	out, err := s.CreateOrAttach([]TaskInput{{Title: "Fix ABC-871 QA failures"}}, "", OnMatchAttach, Mutation{Actor: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if !out[0].Attached || out[0].Task.ID != dedicated.ID {
		t.Fatalf("attached to %q; want dedicated card", out[0].Task.Title)
	}
}
