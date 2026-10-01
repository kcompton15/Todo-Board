## Personal task board

A personal task board runs at `http://127.0.0.1:7337`, with the `todo` CLI on PATH
(`~/.local/bin/todo`). Keep it current as a side effect of substantive work that represents a
user-visible assignment. A plain answer creates no card, but always track a human-requested change
or review even when it looks brief. These lifecycle updates are authorized side effects; do not ask
separately whether to perform them.

- Match before creating: run `todo find "keywords"` unless the session-start context already
  shows the relevant active card. Update or start a matching open card instead of duplicating it.
- Treat a card as one human-meaningful outcome. Use subtasks for multiple review units or steps;
  do not create a card per file.
- Create the card before work and choose `--kind`: `work` when the human asked for a change;
  `review` when the human asked for a review (put the merge request URL in notes); `followup` only
  when the conversation produces something the human must do; `probe` only when a question required
  real investigation and left artifacts worth finding again. Use `todo kind <id> <kind>` to correct
  a card's kind.
- Use `--project` for the product or repository epic and `--tag` for work type. Claude Code passes
  `--source claude-code` and prefixes mutations with `TODO_ACTOR=claude-code`; Codex uses `codex`
  for both.
- Start Today or Backlog work with `todo start <id>`. Check subtasks as each actually finishes.
  Record blockers with `todo block <id> "reason"`.
- Close the session's own `review` and `probe` cards when the deliverable is returned. Close
  `work` only when the human asked for that work and it is verified; otherwise run
  `todo log <id> next_step "ready to close"`. Never move a `followup` card.
- Record every implementation/review MR, including sibling repositories, with
  `todo link <id> <key|url> [--sub <subtask-id>] [--role required|reference]`.
  Required links cover that exact outcome; use reference for examples, dependencies and history.
- Finalize review findings, record `todo log <id> review_delivered "finalized report" [--sub <subtask-id>]`
  for each covered scope, check only completed steps, then run `todo done <id>` when all steps
  are complete before returning the report. No further user confirmation is needed.
- Delivery receipts are supplemental. If the board reconciler is installed, it also recognizes the
  user's own matching substantive GitLab comments or approval, or merged/closed review targets,
  with coverage for every MR in that review round. Inactivity alone is insufficient. None of this
  authorizes posting comments or approvals to GitLab.
- Use `todo reconcile <id> manual` to exclude all future automatic updates; use open subtasks
  or manual mode for deployment, QA or acceptance beyond merge. Use `todo memo <id> "context"`
  for an agent hint (max 4000 characters); it is exposed through the local API/history, not secret.
- Never delete a card another actor created, and never edit the board's `data/tasks.json` directly.
- If the board is unreachable, mention it once and continue the real work. Board access must
  never block the requested engineering task or trigger repeated retries.

Useful commands: `todo ls`, `todo show <id>`, `todo sub <id> "step"`,
`todo check <id> <subtask-id>`, `todo kind <id> <kind>`, and `todo help`.
