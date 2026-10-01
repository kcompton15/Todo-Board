package scripts_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fakeDocker = `#!/usr/bin/env bash
printf 'docker %s\n' "$*" >> "$FAKE_STATE/calls"
exit 0
`

const fakeClaude = `#!/usr/bin/env bash
state="$FAKE_STATE"
printf 'claude %s\n' "$*" >> "$state/calls"
case "$*" in
  "plugin marketplace list --json")
    if [[ -f "$state/marketplace" ]]; then printf '[{"name":"kcompton15"}]'; else printf '[]'; fi ;;
  "plugin list --json")
    if [[ -f "$state/plugin" ]]; then printf '[{"id":"todo-board@kcompton15","enabled":true,"installPath":"%s"}]' "$state/cache"; else printf '[]'; fi ;;
  "plugin marketplace add "*) touch "$state/marketplace" ;;
  "plugin install todo-board@kcompton15") touch "$state/plugin" && rm -rf "$state/cache" && cp -R "$FAKE_PLUGIN_SOURCE" "$state/cache" ;;
  "plugin uninstall todo-board@kcompton15") rm -rf "$state/plugin" "$state/cache" ;;
  "plugin marketplace remove kcompton15") rm -f "$state/marketplace" ;;
  *) exit 64 ;;
esac
`

type teamEnv struct {
	home, state, repo string
	env               []string
}

func newTeamEnv(t *testing.T) teamEnv {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("team setup targets macOS")
	}
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(`ok`)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(board.Close)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(board.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	state := filepath.Join(root, "state")
	fake := filepath.Join(root, "bin")
	for _, dir := range []string{home, state, fake} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, filepath.Join(fake, "docker"), fakeDocker)
	writeExecutable(t, filepath.Join(fake, "claude"), fakeClaude)
	writeExecutable(t, filepath.Join(fake, "codex"), "#!/usr/bin/env bash\nexit 0\n")
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	repo, err = filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	return teamEnv{home: home, state: state, repo: repo, env: []string{
		"HOME=" + home,
		"PATH=" + fake + ":" + filepath.Join(home, ".local", "bin") + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"FAKE_STATE=" + state,
		"FAKE_PLUGIN_SOURCE=" + filepath.Join(repo, "plugin"),
		"TODO_PORT=" + port,
	}}
}

func (e teamEnv) run(t *testing.T, script string, args ...string) (string, error) {
	t.Helper()
	command := exec.Command("./"+script, args...)
	command.Env = e.env
	output, err := command.CombinedOutput()
	return string(output), err
}

func (e teamEnv) calls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.state, "calls"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTeamSetupInstallsIdempotentlyAndUninstalls(t *testing.T) {
	e := newTeamEnv(t)
	if output, err := e.run(t, "team-setup"); err != nil {
		t.Fatalf("setup: %v\n%s", err, output)
	}
	link := filepath.Join(e.home, ".local", "bin", "todo")
	if target, err := os.Readlink(link); err != nil || target != filepath.Join(e.repo, "scripts", "todo") {
		t.Fatalf("todo link %q %v", target, err)
	}
	agents := readFile(t, filepath.Join(e.home, ".codex", "AGENTS.md"))
	if !strings.Contains(agents, "<!-- TODO-BOARD:BEGIN -->\n## Personal task board") {
		t.Fatalf("AGENTS.md:\n%s", agents)
	}
	hooks := readFile(t, filepath.Join(e.home, ".codex", "hooks.json"))
	if !strings.Contains(hooks, filepath.Join(e.repo, "scripts", "board-context.sh")) {
		t.Fatalf("hooks.json:\n%s", hooks)
	}
	calls := e.calls(t)
	if !strings.Contains(calls, "docker compose -f "+filepath.Join(e.repo, "docker-compose.yml")+" up -d --build") ||
		!strings.Contains(calls, "claude plugin marketplace add "+e.repo) ||
		!strings.Contains(calls, "claude plugin install todo-board@kcompton15") {
		t.Fatalf("calls:\n%s", calls)
	}

	output, err := e.run(t, "team-setup")
	if err != nil || !strings.Contains(output, "already points at this clone") || !strings.Contains(output, "already installed") {
		t.Fatalf("rerun: %v\n%s", err, output)
	}
	if strings.Count(e.calls(t), "plugin install") != 1 || strings.Count(e.calls(t), "marketplace add") != 1 {
		t.Fatalf("rerun repeated plugin installs:\n%s", e.calls(t))
	}

	stale := filepath.Join(e.state, "cache", "rules", "board-rules.md")
	if err := os.WriteFile(stale, []byte("old rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err = e.run(t, "team-setup")
	if err != nil || !strings.Contains(output, "reinstalling") {
		t.Fatalf("stale plugin rerun: %v\n%s", err, output)
	}
	if strings.Count(e.calls(t), "plugin install") != 2 || readFile(t, stale) == "old rules\n" {
		t.Fatalf("stale plugin copy was not refreshed:\n%s", e.calls(t))
	}
	if readFile(t, filepath.Join(e.home, ".codex", "AGENTS.md")) != agents {
		t.Fatal("rerun changed AGENTS.md")
	}

	if output, err := e.run(t, "team-uninstall"); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, output)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("todo link left behind")
	}
	if strings.Contains(readFile(t, filepath.Join(e.home, ".codex", "AGENTS.md")), "TODO-BOARD") {
		t.Fatal("board block left behind")
	}
	if strings.Contains(readFile(t, filepath.Join(e.home, ".codex", "hooks.json")), "board-context.sh") {
		t.Fatal("board hook left behind")
	}
	calls = e.calls(t)
	if !strings.Contains(calls, "claude plugin uninstall todo-board@kcompton15") || !strings.Contains(calls, " down") {
		t.Fatalf("uninstall calls:\n%s", calls)
	}
}

func TestTeamSetupDryRunChangesNothing(t *testing.T) {
	e := newTeamEnv(t)
	output, err := e.run(t, "team-setup", "--dry-run")
	if err != nil || !strings.Contains(output, "dry run, nothing changed") {
		t.Fatalf("dry run: %v\n%s", err, output)
	}
	entries, err := os.ReadDir(e.home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("dry run wrote into HOME: %v", entries)
	}
	for _, mutation := range []string{"up -d", "marketplace add", "plugin install"} {
		if strings.Contains(e.calls(t), mutation) {
			t.Fatalf("dry run ran %q:\n%s", mutation, e.calls(t))
		}
	}
}

func TestTeamSetupRefusesForeignTodoLink(t *testing.T) {
	e := newTeamEnv(t)
	bin := filepath.Join(e.home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/somewhere/else/todo", filepath.Join(bin, "todo")); err != nil {
		t.Fatal(err)
	}
	output, err := e.run(t, "team-setup")
	if err == nil || !strings.Contains(output, "points elsewhere") {
		t.Fatalf("foreign link replaced: %v\n%s", err, output)
	}
	if target, err := os.Readlink(filepath.Join(bin, "todo")); err != nil || target != "/somewhere/else/todo" {
		t.Fatalf("foreign link changed: %q %v", target, err)
	}
}

func TestCodexIntegrationGuardsAndPreservesContent(t *testing.T) {
	e := newTeamEnv(t)
	codex := filepath.Join(e.home, ".codex")
	if err := os.MkdirAll(codex, 0o755); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(codex, "AGENTS.md")
	hooks := filepath.Join(codex, "hooks.json")
	misplaced := "# Mine\n\n<!-- TODO-BOARD:BEGIN -->\n## Versions\n\nkeep\n\n## Personal task board\n\nold\n<!-- TODO-BOARD:END -->\n"
	if err := os.WriteFile(agents, []byte(misplaced), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := e.run(t, "install-codex-integration")
	if err == nil || !strings.Contains(output, "## Versions") {
		t.Fatalf("misplaced marker accepted: %v\n%s", err, output)
	}
	if readFile(t, agents) != misplaced {
		t.Fatal("refused run changed AGENTS.md")
	}

	stale := "# Mine\n\n<!-- TODO-BOARD:BEGIN -->\n## Personal task board\n\nold rules\n<!-- TODO-BOARD:END -->\n\n## After\n"
	if err := os.WriteFile(agents, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooks, []byte(`{"description":"d","hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"guard"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := e.run(t, "install-codex-integration"); err != nil {
		t.Fatalf("install: %v\n%s", err, output)
	}
	got := readFile(t, agents)
	if strings.Contains(got, "old rules") || !strings.HasPrefix(got, "# Mine\n") || !strings.HasSuffix(got, "## After\n") || strings.Count(got, "TODO-BOARD:BEGIN") != 1 {
		t.Fatalf("replacement:\n%s", got)
	}
	hooksJSON := readFile(t, hooks)
	if !strings.Contains(hooksJSON, `"guard"`) || !strings.Contains(hooksJSON, `"description": "d"`) || !strings.Contains(hooksJSON, "board-context.sh") {
		t.Fatalf("hooks merge:\n%s", hooksJSON)
	}

	for _, broken := range []string{
		"<!-- TODO-BOARD:BEGIN -->\n<!-- TODO-BOARD:BEGIN -->\n<!-- TODO-BOARD:END -->\n",
		"<!-- TODO-BOARD:END -->\nx\n<!-- TODO-BOARD:BEGIN -->\n",
	} {
		if err := os.WriteFile(agents, []byte(broken), 0o644); err != nil {
			t.Fatal(err)
		}
		if output, err := e.run(t, "install-codex-integration"); err == nil {
			t.Fatalf("broken markers accepted:\n%s", output)
		}
		if readFile(t, agents) != broken {
			t.Fatal("refused run changed AGENTS.md")
		}
	}
}
