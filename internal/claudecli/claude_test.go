package claudecli

import (
	"context"
	"strings"
	"testing"
)

type commandFunc func(context.Context, Command) ([]byte, error)

func (f commandFunc) Run(c context.Context, in Command) ([]byte, error) { return f(c, in) }

func TestRunRejectsBeforeRequest(t *testing.T) {
	var calls []string
	client := Client{Command: commandFunc(func(_ context.Context, c Command) ([]byte, error) {
		calls = append(calls, strings.Join(c.Args, " "))
		if c.Args[0] == "--version" {
			return []byte("9.9.9 (Claude Code)"), nil
		}
		t.Fatal("ran past the version check")
		return nil, nil
	})}
	valid := Request{SystemPrompt: "p", Schema: `{}`, Budget: 0.5}
	for name, request := range map[string]Request{
		"no prompt": {Schema: `{}`, Budget: 0.5},
		"no schema": {SystemPrompt: "p", Budget: 0.5},
		"no budget": {SystemPrompt: "p", Schema: `{}`},
	} {
		if _, err := client.Run(context.Background(), request); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("invalid request invoked claude: %q", calls)
	}
	if _, err := client.Run(context.Background(), valid); err == nil || !strings.Contains(err.Error(), "isolation-verified") {
		t.Fatal(err)
	}
}

func TestEnvAllowlist(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "never-pass")
	t.Setenv("CLAUDE_CONFIG_DIR", "/elsewhere")
	for _, value := range Env() {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "HOME", "PATH", "USER", "LOGNAME", "TMPDIR", "LANG":
		default:
			t.Fatalf("%s passed through", key)
		}
	}
}
