package reconcile

import (
	"context"
	"reflect"
	"testing"
)

type captureCommander struct{ got []Command }

func (c *captureCommander) Run(_ context.Context, command Command) ([]byte, error) {
	c.got = append(c.got, command)
	return nil, nil
}

func TestOSANotifierKeepsTextOutOfScript(t *testing.T) {
	capture := &captureCommander{}
	hostile := `" & (do shell script "touch /tmp/pwned") & "`
	if err := (OSANotifier{Command: capture}).Notify(context.Background(), "Board reconciler", hostile); err != nil {
		t.Fatal(err)
	}
	want := []string{"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", "Board reconciler", hostile}
	if len(capture.got) != 1 || capture.got[0].Name != "/usr/bin/osascript" || !reflect.DeepEqual(capture.got[0].Args, want) {
		t.Fatalf("osascript call %+v", capture.got)
	}
}
