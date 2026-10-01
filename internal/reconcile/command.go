package reconcile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

type Command struct {
	Name    string
	Args    []string
	Stdin   []byte
	Dir     string
	Env     []string
	Timeout time.Duration
}
type Commander interface {
	Run(context.Context, Command) ([]byte, error)
}
type ExecCommander struct{}
type boundedOutput struct {
	bytes.Buffer
	max int
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	if len(data) > b.max-b.Len() {
		return 0, errors.New("command output limit")
	}
	return b.Buffer.Write(data)
}
func (ExecCommander) Run(ctx context.Context, input Command) ([]byte, error) {
	timeout := input.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, input.Name, input.Args...)
	cmd.Dir = input.Dir
	cmd.Env = input.Env
	cmd.Stdin = bytes.NewReader(input.Stdin)
	cmd.Stderr = io.Discard
	output := &boundedOutput{max: 4 << 20}
	cmd.Stdout = output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("command failed (output redacted)")
	}
	return output.Bytes(), nil
}
