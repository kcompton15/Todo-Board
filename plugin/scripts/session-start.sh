#!/usr/bin/env bash
set -u

plugin_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
cat "$plugin_root/rules/board-rules.md"
printf '\n'

todo_bin="$plugin_root/../scripts/todo"
[[ -x "$todo_bin" ]] || todo_bin=$(command -v todo 2>/dev/null || printf '%s' "$HOME/.local/bin/todo")
if context=$("$todo_bin" context 2>/dev/null); then
  printf '%s\n' "$context"
else
  printf '## Task board unreachable at %s. Mention it once if board work comes up, then carry on.\n' \
    "${TODO_URL:-http://127.0.0.1:7337}"
fi
exit 0
