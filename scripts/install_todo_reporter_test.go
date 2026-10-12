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

type reporterEnv struct {
	home, launchd, source, fake, url string
	env                              []string
}

func newReporterEnv(t *testing.T, home string) reporterEnv {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("installer targets macOS launchd and plutil")
	}
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/reports/worker" {
			http.NotFound(w, r)
			return
		}
		if _, err := w.Write([]byte(`{"lastClaimAt":null}`)); err != nil {
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
	writeExecutable(t, filepath.Join(fake, "claude"), "#!/usr/bin/env bash\necho '0.0.0 (Claude Code)'\n")
	source := filepath.Join(root, "todo-reporter-new")
	writeExecutable(t, source, "#!/usr/bin/env bash\necho new\n")
	return reporterEnv{home: home, launchd: launchd, source: source, fake: fake, url: board.URL, env: []string{
		"HOME=" + home,
		"PATH=" + fake + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"FAKE_LAUNCHD=" + launchd,
		"TODO_REPORTER_SOURCE=" + source,
		"TODO_URL=" + board.URL,
	}}
}

func (e reporterEnv) run(t *testing.T, extra []string, args ...string) (string, error) {
	t.Helper()
	command := exec.Command("./install-todo-reporter", args...)
	command.Env = append(append([]string{}, e.env...), extra...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func (e reporterEnv) plist() string {
	return filepath.Join(e.home, "Library", "LaunchAgents", "io.github.kcompton15.todo-reporter.plist")
}

func TestInstallTodoReporterEnableKeepsAliveAndReadsBack(t *testing.T) {
	e := newReporterEnv(t, filepath.Join(t.TempDir(), `home & <co> "q"`))
	output, err := e.run(t, nil, "--enable")
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, output)
	}
	bin := filepath.Join(e.home, ".local", "bin", "todo-reporter")
	if readFile(t, bin) != "#!/usr/bin/env bash\necho new\n" {
		t.Fatal("binary not installed")
	}
	if err := exec.Command("plutil", "-lint", e.plist()).Run(); err != nil {
		t.Fatal("installed plist does not lint")
	}
	arguments := []string{}
	for i := 0; i < 3; i++ {
		arguments = append(arguments, plistValue(t, e.plist(), "ProgramArguments."+string(rune('0'+i))))
	}
	if strings.Join(arguments, " ") != bin+" --url "+e.url {
		t.Fatalf("arguments %q", arguments)
	}
	for key, want := range map[string]string{
		"KeepAlive":         "true",
		"RunAtLoad":         "true",
		"StandardErrorPath": filepath.Join(e.home, ".local", "log", "todo-reporter.log"),
	} {
		if got := plistValue(t, e.plist(), key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if path := plistValue(t, e.plist(), "EnvironmentVariables.PATH"); !strings.HasPrefix(path, e.fake+":") {
		t.Fatalf("claude directory missing from PATH %q", path)
	}
	if info, err := os.Stat(plistValue(t, e.plist(), "WorkingDirectory")); err != nil || !info.IsDir() {
		t.Fatalf("working directory missing: %v", err)
	}
	calls := readFile(t, filepath.Join(e.launchd, "calls"))
	if !strings.Contains(calls, "enable gui/") || !strings.Contains(calls, "bootstrap gui/") {
		t.Fatalf("launchctl calls:\n%s", calls)
	}
}

func TestInstallTodoReporterRestoresPreviousOnFailedBootstrap(t *testing.T) {
	e := newReporterEnv(t, t.TempDir())
	if output, err := e.run(t, nil, "--enable"); err != nil {
		t.Fatalf("first install: %v\n%s", err, output)
	}
	oldPlist := readFile(t, e.plist())
	bin := filepath.Join(e.home, ".local", "bin", "todo-reporter")
	writeExecutable(t, bin, "#!/usr/bin/env bash\necho old\n")
	writeExecutable(t, e.source, "#!/usr/bin/env bash\necho broken\n")
	if err := os.WriteFile(filepath.Join(e.launchd, "fail-bootstrap-once"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := e.run(t, nil, "--enable")
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

func TestInstallTodoReporterDisableAndUninstall(t *testing.T) {
	e := newReporterEnv(t, t.TempDir())
	if output, err := e.run(t, nil, "--enable"); err != nil {
		t.Fatalf("enable: %v\n%s", err, output)
	}
	logFile := filepath.Join(e.home, ".local", "log", "todo-reporter.log")
	if err := os.WriteFile(logFile, []byte("history\n"), 0o644); err != nil {
		t.Fatal(err)
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
	if output, err := e.run(t, nil, "--uninstall"); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, output)
	}
	for _, path := range []string{e.plist(), filepath.Join(e.home, ".local", "bin", "todo-reporter")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s left behind", path)
		}
	}
	if readFile(t, logFile) != "history\n" {
		t.Fatal("uninstall removed the log")
	}
}

func TestInstallTodoReporterRejectsBadInputsBeforeChanges(t *testing.T) {
	e := newReporterEnv(t, t.TempDir())
	if err := os.Remove(filepath.Join(e.fake, "claude")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		extra []string
		args  []string
	}{
		{nil, []string{}},
		{nil, []string{"--enable", "--disable"}},
		{nil, []string{"--enable", "--with-model"}},
		{nil, []string{"--enable"}},
		{[]string{"TODO_URL=http://example.com"}, []string{"--enable"}},
		{[]string{"TODO_URL=http://127.0.0.1:9"}, []string{"--enable"}},
		{nil, []string{"--enable", "--label", "com.example.other"}},
		{nil, []string{"--enable", "--label"}},
	} {
		if output, err := e.run(t, c.extra, c.args...); err == nil {
			t.Fatalf("%v %v accepted:\n%s", c.extra, c.args, output)
		}
	}
	if output, err := e.run(t, nil, "--enable"); err == nil || !strings.Contains(output, "'claude'") {
		t.Fatalf("missing claude not named: %v\n%s", err, output)
	}
	if _, err := os.Stat(e.plist()); err == nil {
		t.Fatal("rejected install wrote a plist")
	}
	if _, err := os.Stat(filepath.Join(e.launchd, "calls")); err == nil {
		t.Fatal("rejected install called launchctl mutations")
	}
}

func TestInstallTodoReporterRefusesSecondAgent(t *testing.T) {
	e := newReporterEnv(t, t.TempDir())
	agents := filepath.Join(e.home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "com.someone.todo-reporter.plist"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := e.run(t, nil, "--enable")
	if err == nil || !strings.Contains(output, "--label com.someone.todo-reporter") {
		t.Fatalf("second agent installed: %v\n%s", err, output)
	}
	if _, err := os.Stat(e.plist()); err == nil {
		t.Fatal("refused install wrote a plist")
	}
}
