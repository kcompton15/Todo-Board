#!/usr/bin/env bash
set -u

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
"$script_dir/todo" context 2>/dev/null || true
exit 0
