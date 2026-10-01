package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runNotifierInstaller(t *testing.T, home, launchd string, args ...string) (string, error) {
	t.Helper()
	fake := filepath.Join(filepath.Dir(launchd), "bin")
	command := exec.Command("./install-board-notifier", args...)
	command.Env = append(os.Environ(), "HOME="+home, "FAKE_LAUNCHD="+launchd, "PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestInstallBoardNotifier(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("installer targets macOS launchd and plutil")
	}
	root := t.TempDir()
	home := filepath.Join(root, `home & "q"`)
	launchd := filepath.Join(root, "launchd")
	for _, dir := range []string{home, launchd, filepath.Join(root, "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, filepath.Join(root, "bin", "launchctl"), fakeLaunchctl)
	plist := filepath.Join(home, "Library", "LaunchAgents", "io.github.kcompton15.board-notifier.plist")

	if output, err := runNotifierInstaller(t, home, launchd); err != nil {
		t.Fatalf("install: %v\n%s", err, output)
	}
	if err := exec.Command("plutil", "-lint", plist).Run(); err != nil {
		t.Fatal("plist does not lint")
	}
	if got := plistValue(t, plist, "StartCalendarInterval.4.Weekday"); got != "5" {
		t.Fatalf("weekday %q", got)
	}
	if got := plistValue(t, plist, "ProgramArguments.0"); !strings.HasSuffix(got, "/scripts/board-notifier") {
		t.Fatalf("program %q", got)
	}
	if output, err := runNotifierInstaller(t, home, launchd); err != nil {
		t.Fatalf("rerun: %v\n%s", err, output)
	}

	other := filepath.Join(home, "Library", "LaunchAgents", "com.someone.board-notifier.plist")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := runNotifierInstaller(t, home, launchd); err == nil || !strings.Contains(output, "--label com.someone.board-notifier") {
		t.Fatalf("second agent accepted: %v\n%s", err, output)
	}
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}

	if output, err := runNotifierInstaller(t, home, launchd, "--uninstall"); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, output)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatal("plist left behind")
	}
	if _, err := os.Stat(filepath.Join(launchd, "loaded")); !os.IsNotExist(err) {
		t.Fatal("agent still loaded")
	}
}
