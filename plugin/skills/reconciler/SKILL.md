---
name: reconciler
description: Install the optional board reconciler, which closes cards when their GitLab MRs merge or Jira issues finish. Walks through glab auth, the Jira Keychain token, config, a probe and enabling the schedule.
disable-model-invocation: true
---

The repository root is `${CLAUDE_PLUGIN_ROOT}/..`. Background: `docs/RECONCILER.md` there. The reconciler only reads GitLab (as the user, through `glab`) and Jira (with the user's token); it never writes to either.

Work through these in order. Stop and report if a step fails.

1. Board: `todo health` must pass. If not, run `/todo-board:doctor` first.
2. GitLab: `glab auth status --hostname gitlab.com`. If it fails, ask the user to run `! glab auth login --hostname gitlab.com` themselves.
3. Jira token: check `security find-generic-password -s atlassian-api-token` (attributes only; never pass `-w` and never print the token). If missing, the user creates a token at https://id.atlassian.com/manage-profile/security/api-tokens and stores it. The interactive `security -w` prompt truncates input at 128 characters and Atlassian tokens are longer, so give them this to run, which reads the token without echoing it and keeps it out of shell history:

   ```bash
   ! read -rs "TOKEN?Paste Jira API token: " && security add-generic-password -U -a "you@example.com" -s atlassian-api-token -w "$TOKEN" && unset TOKEN
   ```

   Replace the email with theirs. Verify with `security find-generic-password -a <email> -s atlassian-api-token` (no `-w`).
4. Build: `make -C "${CLAUDE_PLUGIN_ROOT}/.." build-darwin`.
5. Config: ask the user for their Jira site (bare hostname, for example `example.atlassian.net`), optionally their GitLab group (enables MR title search) and Jira project keys (for example `PROJ,OPS`). Run `"${CLAUDE_PLUGIN_ROOT}/../scripts/install-board-reconciler" --configure --jira-site HOST [--gitlab-group G] [--issue-prefixes PROJ,OPS]`. Show the detected GitLab user and Jira email and confirm with the user. If either is wrong, rerun with `--gitlab-user`/`--jira-email` and `--force`.
6. Install disabled, then probe: `--disable`, then `--probe`. The probe is one read-only dry run under launchd; if it hangs, a macOS Keychain prompt is waiting and the user should click Always Allow. Summarize the probe's result JSON (counts only).
7. Enable: `--enable` (deterministic only; do not pass `--with-model`). Report the schedule: weekdays 09:20, 11:20, 13:20, 15:20, 16:45 and 18:30 Central.
8. Confirm with `~/.local/bin/board-reconciler --status`.

If the installer says another reconciler agent exists, pass the `--label` it names rather than installing a second one.
