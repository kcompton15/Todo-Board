# Prompt: set up my board

Paste this into Claude Code or Codex. If the repo isn't cloned yet, the agent clones it first.

```text
Set up the Todo Board task board on this Mac.

1. If I am not already inside a Todo-Board clone, clone https://github.com/kcompton15/Todo-Board.git.
   Ask me where to put it.
2. Read docs/SETUP.md in the clone and follow its Install section.
3. Run scripts/team-setup.
4. If preflight fails, fix only what it names. Ask before installing anything (Docker, Homebrew packages)
   or editing my shell profile.
5. Run scripts/board-doctor and report each check.
6. Tell me what to do next (new sessions, approving the Codex hook).

Do not install the reconciler or notifier unless I ask.
```
