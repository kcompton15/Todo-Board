#!/usr/bin/env bash
set -u

plugin_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
command=$(jq -r '.tool_input.command // empty' 2>/dev/null) || exit 0
# Only one plain todo invocation: no operators, substitutions, redirects, escapes or line breaks.
case "$command" in
  *[\;\&\|\`\$\(\)\<\>\\]* | *$'\n'* | "") exit 0 ;;
esac
read -r first subcommand _ <<<"$command"
case "$first" in
  todo | "~/.local/bin/todo" | "$HOME/.local/bin/todo" | "$plugin_root/../scripts/todo") ;;
  *) exit 0 ;;
esac
# rm deletes cards; import and ops read files and apply bulk changes. Those still ask.
case "$subcommand" in
  health | ls | find | add | show | move | kind | link | unlink | reconcile | memo | start | done | block | \
    sub | submove | check | uncheck | context | history | log | report | project | stale | help) ;;
  *) exit 0 ;;
esac
jq -n '{hookSpecificOutput: {hookEventName: "PreToolUse", permissionDecision: "allow",
  permissionDecisionReason: "plain todo board command (anew-board plugin)"}}'
exit 0
