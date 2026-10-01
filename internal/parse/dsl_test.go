package parse

import "testing"

func TestTasks(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantTitle   string
		wantLane    string
		wantTag     string
		wantProject string
		wantPrio    int
		wantSubs    int
	}{
		{
			name: "markers in any order", input: "Fix webhook @today #Review %API !1\n  - reproduce\n\tpatch",
			wantTitle: "Fix webhook", wantLane: "today", wantTag: "Review", wantProject: "API", wantPrio: 1, wantSubs: 2,
		},
		{name: "no markers", input: "Fix this!", wantTitle: "Fix this!", wantLane: "backlog", wantPrio: 2},
		{name: "numeric hashtag is prose", input: "Investigate #1 failure", wantTitle: "Investigate #1 failure", wantLane: "backlog", wantPrio: 2},
		{name: "crlf", input: "Ship board %Internal_Tools\r\n  first\r\n", wantTitle: "Ship board", wantProject: "Internal_Tools", wantLane: "backlog", wantPrio: 2, wantSubs: 1},
		{name: "orphan ignored", input: "  orphan\nReal task", wantTitle: "Real task", wantLane: "backlog", wantPrio: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tasks, err := Tasks(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if len(tasks) != 1 {
				t.Fatalf("expected one task, got %d", len(tasks))
			}
			got := tasks[0]
			if got.Title != test.wantTitle || got.Lane != test.wantLane || got.Tag != test.wantTag ||
				got.Project != test.wantProject || got.Priority != test.wantPrio || len(got.Subtasks) != test.wantSubs {
				t.Fatalf("unexpected parse: %#v", got)
			}
		})
	}
}

func TestTasksRejectsMarkerOnlyLine(t *testing.T) {
	if _, err := Tasks("@today !1 #Review %Web"); err == nil {
		t.Fatal("expected marker-only line to fail")
	}
}
