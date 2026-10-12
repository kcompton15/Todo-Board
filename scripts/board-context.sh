#!/usr/bin/env bash
set -u

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
cwd=""
[[ -t 0 ]] || cwd=$(jq -r '.cwd // empty' 2>/dev/null) || cwd=""
[[ -d "$cwd" ]] || cwd="$PWD"
"$script_dir/todo" context --cwd "$cwd" 2>/dev/null || true
exit 0
