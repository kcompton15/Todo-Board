package reconcile

import (
	"context"
	"time"
)

// OSANotifier passes title and body as argv so neither is ever parsed as AppleScript.
type OSANotifier struct{ Command Commander }

func (n OSANotifier) Notify(ctx context.Context, title, body string) error {
	_, err := n.Command.Run(ctx, Command{
		Name:    "/usr/bin/osascript",
		Args:    []string{"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body},
		Timeout: 15 * time.Second,
	})
	return err
}
