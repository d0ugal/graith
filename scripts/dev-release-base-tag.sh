#!/usr/bin/env bash
# Select the stable release tag that GoReleaser should use as the base for a
# dev snapshot. Release Please owns the vMAJOR.MINOR.PATCH namespace; other
# tags are operational artifacts and must not affect the dev version.

set -euo pipefail

tags="$(git tag --merged HEAD --list 'v*' --sort=-version:refname)"

while IFS= read -r tag; do
    if [[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
        printf '%s\n' "$tag"
        exit 0
    fi
done <<< "$tags"

echo "error: no stable semantic release tag (vMAJOR.MINOR.PATCH) is reachable from HEAD" >&2
exit 1
