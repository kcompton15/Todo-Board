package scripts_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func (e teamEnv) envFile() string {
	for _, kv := range e.env {
		if value, ok := strings.CutPrefix(kv, "TODO_ENV_FILE="); ok {
			return value
		}
	}
	return ""
}

func TestTeamSetupSavesAndKeepsSettings(t *testing.T) {
	e := newTeamEnv(t)
	output, err := e.run(t, "team-setup", "--jira-site", "https://Acme.Atlassian.net/browse", "--issue-prefixes", "proj, ops")
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, output)
	}
	saved := readFile(t, e.envFile())
	for _, want := range []string{"TODO_JIRA_SITE=acme.atlassian.net\n", "TODO_ISSUE_PREFIXES=PROJ,OPS\n", "TODO_CONTAINER_NAME=todo-board\n"} {
		if !strings.Contains(saved, want) {
			t.Fatalf(".env missing %q:\n%s", want, saved)
		}
	}
	if err := os.WriteFile(e.envFile(), []byte("# mine\nEXTRA=1\n"+saved), 0o600); err != nil {
		t.Fatal(err)
	}

	output, err = e.run(t, "team-setup")
	if err != nil || !strings.Contains(output, "Jira acme.atlassian.net, title keys PROJ,OPS") {
		t.Fatalf("rerun did not keep settings: %v\n%s", err, output)
	}
	if again := readFile(t, e.envFile()); !strings.Contains(again, "EXTRA=1") || strings.Count(again, "TODO_JIRA_SITE=") != 1 {
		t.Fatalf("rerun rewrote .env badly:\n%s", again)
	}

	output, err = e.run(t, "team-setup", "--jira-site", "other.atlassian.net")
	if err == nil || !strings.Contains(output, "set up for Jira site acme.atlassian.net") {
		t.Fatalf("site change was not refused: %v\n%s", err, output)
	}
}

func TestTeamSetupRejectsBadSettings(t *testing.T) {
	for _, args := range [][]string{
		{"--jira-site", "not a host"},
		{"--jira-site", "acme.atlassian.net", "--issue-prefixes", "1X"},
		{"--issue-prefixes", "PROJ"},
	} {
		e := newTeamEnv(t)
		if output, err := e.run(t, "team-setup", args...); err == nil {
			t.Fatalf("%v succeeded:\n%s", args, output)
		}
		if _, err := os.Stat(e.envFile()); !os.IsNotExist(err) {
			t.Fatalf("%v wrote .env", args)
		}
	}
	e := newTeamEnv(t)
	output, err := e.run(t, "team-setup", "--no-jira")
	if err != nil || !strings.Contains(readFile(t, e.envFile()), "TODO_JIRA_SITE=\n") {
		t.Fatalf("--no-jira: %v\n%s", err, output)
	}
}

func TestTodoFollowsTheSavedPort(t *testing.T) {
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte("saved-port board")); err != nil {
			t.Error(err)
		}
	}))
	defer board.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(board.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	clone := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clone, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"todo", "board-url"} {
		writeExecutable(t, filepath.Join(clone, "scripts", script), readFile(t, script))
	}
	if err := os.WriteFile(filepath.Join(clone, ".env"), []byte("TODO_PORT="+port+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "todo")
	if err := os.Symlink(filepath.Join(clone, "scripts", "todo"), link); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(link, "health")
	cmd.Env = append(os.Environ(), "TODO_URL=")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "saved-port board") {
		t.Fatalf("todo health: %v\n%s", err, out)
	}
}

func TestTeamSetupAddsPathOnRequest(t *testing.T) {
	e := newTeamEnv(t)
	for i, kv := range e.env {
		if strings.HasPrefix(kv, "PATH=") {
			e.env[i] = strings.Replace(kv, ":"+filepath.Join(e.home, ".local", "bin"), "", 1)
		}
	}
	zshrc := filepath.Join(e.home, ".zshrc")
	for range 2 {
		if output, err := e.run(t, "team-setup", "--add-to-path", "--no-jira"); err != nil {
			t.Fatalf("setup: %v\n%s", err, output)
		}
	}
	if got := readFile(t, zshrc); strings.Count(got, `export PATH="$HOME/.local/bin:$PATH"`) != 1 {
		t.Fatalf(".zshrc:\n%s", got)
	}
}
