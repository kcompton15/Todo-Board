package scripts_test

import (
	"github.com/kcompton15/Todo-Board/internal/httpapi"
	"github.com/kcompton15/Todo-Board/internal/store"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackfillProjects(t *testing.T) {
	dir := t.TempDir()
	fixture := `{"version":2,"tasks":[
{"id":"ta","title":"alpha","lane":"today","priority":2,"source":"test","project":"web-app","tag":"feature"},
{"id":"tb","title":"beta","lane":"today","priority":2,"source":"test","project":"web-app","tag":"feature"},
{"id":"tc","title":"gamma","lane":"done","priority":2,"source":"test","project":"Web App","tag":"Feature"},
{"id":"td","title":"delta","lane":"backlog","priority":2,"source":"test","project":"billing-sync","tag":"Tooling"},
{"id":"te","title":"epsilon","lane":"backlog","priority":2,"source":"test","project":"billing-sync-worker","tag":"tooling"}]}`
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(s, httpapi.NewHub(), nil, nil).Handler())
	defer server.Close()
	run := func(wantError bool, url string, args ...string) string {
		t.Helper()
		cmd := exec.Command("bash", append([]string{"./backfill-projects.sh"}, args...)...)
		cmd.Env = append(os.Environ(), "TODO_URL="+url)
		data, err := cmd.CombinedOutput()
		if (err != nil) != wantError {
			t.Fatalf("%v: %v %s", args, err, data)
		}
		return string(data)
	}
	dry := run(false, server.URL)
	if again := run(false, server.URL, "--dry-run"); again != dry {
		t.Fatalf("unstable dry run:\n%s\n%s", dry, again)
	}
	for _, want := range []string{
		"project: web-app (2), Web App (1) -> web-app",
		"tag: feature (2), Feature (1) -> feature",
		"tag: tooling (1), Tooling (1) -> tooling",
		"#3    project  Web App  web-app  gamma",
		"#3    tag      Feature  feature  gamma",
		"#4    tag      Tooling  tooling  delta",
		"project: billing-sync (1)  ~  billing-sync-worker (1)",
		"Dry run: 2 card(s) would change.",
	} {
		if !strings.Contains(dry, want) {
			t.Fatalf("dry run missing %q:\n%s", want, dry)
		}
	}
	gamma, _ := s.Get("tc")
	if gamma.Project != "Web App" || gamma.Revision != 1 {
		t.Fatalf("dry run mutated: %#v", gamma)
	}
	applied := run(false, server.URL, "--apply")
	if !strings.Contains(applied, "Applied: 2 of 2 card(s) updated.") {
		t.Fatal(applied)
	}
	gamma, _ = s.Get("tc")
	epsilon, _ := s.Get("te")
	delta, _ := s.Get("td")
	if gamma.Project != "web-app" || gamma.Tag != "feature" || gamma.Revision != 2 || delta.Tag != "tooling" || delta.Project != "billing-sync" {
		t.Fatalf("apply result: %#v %#v %#v", gamma, epsilon, delta)
	}
	if s.Activity("tc")[0].Actor != "backfill-projects" {
		t.Fatal("actor missing")
	}
	if after := run(false, server.URL); !strings.Contains(after, "No drifted spellings.") || !strings.Contains(after, "Dry run: 0 card(s)") {
		t.Fatal(after)
	}
	run(true, server.URL, "--apply", "extra")
	run(true, "http://example.com:7337")
}
