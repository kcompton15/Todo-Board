---
name: doctor
description: Diagnose the local todo-board board setup (container health, todo CLI, Claude plugin, Codex hook, reconciler). Use when board context is missing from sessions or todo commands fail.
---

Run `"${CLAUDE_PLUGIN_ROOT}/../scripts/board-doctor"` and read its output. It is read-only.

For each failing check, explain the cause in one sentence and give the exact fix from the troubleshooting section of `${CLAUDE_PLUGIN_ROOT}/../docs/SETUP.md`. Apply a fix only when the user asks, then run the doctor again to confirm.
