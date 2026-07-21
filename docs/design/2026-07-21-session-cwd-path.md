---
title: "Design Doc: Authoritative Session Working Directories"
authors: Codex
created: 2026-07-21
status: Implemented
reviewers: Independent implementation review
informed: Graith maintainers
issue: https://github.com/d0ugal/graith/issues/1398
---

# Authoritative Session Working Directories

Graith will persist the effective working directory assigned to each session,
project it as `cwd` in session metadata, and make `gr path` return that directory
for either a named session or the calling session itself.

## Background

`SessionState.WorktreePath` records a Git ownership concept used by worktree
cleanup, status, mirroring, and deletion. Most ordinary sessions also launch in
that path, so `gr path` historically returned it. The two concepts diverge for
the orchestrator, which launches in a managed scratch directory, and mirrors,
which expose a source worktree read-only while the sandbox launches the agent in
a writable per-session scratch directory. Resume currently reconstructs those
directories from daemon paths rather than retaining the original launch choice.

The CLI already has shared `--self` resolution for delete, stop, and purge. It
prefers `GRAITH_SESSION_ID`, falls back to `GRAITH_SESSION_NAME`, and reports a
clear error outside a Graith session.

## Problem

`gr path` requires a positional name or ID and rejects sessions whose
`worktree_path` is empty, even when Graith assigned them a real cwd. Deriving a
replacement from the CLI process would be incorrect for named lookup, nested
shells, daemon restarts, and sessions whose launch directory is not a worktree.

## Goals

- Persist the exact effective cwd for every newly launched session type.
- Reconcile older state, including orchestrators with no worktree path.
- Preserve the cwd through daemon restart and session resume.
- Return only existing absolute directories from `gr path` in plain and JSON
  output.
- Add `gr path --self` using the existing shared environment resolver.
- Keep worktree ownership and cleanup semantics unchanged.

### Non-Goals

- Changing what `GRAITH_WORKTREE_PATH` means.
- Letting users edit a session cwd after creation.
- Adding native-app navigation to local daemon filesystem paths.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | `gr path` is a shell-navigation command and owns `--self`. |
| iOS | Excluded | A daemon-local filesystem path is not navigable from iOS. |
| macOS | Excluded | The app receives the protocol field for conformance, but no new UI is useful for this CLI cleanup. |

## Proposals

### Proposal 0: Do Nothing

Callers can special-case `worktree_path` and reconstruct known scratch layouts,
but that duplicates daemon policy, fails for the orchestrator today, and cannot
reliably survive future launch-directory changes.

### Proposal 1: Persist `cwd` independently (Recommended)

Add `CWD` to `SessionState` and `SessionInfo`. Ordinary worktree, in-place, and
repo-less sessions store their assigned worktree or scratch path. Mirrors store
their own writable sandbox scratch directory while retaining the source in
`WorktreePath`. The orchestrator stores its managed scratch directory.

A state migration copies `WorktreePath` only where it is also authoritative.
Manager startup then reconciles layout-dependent legacy mirror and orchestrator
paths using the configured data directory and saves the result. Resume consumes
the persisted value and recreates managed scratch directories at that same path;
it does not replace a non-empty cwd with a newly derived one.

`gr path` resolves a live session through the existing list protocol, validates
that `cwd` is absolute and names an existing directory, then prints it without a
newline for shell use. JSON uses the semantically accurate `cwd` key. `--self`
passes through the same ID-first/name-fallback helper used by lifecycle commands
and is mutually exclusive with a positional session argument.

### Proposal 2: Redefine `worktree_path`

Making `worktree_path` mean process cwd would avoid a field, but a mirror would
lose the source worktree required for Git display, read-only grants, resume, and
safe deletion. It would also keep a misleading name for repo-less and system
sessions.

## Other Notes

### References

- Issue #1398.
- `internal/daemon/session_create.go`, `orchestrator.go`, and
  `session_resume.go`.
- `internal/cli/path.go` and the shared self resolver in `batch.go`.

### Implementation Notes

The protocol field is required for the current daemon/client generation and is
added to the required Swift model. `worktree_path` remains in `SessionInfo`
because other consumers still need its Git ownership semantics; only `gr path`
JSON drops the misleading key.

### Testing

State, daemon, CLI, protocol, Swift, and integration coverage will exercise
ordinary worktrees, in-place and repo-less sessions, mirrors, orchestrators,
legacy migration, restart/resume, self ID/name fallback, argument conflicts,
invalid or missing cwd paths, nested caller directories, and both output modes.
