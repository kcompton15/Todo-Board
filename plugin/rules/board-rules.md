## Personal task board

A personal task board runs at `http://127.0.0.1:7337`, with the `todo` CLI on PATH
(`~/.local/bin/todo`). Keep it current as a side effect of substantive work that represents a
user-visible assignment. A plain answer creates no card. A request worth tracking gets one card
per outcome, not one per session, round or file. These lifecycle updates are authorized side
effects; do not ask separately whether to perform them.

- Match before creating: run `todo find "keywords"` unless the session-start context already
  shows the relevant card. Update or start a matching open card instead of duplicating it.
- The session-start context lists this directory's project first, and `todo add` files new cards
  under it when you omit `--project` (`todo project` prints it). Pass `--project` only for work
  that belongs elsewhere. If a directory maps to the wrong project, put the right name in a
  `.todo-project` file there.
- A card marked `(idle Nd)`, or counted in the idle line, has had no real activity for that long.
  If your work covers it, pick it up with `todo start <N>` rather than creating a new card.
- No card for a step of a card already listed (add or check a step), a question, or a chore
  inside the current task.
- One open card per Jira key or MR. Re-review rounds, fix-ups and follow-up passes on the same
  key or MR are steps on that card, not new cards. Use subtasks for review units and steps.
- Whenever a Jira key or MR exists, pass `--link KEY` (or put the MR, or a key from a project in
  the board's `TODO_ISSUE_PREFIXES`, in the title).
  `todo add` then attaches to the open card that already covers it and prints where, such as
  `attached to #142 (...) as #142.4`. Use `--new` only for a genuinely separate outcome.
- Refer to cards and steps by number, `#N` and `#N.M`: `todo show 142`, `todo check 142 3`.
- Create the card before work and choose `--kind`: `work` when the human asked for a change;
  `review` when the human asked for a review (pass the merge request URL with `--link`, not in
  notes); `followup` only when the conversation produces something the human must do; `probe`
  only when a question required real investigation and left artifacts worth finding again. Use
  `todo kind <N> <kind>` to correct a card's kind.
- Use `--tag` for work type. `todo` labels its changes `claude-code` or `codex` on its own, so
  run it plainly, with no `TODO_ACTOR=` prefix or `--source`.
- Start Today or Backlog work with `todo start <N>`. Check subtasks as each actually finishes.
  Record blockers with `todo block <N> "reason"`.
- Close the session's own `review` and `probe` cards when the deliverable is returned. Close
  `work` only when the human asked for that work and it is verified; otherwise run
  `todo log <N> next_step "ready to close"`. Never move a `followup` card.
- Record every implementation/review MR, including sibling repositories, with
  `todo link <N> <key|url> [--sub <M>] [--role required|reference]`.
  Required links cover that exact outcome; use reference for examples, dependencies and history.
- Finalize review findings, record `todo log <N> review_delivered "finalized report" [--sub <M>]`
  for each covered scope, check only completed steps, then run `todo done <N>` when all steps
  are complete before returning the report. No further user confirmation is needed.
- Delivery receipts are supplemental. If the board reconciler is installed, it also recognizes the
  user's own matching substantive GitLab comments or approval, or merged/closed review targets,
  with coverage for every MR in that review round. Historical reviews do not require a new receipt.
  Inactivity alone is insufficient. None of this
  authorizes posting comments or approvals to GitLab.
- Use `todo reconcile <N> manual` to exclude all future automatic updates; use open subtasks
  or manual mode for deployment, QA or acceptance beyond merge. Use `todo memo <N> "context"`
  for an agent hint (max 4000 characters); it is exposed through the local API/history, not secret.
- Never delete a card another actor created, and never edit the board's `data/tasks.json` directly.
- If the board is unreachable, mention it once and continue the real work. Board access must
  never block the requested engineering task or trigger repeated retries.

Useful commands: `todo ls`, `todo find "words"`, `todo show <N>`, `todo sub <N> "step"`,
`todo check <N> <M>`, `todo kind <N> <kind>`, and `todo help`.
