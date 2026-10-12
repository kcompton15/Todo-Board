# Todo Board

A local, single-user kanban board that your coding agents keep current while they work. It runs as one small Go service in Docker, stores its state in a JSON file, and listens only on `127.0.0.1:7337`.

Claude Code and Codex find or create a card when you ask for a change or a review, check off steps as they finish, and close what they can verify. You get one place to see what is in flight, without asking anyone for a status update.

The board has five lanes: Backlog, Today, In progress, Blocked and Done. Cards can belong to a project, carry a tag, and hold subtasks, links to Jira issues and GitLab merge requests, and a work log. The UI shows one aggregate board or a stacked view grouped by project. A parent card cannot move to Done while any of its steps is still open.

## Quick start

```bash
git clone https://github.com/kcompton15/Todo-Board.git
cd Todo-Board
scripts/team-setup
```

If you use SSH with GitHub, `git@github.com:kcompton15/Todo-Board.git` works too. Setup builds the container, links the `todo` CLI into `~/.local/bin`, installs the Claude Code plugin, and writes the Codex integration. When it finishes, open [http://127.0.0.1:7337](http://127.0.0.1:7337).

[docs/SETUP.md](docs/SETUP.md) covers what setup changes, updating, troubleshooting and uninstalling. A browser-friendly copy is in [docs/setup.html](docs/setup.html).

### Requirements

- macOS
- Docker Desktop or OrbStack, running
- `git`, `curl` and `jq`
- Claude Code, Codex, or both

Go is not required. Anything compiled for your Mac is built inside Docker.

## How agents use it

The integrations tell agents to run `todo find` before creating anything, so they update an existing card instead of making a duplicate. A plain answer creates no card. Otherwise they create one card per outcome, with steps as subtasks, and pick one of four kinds:

- `work` when you asked for a change.
- `review` when you asked for a review. The merge request URL goes in as a `--link`, not in notes.
- `followup` when the conversation produces something the human must do. Agents never move these.
- `probe` only when a question required real investigation and left something worth finding again.

They keep one open card per Jira key or merge request, so a second review round or a fix-up becomes a step on the existing card rather than a new card. The board backs this up with [card numbers and automatic attach](#card-numbers-attach-and-search).

Agents close their own reviews and probes when they hand back the result. They close `work` only when you asked for it and it is verified. Otherwise they leave it open and record a `next_step` saying it is ready to close. These updates are authorized side effects of the assignment, so agents do not ask separately before creating or closing a card. A session-start hook loads your active cards, plus a short list of open backlog and probe cards, into each new Claude Code or Codex session. Cards for the project you are working in come first, and new cards file under that project on their own (see [the directory's project](#card-numbers-attach-and-search)). Subagents get the rules too, but leave card creation to the session that started them. If the board is down, agents mention it once and keep working. Details are in [docs/AGENT-INTEGRATION.md](docs/AGENT-INTEGRATION.md).

## Card numbers, attach and search

Six features keep the board readable when several agents write to it at once.

**Numbers.** Every card gets a board-wide number (`#142`) and every step a number within its card (`#142.3`). Numbers are never reused, and the CLI, HTTP API, MCP tools and web UI accept them anywhere they accept an ID. Internal IDs remain the primary key.

**Attach instead of duplicate.** When new work names a Jira key or merge request that an open card already covers, it becomes a step on that card. If only Done cards cover it, a new card is created with `Follow-on to #N.` in its notes. `--new` creates a separate card anyway. The CLI, MCP server and inbox attach by default. The HTTP API and web UI ask first: the API returns `409` naming the card, and the UI offers *Open*, *Create anyway* or *Dismiss*.

**Folded names.** Project and tag spellings that differ only by case, spaces, `_` or `-` are stored as the spelling most cards already use, so `web app`, `Web_App` and `Web App` stay one project. `scripts/backfill-projects.py` previews fixes for spellings already on the board, `--apply` writes them, and near-duplicate names are reported but left alone.

**The directory's project.** `todo project` names the board project for the current directory, and `todo add` without `--project` files the card there. It needs no setup: it uses the nearest folder name or git repository name that matches a project already on your board, ignoring case and punctuation, and also matches names that start or end with a project's name (`web-app-worktrees` and `acme-web-app` both give `web-app`). It never invents a project. To choose one yourself, put the name on the first line of a `.todo-project` file in the folder, or set `TODO_PROJECT`.

**Idle cards.** Each card records `activityAt`, the last time its content, lane or steps changed. Dragging a card or a link-status refresh does not count. Session context marks cards with no activity for 7 days as `(idle Nd)` and folds idle cards from other projects into one line, and `todo stale` lists them so you can close or restart them.

**Ranked search.** `todo find`, `?q=` and the UI search match a card when any word appears in its title, notes, project, tag, step titles or link refs, or equals its number. Results put exact numbers and title hits first.

A short session on a board with `TODO_ISSUE_PREFIXES=PROJ`:

```console
$ todo add "Ship PROJ-41 CSV export" --project "Web App" --sub "Write the exporter"
created #12 [backlog] Ship PROJ-41 CSV export
$ todo add "Address review comments on the export" --link PROJ-41
attached to #12 (Ship PROJ-41 CSV export) as #12.2
$ todo add "Benchmark exports over 1M rows" --link PROJ-41 --new
created #13 [backlog] Benchmark exports over 1M rows
$ todo add "Polish the export dialog" --project web_app
created #14 [backlog] Polish the export dialog
$ todo check 12 2
updated #12.2  Address review comments on the export
$ todo find "csv dialog"
#       LANE      P   KIND      PROJECT             TITLE
#12     backlog   2   work      Web App             Ship PROJ-41 CSV export
#14     backlog   2   work      Web App             Polish the export dialog
```

The title of #12 carries `PROJ-41`, so the second `add` attaches to it. `web_app` is stored as `Web App`, and `find` returns both cards because any word can match.

## Standup and weekly reports

**Generate standup** and **Weekly report** turn the recorded work in a date range into a short list of talking points you can speak from. A standup is three to six bullets; a weekly status groups outcomes for a department audience. Each point cites the cards behind it, sub-bullets appear only for facts you would otherwise look up (an MR number, a date, who owns a blocker), and the text stays editable before you copy it. The standup defaults to the previous working day through today; the weekly report defaults to last Monday through Sunday.

The board does not call a model itself. Generate queues a report job, and a host worker, `todo-reporter`, claims it, sends the card evidence to Claude through the same isolated `claude -p` invocation the reconciler uses (your existing Claude Code login, no tools, hooks, memory or MCP servers), checks that every point cites a card from the evidence, and posts the Markdown back. The weekly audience rules are `styleRules` in `internal/reportwriter/prompt.go`; edit them to fit your audience. A weekly report usually takes 10 to 30 seconds. Install and start the worker once:

```bash
make build-reporter
scripts/install-todo-reporter --enable
```

`--disable` stops it and `--uninstall` removes it. Its log is `~/.local/log/todo-reporter.log`.

The worker needs the `claude` CLI on your PATH and logged in. The installer is macOS-only; elsewhere, run `go run ./cmd/todo-reporter` yourself. Only Claude Code **2.1.296** is accepted, because that is the release whose isolation has been probed. Any other version fails the job with that reason and you get the digest. [docs/RECONCILER.md](docs/RECONCILER.md) covers verifying a new release.

When no AI draft is possible, the dialog says why and shows the **evidence digest** instead, labeled as the digest: a grouped, de-duplicated list of what each card recorded in the range, with no model involved. That happens when the worker has not checked in for 30 seconds, when the job fails (for example, an untested Claude Code version), or after three minutes without a result. **Show evidence** swaps between the AI draft and the digest it was written from, keeping edits to each. `todo report` gives the same result on the command line.

## Command line

`scripts/todo` is a small bash script (curl and jq) that talks to the HTTP API. Setup links it to `~/.local/bin/todo`.

```bash
todo health
todo find "rate confirmation"
todo add "Review billing changes" --kind review --project Billing --tag Review \
  --link "team/api!175" --sub "Review API MR" --sub "Review web MR"
todo ls --project Web
todo project                      # this folder's board project
todo stale                        # open cards idle for 7+ days
todo show 142
todo start 142
todo sub 142 "Confirm callback URLs" --lane doing
todo check 142 3
todo block '#142' "waiting for product clarification"
todo link 142 PROJ-123 --role required
todo log 142 progress "API change merged"
todo done 142
todo report weekly --from 2026-10-05 --to 2026-10-11 > weekly.md
```

A card argument takes `142`, `'#142'` (quote it, or the shell treats `#` as a comment) or an unambiguous ID prefix. A step argument takes its number (`3`), `'#142.3'` or an ID prefix. `todo show` and `--json` keep the full IDs. A Jira key or merge request counts for attach when it is passed with `--link`, appears in the title as a merge request, or appears in the title as a key from a project in [`TODO_ISSUE_PREFIXES`](#jira-keys-in-card-titles). `todo find` prints the best 10 matches and how many more matched.

Run `todo help` for every command. When the board is unreachable, the CLI exits with code 3 and one short message so agent sessions can carry on.

`todo report standup|weekly [--from YYYY-MM-DD] [--to YYYY-MM-DD] [--project P]...` prints the AI talking points with the same default dates as the board. It waits up to three minutes (`TODO_REPORT_TIMEOUT` seconds). If the report writer is not running, fails or times out, it prints the evidence digest to stdout, the reason to stderr, and exits `4`.

`todo import` reads a plain-text format: `%project`, `#tag`, `!1` to `!3` for priority and `@lane`, with indented lines as subtasks.

```bash
todo import <<'EOF'
Review billing changes %Billing #Review !1 @today
  - inspect API behavior
  - verify the browser flow
Write SSO handoff %Web @backlog
EOF
```

## Configuration

| Variable | Used by | Default | Purpose |
| --- | --- | --- | --- |
| `TODO_PORT` | `docker compose` | `7337` | Host port. The bind address stays `127.0.0.1`. |
| `TODO_URL` | `todo`, notifier, reconciler, MCP server | `http://127.0.0.1:7337` | Where clients find the board. Set it if you change the port. |
| `TODO_JIRA_SITE` | `docker compose`, server | unset | Bare hostname such as `yourcompany.atlassian.net`. Enables Jira links. |
| `TODO_ISSUE_PREFIXES` | `docker compose`, server | unset | Comma-separated Jira project keys such as `PROJ,OPS`. See [Jira keys in card titles](#jira-keys-in-card-titles). |
| `TODO_ACTOR` | `todo` | `codex` or `claude-code` inside those agents, else `cli` | Label recorded as the author of each change. |
| `TODO_PROJECT` | `todo` | unset | Board project for new cards and session context, overriding the folder's `.todo-project` file and name matching. |
| `TODO_DATA` | `docker compose` | `./data` | Host folder holding the board's state. |
| `TODO_CONTAINER_NAME` | `docker compose` | `todo-board` | Container name. |

Without `TODO_JIRA_SITE`, Jira links are rejected. GitLab merge request links on `gitlab.com` always work. If you will link Jira issues, set it before the first start and keep it set. Changing or removing it after Jira links exist stops the server from loading the board. Set it when you start the container:

```bash
TODO_JIRA_SITE=yourcompany.atlassian.net docker compose up -d
```

### Jira keys in card titles

`TODO_ISSUE_PREFIXES` lists the Jira projects whose keys the server should read from card titles, for example `PROJ,OPS`. A new card titled `Ship PROJ-41 CSV export` then gets a required link to `PROJ-41`, the same as if it had been created with `--link PROJ-41`. That link lets later work on `PROJ-41` attach to the card, and lets the reconciler close it when the issue is Done.

Without the setting, the server reads no Jira keys from titles, so strings such as `UTF-8` or `ISO-8601` never turn into links. Merge request URLs and `group/project!N` refs in titles are always read, and `--link` accepts any key. Each prefix is an uppercase project key matching `^[A-Z][A-Z0-9]+$`, the same rule as the reconciler's `issuePrefixes`, and title keys also need `TODO_JIRA_SITE`. The server refuses to start with an invalid prefix.

Prefixes apply when cards are created and matched, and links already stored never change, so you can add or change the setting at any time:

```bash
TODO_JIRA_SITE=yourcompany.atlassian.net TODO_ISSUE_PREFIXES=PROJ,OPS docker compose up -d
```

## Optional components

**Weekday notifier.** A launchd agent that sends a macOS notification at 09:00 on weekdays: a Monday digest of blocked cards, `remind:` lines in notes, idle probes, and work left in progress for more than five days. Install it with `scripts/install-board-notifier`. See [docs/SETUP.md](docs/SETUP.md#optional-weekday-notifier).

**Reconciler.** A launchd agent that closes cards when the evidence is in: required merge requests merged, a Jira issue Done, or a review you approved. It only reads GitLab and Jira, and only writes to your board. It needs `glab`, a Jira API token in the Keychain, and `~/.config/todo-board/reconciler.json`. See [docs/RECONCILER.md](docs/RECONCILER.md).

**Report writer.** A launchd agent, `todo-reporter`, that turns report jobs into Claude-written talking points using your Claude Code login. Without it, reports fall back to the evidence digest. See [Standup and weekly reports](#standup-and-weekly-reports).

**Claude Desktop.** `make install-claude-desktop` builds a local MCP server named `todo-board` and adds it to the Claude Desktop config. Its `create_task` attaches to an open card that already covers the same Jira key or merge request unless `onMatch` says otherwise, and every `id` accepts a card number. It works only while your Mac and the container are running. See [docs/AGENT-INTEGRATION.md](docs/AGENT-INTEGRATION.md#claude-desktop-chat).

**Inbox.** Chat sessions that can write files but cannot reach `127.0.0.1` can drop a JSON payload in `data/inbox/`. The server applies it atomically and moves it to `.processed/` or `.failed/`. See [docs/AGENT-INTEGRATION.md](docs/AGENT-INTEGRATION.md#filesystem-inbox-for-remote-chat) and [reference/sample-inbox.json](reference/sample-inbox.json).

## Data

State lives in `data/tasks.json`, a versioned JSON file written through a temporary file and an atomic rename. A malformed file stops the server from starting rather than being replaced with an empty board. The directory is a bind mount, so it survives `docker compose down` and rebuilds. Never edit `tasks.json` by hand. To back it up:

```bash
cp data/tasks.json "data/tasks.$(date +%Y%m%d-%H%M%S).backup.json"
```

The first write by a newer version saves a backup of the old format automatically: `data/tasks.json.v1-<timestamp>.backup` for version 1 files and `data/tasks.json.v2-<timestamp>.backup` before card numbers are added. An older binary refuses a file written by a newer one.

## Development

The service uses only the Go standard library and vanilla browser APIs. There is no JavaScript build and no CDN dependency.

```bash
make test
make validate
make up
make logs
make down
```

`make test` runs `go test -race`, `go vet` and a gofmt check. `make validate` adds shell, plugin, JavaScript and Compose checks, and needs Node, Deno and Docker. Browser acceptance tests are opt-in:

```bash
make sandbox                                  # fresh board from this checkout on 127.0.0.1:7348
export TODO_URL=http://127.0.0.1:7348
uv run --with playwright python -m playwright install chromium
uv run --with playwright python scripts/acceptance.py run /tmp/board-qa --restart
uv run --with playwright python scripts/ui-regression.py
uv run --with playwright python scripts/ui-polish-regression.py
uv run --with playwright python scripts/links-regression.py
make sandbox-down
```

The browser tests create and delete their own cards. They refuse `127.0.0.1:7337` unless `TODO_ALLOW_LIVE=1`, the polish regression also refuses 7338, and a container restart only touches the container publishing `TODO_URL`'s port.

The HTTP API is documented in [docs/API.md](docs/API.md). Contributor notes for agents are in [AGENTS.md](AGENTS.md).

## Project layout

```text
cmd/todod              server
cmd/todo-mcp           Claude Desktop MCP server
cmd/board-reconciler   reconciler
internal/              store, httpapi, inbox, parse, reconcile, mcpserver
webui/                 single-file web UI
scripts/               todo CLI, installers, notifier, doctor, tests
plugin/                Claude Code plugin
.claude-plugin/        plugin marketplace manifest
docs/                  setup, API, agent integration, reconciler
reference/             sample inbox payload
data/                  board state (gitignored)
```
