# Agent integration

The board is only useful if agents update it as part of their real work. Local CLI sessions use `todo`, Claude Desktop chat uses the local MCP bridge, and chats that can write files but cannot reach the board use the watched inbox.

Record every implementation MR, including sibling repositories, with `todo link <card> <key|url> [--sub <prefix>] [--role required|reference]`. Required links cover the exact card/child outcome; references are examples, dependencies or historical context. `todo unlink <card> <link-prefix>` removes a link. Ambiguous prefixes fail. `todo add` accepts repeated `--link` and `--agent-context`. `todo show` displays scope, role and cached status.

After you finalize review findings, record `todo log <card> review_delivered "finalized report/findings" [--sub <prefix>]` for covered scopes. Check only completed steps, run `todo done` when all steps are complete, then return the report without another confirmation. Receipts supplement future recovery: the configured reviewer's matching substantive published GitLab comments or approval, or merged/closed review targets, can also finish a review with coverage for every MR in that review round. Historical reviews do not require receipts. Local reviews whose findings remain in chat benefit from a receipt. Only reviews without qualifying evidence remain uncertain; inactivity is insufficient. This grants no authority to post comments or approvals to GitLab.

`todo reconcile <card> manual` excludes all future automatic updates, including link discovery/cache updates. Use open steps or manual mode for deployment/QA/acceptance beyond merge. `todo memo <card> "text"` saves up to 4000 Unicode characters of agent context; the ordinary UI hides it, but API/history expose it. It is not a secret store.

## Claude Desktop chat

Run `make install-claude-desktop` from the repository root, quit and reopen Claude Desktop, and start a new chat. The `todo-board` MCP server exposes ten tools:

- `list_tasks` searches and filters before creation.
- `get_task` reads one card and its subtasks.
- `get_activity` reads the latest 50 retained changes, optionally filtered by full task ID (including deleted IDs).
- `create_task` creates a card with source `claude-chat`.
- `update_task` changes fields or moves a card between lanes.
- `add_subtask` adds a step to an existing outcome, optionally in its own lane.
- `update_subtask` renames a step or moves its independent lane; `done` remains a completion shortcut.
- `link_task` upserts a scoped required/reference link.
- `unlink_task` removes a link belonging to that card.
- `log_review_delivered` records finalized findings for a review card or child scope.

Card deletion is intentionally unavailable from chat. Create/update tools also accept links, agentContext and reconcileMode. New link/receipt tools accept expectedRevision. The bridge rejects non-loopback board URLs, applies a five-second HTTP timeout, bounds response sizes, validates tool arguments, and reports board/API failures as visible tool errors. It never writes `data/tasks.json` directly.

The MCP server gives Claude the task operations, but the model still decides when to use them. When tracking matters, say "track this on my board" or "update the matching card". The tool descriptions carry the same match-before-create, kind and closure rules as the CLI integrations.

## Match and choose a kind before creating

Always run `todo find "keywords"` unless the session-start hook already showed the matching active card.

- A match in In progress is the current work. Add or check subtasks; do not create a duplicate.
- A match in Today or Backlog should be started with `todo start <id>`.
- A match in Blocked should move back to In progress only when the blocker is actually resolved.
- A match in Done represents older work; create a new card for genuinely new work.
- A partial match normally belongs as a subtask under the existing human-sized unit of work.

Use one task per meaningful outcome, not one per file. A plain answer creates no card. Create a card only under these rules:

- `work`: the human asked for a change.
- `review`: the human asked for a review. Put the merge request URL in notes.
- `followup`: the conversation produced something the human must do.
- `probe`: a question required real investigation and left artifacts worth finding again.

The applicable create and close operations are authorized side effects of the assignment. Do not ask separately whether to perform them.

For the CLI, pass the kind at creation with `todo add "title" --kind work|followup|review|probe`. Change an existing classification with `todo kind <id> <kind>`. Use `project` as the epic-level product or repository grouping and `tag` for work type such as Review, Incident, or Planning.

## During work

Create or start a qualifying task before beginning. Check subtasks as they finish rather than all at the end. Record blockers with a reason:

```bash
todo block t123 "waiting for the SSO redirect URI"
todo submove t123 s456 blocked
```

A subtask lane is independent of the parent task. Move the subtask when only that step is blocked or in progress; move the parent only when the overall outcome changes state.

Close cards according to their kind:

- Close the session's own `review` and `probe` cards when the deliverable is returned.
- Close `work` only when the human asked for that work and the result is verified. Otherwise leave it open and run `todo log <id> next_step "ready to close"`.
- Never move a `followup` card; it is owned by the human.

Do not delete cards created by another actor. A stopped board is never a reason to stop engineering work; report it once and continue.

Codex should pass `--source codex`; Claude Code should pass `--source claude-code`.

For attribution on **all** CLI mutations, use `TODO_ACTOR=codex todo ...` or `TODO_ACTOR=claude-code todo ...`. This non-secret label is independent of the task's creation source; `todo add --source` also provides the actor when `TODO_ACTOR` is unset. The session-start context includes active subtasks under Backlog cards and all unfinished subtasks under Done cards, so they don't drop out of view.

Use `todo history <id>` (or `--json`) to explain recent changes; omit ID for board-wide history. Labels identify the reporting client, not verified human identity. History keeps the latest 1,000 task changes, with bounded previews.

Read a task's `revision` before making changes based on its contents. MCP `update_task`, `add_subtask` and `update_subtask` accept `expectedRevision`; pass the **parent** revision from `get_task`. REST uses `If-Match`, and inbox/batch operations use `expectedRevision` on each existing-task operation. On conflict, read again, reconcile the intended change and retry with the newly read revision only after checking it. Never automatically overwrite a concurrent edit. `todo block` protects its read/append/write of notes automatically; simple CLI lane/check commands remain unconditional intent commands for compatibility. A stale batch is rejected in full.

Done requires all children finished. On a completion error, inspect open children and finish only steps that actually completed, or leave/reopen the parent. Do not bypass the guard by deleting/replacing children merely to close a task. Existing Done parents with open steps represent unfinished work, not a reason to create a duplicate. To reopen a completed child, reopen its Done parent first. A batch may finish the final child and parent atomically.

To update the bridge, run `make build-mcp`. It replaces the local binary without touching configuration. Quit and reopen Claude Desktop to load the new tools.

## Filesystem inbox for remote chat

Read `data/tasks.json` to find IDs. Do not write that file. Write a complete payload to a hidden staging file and then rename it in the same directory:

```text
data/inbox/.staging-1694970000-chat.json
data/inbox/1694970000-chat.json
```

The payload is either an array of new tasks or an operation envelope documented in `docs/API.md`. Example:

```json
{
  "source": "chat-claude",
  "ops": [
    {
      "op": "create",
      "title": "Confirm webhook retry behavior",
      "lane": "today",
      "priority": 2,
      "project": "API",
      "tag": "Investigation",
      "subtasks": ["Read current adapter", "Check official contract"]
    }
  ]
}
```

On success the file appears under `data/inbox/.processed/`. On failure it appears under `.failed/` with `.error.txt`. Re-read `tasks.json` after processing if the chat needs the server-generated IDs.
