#!/usr/bin/env bash
# Derive the one snapshot version shared by every dev-release platform job.

set -euo pipefail

base_tag="${1:-}"
build_epoch="${2:-}"

if [[ ! "$base_tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "error: dev release base must be vMAJOR.MINOR.PATCH" >&2
    exit 2
fi
major="${BASH_REMATCH[1]}"
minor="${BASH_REMATCH[2]}"
patch="${BASH_REMATCH[3]}"
if [[ ! "$build_epoch" =~ ^[1-9][0-9]*$ ]]; then
    echo "error: dev release build epoch must be a positive integer" >&2
    exit 2
fi

printf '%s.%s.%s-dev.%s\n' \
    "$major" "$minor" "$((patch + 1))" "$build_epoch"
