---
name: setup
description: Set up or repair the local todo-board board on this Mac (container, todo CLI, Claude plugin, Codex wiring). Use when the user asks to install, set up, or fix their task board.
disable-model-invocation: true
---

The repository root is `${CLAUDE_PLUGIN_ROOT}/..`. The full guide is `${CLAUDE_PLUGIN_ROOT}/../docs/SETUP.md`; read it if anything below fails.

1. Run `"${CLAUDE_PLUGIN_ROOT}/../scripts/team-setup" --dry-run` and show the user what it plans to change.
2. Run it for real without `--dry-run`. It is idempotent, so rerunning after a fix is safe.
3. If preflight fails, fix only what it names (start Docker Desktop or OrbStack, add `~/.local/bin` to PATH in `~/.zshrc`, install a missing tool with Homebrew) and run it again. Ask before installing anything.
4. Finish with `"${CLAUDE_PLUGIN_ROOT}/../scripts/board-doctor"` and report each check.
5. Tell the user to start a new Claude Code session (and a new Codex session, approving the board hook when Codex asks) so the session-start context loads.

Do not install the reconciler or notifier here unless the user asks; `/todo-board:reconciler` covers the reconciler.
