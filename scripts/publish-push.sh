#!/usr/bin/env bash
# Commit the regenerated static apt/yum repo tree and push it to origin/main
# with a bounded rebase+retry.
#
# The publish-repo job checks out d0ugal/graith-repo@main, regenerates the tree,
# and pushes. A job-level concurrency guard serializes publish jobs so two
# releases can't race, but a manual commit to graith-repo (or any other external
# write) between checkout and push can still make the push non-fast-forward. When
# that happens we rebase onto the latest origin/main and retry, so a publish is
# never silently lost to a rejected push (issue #769).
#
# Scope of the rebase backstop: it recovers from a *non-conflicting* external
# commit (docs, gpg keys, etc.). If a rebase can't apply cleanly — e.g. another
# writer rewrote the same generated metadata — we abort the rebase and fail the
# job loudly rather than push a tree that silently dropped the other change. The
# concurrency guard is what prevents two publish runs from producing genuinely
# conflicting metadata in the first place; reconciling that case would require
# re-running metadata generation, which is out of scope for this helper.
#
# Usage: publish-push.sh <repo-dir> <commit-message> [max-attempts]
set -euo pipefail

repo_dir="${1:?repo dir required}"
message="${2:?commit message required}"
attempts="${3:-5}"

cd "$repo_dir"

git config user.name "github-actions[bot]"
git config user.email "github-actions[bot]@users.noreply.github.com"

git add -A
if git diff --cached --quiet; then
  echo "No repo changes to publish."
  exit 0
fi
git commit -m "$message"

for i in $(seq 1 "$attempts"); do
  if git push origin HEAD:main; then
    echo "Pushed on attempt $i."
    exit 0
  fi
  # No point rebasing after the final failed push — there's no retry left.
  if [ "$i" -eq "$attempts" ]; then
    break
  fi
  echo "Push rejected on attempt $i; rebasing onto origin/main and retrying." >&2
  if ! git pull --rebase origin main; then
    # Conflicting change on origin/main; abort so we don't leave the checkout
    # mid-rebase or push a tree that dropped it.
    git rebase --abort || true
    echo "Rebase onto origin/main failed (conflicting change); aborting." >&2
    exit 1
  fi
done

echo "Failed to push after $attempts attempts." >&2
exit 1
