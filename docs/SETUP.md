# Setup

This gets you your own copy of the task board on your Mac. Your Claude Code and Codex sessions keep it current on their own: they find or create a card when you ask for a change or a review, check off steps as they finish, and close what they can verify. Everything stays local. The board listens on `127.0.0.1` only and has no login.

Setup takes about five minutes, most of it Docker building the image.

## Before you start

- macOS with Docker Desktop or OrbStack installed and running.
- `git`.
- Claude Code, Codex, or both. Setup wires up whichever it finds.
- `jq` and `curl` come with current macOS. On an older release, run `brew install jq`.

You don't need Go. Anything that has to be compiled for your Mac is built inside Docker.

## Install

```bash
git clone git@github.com:kcompton15/Todo-Board.git
cd Todo-Board
scripts/team-setup
```

Clone it anywhere you like. The script finds its own location. Run it with `--dry-run` first if you want to see what it will change. It's safe to rerun at any time.

When it finishes:

1. Open http://127.0.0.1:7337. You should see an empty board.
2. Start a new Claude Code session. The first thing in its context is the board rules plus your active cards.
3. Start a new Codex session. Codex asks you to trust a hook called "Loading active task board". Approve it once.

Would you rather have an agent do the setup? Paste [prompts/setup.md](prompts/setup.md) into Claude Code or Codex.

## What setup changes

| Where | What | Undo |
| --- | --- | --- |
| Docker | Container `todo-board` on `127.0.0.1:7337`, with data in the clone's `data/` folder | `docker compose down` (data stays) |
| `~/.local/bin/todo` | Symlink to the clone's `scripts/todo` | `rm ~/.local/bin/todo` |
| Claude Code | Marketplace `kcompton15` pointing at your clone, plugin `todo-board` | `claude plugin uninstall todo-board@kcompton15` |
| `~/.codex/AGENTS.md` | A block between `<!-- TODO-BOARD:BEGIN -->` and `<!-- TODO-BOARD:END -->` | `scripts/install-codex-integration --uninstall` |
| `~/.codex/hooks.json` | One SessionStart hook running `scripts/board-context.sh` | same as above |

Setup never edits your `~/.claude/CLAUDE.md` or `~/.claude/settings.json`. The Claude side lives entirely in the plugin. Before it changes an existing Codex file, setup saves a backup next to it named `*.bak-todo-board-<timestamp>`.

The plugin also adds three slash commands:

- `/todo-board:setup` reruns and checks the setup.
- `/todo-board:doctor` diagnoses a broken setup.
- `/todo-board:reconciler` walks you through the optional reconciler.

## How it behaves day to day

You don't have to do anything. The rules tell the agent to:

- Run `todo find` before creating a card, so it updates an existing card rather than making a duplicate.
- Create one card per outcome, with steps as subtasks.
- Pick one of four kinds:
  - `work` when you asked for a change.
  - `review` when you asked for a review, with the MR URL in notes.
  - `followup` for something only you can do.
  - `probe` for an investigation worth finding again.
- Close its own reviews and probes when it hands back the result.
- Close `work` only after it has verified the result. Otherwise it leaves the card open, marked "ready to close".
- Never touch a `followup`. That one is yours.

If you want something tracked for certain, say "track this on my board". You can use `todo` yourself, too:

```bash
todo ls
todo add "Ship the quote rounding fix" --kind work --project Billing
todo help
```

## Updating

```bash
cd Todo-Board && git pull && scripts/team-setup
```

The Claude plugin is read straight from your clone, so new rules and commands apply at the next session start, or after `/reload-plugins`. Rerunning `team-setup` rebuilds the container when server code changed and refreshes the Codex block.

## Optional: Jira links

The board accepts Jira links only when the server knows your Jira host. Start the container with it set to a bare hostname:

```bash
TODO_JIRA_SITE=yourcompany.atlassian.net docker compose up -d
```

Without it, Jira links are rejected. GitLab merge request links on `gitlab.com` always work. Set it before the first start and keep it set. Changing or removing it after Jira links exist stops the server from loading the board.

## Optional: weekday notifier

The notifier sends a macOS notification at 09:00 on weekdays. It covers:

- a Monday digest of blocked cards
- `remind: daily`, `remind: weekly` and `remind: YYYY-MM-DD` lines in card notes
- probes that have sat for more than a day
- work that has been In progress for more than five days

```bash
scripts/install-board-notifier
scripts/install-board-notifier --uninstall
```

## Optional: reconciler

The reconciler closes cards on its own once the evidence is in: the required MRs are merged, the Jira issue is Done, or (for reviews) you approved or left substantive findings on every MR in the round. It runs on weekdays at 09:20, 11:20, 13:20, 15:20, 16:45 and 18:30 Central.

It **reads** GitLab as you, through `glab`, and Jira with your API token. It never writes to either. It only changes your board, and every change shows up in the card's activity.

The easiest route is `/todo-board:reconciler` in Claude Code. To do it by hand:

1. Sign in to GitLab: `glab auth login --hostname gitlab.com`.
2. Create a Jira API token at https://id.atlassian.com/manage-profile/security/api-tokens and store it in your Keychain. Don't paste it at the interactive `security ... -w` prompt, because that prompt cuts input off at 128 characters and Atlassian tokens are longer. Use this instead (zsh), with your own email:

   ```bash
   read -rs "TOKEN?Paste Jira API token: " && \
     security add-generic-password -U -a "you@yourcompany.com" -s atlassian-api-token -w "$TOKEN" && \
     unset TOKEN
   ```

3. Build, configure, probe and enable:

   ```bash
   make build-darwin
   scripts/install-board-reconciler --configure --jira-site yourcompany.atlassian.net
   scripts/install-board-reconciler --disable
   scripts/install-board-reconciler --probe
   scripts/install-board-reconciler --enable
   ```

   `--configure` detects your GitLab user and Jira email and saves them with the Jira site to `~/.config/todo-board/reconciler.json`, so check what it prints. `--jira-site` is required the first time. `--disable` installs without scheduling, and `--probe` does one read-only dry run under launchd.

   The probe may stall on a macOS Keychain prompt asking whether `board-reconciler` can read `atlassian-api-token`. Click Always Allow.

To check on it, run `~/.local/bin/board-reconciler --status`. To stop it, run `scripts/install-board-reconciler --uninstall`. That keeps its state, logs and your config file. [RECONCILER.md](RECONCILER.md) has the config fields and the full rules.

## Optional: Claude Desktop

The regular Claude Desktop chat app can use the board through a local MCP server:

```bash
make install-claude-desktop
```

Quit and reopen Claude Desktop afterwards. Claude on the web or your phone can't reach a board on your Mac.

## Troubleshooting

Start with `scripts/board-doctor` (or `make doctor`). It checks each piece and prints the fix for anything that fails.

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `todo: command not found` | `~/.local/bin` is not on PATH | `echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc && exec zsh` |
| `todo` says the board is unreachable | Docker isn't running, or the container stopped | Start Docker, then `docker compose up -d` in the clone |
| Port 7337 is taken | Something else is listening there | `TODO_PORT=7347 scripts/team-setup`, then `export TODO_URL=http://127.0.0.1:7347` in `~/.zshrc` |
| New Claude sessions show no board context | Plugin disabled, or the session predates setup | `claude plugin list`, then start a new session |
| New Codex sessions show no board context | The hook wasn't approved | Start a session and approve "Loading active task board" |
| Codex setup says the board block "contains other sections" | An old hand-made block has its BEGIN marker above unrelated text | Move `<!-- TODO-BOARD:BEGIN -->` down to the heading of the board section and rerun |
| Jira links are rejected | The server doesn't know your Jira host | Start the container with `TODO_JIRA_SITE=yourcompany.atlassian.net` |
| Reconciler probe never finishes | A Keychain prompt is waiting | Find the prompt and click Always Allow |
| Setup says `~/.local/bin/todo` points elsewhere | An older `todo` is installed | Move it aside and rerun setup |

The board staying down never blocks your agents. They mention it once and carry on with the real work. The board simply misses those updates.

## Uninstall

```bash
scripts/team-uninstall
```

This removes the plugin, the Codex block and hook, the `todo` link, any reconciler or notifier installed with the default labels, and stops the container. Your cards stay in `data/` until you delete the clone.
