---
title: "Design Doc: Read-only Branch Sessions"
authors: Codex
created: 2026-07-27
status: Implemented
reviewers: Ship-it implementation review pending
informed: Graith maintainers
issue: https://github.com/d0ugal/graith/issues/1690
---

# Read-only Branch Sessions

Graith gains repository-backed read-only sessions that resolve a branch to a
detached Git worktree, expose those files through the existing sandbox
read-only path, and launch the agent from writable scratch state.

## Background

Ordinary repository sessions own a writable branch and worktree. In-place
sessions run directly in the selected repository. Session mirrors created with
`gr new --mirror <session>` reuse another session's worktree read-only and
launch from scratch, so they are persisted as mirror sessions and file-watch
triggers skip them.

Some orchestration sessions need repository context but do not need to edit the
tree. Creating them as ordinary writable sessions wastes worktree resources and
can bind recursive file-watch triggers even though those sessions cannot
produce useful repository changes. Session mirrors are the right primitive when
the source is another live session, but they cannot name a repository branch
without first creating a separate source session.

## Problem

The daemon needs a self-contained way to create a read-only session from a
repository branch. The solution must resolve and refresh the branch according to
Graith's existing Git fetch policy, keep coordination files writable, remain
safe under sandbox enforcement, persist enough metadata for restart and resume,
and be classified early enough that automation never binds file-watch triggers
to it.

## Goals

- Let `gr new` create a repository-backed read-only session without another
  mirror source session.
- Reuse existing branch selection and fetch policy semantics.
- Keep repository files read-only while scratch, prompts, logs, and coordination
  state remain writable.
- Classify the session as mirror/read-only before trigger binding.
- Persist and report source repository, branch, and resolved revision.
- Cover creation, refresh/resume, default and remote branches, scenarios,
  sandbox policy, labels, restart, and failure rollback.
- Preserve normal worktree, in-place, repo-less, and session mirror semantics.

### Non-Goals

- A general `--no-watch` escape hatch for writable sessions.
- Making read-only branch sessions commit or edit their source branch.
- Replacing `gr new --mirror <session>` for read-only access to an existing
  session's uncommitted files.
- Changing the global trigger matching policy for root orchestrators that still
  require writable worktrees.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | Creation, scenarios, status, and JSON output are CLI-owned session surfaces. |
| iOS | Excluded | The mobile app receives protocol metadata but cannot create local daemon worktrees. |
| macOS | Excluded | The shared Swift model is kept conformant, but no new native UI is required. |

## Proposals

### Proposal 0: Do Nothing

Operators can keep creating writable orchestration sessions or manually create a
source session for `--mirror`. Writable sessions bind automation as if edits are
expected, while the synthetic source-session workaround adds lifecycle state and
does not model the desired branch reference directly.

### Proposal 1: Branch-backed read-only sessions (Recommended)

Add `gr new --read-only --repo <path> [--base <branch>]`. The existing `--base`
flag remains the branch selector; omitting it discovers the repository default
branch using the normal default-branch helper. `--read-only` is mutually
exclusive with `--mirror`, `--in-place`, `--no-repo`, and multi-repo includes.
It still requires a configured sandbox because repository access must be granted
read-only while the launch directory stays writable.

Creation resolves the repository through the same allow-list, singleton, and
label code paths as ordinary sessions, optionally fetches according to
`fetch_on_create` and `--no-fetch`, resolves the selected local or remote
branch to a commit, and creates a detached Git worktree at that revision. The
session launches from its own scratch directory. State stores `ReadOnlyBranch`
and `ReadOnlyRevision`; `Mirror` is also set so existing trigger binding skips
the session before any recursive watches are created. Unlike a session mirror,
`MirrorSourceID` remains empty because the source is a repository branch rather
than another session.

Resume and restart refresh the detached worktree by resolving the branch again
under the current fetch policy. If the branch moved, the worktree is checked out
to the new detached commit and `ReadOnlyRevision` is updated atomically with the
session relaunch. If setup fails during create, rollback removes the detached
worktree and scratch directory. Delete and purge tear down the detached worktree
without deleting a Graith branch, and remove scratch state as with session
mirrors.

Scenarios add `read_only = true` for members that should use this mode. The
daemon and parser reject incompatible combinations before topology reservation,
and read-only members are excluded from watch trigger role selection for the
same reason mirrors are excluded. Scenario mirrors cannot target a read-only
branch member; declare a second read-only branch member when multiple agents
need independent branch-backed views. Status and protocol JSON include
`read_only_branch` and `read_only_revision`; scenario status includes
`read_only`.

The trade-off is that read-only branch sessions do not expose uncommitted files
from another agent. That remains the job of `--mirror <session>`.

### Proposal 2: Add a `--no-watch` flag

A per-session watch opt-out would reduce some resource pressure, but it relies
on the agent or operator knowing that file-watch bindings exist. It also leaves
the session writable and does not provide a read-only repository boundary. Watch
policy for writable root orchestrators remains a separate administrator-facing
problem.

### Proposal 3: Reuse `--mirror <branch>`

Overloading `--mirror` to sometimes mean a session selector and sometimes mean
a branch makes errors ambiguous and weakens the important distinction between a
session-backed mirror and a repository-backed read-only view. A dedicated
`--read-only` mode with `--base` keeps terminology explicit.

## Other Notes

### References

- Issue #1690.
- `docs/design/2026-07-17-scenario-member-mirrors.md`.
- `docs/design/2026-07-25-file-watch-resource-bounds.md`.
- `internal/daemon/session_create.go`, `session_resume.go`, and
  `filewatch.go`.
- `internal/git/worktree.go` and `internal/git/branch.go`.

### Implementation Notes

The detached worktree is a real Git worktree so existing path handling,
teardown, and restart behavior remain close to normal sessions. The branch name
is stored separately from the resolved revision because refresh needs the branch
selector while status needs the exact commit exposed to the agent.

Swift protocol fixtures are regenerated with the new optional fields. Old
clients continue to decode because omitted read-only fields preserve the
previous zero values. The daemon state schema advances even though migration is
a no-op for old files, so downgraded daemons reject state that may contain
branch-backed read-only sessions instead of misclassifying them during cleanup.

### Testing

Unit and daemon coverage exercises local and remote branch resolution, detached
worktree setup and refresh, create validation and rollback, sandbox grants,
resume revision updates, file-watch exclusion, scenario parsing and status,
protocol round trips, Swift fixture conformance, CLI conflicts, and docs build
coverage. Broad `go test ./...` remains subject to the local Unix-socket bind
restriction in `internal/client` tests, so CI is the authoritative full-suite
environment for that package.
