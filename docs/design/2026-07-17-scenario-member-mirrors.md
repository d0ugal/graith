---
title: "Design Doc: Scenario Member Mirrors"
authors: Codex
created: 2026-07-17
status: Implemented
reviewers: Implementation review complete
informed: Graith maintainers
issue: https://github.com/d0ugal/graith/issues/1378
---

# Scenario Member Mirrors

Scenario definitions gain a member-scoped `mirror` reference so several
sandboxed workers can inspect the exact committed and uncommitted filesystem
view of another member without creating branches or writable worktrees.

## Background

`gr new --mirror <session>` already creates a normal session whose PTY starts in
another session's worktree. `internal/daemon/session_create.go` derives the
source repo, worktree, base branch, and included worktrees from the source;
safehouse or nono grants that source read-only and gives the mirror a writable
scratch directory. Mirror identity is durable through `SessionState.Mirror` and
`MirrorSourceID`, and resume reconstructs the same sandbox policy.

Scenarios use a two-phase flow in `internal/daemon/scenario.go`: validate and
reserve the whole topology, then create members, rolling all created members
back if any creation fails. A `shared = true` member binds an existing running
or stopped session and is deliberately skipped by scenario stop, resume,
delete, rollback, and automatic cleanup.

## Problem

Every scenario-created member currently gets an independent writable Git
worktree. A branch can share committed history but cannot expose a source
session's uncommitted files, and pointing an agent at the source path would lose
the sandbox-enforced read-only boundary. Imperative `gr new --mirror` calls can
build this topology, but they are not declarative, atomic, durable as scenario
relationships, or visible through scenario status and manifests.

## Goals

- Let a scenario member reference another member as its mirror source.
- Resolve missing, incompatible, ambiguous, and cyclic topology before starting
  any member.
- Reuse the existing mirror creation and resume paths without a second sandbox
  implementation.
- Preserve atomic rollback and normal scenario lifecycle ownership.
- Persist and report the declared relationship across daemon restarts.
- Preserve all behavior for scenario files that do not use mirrors.

### Non-Goals

- A review- or tribunal-specific scenario primitive.
- Writable access from a mirrored member into its source.
- Arbitrary filesystem paths or sessions outside the scenario definition as
  mirror references.
- Changing the semantics of imperative `gr new --mirror`.

## Proposals

### Proposal 0: Do Nothing

Orchestrators can imperatively create mirror sessions after starting a
scenario. This loses scenario ownership, topology reporting, atomic rollback,
and reproducibility, so it does not meet the issue's requirements.

### Proposal 1: Member-name `mirror` references (Recommended)

Add `mirror = "subject"` to `[[sessions]]`. The value matches exactly one
member name in the same file; it is never passed through as an arbitrary daemon
session selector. A mirrored member must not also set `shared`, `repo`, `base`,
or `includes`. Agent, model, role, task, hooks, and star remain properties of
the new read-only worker.

Preflight builds a dependency graph over member indexes. Missing references and
self-references fail immediately. A depth-first traversal detects cycles and
produces dependency levels, allowing ordinary roots to start concurrently,
then their mirrors, then any later mirror-chain level. Shared roots are resolved
to exactly one running or stopped existing session; zero or multiple available
candidates fail before any session starts. Soft-deleted, errored, creating, and
deleting rows remain unavailable. A shared source without a worktree, or whose
saved worktree is no longer an accessible directory, is incompatible with
mirroring. Filesystem validation is performed outside the manager lock and the
selected session identity and paths are revalidated while reserving the
topology.

Each mirror is created through `SessionManager.Create` with `CreateOpts.Mirror`
set to the already-resolved source session ID. This is the existing primitive:
it copies source identity and includes, creates no Git worktree or branch, and
requires a functioning sandbox. Sandbox availability for every mirrored
member is checked during preflight so a disabled or unavailable backend cannot
partially start the scenario.

The direct member-name relationship is stored as `ScenarioSession.Mirror` and
returned as `ScenarioSessionInfo.Mirror`. Manifests add `mirror` to both the
current member and sibling entries. `SessionState.MirrorSourceID` continues to
store the runtime session relationship used by resume. Stop, resume, delete,
rollback, and trigger behavior continue to treat mirrored workers as ordinary
scenario-owned sessions; only `shared` members retain lifecycle protection. In
particular, scenario resume skips a stopped shared source rather than
relaunching it.

This adds optional JSON fields and a no-op state migration. Old state and old
clients continue to decode because omitted mirror fields preserve the previous
shape.

### Proposal 2: Mirror only shared members

Requiring every source to be `shared = true` simplifies launch ordering, but it
unnecessarily prevents a scenario-created implementer from being reviewed by a
scenario-created reader. Dependency ordering is small and makes the primitive
generally useful, so the restriction is not justified.

## Other Notes

### References

- Issue #1378 and parent issue #111.
- `docs/design/2026-06-22-scenarios.md`.
- `internal/daemon/scenario.go` and `internal/daemon/session_create.go`.
- `internal/sandbox/nono_enforce_test.go` and
  `internal/sandbox/safehouse_enforce_test.go`.

### Implementation Notes

The CLI performs fast structural validation for useful TOML errors, while the
daemon repeats all validation authoritatively. Repository allow-list and Git
checks apply to root members; mirrored members derive those values and never
interpret a user path. Scenario add remains limited to independent members in
this change because it accepts one member at a time and cannot safely add a
new member-scoped dependency without a separate topology mutation design.

### Testing

Tests cover CLI parsing and constraints; daemon graph validation; running,
stopped, deleted, ambiguous, and cleaned shared sources; multiple readers;
mirror chains; atomic rollback; status and manifest fields; stop/resume/delete
and automatic-cleanup ownership; state restart/migration; and the
protocol/Swift shapes. Existing backend enforcement tests remain the authority
that the reused mirror primitive denies source writes under safehouse and nono;
scenario tests assert that creation reaches that exact `Mirror` state and source
ID without allocating another worktree.
