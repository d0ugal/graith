---
title: "Design Doc: Shared Cursor Hook Ownership"
authors: Dougal Matthews
created: 2026-07-22
status: Implemented
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1328
---

# Shared Cursor Hook Ownership

Cursor sessions that intentionally use the same worktree share one generated
`.cursor/hooks.json` when they require identical hook definitions. Graith keeps
one durable ownership record per session, removes stale owners without treating
them as live references, and deletes the generated file only after the last
persisted session releases it.

## Background

Claude and Codex receive hooks through per-process arguments, but Cursor reads a
project-level `.cursor/hooks.json`. Graith therefore publishes that file in the
worktree and records its SHA-256 in `DataDir/hooks/<session>/cursor_hooks_owned`.
Publication and cleanup use exclusive creation plus claim-and-quarantine helpers
so a pathname replacement cannot make a prior hash check authorize overwriting
or deleting a different file object.

`--allow-concurrent` deliberately permits several in-place sessions to use the
same worktree. Session creation is reserved durably as `StatusCreating` before
hook injection begins, and deletion removes the session from durable state
before hook cleanup. Failed launches clean their generated files before rolling
back the session reservation. These ordering points provide a durable source of
truth for deciding whether another marker still represents a session.

## Problem

The current marker contains only a content hash and is consulted only at the
launching session's own marker path. A second Cursor session sees the first
session's generated file but cannot prove ownership, so launch fails as if the
file were user-owned. Transferring the marker would let either session delete
hooks while the other still needs them. A plain refcount would also become
incorrect after a crash or failed launch unless membership can be reconciled
against persisted sessions.

## Goals

- Let concurrent sessions share an identical generated Cursor hook file.
- Reject a requested definition that conflicts with another live owner.
- Delete only the unchanged generated file and only after its last live owner
  releases it.
- Preserve markerless, unreadable, modified, or concurrently replaced user
  files.
- Recover safely after daemon restart and ignore or retire markers whose
  session no longer exists.
- Preserve the existing exclusive publish, claim, quarantine, and rollback
  invariants.

### Non-Goals

- Merging arbitrary user Cursor hook definitions with Graith's hooks.
- Sharing Cursor prompt-rule ownership; this change is limited to
  `.cursor/hooks.json`.
- Adding a CLI, protocol, configuration, iOS, or macOS ownership surface.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | `gr new --in-place --allow-concurrent` is the affected workflow. |
| iOS | Excluded | The behavior is daemon-side and exposes no new client operation. |
| macOS | Excluded | The behavior is daemon-side and exposes no new client operation. |

## Proposals

### Proposal 0: Do Nothing

Keep ownership session-exclusive. This preserves the current file-safety
properties but leaves documented concurrent in-place mode unusable with Cursor
hooks.

### Proposal 1: Structured Per-Session Owners (Recommended)

Replace the marker's bare hash with a versioned JSON record containing the
canonical worktree path and exact content hash. The marker remains per-session,
so cleanup stays aligned with the existing hook directory lifecycle and no new
global state migration is required. Bare-hash legacy markers remain readable;
their worktree is inferred only while the corresponding persisted session is
available.

A dedicated Cursor-ownership mutex serializes join, republish, and leave
decisions. It is separate from `SessionManager.mu`, so directory reads and
claim/publish I/O never run under the manager state lock. Each operation first
snapshots the persisted Cursor sessions, then scans marker files.

An existing file is shareable only when its exact hash and canonical worktree
match a valid marker. Byte equality alone is insufficient, so a pre-existing
user file remains unowned even when it happens to equal Graith's generated
definition. A joining session writes its own marker atomically. If another live
owner requires the existing bytes and the joining session requests different
bytes, launch fails before process spawn with an incompatibility error. A sole
owner may continue to republish through the current claim-and-quarantine path.

During cleanup, a marker counts as a live reference only if its session remains
in persisted state as a Cursor session for the same worktree. The session being
cleaned is handled explicitly because normal delete has already removed it from
state. If another live owner remains, cleanup removes only the current marker.
If none remains, cleanup claims and verifies the public file, removes the
current and matching stale markers, syncs those removals, and only then deletes
the quarantined artifact. A crash before marker removal leaves no public file;
a crash after marker removal leaves at most a private quarantine. It never
leaves a stale public-file marker as authority over a later user replacement.

Stale structured markers can prove provenance for an unchanged artifact during
a later join. Once a new owner is durable, matching stale markers are retired.
Unreadable ownership metadata never authorizes replacement or last-owner
deletion.

### Proposal 2: One Shared Refcount File

Store a worktree-keyed owner list in one global JSON file. This makes the owner
set easy to inspect, but every join/leave rewrites shared mutable state and
introduces an additional transaction spanning the data directory and worktree.
It also needs collision-resistant path keys and explicit migration/repair. The
per-session markers already align with session cleanup and make abandoned
members independently recoverable, so a central ledger adds risk without
improving the safety proof.

## Other Notes

### References

- Issue [#1328](https://github.com/d0ugal/graith/issues/1328)
- `internal/daemon/hooks.go` — Cursor publication, ownership, and cleanup
- `internal/daemon/session_create.go` — durable creation reservation and failed-launch cleanup
- `internal/daemon/session_delete.go` — removal-from-state commit before hook cleanup
- `docs/design/2026-06-24-cross-agent-conversation-migration-design.md`

### Implementation Notes

The marker version change is backward-compatible rather than a state-schema
migration. Legacy markers are rewritten to the structured form when their owner
successfully injects again. Stale legacy markers without a persisted session
cannot be bound to a worktree and therefore remain non-authoritative.

Marker deletion must be ordered before final artifact deletion. The artifact is
first moved to a unique quarantine so removing marker authority cannot expose an
unowned generated file at the public path. Restoration remains no-replace.

### Testing

Focused tests cover identical sharing, incompatible definitions, both
two-session deletion orders, restart between owners, failed second launch,
stale-owner recovery, legacy markers, user-owned files, user modification, and
all existing deterministic publication/cleanup pathname races. Lifecycle tests
use real `Create` and `Delete` calls with a hermetic shell-backed Cursor agent.
The daemon package runs under the race detector, followed by the repository unit
and integration suites, vet, and lint.
