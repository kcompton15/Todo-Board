# Board reconciler

The reconciler closes cards when the evidence is in. It reads GitLab merge requests and Jira issues, compares them with the links on your cards, and checks off or completes work that is provably finished. It never writes to GitLab or Jira. It only changes your board.

It is installed at `~/.local/bin/board-reconciler` and runs from the `io.github.kcompton15.board-reconciler` launchd agent. You can also run it from the repository:

```sh
go run ./cmd/board-reconciler --dry-run --no-llm
go run ./cmd/board-reconciler --dry-run --task <full-id>
go run ./cmd/board-reconciler --task <disposable-full-id> --no-llm
python3 scripts/reconciler-smoke.py --model
```

The smoke script creates and deletes only its own disposable cards, checks that your original cards stay unchanged, checks that a second run changes nothing, and can run one isolated model dry-run. A run without `--dry-run` can update eligible cards, so look at the dry-run first.

## Configuration

The reconciler reads `~/.config/todo-board/reconciler.json`:

```json
{"gitlabUser":"reviewer.one","jiraEmail":"you@example.com","jiraSite":"yourcompany.atlassian.net","gitlabGroup":"team","issuePrefixes":["PROJ","OPS"]}
```

| Field | Required | Purpose |
| --- | --- | --- |
| `gitlabUser` | yes | The reviewer. Approvals and review comments count only when this user wrote them, never the ambient `glab` user. |
| `jiraEmail` | yes | Jira account name, and the account attribute of the Keychain item. |
| `jiraSite` | yes | Bare Jira hostname, such as `yourcompany.atlassian.net`. |
| `gitlabGroup` | no | Turns on merge request title search inside this GitLab group. |
| `issuePrefixes` | no | Jira project keys. Turns on discovery of plain-text issue mentions such as `PROJ-123` in card text. |

The Jira API token is read from the macOS Keychain (service `atlassian-api-token`). It is never stored in the config file.

`scripts/install-board-reconciler --configure` writes the file for you. It detects `gitlabUser` from `glab api user` and `jiraEmail` from the Keychain item's account attribute, without reading the secret. Flags:

| Flag | Meaning |
| --- | --- |
| `--jira-site HOST` | Required the first time you configure. |
| `--gitlab-group GROUP` | Sets `gitlabGroup`. |
| `--issue-prefixes "PROJ,OPS"` | Sets `issuePrefixes`. |

When you leave `--gitlab-group` or `--issue-prefixes` out, `--configure` keeps the values already in the file. To clear the group, pass `--gitlab-group ""` with `--force`.

The binary accepts `--config PATH`, `--gitlab-user`, `--jira-email`, `--jira-site`, `--gitlab-group` and `--issue-prefixes`, which override the file for one run. A manual run with no valid identity fails. A scheduled run reports the providers as unavailable.

## What it decides

- Manual, Done, `followup` and `probe` cards are skipped, including link cache updates.
- Work is complete when every required merge request in its scope is merged. A merge request closed without merging does not count. Jira Done counts only when the scope has no required merge request.
- A review is complete when it has a delivery receipt for its exact scope, or when every required merge request is covered by a terminal state, by your timestamped approval, or by your substantive published findings in the matching review round. Receipts are optional, so older reviews can still be recovered. Approvals with unknown timing, other reviewers, bot or logistical comments, partial comments and old edited comments do not qualify.
- Subtasks need their own evidence. A parent's proof cannot check off deployment or QA subtasks. A parent whose subtasks are all done still needs its own required links satisfied.
- Unresolved blockers and later human pauses are left alone. Nothing completes because of inactivity, and nothing moves automatically between Backlog, Today and In progress.
- Jira mentions and title-search matches are references, not proof that the work is complete. A full merge request URL that clearly identifies a review target can become a required link, with a note about where it came from. Deployment subtasks, examples and earlier rounds stay references. Existing reference links stay references.

GitLab is read through `glab` with GET requests only. Provider errors count as missing evidence, never as Done, and cached link state never authorizes completion. Title search does not see keys that appear only in branch names, so add explicit links for work that spans several repositories.

## Atomic updates

Each card gets one atomic operation containing its starting revision, the latest history ID and a progress entry tagged with an action ID. The server checks these under the store lock. A standalone blocker changes history without changing the revision, so a stale plan cannot overwrite it. When a card has changed, the reconciler skips it and does not retry blindly.

- `expectedHistoryHeads` maps a card ID to its latest history record ID. An empty string means the card must have no history.
- `history_conflict` and `action_conflict` are HTTP 409 codes.
- An action envelope needs one progress entry with an `actionId` (128 characters at most), the same `expectedRevision` on every operation, and exactly one card. Creating and deleting are not allowed.
- The server computes `actionHash`. Replaying the same action returns `alreadyApplied: true` and `applied: 0`, with no SSE event and no duplicate history. A different payload under the same action ID is rejected.

If a POST response is lost, the client reads the matching action ID and hash to find out whether the write landed. Its state file is `~/.local/state/todo-board/board-reconciler.json`. It is private, fsynced and atomically renamed, bound to the board's origin, and guarded by an exclusive file lock. The history cursor moves to the history head from the start of the run only after a fully successful live run over the whole board. A partial failure keeps the old cursor, and `--task` never moves it. A corrupt or mismatched state file is kept and reported. `--since <RFC3339>` only filters what history is reported. External status is always checked.

A dry-run writes no board data, state, cursor or notifications. Limits per run: 200 eligible cards, 100 Jira keys, 200 explicit merge request identities, 100 pages per fetch, bounded response sizes, command and HTTP deadlines, and a five-minute overall deadline. Input over a limit is reported, not treated as proof.

## Evidence gaps

Each identity the reconciler cannot read is listed under `gaps`, with `absent` (the provider answered without it) and `required` (some eligible scope depends on it). Only a transient failure on a required identity counts as unavailable. That blocks the cursor and marks the run incomplete. A failure on a reference-only identity, or an identity the provider confirms is absent, cannot change a decision and retrying won't fix it, so it is reported without failing the run. A missing identity is never treated as Done, so cards that require it stay open. A Jira answer that omits every requested key is treated as a provider failure. After three GitLab failures in a row, the rest of that run's GitLab calls are skipped so an outage can't use up the time Jira-only cards need.

## Optional model assistance

`--no-llm` skips Claude entirely. Without it, the deterministic changes are written first, then the board is read again and Claude is asked for suggestions. A dry-run shows the deterministic changes as a labelled hypothetical snapshot instead. Notes, comments and agent memos are treated as untrusted data, common credential patterns are redacted, and the context is size-limited. No provider credentials or raw command errors reach the model.

Each invocation checks that the installed Claude Code has the flags it needs and runs memory and hook canaries in a fresh temporary directory. The model runs with no MCP servers, no ordinary tools, no skills, hooks disabled and a small environment allowlist. This is a checked invocation contract, not an OS sandbox. A Claude Code version the reconciler has not been verified against is skipped safely.

There is one preflight call (ceiling $0.25) and one suggestion call (ceiling $1), with no automatic retries and a shared three-minute deadline. Input is limited to 128 KiB, 40 cards and a bounded slice of history. An oversized bundle skips the model. Output is limited to 256 KiB and 40 proposals, and the parser requires a successful CLI result and strict structured output rather than trusting the exit status or free text.

Proposals can suggest reference links, subtask checks that have their own evidence, agent memos, progress logs and report-only lane suggestions. They cannot edit notes, delete or unlink, promote a link to required, create receipts, resolve blockers or close parents. Duplicate or conflicting proposals for a card are rejected. Unchanged evidence produces the same signature each time, so the model can't cause repeated logs or memo rewrites by citing different sources. Logs written by the model never count as human completion evidence. Accepted changes use the same atomic guards as everything else. If the model fails, the deterministic changes stay committed and the run is reported as incomplete.

## Scheduling

`board-reconciler --scheduled` is launched every 60 seconds by launchd and exits at once unless a slot is due. Slots are weekdays at 09:20, 11:20, 13:20, 15:20, 16:45 and 18:30 America/Chicago, computed from embedded timezone data, so the Mac's timezone and travel don't shift them. Nothing runs outside 09:00 to 19:00. After sleep, only the latest due slot of the day runs. Earlier slots and earlier days are not replayed. The lock sits next to the state file, is taken before any clock, state or network check, and is shared with manual runs. A held lock is a quiet skip.

A failed attempt retries after 5 minutes, then 15, with at most three attempts per slot and never past the next slot or 19:00. When a slot runs out of attempts, the schedule is marked degraded and you get one notification. The next healthy slot ends the episode with a single "healthy again" notice. An identity that the provider confirms is absent is a known gap: the run stays healthy, doesn't retry, notifies once when the gap first appears, and forgets it when it resolves. Only runs that complete cards or check off steps notify. Link cache refreshes and no-ops are quiet.

Each scheduled attempt logs one JSON line to `~/.local/log/board-reconciler.log` (rotated at 1 MiB, one backup). Skips are not logged. `board-reconciler --status` prints the cursor, slot, retry and degradation state without calling any provider.

```sh
make build-darwin
scripts/install-board-reconciler --configure --jira-site yourcompany.atlassian.net
scripts/install-board-reconciler --disable
scripts/install-board-reconciler --probe
scripts/install-board-reconciler --enable
scripts/install-board-reconciler --enable --with-model
launchctl bootout gui/$(id -u)/io.github.kcompton15.board-reconciler
scripts/install-board-reconciler --uninstall
```

- `make build-darwin` uses local Go if installed and the pinned golang image otherwise.
- `--disable` installs the binary and plist without loading the agent.
- `--probe` runs one read-only dry-run under launchd.
- `--enable` loads the agent, deterministic only. Add `--with-model` for suggestions.
- The `launchctl bootout` line stops it immediately.
- `--uninstall` removes the agent and binary and keeps state, logs and the config file.

Every mode accepts `--label <reverse-dns>.board-reconciler`. The default is `io.github.kcompton15.board-reconciler`. The installer refuses to add a second reconciler agent and names the `--label` that manages the existing one.

Before installing, the installer checks that the board is reachable, the config file is valid, `glab` is signed in, and the Keychain item exists (never its value). It XML-escapes and lints the plist, installs it atomically, and restores the previous binary and plist if bootstrap or read-back fails. Scheduled runs pass `--no-llm` unless you installed with `--with-model`.

The reconciler also replaces the notifier's old review sweep, which closed a review card and checked all its subtasks as soon as the first merge request in the notes merged or closed. The reconciler never checks review subtasks from parent evidence. A card with per-merge-request review subtasks and only card-level links stays open after a merge until its producer records scoped receipts or checks the steps.
