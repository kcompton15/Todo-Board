package store

import "testing"

func TestFoldKey(t *testing.T) {
	cases := map[string]string{
		"Web App":    "web-app",
		"web-app":    "web-app",
		"  Web__App": "web-app",
		"web - app-": "web-app",
		"API":        "api",
		"":           "",
		"todo-board": "todo-board",
	}
	for in, want := range cases {
		if got := foldKey(in); got != want {
			t.Fatalf("foldKey(%q) = %q; want %q", in, got, want)
		}
	}
}

// driftedStore plants spellings directly, since every write path now folds them.
func driftedStore(t *testing.T, names [][2]string) (*Store, []Task) {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]TaskInput, len(names))
	for i := range names {
		inputs[i] = TaskInput{Title: "seed"}
	}
	created, err := s.CreateMany(inputs, "")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	for i, name := range names {
		task := s.tasks[created[i].ID]
		task.Project, task.Tag = name[0], name[1]
		s.tasks[task.ID] = task
		created[i] = task
	}
	s.mu.Unlock()
	return s, created
}

func TestFoldProjectAndTagOnCreate(t *testing.T) {
	s, _ := driftedStore(t, [][2]string{
		{"web-app", "feature"}, {"web-app", "feature"}, {"Web App", "Feature"}, {"billing-sync", ""},
	})
	created, err := s.CreateMany([]TaskInput{
		{Title: "e", Project: "Web_App", Tag: "FEATURE"},
		{Title: "f", Project: "billing-sync-worker", Tag: "BrandNew"},
		{Title: "g", Tag: "brandnew"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if created[0].Project != "web-app" || created[0].Tag != "feature" {
		t.Fatalf("folded = %q/%q", created[0].Project, created[0].Tag)
	}
	if created[1].Project != "billing-sync-worker" || created[1].Tag != "BrandNew" {
		t.Fatalf("key-different names changed: %q/%q", created[1].Project, created[1].Tag)
	}
	if created[2].Tag != "BrandNew" {
		t.Fatalf("same-batch fold = %q", created[2].Tag)
	}
}

func TestFoldTiePrefersLowercaseSpelling(t *testing.T) {
	s, _ := driftedStore(t, [][2]string{{"", "tooling"}, {"", "Tooling"}})
	created, err := s.CreateMany([]TaskInput{{Title: "c", Tag: "TOOLING"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if created[0].Tag != "tooling" {
		t.Fatalf("tie = %q; want tooling", created[0].Tag)
	}
}

func TestFoldOnPatchAndReplace(t *testing.T) {
	s, tasks := driftedStore(t, [][2]string{
		{"web-app", ""}, {"web-app", ""}, {"Web App", ""}, {"API", ""},
	})
	drifted := tasks[2]
	lane := "today"
	moved, err := s.Patch(drifted.ID, TaskPatch{Lane: &lane})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Project != "Web App" {
		t.Fatalf("patch without project changed it to %q", moved.Project)
	}
	project := "Web App"
	patched, err := s.Patch(drifted.ID, TaskPatch{Project: &project})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Project != "web-app" {
		t.Fatalf("patch project = %q", patched.Project)
	}
	replaced, err := s.Replace(tasks[3].ID, TaskInput{Title: "d", Project: "web app"})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Project != "web-app" {
		t.Fatalf("replace project = %q", replaced.Project)
	}
	alone := "Solo Project"
	solo, err := s.Patch(tasks[0].ID, TaskPatch{Project: &alone})
	if err != nil {
		t.Fatal(err)
	}
	if solo.Project != "Solo Project" {
		t.Fatalf("unique project = %q", solo.Project)
	}
}

func TestFoldPatchExcludesOwnSpelling(t *testing.T) {
	s, tasks := driftedStore(t, [][2]string{{"docs", ""}, {"Docs", ""}})
	project := "docs"
	patched, err := s.Patch(tasks[0].ID, TaskPatch{Project: &project})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Project != "Docs" {
		t.Fatalf("project = %q; want Docs from the other card", patched.Project)
	}
}
