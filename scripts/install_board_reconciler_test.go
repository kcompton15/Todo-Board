package scripts_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fakeLaunchctl = `#!/usr/bin/env bash
set -euo pipefail
state="$FAKE_LAUNCHD"
printf '%s\n' "$*" >> "$state/calls"
case "$1" in
  print)
    [[ -f "$state/loaded" ]] || exit 113
    printf 'run interval = 60 seconds\nprogram = %s\n' "$(cat "$state/program")"
    ;;
  bootstrap)
    if [[ -f "$state/fail-bootstrap-once" ]]; then rm "$state/fail-bootstrap-once"; exit 5; fi
    plutil -extract ProgramArguments.0 raw "$3" > "$state/program"
    cp "$3" "$state/loaded"
    ;;
  bootout) rm -f "$state/loaded" ;;
  enable) rm -f "$state/disabled" ;;
  disable) printf '%s' "${2##*/}" > "$state/disabled" ;;
  print-disabled)
    [[ -f "$state/disabled" ]] && printf '\t"%s" => disabled\n' "$(cat "$state/disabled")"
    exit 0
    ;;
  *) exit 64 ;;
esac
`

const fakeGlab = `#!/usr/bin/env bash
if [[ "$*" == "api --method GET --hostname gitlab.com user" ]]; then
  [[ -n "${FAKE_GLAB_USER:-}" ]] && printf '{"id":7,"username":"%s"}' "$FAKE_GLAB_USER"
fi
exit 0
`

const fakeSecurity = `#!/usr/bin/env bash
[[ -z "${FAKE_NO_TOKEN:-}" ]] || exit 44
if [[ "$*" == "find-generic-password -s atlassian-api-token" ]]; then
  printf 'keychain: "login.keychain-db"\nattributes:\n    "acct"<blob>="%s"\n    "svce"<blob>="atlassian-api-token"\n' "${FAKE_KEYCHAIN_ACCOUNT:-owner@example.com}"
  exit 0
fi
[[ "$*" == "find-generic-password -a owner@example.com -s atlassian-api-token" ]]
`

type installEnv struct {
	home, launchd, source string
	env                   []string
}

func newInstallEnv(t *testing.T, home string) installEnv {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("installer targets macOS launchd and plutil")
	}
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(`{"tasks":[]}`)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(board.Close)
	root := t.TempDir()
	fake := filepath.Join(root, "bin")
	launchd := filepath.Join(root, "launchd")
	for _, dir := range []string{fake, launchd, home} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, filepath.Join(fake, "launchctl"), fakeLaunchctl)
	writeExecutable(t, filepath.Join(fake, "glab"), fakeGlab)
	writeExecutable(t, filepath.Join(fake, "security"), fakeSecurity)
	identity := filepath.Join(home, ".config", "todo-board", "reconciler.json")
	if err := os.MkdirAll(filepath.Dir(identity), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identity, []byte(`{"gitlabUser":"reviewer.one","jiraEmail":"owner@example.com","jiraSite":"example.atlassian.net"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "board-reconciler-new")
	writeExecutable(t, source, "#!/usr/bin/env bash\necho new\n")
	return installEnv{home: home, launchd: launchd, source: source, env: []string{
		"HOME=" + home,
		"PATH=" + fake + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_LAUNCHD=" + launchd,
		"BOARD_RECONCILER_SOURCE=" + source,
		"TODO_URL=" + board.URL,
	}}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (e installEnv) run(t *testing.T, extra []string, args ...string) (string, error) {
	t.Helper()
	command := exec.Command("./install-board-reconciler", args...)
	command.Env = append(append([]string{}, e.env...), extra...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func (e installEnv) plist() string {
	return filepath.Join(e.home, "Library", "LaunchAgents", "io.github.kcompton15.board-reconciler.plist")
}

func plistValue(t *testing.T, path, key string) string {
	t.Helper()
	output, err := exec.Command("plutil", "-extract", key, "raw", path).Output()
	if err != nil {
		t.Fatalf("plutil -extract %s: %v", key, err)
	}
	return strings.TrimSpace(string(output))
}

func TestInstallBoardReconcilerEnableEscapesAndReadsBack(t *testing.T) {
	e := newInstallEnv(t, filepath.Join(t.TempDir(), `home & <co> "q"`))
	output, err := e.run(t, nil, "--enable")
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, output)
	}
	bin := filepath.Join(e.home, ".local", "bin", "board-reconciler")
	if data, err := os.ReadFile(bin); err != nil || string(data) != "#!/usr/bin/env bash\necho new\n" {
		t.Fatalf("binary not installed: %v", err)
	}
	if err := exec.Command("plutil", "-lint", e.plist()).Run(); err != nil {
		t.Fatal("installed plist does not lint")
	}
	if got := plistValue(t, e.plist(), "ProgramArguments.0"); got != bin {
		t.Fatalf("program %q", got)
	}
	if got := plistValue(t, e.plist(), "StartInterval"); got != "60" {
		t.Fatalf("interval %q", got)
	}
	arguments := []string{}
	for i := 1; i <= 6; i++ {
		arguments = append(arguments, plistValue(t, e.plist(), "ProgramArguments."+string(rune('0'+i))))
	}
	if strings.Join(arguments, " ") != "--scheduled --todo-url "+strings.TrimPrefix(e.env[4], "TODO_URL=")+" --log-file "+filepath.Join(e.home, ".local", "log", "board-reconciler.log")+" --no-llm" {
		t.Fatalf("arguments %q", arguments)
	}
	if info, err := os.Stat(plistValue(t, e.plist(), "WorkingDirectory")); err != nil || !info.IsDir() {
		t.Fatalf("working directory missing: %v", err)
	}
	if !strings.HasPrefix(plistValue(t, e.plist(), "EnvironmentVariables.PATH"), "/") {
		t.Fatal("PATH is not explicit")
	}
	calls := readFile(t, filepath.Join(e.launchd, "calls"))
	if !strings.Contains(calls, "enable gui/") || !strings.Contains(calls, "bootstrap gui/") {
		t.Fatalf("launchctl calls:\n%s", calls)
	}
}

func TestInstallBoardReconcilerRestoresPreviousOnFailedBootstrap(t *testing.T) {
	e := newInstallEnv(t, t.TempDir())
	if output, err := e.run(t, nil, "--enable"); err != nil {
		t.Fatalf("first install: %v\n%s", err, output)
	}
	oldPlist := readFile(t, e.plist())
	bin := filepath.Join(e.home, ".local", "bin", "board-reconciler")
	writeExecutable(t, bin, "#!/usr/bin/env bash\necho old\n")
	writeExecutable(t, e.source, "#!/usr/bin/env bash\necho broken\n")
	if err := os.WriteFile(filepath.Join(e.launchd, "fail-bootstrap-once"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := e.run(t, []string{"TODO_URL=http://127.0.0.1:9"}, "--enable")
	if err == nil {
		t.Fatalf("unreachable board accepted:\n%s", output)
	}
	output, err = e.run(t, nil, "--enable")
	if err == nil || !strings.Contains(output, "restoring the previous installation") {
		t.Fatalf("failed bootstrap not reported: %v\n%s", err, output)
	}
	if data := readFile(t, bin); data != "#!/usr/bin/env bash\necho old\n" {
		t.Fatalf("binary not restored: %q", data)
	}
	if readFile(t, e.plist()) != oldPlist {
		t.Fatal("plist not restored")
	}
	if _, err := os.Stat(filepath.Join(e.launchd, "loaded")); err != nil {
		t.Fatal("previous agent not reloaded")
	}
}

func TestInstallBoardReconcilerDisableLeavesAgentUnloaded(t *testing.T) {
	e := newInstallEnv(t, t.TempDir())
	if output, err := e.run(t, nil, "--enable"); err != nil {
		t.Fatalf("enable: %v\n%s", err, output)
	}
	output, err := e.run(t, nil, "--disable")
	if err != nil || !strings.Contains(output, "disabled, not loaded") {
		t.Fatalf("disable: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(e.launchd, "loaded")); err == nil {
		t.Fatal("agent still loaded")
	}
	if _, err := os.Stat(filepath.Join(e.launchd, "disabled")); err != nil {
		t.Fatal("agent not disabled")
	}
}

func TestInstallBoardReconcilerRejectsBadInputsBeforeChanges(t *testing.T) {
	e := newInstallEnv(t, t.TempDir())
	for _, c := range []struct {
		extra []string
		args  []string
	}{
		{nil, []string{}},
		{nil, []string{"--disable", "--with-model"}},
		{nil, []string{"--enable", "--disable"}},
		{[]string{"FAKE_NO_TOKEN=1"}, []string{"--enable"}},
		{[]string{"TODO_URL=http://example.com"}, []string{"--enable"}},
		{nil, []string{"--enable", "--force"}},
		{nil, []string{"--enable", "--gitlab-user", "x"}},
		{nil, []string{"--enable", "--label", "com.example.other"}},
		{nil, []string{"--enable", "--label"}},
	} {
		if output, err := e.run(t, c.extra, c.args...); err == nil {
			t.Fatalf("%v %v accepted:\n%s", c.extra, c.args, output)
		}
	}
	if _, err := os.Stat(e.plist()); err == nil {
		t.Fatal("rejected install wrote a plist")
	}
	if _, err := os.Stat(filepath.Join(e.launchd, "calls")); err == nil {
		t.Fatal("rejected install called launchctl mutations")
	}
}

func TestInstallBoardReconcilerRequiresIdentity(t *testing.T) {
	e := newInstallEnv(t, t.TempDir())
	identity := filepath.Join(e.home, ".config", "todo-board", "reconciler.json")
	if err := os.Remove(identity); err != nil {
		t.Fatal(err)
	}
	output, err := e.run(t, nil, "--enable")
	if err == nil || !strings.Contains(output, "--configure") {
		t.Fatalf("missing identity accepted: %v\n%s", err, output)
	}
	if err := os.WriteFile(identity, []byte(`{"gitlabUser":"reviewer.one","jiraEmail":"someone@else.com","jiraSite":"example.atlassian.net"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := e.run(t, nil, "--enable"); err == nil || !strings.Contains(output, "someone@else.com") {
		t.Fatalf("identity without Keychain token accepted: %v\n%s", err, output)
	}
	if _, err := os.Stat(e.plist()); err == nil {
		t.Fatal("rejected install wrote a plist")
	}
}

func TestInstallBoardReconcilerConfigure(t *testing.T) {
	e := newInstallEnv(t, t.TempDir())
	identity := filepath.Join(e.home, ".config", "todo-board", "reconciler.json")
	if err := os.Remove(identity); err != nil {
		t.Fatal(err)
	}
	if output, err := e.run(t, nil, "--configure"); err == nil || !strings.Contains(output, "glab auth login") {
		t.Fatalf("undetectable GitLab user accepted: %v\n%s", err, output)
	}
	glab := []string{"FAKE_GLAB_USER=reviewer.one"}
	if output, err := e.run(t, glab, "--configure"); err == nil || !strings.Contains(output, "--jira-site") {
		t.Fatalf("fresh configure without --jira-site accepted: %v\n%s", err, output)
	}
	output, err := e.run(t, glab, "--configure", "--jira-site", "example.atlassian.net")
	if err != nil || !strings.Contains(output, "config saved: GitLab reviewer.one, Jira owner@example.com at example.atlassian.net") {
		t.Fatalf("configure: %v\n%s", err, output)
	}
	if got := readFile(t, identity); got != `{"gitlabUser":"reviewer.one","jiraEmail":"owner@example.com","jiraSite":"example.atlassian.net"}`+"\n" {
		t.Fatalf("config %q", got)
	}
	if info, err := os.Stat(identity); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode: %v %v", info, err)
	}
	if output, err := e.run(t, glab, "--configure"); err != nil || !strings.Contains(output, "unchanged") {
		t.Fatalf("rerun reusing saved site: %v\n%s", err, output)
	}
	if output, err := e.run(t, nil, "--configure", "--gitlab-user", "someone.else"); err == nil || !strings.Contains(output, "--force") {
		t.Fatalf("silent overwrite: %v\n%s", err, output)
	}
	if output, err := e.run(t, nil, "--configure", "--gitlab-user", "bad user"); err == nil {
		t.Fatalf("bad username accepted:\n%s", output)
	}
	if output, err := e.run(t, nil, "--configure", "--gitlab-user", "someone.else", "--force"); err != nil || !strings.Contains(readFile(t, identity), "someone.else") {
		t.Fatalf("forced overwrite: %v\n%s", err, output)
	}
}

func TestInstallBoardReconcilerConfigureOptionalFields(t *testing.T) {
	e := newInstallEnv(t, t.TempDir())
	identity := filepath.Join(e.home, ".config", "todo-board", "reconciler.json")
	if err := os.Remove(identity); err != nil {
		t.Fatal(err)
	}
	base := []string{"--configure", "--gitlab-user", "reviewer.one", "--jira-site", "example.atlassian.net"}
	for _, bad := range [][]string{
		{"--issue-prefixes", "proj"},
		{"--issue-prefixes", "PROJ,,OPS"},
		{"--issue-prefixes", "PROJ OPS"},
		{"--jira-site", "https://example.atlassian.net"},
		{"--gitlab-group", "bad group"},
	} {
		if output, err := e.run(t, nil, append(append([]string{}, base...), bad...)...); err == nil {
			t.Fatalf("%v accepted:\n%s", bad, output)
		}
	}
	if _, err := os.Stat(identity); err == nil {
		t.Fatal("rejected configure wrote a file")
	}
	args := append(append([]string{}, base...), "--gitlab-group", "team", "--issue-prefixes", "PROJ,OPS")
	if output, err := e.run(t, nil, args...); err != nil {
		t.Fatalf("configure: %v\n%s", err, output)
	}
	want := `{"gitlabUser":"reviewer.one","jiraEmail":"owner@example.com","jiraSite":"example.atlassian.net","gitlabGroup":"team","issuePrefixes":["PROJ","OPS"]}` + "\n"
	if got := readFile(t, identity); got != want {
		t.Fatalf("config %q", got)
	}
	if output, err := e.run(t, nil, args...); err != nil || !strings.Contains(output, "unchanged") {
		t.Fatalf("rerun: %v\n%s", err, output)
	}
}

func TestInstallBoardReconcilerRefusesSecondAgent(t *testing.T) {
	e := newInstallEnv(t, t.TempDir())
	agents := filepath.Join(e.home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(agents, "com.someone.board-reconciler.plist")
	if err := os.WriteFile(existing, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := e.run(t, nil, "--enable")
	if err == nil || !strings.Contains(output, "--label com.someone.board-reconciler") {
		t.Fatalf("second agent installed: %v\n%s", err, output)
	}
	if _, err := os.Stat(e.plist()); err == nil {
		t.Fatal("refused install wrote a plist")
	}
	if output, err := e.run(t, nil, "--disable", "--label", "com.someone.board-reconciler"); err != nil {
		t.Fatalf("managing the existing label failed: %v\n%s", err, output)
	}
	if output, err := e.run(t, nil, "--uninstall", "--label", "com.someone.board-reconciler"); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, output)
	}
	for _, path := range []string{existing, filepath.Join(e.home, ".local", "bin", "board-reconciler")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s left behind", path)
		}
	}
	if _, err := os.Stat(filepath.Join(e.home, ".config", "todo-board", "reconciler.json")); err != nil {
		t.Fatal("uninstall removed the identity")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
