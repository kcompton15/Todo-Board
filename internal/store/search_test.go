package store

import (
	"reflect"
	"testing"
)

func TestSearchRanksByDistinctTokens(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateMany([]TaskInput{
		{Title: "git hooks lint", Lane: "doing"},
		{Title: "api lint gate", Lane: "done"},
		{Title: "unrelated", Notes: "talk about the gate", Lane: "today"},
		{Title: "nothing here", Lane: "today"},
		{Title: "tagged", Tag: "lint-tools", Project: "gate-keeper", Lane: "backlog"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := s.List(Filter{Query: "Lint  GATE lint"})
	var titles []string
	for _, task := range got {
		titles = append(titles, task.Title)
	}
	want := []string{"api lint gate", "tagged", "git hooks lint", "unrelated"}
	if !reflect.DeepEqual(titles, want) {
		t.Fatalf("titles = %v; want %v", titles, want)
	}
	if score := searchScore(got[0], searchTokens("lint gate")); score != 4 {
		t.Fatalf("score = %d", score)
	}
	midWord := Task{Title: "Investigate flaky lint"}
	if score := searchScore(midWord, searchTokens("lint gate")); score != 3 {
		t.Fatalf("mid-word title score = %d; want 3", score)
	}
}

func TestSearchMatchesSubtasksLinksAndNumbers(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	links := []LinkInput{{Ref: "group/api!12", Role: "reference"}}
	created, err := s.CreateMany([]TaskInput{
		{Title: "first", Subtasks: SubtaskInputs{{Title: "Confirm callback URLs"}}},
		{Title: "second", Links: &links},
		{Title: "third mentions 1"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.List(Filter{Query: "callback"}); len(got) != 1 || got[0].ID != created[0].ID {
		t.Fatalf("subtask search = %v", got)
	}
	if got := s.List(Filter{Query: "group/api!12"}); len(got) != 1 || got[0].ID != created[1].ID {
		t.Fatalf("link search = %v", got)
	}
	for _, query := range []string{"#1", "1", "1.1"} {
		got := s.List(Filter{Query: query})
		if len(got) == 0 || got[0].ID != created[0].ID {
			t.Fatalf("number search %q = %v", query, got)
		}
	}
	if got := s.List(Filter{Query: "#2"}); len(got) != 1 || got[0].ID != created[1].ID {
		t.Fatalf("#2 = %v", got)
	}
}
