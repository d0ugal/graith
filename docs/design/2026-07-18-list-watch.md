---
title: "Design Doc: Consolidate Live Session Monitoring Under List"
authors: OpenAI Codex
created: 2026-07-18
status: Implemented
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1393
---

# Consolidate Live Session Monitoring Under List

graith will expose one session-listing command: `gr list` for a snapshot and
`gr list --watch` for an interactive, live-updating view. The watch mode reuses
the existing full-screen session model and the shared session-column registry;
the separate `gr dashboard` command is removed.

## Background

`gr list` renders a script-friendly snapshot, the attached-session picker
renders an interactive list, and `gr dashboard` renders a second interactive
list with lifecycle actions. The snapshot and picker already draw their fields
from `internal/client/sessioncols.go`, while the dashboard has its own column
selection and width calculations. The dashboard does, however, already have the
desired refresh, navigation, viewport, confirmation, and action-result model.

## Problem

Users must discover a separate top-level command to get a live version of the
session list. Its filters and columns can diverge from `gr list`, and its name
does not communicate that it is the same session inventory. Adding another TUI
would deepen that divergence, while simply deleting the dashboard would lose a
useful monitoring and control surface.

## Goals

- Make `gr list --watch` and `gr ls --watch` the only standalone live session
  view.
- Share filters and meaningful display options with snapshot mode.
- Preserve attach, stop, recoverable delete, and resume actions, including
  stop/delete confirmation bound to stable session IDs.
- Reuse the existing full-screen model and session-column registry.
- Fail explicitly when a caller requests interactive watch output without a
  terminal or combines it with machine-output modes.

### Non-Goals

- Changing the attached-session picker or native GUI session views.
- Adding a streaming JSON protocol.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | This change consolidates two CLI session-listing commands. |
| iOS | Excluded | The native app already owns its session-list presentation and has no `gr dashboard` command. |
| macOS | Excluded | The native app already owns its session-list presentation and has no `gr dashboard` command. |

## Proposals

### Proposal 0: Do Nothing

Keep both commands. This avoids migration work but leaves duplicate discovery,
help, filtering, documentation, and rendering surfaces.

### Proposal 1: Add Watch Mode and Migrate the Existing TUI (Recommended)

Add a `--watch` flag to the existing list command. Snapshot and watch modes use
the same initial fetch and filter state. Watch refreshes fetch new session data
and reapplies `--repo`, `--children`, and `--starred`; the descendants filter is
resolved to a stable parent ID before entering the TUI. `--tree`, `--wide`, and
`--no-color` configure the migrated renderer.

The renderer keeps the dashboard's Bubble Tea update loop, alternate-screen
presentation, selection preservation, viewport, and action confirmations. Its
visible columns and cell values come from `SessionColumns`, using the same
compact/wide split as snapshot output. Tree mode supplies display prefixes
without changing the underlying selected session ID.

Watch mode rejects `--json` (including JSON implied by agent mode), `--quiet`,
and `--deleted`. It also requires both terminal input and terminal output. These
checks happen before daemon connection, so automation never enters an
interactive program or receives partial terminal control streams.

The `gr dashboard` Cobra registration and command implementation are deleted,
and the client model and tests are renamed around list watch. The associated
overlay keys are renamed from `dashboard_*` to `list_watch_*`; the former names
are not accepted as aliases. This is an intentional breaking transition that
leaves no dashboard-named active configuration surface behind.

### Proposal 2: Keep Dashboard as a Hidden Alias

Forwarding `gr dashboard` to `gr list --watch` would soften the migration, but
it would retain an undocumented command surface indefinitely and contradict the
goal of removing the duplicate command. It is therefore rejected.

## Other Notes

### References

- [Issue #1393](https://github.com/d0ugal/graith/issues/1393)
- `internal/cli/list.go`
- `internal/client/listwatch.go` (migrated from the former dashboard model)
- `internal/client/sessioncols.go`

### Testing

Client-model tests migrate with the renderer and continue covering refresh,
selection, viewport behavior, action gating, stable-ID confirmations, clean
exit, narrow terminals, and column rendering. CLI tests cover flag validation
before connection, terminal requirements, filter reuse, and command removal.
The affected packages, race-enabled repository tests, vet, and documentation
build run before shipping.
