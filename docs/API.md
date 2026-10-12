# HTTP API

Base URL: `http://127.0.0.1:7337`. There is no authentication because the host port is bound only to loopback. JSON errors use `{"error":"message"}`. Request bodies are limited to 1 MiB.

## Tasks

- `GET /api/tasks` returns `{"tasks":[...]}` in lane, manual order, priority, and ID sequence. Optional filters are `lane`, `project`, `tag`, `kind`, `priority`, and `q`. `kind` must be one of the values below; an unknown value returns `400 {"error":"invalid kind"}`. See [Search](#search) for how `q` matches and ranks.
- `POST /api/tasks` creates one task object or an envelope such as `{"tasks":[...]}`. It returns an array under `tasks` even for one item. `?onMatch=attach|reject|create` decides what happens when an open card already covers the new card's Jira key or MR; see [Matching open cards](#matching-open-cards). The default here is `reject`.
- `GET /api/tasks/{id}` returns one task.
- `PUT /api/tasks/{id}` replaces editable fields. The path ID and original creation time remain authoritative.
- `PATCH /api/tasks/{id}` merges any of `title`, `lane`, `kind`, `priority`, `project`, `tag`, `notes`, `source`, `order`, and `subtasks`.
- `DELETE /api/tasks/{id}` deletes the task and its subtasks.

Task example:

```json
{
  "id": "tmxyz123abcde",
  "revision": 3,
  "title": "Review billing signing changes",
  "lane": "doing",
  "kind": "review",
  "priority": 1,
  "project": "Billing",
  "tag": "Review",
  "notes": "API and web MRs are one coordinated review.",
  "source": "codex",
  "number": 142,
  "nextSubtaskNumber": 2,
  "order": 1000,
  "createdAt": "2026-09-17T16:30:00Z",
  "updatedAt": "2026-09-17T16:45:00Z",
  "subtasks": [{"id":"sabc1234","number":1,"title":"Review API MR","lane":"blocked","done":false}]
}
```

## Numbers and references

Every card has a `number` and every subtask has a per-card `number`, written `#142` and `#142.3`. The server assigns them; clients cannot set them. A `number` sent on PUT, PATCH or create is ignored, so a full task sent back unchanged still saves. Card numbers come from a board-wide counter (`nextNumber` in `tasks.json`) and step numbers from the card's `nextSubtaskNumber`. Neither is reused after a delete, and a failed or rolled-back transaction consumes none, so gaps are normal.

Every `{id}` route (`GET/PUT/PATCH/DELETE /api/tasks/{id}`, subtasks, links, work-log and history) accepts the full ID, `142` or `%23142` (URL-encoded `#142`). A step reference such as `142.3` on a card route returns 400 naming the subtask route. `{subtaskID}` accepts the subtask ID, its step number (`3` or `%233`), or `142.3` when 142 is that card. An unknown number returns the route's usual 404. `taskId` on `/api/activity` and `/api/history` resolves numbers too. Ops, inbox envelopes and import still take full IDs.

`tasks.json` is state version 3. A version 2 file is numbered on load in `(createdAt, id)` order, with steps numbered in array order, and `tasks.json.v2-<UTC timestamp>.backup` is written before the first version 3 save.

## Matching open cards

On create, the server reads Jira keys whose project is listed in `TODO_ISSUE_PREFIXES` (none when it is unset), GitLab MR URLs and `group/project!N` refs from the title and adds them as `required` links. Explicit links win on role, and an explicit link accepts any Jira key. A card covers a ref when its own required links (card or step scope) include it, or its title contains it. Reference links never match.

When an open card (lane not `done`) covers one of the new card's required refs, `onMatch` decides. If several open cards match, the server picks the one covering the most of the new card's refs, then the one with the fewest refs of its own, then the newest, so a card dedicated to a key wins over an umbrella card that lists many.

- `attach` adds a step to that card in one transaction: the step title is the incoming title (cut to 300 characters), in the input lane or `today`. Refs the card lacks are added as card links, and a `progress` work log `attached from <source>: <title>` records the incoming notes and step titles. A single attach returns 200 `{"attached":true,"task":{...},"subtask":{...}}`.
- `reject` returns 409 `{"code":"open_match","error":"#142 already covers PROJ-942; use onMatch=attach to add a step there or onMatch=create for a separate card","match":{"id":"tm...","number":142,"title":"...","ref":"PROJ-942"}}`. Nothing is written.
- `create` always makes a new card.

When a new card is created and a Done card covers one of its refs, its notes start with `Follow-on to #N.`, naming the newest Done match.

A batch that attaches anything returns `{"tasks":[created...],"attachments":[{"task":{...},"subtask":{...}}]}`, with 201 if any card was created and 200 if every item attached. Defaults by surface: `POST /api/tasks` and the web UI `reject` (the UI offers *Open #N* or *Create anyway*); `todo add`, MCP `create_task`, inbox arrays, ops `create` and `/api/import` `attach`. `todo add --new` and MCP `onMatch:"create"` force a separate card.

## Search

`q` is split into lowercase words. A card matches when any word appears in its title, notes, project, tag, step titles, link refs or URLs, or equals its number (`142`, `#142`, `142.3`). Results are sorted by score, then the usual order: an exact number scores 3, a title hit at a word start 2, and any other hit 1, counted once per word. `todo find` prints the top 10 and how many more matched. The browser filter applies the same words and fields as an any-match filter but keeps lane order.

Project and tag spellings are folded on write: names that differ only by case, spaces, `_` or `-` are stored as the spelling most other cards already use (ties prefer an all-lowercase spelling, then the alphabetically first). Create and PUT always fold; PATCH folds only the fields it sends. `scripts/backfill-projects.py` (dry run by default, `--apply` to write) fixes drifted spellings already on the board and reports near-duplicates without changing them.

Lanes are `backlog`, `today`, `doing`, `blocked`, and `done`. Kinds are `work`, `followup`, `review`, and `probe`. An omitted `kind` on create or full replacement defaults to `work`, and legacy persisted tasks without `kind` load as `work`. Omitting `kind` on PATCH preserves the existing value; an explicitly empty mutation `kind` resets/defaults to `work`. Non-empty unsupported mutation values fail validation, while an unsupported `GET /api/tasks?kind=...` filter returns `400 {"error":"invalid kind"}`. Priorities are `1` (critical), `2` (normal), and `3` (low). Project may contain spaces; tag may not.

## Links and reconciliation intent

Tasks expose `links` (always an array), `reconcileMode` (`automatic` by default or `manual`), and optional `agentContext` (4000 Unicode characters maximum). Create, PUT, PATCH, JSON import, ops and inbox support these fields. Omission preserves these fields on PUT/PATCH; `links:[]` clears links and `agentContext:""` clears the memo. Null is invalid. The memo is hidden in the ordinary UI, but is visible in the localhost API and full history. Manual excludes all future reconciler changes.

Each link has server-issued `id`, derived `kind` (`jira` or `mr`), canonical `ref` and `url`, optional `subtaskId`, `role` (`required` or `reference`), optional cached `state`, `stateCategory`, and `stateChangedAt`. At most 30 links are allowed. Identity is kind/ref/subtask scope. Explicit additions default to required. References never authorize completion. Removing a child removes its links atomically; explicitly linking a nonexistent child fails.

Link input accepts `ref` or `url`, optional existing `id`, `subtaskId`, `role`, `state` and `stateCategory`. Both ref and URL must agree. Supported forms are Jira keys, `https://<TODO_JIRA_SITE>/browse/KEY`, `namespace/project!iid`, and `https://gitlab.com/namespace/project/-/merge_requests/iid`. Jira keys and URLs are accepted only when the server was started with `TODO_JIRA_SITE` set to a bare hostname such as `yourcompany.atlassian.net`. Otherwise they are rejected. GitLab links always work. Query/fragment are stripped from valid URLs; other hosts, schemes, ports, userinfo, encoded separators and malformed paths are rejected. IDs cannot be transferred between identities. Omitted cached state is preserved. MR states are opened/merged/closed/locked, with no category. Jira state and category must both be present or both empty; categories are new/indeterminate/done. Only the server sets `stateChangedAt`, when cached state/category changes; it is observation time, not provider transition time. A duplicate link upsert preserves ID, revision and history.

- `POST /api/tasks/{id}/links` upserts a LinkInput and returns the canonical link with status 200.
- `DELETE /api/tasks/{id}/links/{linkId}` returns `{"deleted":"<linkId>","revision":4}` with status 200; another card's or missing link returns 404.

Both routes honor If-Match, return the committed ETag and publish one SSE snapshot after success. Failed writes publish none. Ops/inbox accept `{"op":"link","id":"card","link":{"ref":"PROJ-931","role":"reference"},"expectedRevision":3}` and `{"op":"unlink","id":"card","linkId":"link","expectedRevision":3}`. Multiple operations on one card use the same starting revision.

`review_delivered` is a work-log kind available only for review cards, with nonempty text and `needsReview:false`. Optional `subtaskId` scopes delivery to one existing review unit. Its immutable history ID and server RecordedAt are a supplemental delivery receipt. It appears under Progress in reports and does not change completion or task revision. Client attribution is not authenticated human acceptance. The reconciler can also recover a review without a receipt, from the configured reviewer's substantive comments or approval, or from terminal merge request state.

## Subtask mutations

- `POST /api/tasks/{id}/subtasks` accepts `{"title":"...","lane":"doing"}` or `{"titles":["...","..."],"lane":"doing"}`. Lane is optional and defaults to the parent's lane.
- `PATCH /api/tasks/{id}/subtasks/{subtaskID}` accepts any non-empty combination of `title`, `lane`, and `done`. Lane is authoritative when both lane and done are supplied. Setting only `done` to true moves the subtask to Done; setting it to false returns a Done subtask to its parent's non-Done lane, or Today when the parent is Done.
- `DELETE /api/tasks/{id}/subtasks/{subtaskID}` removes one subtask.

Subtask lane changes never move the parent, and parent lane changes never cascade to existing subtasks. The `done` field remains available for compatibility and is always equivalent to `lane == "done"`.

## Bulk operations

`POST /api/import` accepts `text/plain` using the task DSL or an `application/json` array of task inputs. `X-Source` sets provenance and defaults to `cli`. It always attaches to covering open cards and returns 201 `{"created":N,"tasks":[...],"attachments":[...]}`.

`POST /api/ops` atomically applies an envelope:

```json
{
  "source": "codex",
  "ops": [
    {"op":"create","title":"New task","project":"Web","kind":"work","onMatch":"attach"},
    {"op":"patch","id":"tmxyz","lane":"doing","kind":"followup"},
    {"op":"subtask","id":"tmxyz","title":"New step","lane":"doing"},
    {"op":"check","id":"tmxyz","subtaskId":"sabc","done":true},
    {"op":"work_log","id":"tmxyz","workLog":{"kind":"progress","text":"Finished review"}},
    {"op":"delete","id":"told"}
  ]
}
```

`onMatch` is optional on `create` ops (default `attach`) and rejected on any other op. If any operation is invalid, no operation is persisted. Per-operation errors name the failing array index; final transaction completion validation identifies the offending task. A `work_log` operation requires an existing task `id` and a nested `workLog` using the same typed fields as the direct work-log endpoint; it can therefore journal and close a card in the same atomic persistence transaction. Each changed task receives one activity entry and one revision increment per transaction, even when multiple operations target it. Activity describes the net committed change, not transient intermediate operations.

## Durable history and work log

- `GET /api/history?taskId=<id>&limit=50&before=<record-id>` returns newest-first durable records. `GET /api/tasks/{id}/history` is the equivalent task-scoped route and continues to return deleted-task evidence when the task no longer exists. `limit` is 1 through 100. The response includes `partialLegacyHistory`; a true value means that only the older, bounded activity feed exists for the earlier period.
- `POST /api/tasks/{id}/work-log` creates an operational entry without changing Notes, lane, priority, rank, or completion state. Its body is `{"kind":"progress|decision|blocker|blocker_resolved|next_step","text":"...","occurredAt":"RFC3339 optional","subtaskId":"optional","sessionId":"optional","correctsId":"optional","resolvesBlockerId":"optional"}`. The actor comes from `X-Actor`; `recordedAt` is assigned by the server. `correctsId` must name an existing history record and `resolvesBlockerId` must name an existing blocker entry.

Work logs and full task change records are authoritative durable journal data. `/api/activity` remains a compatibility recent-feed endpoint with 160-character field previews and a 1,000-entry retention limit.

## Report preview

`POST /api/reports/preview` accepts `{"kind":"standup|weekly","start":"YYYY-MM-DD","end":"YYYY-MM-DD","projects":["optional project"]}` and returns `{"kind","start","end","markdown","sourceRecordIds","partialLegacyHistory"}`. Dates are inclusive Chicago calendar dates, including daylight-saving transitions. The preview makes no model call. Its Markdown is a grouped evidence digest: one heading per project, one line per card (`- #N Title (done)`) with at most three short sub-bullets, then Blockers, Next steps and, for weekly reports, cards still in flight.

The digest is built from an evidence packet with these rules:

- Probe cards are left out. A card appears only when the window holds a fact for it: a work-log entry, a checked step, or a move to `done`. Rank, tag and other edits alone do not count.
- Reconciler text is cleaned first. Multi-line entries are split. `observed <ref> <state>` lines and `merged <ref>; merged <ref>` lines collapse into one fact per ref with its latest state (`web!140 merged`). `discovered reference: ...` lines are dropped, as are exact duplicate lines on the same card.
- Each entry is capped at 500 characters. If the packet passes 96 KiB, the oldest progress notes are dropped first and the packet is marked `truncated`.
- Blockers are unresolved blocker entries from any date on cards that are not done (the digest shows the latest per card). Weekly packets also list `activeCards`: cards in `doing` or `blocked` with no evidence in the window.

## Report jobs

Report jobs let the board and CLI hand an evidence packet to a host worker that writes the report, then poll for the result. Jobs live in memory only: a service restart drops them, and they never touch `tasks.json` or publish SSE updates.

- `POST /api/reports/jobs` takes the same body as the preview, snapshots the evidence and digest, and returns `202` with the job.
- `GET /api/reports/jobs/{id}` returns the job, or `404` once it is gone.
- `POST /api/reports/jobs/claim` moves the oldest queued job to `running` with a five-minute lease and returns the job plus its `evidence` packet. It returns `204` when nothing is queued. Every call records the worker as polling.
- `POST /api/reports/jobs/{id}/complete` takes `{"markdown":"...","cardCount":3}` (`cardCount` optional, markdown up to 32 KiB) and marks the job `done`.
- `POST /api/reports/jobs/{id}/fail` takes `{"error":"..."}` and marks the job `failed`; text past 500 characters is cut off.
- `GET /api/reports/worker` returns `{"lastClaimAt":"RFC3339"}`, or `{"lastClaimAt":null}` when no worker has polled since the service started. The board dialog and `todo report` treat the worker as missing when this is null or older than 30 seconds and show the digest instead of queueing a job.

Job shape:

```json
{
  "id": "r1f2e3d4c5b6a7980",
  "kind": "weekly",
  "start": "2026-10-05",
  "end": "2026-10-11",
  "state": "queued",
  "markdown": "",
  "digestMarkdown": "# Weekly report (2026-10-05 through 2026-10-11)\n...",
  "cardCount": 28,
  "createdAt": "2026-10-12T15:00:00Z",
  "updatedAt": "2026-10-12T15:00:00Z"
}
```

`state` is `queued`, `running`, `done` or `failed`; failed jobs carry `error`. Complete and fail return `409` unless the job is `running`. An expired lease puts the job back in the queue once; a second expiry fails it. At most 50 jobs are kept: finished jobs are dropped after an hour, or sooner (oldest first) to make room, and creating a job while 50 are still queued or running returns `429`.

## Completion safeguards

New Done tasks with explicitly unfinished children, moving an open parent with unfinished children to Done, or adding/reopening unfinished children under a Done parent returns HTTP `409` with `{"code":"open_subtasks","error":"..."}`. No state or activity is persisted and no SSE update is published. Finish the children first or reopen the parent. Completion is checked against the final state of a batch, so checking the last child and completing the parent in one batch is supported.

Legacy Done parents with unfinished children remain loadable and editable. Existing unfinished children may be completed, renamed or moved, and the parent may be reopened. New unfinished children are not allowed under Done. The browser's Hide done filter retains these cards; API `lane` filtering still means the parent's actual lane.

## Revisions and attribution

Reconciler action envelopes also support atomic per-card `expectedHistoryHeads` guards and durable `actionId` replay protection. See [reconciler atomicity and recovery](RECONCILER.md#atomicity-and-recovery) for the contract, HTTP 409 codes and replay response. These safeguards also apply to inbox envelopes; ordinary clients remain compatible.

Every task includes a positive `revision`. Existing version 1 tasks without it load at revision 1. Task GET/PUT/PATCH responses include a quoted revision `ETag`. Individual task and subtask mutation routes accept `If-Match: "3"` (unquoted `3` also accepted), comparing against the **parent task** revision under the store lock. A stale revision returns HTTP `409` with `{"code":"revision_conflict","error":"..."}`. The client must read again and reconcile, not blindly retry. Deletion returns 404 if the task is already gone. Unsupported wildcard/list/weak entity tags and nonpositive revisions are rejected. Omitted preconditions preserve unconditional legacy behavior.

Each existing-task operation in `/api/ops` or an inbox envelope may include `"expectedRevision":3`. All preconditions compare to the transaction's starting snapshot, so multiple operations on one card use the same revision. Use per-operation preconditions, not `If-Match`, on batch routes.

`X-Actor` identifies the client making an HTTP change (up to 80 characters, no control characters). Browser writes use `web`, CLI writes use `TODO_ACTOR` or `cli`, and MCP writes use `claude-chat`. Without that header, ordinary HTTP writes are labeled `api`; imports use `X-Source`/`cli`, batches use envelope `source`/`cli`. Inbox batches use envelope source or `inbox`. This is self-reported attribution, not authentication. Task `source` remains creation provenance and is not overwritten to record the latest actor.

## Activity

`GET /api/activity?taskId=<id-or-number>&limit=50` returns `{"activity":[...],"more":false,"retention":1000}`. Omit `taskId` for board-wide activity, including deleted tasks; a deleted full ID can still filter retained history. Limit defaults to 50 and accepts 1–100. `more` indicates additional retained entries beyond the requested limit; there is no pagination cursor. An unknown ID returns an empty list.

Entries are newest-first and contain `taskId`, `title`, `at`, `actor`, `action` (`created`, `updated`, `deleted`), `revision`, and `changes`: an array of `{field,before,after}` strings. Individual subtask IDs identify child changes. Text previews are truncated after 160 Unicode characters with an ellipsis. The store retains at most 1,000 entries globally, atomically with task state. No history is invented for changes made before the journal existed, and old entries age out. Failed validation, revision conflicts and failed persistence create no activity. An accepted no-op request may still update timestamp/revision and create an empty-change activity entry.

## Live state and operations

- `GET /api/events` is a server-sent event stream. It sends the complete board immediately, after every mutation, and a heartbeat comment every 25 seconds.
- `GET /api/projects` returns the sorted non-empty project names.
- `GET /healthz` returns `ok`.
