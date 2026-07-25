---
title: "Design Doc: Bounded file-watch registrations"
authors: d0ugal
created: 2026-07-25
status: Implemented (v1)
reviewers: (none yet)
informed: (none yet)
issue: https://github.com/d0ugal/graith/issues/1666
---

# Bounded file-watch registrations

File-watch triggers must remain useful as sessions and worktree trees grow
without multiplying kqueue/inotify registrations until `graithd` reaches
`EMFILE`. Registration is narrowed using include-glob ancestry and protected by
a daemon-wide budget; bindings that cannot fit degrade visibly and retry.

## Background

Each matching live session currently owns an fsnotify watcher. Recursive setup
adds one backend registration per non-ignored directory, while `watch.paths`
only filters events after registration. This is especially expensive on
macOS/kqueue. Binding teardown already closes fsnotify watchers, so the primary
failure mode is multiplication across live worktrees rather than a proven close
leak.

## Problem

A repository trigger such as `**/*.go` creates a registration set proportional to
every live worktree's directory tree. Repeated session churn and large generated
trees can consume the process descriptor budget before the daemon can accept
sockets or write state. A single failed `Add` currently reports only after the
kernel limit is reached.

## Goals

- Register only directory ancestry that can contain configured include globs.
- Bound aggregate registrations before process-wide descriptor exhaustion.
- Keep new-directory and ignore-rule reconciliation safe and observable.
- Preserve teardown/reload ownership and automatic recovery.

### Non-Goals

- Replacing fsnotify or adding a polling fallback.
- Redesigning completion-result indexing or upgrade probing.
- Sharing one watcher across unrelated worktree bindings.

### Known limitations

The budget is an admission-control estimate, not a perfect live descriptor
counter. In particular, macOS kqueue may open additional descriptors when new
files are created inside an already-watched directory; those descriptors are
not exposed by fsnotify's watch list and are not re-counted on every event.
Prune generated/high-churn trees with `watch.ignore` and keep includes narrow
until a future backend-specific live-cost reconciliation is implemented.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | Existing trigger status/doctor surfaces degraded reasons and configuration. |
| iOS | Excluded | File-watch triggers are daemon-side automation, not an iOS capability. |
| macOS | Targeted | kqueue descriptor multiplication is the incident platform. |

## Proposals

### Proposal 0: Do Nothing

Keep recursive registration and rely on fsnotify/kernel limits. This preserves
behavior but allows a large fleet of worktrees to make the daemon unavailable.

### Proposal 1: Include-aware registration plus a shared budget (Recommended)

Derive a static directory prefix from each include glob. Register its literal
ancestry, and recurse only where wildcard directory components or `**` can
contain a match. Every successful registration increments a daemon-owned count;
teardown, failed adds, and ignore pruning release it. The default budget is
8192 estimated watcher descriptors and is configurable as
`triggers.advanced.watch_max_directories`. On macOS the estimate includes the
directory entries that kqueue commonly registers separately; on other
platforms it is one unit per watched directory.

If a binding cannot reserve another directory, creation degrades with a reason
including current usage and budget, retains the existing retry/backoff behavior,
and leaves no watcher or reservation behind. Runtime additions log the same
bounded failure. This trades complete coverage of broad trees for predictable
daemon availability, while precise `paths` and `ignore` patterns reduce the
trade-off.

### Proposal 2: One process-wide shared fsnotify watcher

Deduplicate registrations across bindings and dispatch events to matching
triggers. This could reduce descriptors further, but requires substantial
ownership, path attribution, and teardown redesign; it is deferred until a
separate sharing design can establish semantics for overlapping worktrees.

## Decision

Implement Proposal 1 for issue #1666. The budget is deliberately independent
of the operating system's descriptor limit, and diagnostics explain when it is
the reason a binding is degraded.

## Verification

Unit coverage models include pruning, budget exhaustion and reservation release,
reload/lifecycle teardown, and existing end-to-end event delivery. Focused race
tests cover binding lifecycle paths; broader CI remains responsible for the
repository suite.
