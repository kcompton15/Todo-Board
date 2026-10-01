# HTTP API

Base URL: `http://127.0.0.1:7337`. There is no authentication because the host port is bound only to loopback. JSON errors use `{"error":"message"}`. Request bodies are limited to 1 MiB.

## Tasks

- `GET /api/tasks` returns `{"tasks":[...]}` in lane, manual order, priority, and ID sequence. Optional filters are `lane`, `project`, `tag`, `kind`, `priority`, and `q`. Text search also matches subtask titles. `kind` must be one of the values below; an unknown value returns `400 {"error":"invalid kind"}`.
- `POST /api/tasks` creates one task object or an envelope such as `{"tasks":[...]}`. It returns an array under `tasks` even for one item.
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
  "order": 1000,
  "createdAt": "2026-09-17T16:30:00Z",
  "updatedAt": "2026-09-17T16:45:00Z",
  "subtasks": [{"id":"sabc1234","title":"Review API MR","lane":"blocked","done":false}]
}
```

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

`POST /api/import` accepts `text/plain` using the task DSL or an `application/json` array of task inputs. `X-Source` sets provenance and defaults to `cli`.

`POST /api/ops` atomically applies an envelope:

```json
{
  "source": "codex",
  "ops": [
    {"op":"create","title":"New task","project":"Web","kind":"work"},
    {"op":"patch","id":"tmxyz","lane":"doing","kind":"followup"},
    {"op":"subtask","id":"tmxyz","title":"New step","lane":"doing"},
    {"op":"check","id":"tmxyz","subtaskId":"sabc","done":true},
    {"op":"work_log","id":"tmxyz","workLog":{"kind":"progress","text":"Finished review"}},
    {"op":"delete","id":"told"}
  ]
}
```

If any operation is invalid, no operation is persisted. Per-operation errors name the failing array index; final transaction completion validation identifies the offending task. A `work_log` operation requires an existing task `id` and a nested `workLog` using the same typed fields as the direct work-log endpoint; it can therefore journal and close a card in the same atomic persistence transaction. Each changed task receives one activity entry and one revision increment per transaction, even when multiple operations target it. Activity describes the net committed change, not transient intermediate operations.

## Durable history and work log

- `GET /api/history?taskId=<id>&limit=50&before=<record-id>` returns newest-first durable records. `GET /api/tasks/{id}/history` is the equivalent task-scoped route and continues to return deleted-task evidence when the task no longer exists. `limit` is 1 through 100. The response includes `partialLegacyHistory`; a true value means that only the older, bounded activity feed exists for the earlier period.
- `POST /api/tasks/{id}/work-log` creates an operational entry without changing Notes, lane, priority, rank, or completion state. Its body is `{"kind":"progress|decision|blocker|blocker_resolved|next_step","text":"...","occurredAt":"RFC3339 optional","subtaskId":"optional","sessionId":"optional","correctsId":"optional","resolvesBlockerId":"optional"}`. The actor comes from `X-Actor`; `recordedAt` is assigned by the server. `correctsId` must name an existing history record and `resolvesBlockerId` must name an existing blocker entry.

Work logs and full task change records are authoritative durable journal data. `/api/activity` remains a compatibility recent-feed endpoint with 160-character field previews and a 1,000-entry retention limit.

## Report preview

`POST /api/reports/preview` accepts `{"kind":"standup|weekly","start":"YYYY-MM-DD","end":"YYYY-MM-DD","projects":["optional project"]}` and returns evidence-based Markdown, source record IDs, and `partialLegacyHistory`. Dates are inclusive Chicago calendar dates, including daylight-saving transitions. The preview never fabricates a model result; it includes recorded work-log evidence and, for weekly output, unresolved current blockers as explicitly labeled carry-forward context.

## Completion safeguards

New Done tasks with explicitly unfinished children, moving an open parent with unfinished children to Done, or adding/reopening unfinished children under a Done parent returns HTTP `409` with `{"code":"open_subtasks","error":"..."}`. No state or activity is persisted and no SSE update is published. Finish the children first or reopen the parent. Completion is checked against the final state of a batch, so checking the last child and completing the parent in one batch is supported.

Legacy Done parents with unfinished children remain loadable and editable. Existing unfinished children may be completed, renamed or moved, and the parent may be reopened. New unfinished children are not allowed under Done. The browser's Hide done filter retains these cards; API `lane` filtering still means the parent's actual lane.

## Revisions and attribution

Reconciler action envelopes also support atomic per-card `expectedHistoryHeads` guards and durable `actionId` replay protection. See [reconciler atomicity and recovery](RECONCILER.md#atomicity-and-recovery) for the contract, HTTP 409 codes and replay response. These safeguards also apply to inbox envelopes; ordinary clients remain compatible.

Every task includes a positive `revision`. Existing version 1 tasks without it load at revision 1. Task GET/PUT/PATCH responses include a quoted revision `ETag`. Individual task and subtask mutation routes accept `If-Match: "3"` (unquoted `3` also accepted), comparing against the **parent task** revision under the store lock. A stale revision returns HTTP `409` with `{"code":"revision_conflict","error":"..."}`. The client must read again and reconcile, not blindly retry. Deletion returns 404 if the task is already gone. Unsupported wildcard/list/weak entity tags and nonpositive revisions are rejected. Omitted preconditions preserve unconditional legacy behavior.

Each existing-task operation in `/api/ops` or an inbox envelope may include `"expectedRevision":3`. All preconditions compare to the transaction's starting snapshot, so multiple operations on one card use the same revision. Use per-operation preconditions, not `If-Match`, on batch routes.

`X-Actor` identifies the client making an HTTP change (up to 80 characters, no control characters). Browser writes use `web`, CLI writes use `TODO_ACTOR` or `cli`, and MCP writes use `claude-chat`. Without that header, ordinary HTTP writes are labeled `api`; imports use `X-Source`/`cli`, batches use envelope `source`/`cli`. Inbox batches use envelope source or `inbox`. This is self-reported attribution, not authentication. Task `source` remains creation provenance and is not overwritten to record the latest actor.

## Activity

`GET /api/activity?taskId=<full-id>&limit=50` returns `{"activity":[...],"more":false,"retention":1000}`. Omit `taskId` for board-wide activity, including deleted tasks; a deleted full ID can still filter retained history. Limit defaults to 50 and accepts 1–100. `more` indicates additional retained entries beyond the requested limit; there is no pagination cursor. An unknown ID returns an empty list.

Entries are newest-first and contain `taskId`, `title`, `at`, `actor`, `action` (`created`, `updated`, `deleted`), `revision`, and `changes`: an array of `{field,before,after}` strings. Individual subtask IDs identify child changes. Text previews are truncated after 160 Unicode characters with an ellipsis. The store retains at most 1,000 entries globally, atomically with task state. No history is invented for changes made before the journal existed, and old entries age out. Failed validation, revision conflicts and failed persistence create no activity. An accepted no-op request may still update timestamp/revision and create an empty-change activity entry.

## Live state and operations

- `GET /api/events` is a server-sent event stream. It sends the complete board immediately, after every mutation, and a heartbeat comment every 25 seconds.
- `GET /api/projects` returns the sorted non-empty project names.
- `GET /healthz` returns `ok`.
