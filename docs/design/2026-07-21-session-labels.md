---
title: "Design Doc: Persistent session labels"
authors: Dougal Matthews
created: 2026-07-21
status: Implemented
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1518
---

# Persistent session labels

Sessions will carry a small, durable set of user-managed labels. Labels provide
a repository-independent way to find related work in the CLI picker and native
sidebars without deriving metadata from GitHub, prompts, branches, or terminal
content.

## Background

The daemon persists each session as a `SessionState` and projects it onto the
shared `protocol.SessionInfo` returned by `list` and lifecycle responses.
`gr update` already serializes mutable name, parent, and starred changes under
the session-manager lock. The CLI picker groups its All view by repository and
applies text search after choosing a view. The iOS and macOS sidebars share
filter state and predicates through `GraithSessionKit.SidebarFilter`.

Repository grouping is useful for code location, but it cannot express a
release, customer, incident, or other workstream spread across repositories.
Starred is a single fixed bit and cannot represent more than one such set.

## Problem

People cannot attach durable organizational metadata to a session. Finding a
cross-repository workstream requires naming conventions or external tracking,
neither of which is queryable through `gr list`, the TUI picker, or the native
apps. Any solution must preserve the daemon's existing persistence,
authorization, soft-delete, system-session, and concurrent-update boundaries.

## Goals

- Persist zero or more validated, deduplicated labels on every session.
- Support labels during creation and atomic individual add/remove updates.
- Filter `gr list`, the TUI picker, iOS, and macOS across repository boundaries.
- Expose a complete label array in stable session JSON and the Swift model.
- Preserve labels through restart, migration, soft delete/restore, and fork.
- Make save failure roll back the entire in-memory metadata update.
- Keep label metadata independent of authorization and external services.

### Non-Goals

- Label colours, descriptions, automatic suggestions, or a global registry.
- GitHub label synchronization or inference from any session text or Git state.
- Authorization, sandbox, scenario, or lifecycle behavior derived from labels.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI/TUI | Targeted | Creation, mutation, list filtering, automation JSON, and a cross-repository Labels picker view are primary workflows. |
| iOS | Targeted | The shared model decodes labels; the sidebar filters by label and session create/edit UI manages them. |
| macOS | Targeted | The shared model and filtering match iOS, with desktop create/edit presentation. |
| Stable automation JSON | Targeted | Every `SessionInfo` contains `labels`, including an explicit empty array. |

## Proposals

### Proposal 0: Do Nothing

Continue using starred state, names, and repository grouping. This leaves
cross-repository work undiscoverable and encourages conventions that cannot be
validated or queried. Rejected.

### Proposal 1: Bounded labels on session state (Recommended)

Add `Labels []string` to persisted `SessionState` and wire `SessionInfo`.
`labels` is never omitted from `SessionInfo`, so JSON distinguishes the complete
empty set (`[]`) from a client that did not receive the field. State schema v26
initializes every existing v25 session to an empty slice and is saved in place
through the existing migration backup and atomic-write path.

#### Validation and identity

Each supplied label is trimmed with Go's Unicode whitespace rules. The trimmed
value must be non-empty, at most 64 UTF-8 bytes, and contain no Unicode control
characters or commas (commas delimit labels in interactive forms). A session
may hold at most 32 distinct labels.

Label identity uses Unicode simple case folding (`strings.EqualFold`). The
daemon does not apply canonical Unicode normalization, so canonically equivalent
but byte-distinct spellings remain distinct unless simple folding equates them.
The first stored spelling wins: adding `urgent` to a session already labelled
`Urgent` is an idempotent no-op and preserves `Urgent`; removal is
case-insensitive. Creation input is deduplicated in argument order. These rules
are deterministic, bounded, and require no locale or external registry.

An update containing the same folded label in both add and remove sets is
rejected as ambiguous. Removing a missing label and adding an existing label
both succeed as no-ops. Exceeding the count after applying removals and additions
rejects the whole update.

#### Commands and protocol

`gr new --label <label>` is repeatable. Native and TUI create forms accept a
comma-separated label field and send the same `CreateMsg.labels` array.
`gr update` gains repeatable `--add-label` and `--remove-label` flags; they may
be combined with name, parent, and starred changes in one `UpdateMsg`. The
native editor computes an add/remove delta and uses the same message.
`UpdateResultMsg` returns the complete resulting label set.

`gr list --label <label>` is repeatable and matches sessions containing every
requested label (AND semantics). It composes with repo, children, starred,
deleted, tree, token, quiet, and JSON projections because filtering remains a
pure selection step over daemon-provided metadata. User input is validated with
the same rules before comparison.

No new control verb is introduced: `update` retains its existing remote policy
and handler authorization. A label-only update therefore has exactly the same
self/descendant/human boundary as rename or star. System sessions and
soft-deleted sessions remain non-updatable. Labels are never consulted by an
authorization check.

#### Picker and native filtering

The CLI picker gains a Labels view. It groups the filtered live session set by
label rather than repository; a multi-labelled session appears in each matching
group, and each group includes matches from every repository. Unlabelled
sessions are absent. Tab/shift-tab move between label groups as they do between
repository groups. Text search is applied first and matches labels in addition
to the existing fields, so search and label grouping compose without hidden
data sources. Refresh preserves the selected session and label group when that
pair still exists. The empty view states that there are no labelled sessions.

The shared Swift `SidebarFilter.Criteria` gains an exact case-insensitive label
criterion. Label, repo, starred, and search criteria compose with AND semantics;
search also includes label display text. `FleetModel` exposes a sorted,
case-insensitively deduplicated label list across all connected hosts. Selecting
a label filters the full fleet before repository/host presentation, so matches
cross repository and host boundaries on both iOS and macOS.

#### Forks and lifecycle

A fork inherits a deep copy of the source's complete label set. This keeps the
new session in the same workstreams while allowing later independent edits.
Cross-agent and same-agent forks behave identically. Create does not inherit
labels merely because a parent is assigned; only the explicit fork operation
does. In-place agent migration, soft delete, and restore do not rewrite labels.

#### Atomicity and concurrency

All validation happens before mutation while holding the session-manager lock.
The update starts from the current session value, applies only the requested
fields, and persists the whole state through the existing atomic writer. On save
failure it restores a deep snapshot before returning, including name, parent,
starred, and labels. Independent concurrent updates serialize on that lock and
therefore start from the latest committed value instead of replacing an older
client snapshot; a label delta cannot lose an unrelated metadata update.

### Proposal 2: Global label records with session references

Persist a registry of label IDs, names, colours, and memberships. This could
enforce one spelling globally and support future descriptions, but introduces
referential migration, deletion semantics, and synchronization for features
explicitly outside v1. Rejected in favor of self-contained session metadata.

### Proposal 3: Infer labels from external or textual data

Derive labels from GitHub, branches, names, prompts, summaries, or output. This
would be surprising, nondeterministic, and potentially expose untrusted content
to organizational or authorization decisions. Rejected.

## Other Notes

### References

- Issue [#1518](https://github.com/d0ugal/graith/issues/1518); related #906 and #1454.
- `internal/daemon/state.go`, `session_metadata.go`, `session_create.go`, and
  `session_fork.go` own persistence and mutation.
- `internal/client/overlay.go` owns the CLI picker.
- `gui/shared/Sources/GraithSessionKit/SidebarFilter.swift` and
  `FleetModel.swift` own native filtering.

### Implementation Notes

The protocol changes extend registered, Swift-required `CreateMsg`, `UpdateMsg`,
`UpdateResultMsg`, and `SessionInfo`; no new registered type or auth-matrix row
is needed. Regenerate the protocol manifest and update the Swift models. Add a
frontend capability for session-label management/filtering and regenerate both
capability artifacts.

The label helpers are a small internal package shared by daemon and CLI so list
filtering cannot drift from mutation semantics. Swift mirrors simple-folded
case-insensitive comparison for values chosen from the daemon-provided label
set; the daemon remains authoritative for validation and stored spelling.

### Testing

- Unit-test empty, whitespace, oversized, control-character, duplicate,
  case-folded, multi-label, count-limit, add/remove-conflict, and display-spelling
  behavior.
- Test creation persistence, JSON empty/non-empty arrays, v25 migration,
  restart, soft delete/restore, and same/cross-agent fork inheritance.
- Test atomic combined updates, authorization, system and deleted guards, save
  rollback, and concurrent unrelated updates under `-race`.
- Test list filter AND/composition behavior and CLI flag/message encoding.
- Test TUI label grouping, cross-repo matches, multi-label duplication, search,
  empty state, refresh, and selection preservation.
- Test Swift decoding, shared filter composition, available labels, clear state,
  native create/edit request wiring, and both app builds.
- Regenerate and test protocol/capability fixtures; run Go, Swift, docs, race,
  and relevant tagged integration suites.
