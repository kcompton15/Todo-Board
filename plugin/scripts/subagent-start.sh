#!/usr/bin/env bash
set -u

plugin_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
preface='You are a subagent. Do not create board cards: the session that started you owns the card. Update only the card and steps your task names, and report board changes back.'
jq -n --arg preface "$preface" --rawfile rules "$plugin_root/rules/board-rules.md" \
  '{hookSpecificOutput: {hookEventName: "SubagentStart", additionalContext: ($preface + "\n\n" + $rules)}}'
exit 0
