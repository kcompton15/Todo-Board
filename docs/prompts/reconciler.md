# Prompt: set up the reconciler

Paste this into Claude Code or Codex after the board itself is working. In Claude Code,
`/todo-board:reconciler` does the same thing.

```text
Set up the optional board reconciler from my todo-board clone (the directory I name, or the current directory).

Read docs/SETUP.md ("Optional: reconciler") and docs/RECONCILER.md first. Then:

1. Confirm `todo health` passes.
2. Check `glab auth status --hostname gitlab.com`. If it fails, have me run `glab auth login --hostname gitlab.com` myself.
3. Check for the Jira token with `security find-generic-password -s atlassian-api-token` (attributes only).
   Never pass -w, never print, echo or log the token, and never put it on a command line you run.
   If it is missing, give me the zsh `read -rs` command from the doc to run myself.
4. Run `make build-darwin`, then `scripts/install-board-reconciler --configure --jira-site <my Jira host>`. Ask me for the host,
   for example yourcompany.atlassian.net. Show me the detected GitLab user and Jira email and wait for me to confirm.
5. Run `--disable`, then `--probe`. If the probe stalls, tell me to look for a Keychain prompt.
   Summarize the probe result counts.
6. After I say go, run `--enable` (no --with-model) and show `~/.local/bin/board-reconciler --status`.
```
