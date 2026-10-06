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

They keep one open card per Jira key or merge request. Re-review rounds, fix-ups and follow-up passes become steps on that card, because `todo add --link KEY` attaches to it automatically; `--new` is only for a genuinely separate outcome. Cards and steps are referred to as `#N` and `#N.M`.

Agents close their own reviews and probes when they hand back the result. They close `work` only when you asked for it and it is verified. Otherwise they leave it open and record a `next_step` saying it is ready to close. These updates are authorized side effects of the assignment, so agents do not ask separately before creating or closing a card. A session-start hook loads your active cards, plus a short list of open backlog and probe cards, into each new Claude Code or Codex session. If the board is down, agents mention it once and keep working. Details are in [docs/AGENT-INTEGRATION.md](docs/AGENT-INTEGRATION.md).

## Command line

`scripts/todo` is a small bash script (curl and jq) that talks to the HTTP API. Setup links it to `~/.local/bin/todo`.

```bash
todo health
todo find "rate confirmation"
todo add "Review billing changes" --kind review --project Billing --tag Review \
  --link "team/api!175" --sub "Review API MR" --sub "Review web MR"
todo ls --project Web
todo show 142
todo start 142
todo sub 142 "Confirm callback URLs" --lane doing
todo check 142 3
todo block '#142' "waiting for product clarification"
todo link 142 PROJ-123 --role required
todo log 142 progress "API change merged"
todo done 142
```

Cards are numbered `#142` and their steps `#142.3`. A card argument takes `142`, `'#142'` (quote it, or the shell treats `#` as a comment) or an unambiguous ID prefix. A step argument takes its number (`3`), `'#142.3'` or an ID prefix. `todo ls`, `show`, `add`, `sub` and `context` print numbers; `show` and `--json` keep the full IDs.

`todo add` attaches instead of duplicating. When an open card already covers a Jira key or merge request passed with `--link`, or a merge request in the title, or a title key whose project is listed in `TODO_ISSUE_PREFIXES`, the new item becomes a step on that card and the CLI prints `attached to #142 (<title>) as #142.4`. When only Done cards cover it, a new card is created with `Follow-on to #N.` in its notes. `--new` always creates a separate card.

`todo find "words"` matches a card when any word appears in its title, notes, project, tag, step titles or link refs, or equals its number, and prints the best 10 matches first. Project and tag spellings that differ only by case, spaces, `_` or `-` are folded to the spelling most cards already use. `bash scripts/backfill-projects.sh` previews fixes for spellings already on the board, `--apply` writes them, and near-duplicate names are reported but left alone.

Run `todo help` for every command. When the board is unreachable, the CLI exits with code 3 and one short message so agent sessions can carry on.

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
| `TODO_ISSUE_PREFIXES` | `docker compose`, server | unset | Comma-separated Jira project keys such as `PROJ,OPS`. Keys with these prefixes in a new card's title become required links. |
| `TODO_ACTOR` | `todo` | `cli` | Label recorded as the author of each change. |
| `TODO_CONTAINER_NAME` | `docker compose` | `todo-board` | Container name. |

Without `TODO_JIRA_SITE`, Jira links are rejected. GitLab merge request links on `gitlab.com` always work. If you will link Jira issues, set it before the first start and keep it set. Changing or removing it after Jira links exist stops the server from loading the board. Set it when you start the container:

```bash
TODO_JIRA_SITE=yourcompany.atlassian.net docker compose up -d
```

`TODO_ISSUE_PREFIXES` only affects card titles. Without it the server reads no Jira keys from titles, so text like `UTF-8` never becomes a link; merge request URLs and `group/project!N` refs in titles are still read, and `--link` accepts any key. Each prefix must match `^[A-Z][A-Z0-9]+$`, the same rule as the reconciler's `issuePrefixes`, and title keys also need `TODO_JIRA_SITE`. Unlike the Jira site, you can change it at any time:

```bash
TODO_JIRA_SITE=yourcompany.atlassian.net TODO_ISSUE_PREFIXES=PROJ,OPS docker compose up -d
```

## Optional components

**Weekday notifier.** A launchd agent that sends a macOS notification at 09:00 on weekdays: a Monday digest of blocked cards, `remind:` lines in notes, idle probes, and work left in progress for more than five days. Install it with `scripts/install-board-notifier`. See [docs/SETUP.md](docs/SETUP.md#optional-weekday-notifier).

**Reconciler.** A launchd agent that closes cards when the evidence is in: required merge requests merged, a Jira issue Done, or a review you approved. It only reads GitLab and Jira, and only writes to your board. It needs `glab`, a Jira API token in the Keychain, and `~/.config/todo-board/reconciler.json`. See [docs/RECONCILER.md](docs/RECONCILER.md).

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
uv run --with playwright python -m playwright install chromium
uv run --with playwright python scripts/acceptance.py run /tmp/board-qa --restart
uv run --with playwright python scripts/ui-regression.py
TODO_URL=http://127.0.0.1:7348 uv run --with playwright python scripts/ui-polish-regression.py
```

The polish regression refuses to run against ports 7337 and 7338; point it at a throwaway board started with its own TODO_DATA_DIR.

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
