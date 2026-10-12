package scripts_test

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func runPluginHook(t *testing.T, script string, payload any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "../plugin/scripts/"+script)
	cmd.Stdin = strings.NewReader(string(data))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v", script, err)
	}
	return string(out)
}

func TestPluginAllowsOnlyPlainTodoCommands(t *testing.T) {
	cases := map[string]bool{
		`todo ls`:                     true,
		`todo add "Fix A" --sub "-x"`: true,
		`~/.local/bin/todo show 3`:    true,
		`todo ls; rm -rf /`:           false,
		`todo ls && echo done`:        false,
		`todo ls | head`:              false,
		`todo $(id)`:                  false,
		"todo `id`":                   false,
		`todo ls > /tmp/x`:            false,
		"todo show 3\nrm x":           false,
		`TODO_ACTOR=x todo ls`:        false,
		`echo todo`:                   false,
		`todos ls`:                    false,
		`todo add "a \" ; rm x"`:      false,
		``:                            false,
	}
	for command, allowed := range cases {
		out := runPluginHook(t, "allow-todo.sh", map[string]any{"tool_input": map[string]string{"command": command}})
		got := strings.Contains(out, `"permissionDecision": "allow"`)
		if got != allowed {
			t.Fatalf("%q: allowed=%v, want %v (output %q)", command, got, allowed, out)
		}
	}
}

func TestPluginGivesSubagentsTheRules(t *testing.T) {
	var parsed struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(runPluginHook(t, "subagent-start.sh", map[string]string{"agent_type": "general-purpose"})), &parsed); err != nil {
		t.Fatal(err)
	}
	context := parsed.HookSpecificOutput.AdditionalContext
	if parsed.HookSpecificOutput.HookEventName != "SubagentStart" ||
		!strings.HasPrefix(context, "You are a subagent. Do not create board cards") ||
		!strings.Contains(context, "## Personal task board") {
		t.Fatalf("subagent context: %+v", parsed)
	}
}
