# Prompt: fix my board

Paste this into Claude Code or Codex when board context stops showing up or `todo` fails. In Claude
Code, `/todo-board:doctor` does the same thing.

```text
My Todo Board setup is misbehaving. From my todo-board clone (the directory I name, or the
current directory), run scripts/board-doctor. For each failing check, explain the cause in one sentence
and the fix from the Troubleshooting table in docs/SETUP.md. Apply fixes only after I agree, then
rerun the doctor. Never edit data/tasks.json, and never delete cards.
```
