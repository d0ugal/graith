---
title: "Design Doc: Update Starred Session State"
authors: OpenAI Codex
created: 2026-07-18
status: Implemented
reviewers: (pending review)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1396
---

# Update Starred Session State

Starred state becomes an optional property of the existing session update
operation. The dedicated `star` and `unstar` commands and wire messages are
removed so compound metadata changes have one atomic, persisted daemon path.

## Background

Sessions persist a `Starred` boolean which drives deletion protection, list
filtering, overlay presentation, and native app actions. Name and parent changes
already use an `update` control message with optional fields, but starring uses
two separate messages and two top-level CLI commands. The overlay and Swift
clients also send those dedicated messages.

## Problem

One session property consumes two command and protocol verbs, cannot be changed
atomically with name or parent, and duplicates daemon mutation code. This also
makes scripting asymmetric: setting false requires selecting a different verb
instead of passing the desired value.

## Goals

- Make omission of starred state mean no change and accept explicit true/false.
- Apply name, parent, and starred changes atomically under the session-manager
  lock and persist them once.
- Return the resulting session state to human, JSON, overlay, GUI, and remote
  callers.
- Preserve deletion protection and the existing update authorization boundary.
- Remove the obsolete CLI commands, wire messages, handlers, and client paths.

### Non-Goals

- Removing the interactive star toggle from the terminal or native apps.
- Changing scenario `star = true`, which controls initial session state.
- Changing starred list filtering or soft-delete retention semantics.
- Keeping compatibility aliases for the removed commands or control messages.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | `gr update <session> --starred=<bool>` is the canonical scripting surface. |
| iOS | Targeted | The existing star toggle sends the canonical update request. |
| macOS | Targeted | The shared host client moves the existing toggle to the same request. |

## Proposals

### Proposal 0: Do Nothing

Keep `star` and `unstar` alongside `update`. This preserves duplicated mutation
paths and prevents atomic compound updates, so it does not meet the issue.

### Proposal 1: Extend the update transaction (Recommended)

Add `Starred *bool` to `UpdateMsg`. The CLI uses a Cobra boolean flag whose
changed bit distinguishes omission from explicit false; a bare `--starred`
means true. Target and parent names are resolved before sending one request.

The daemon authorizes the update once, validates every requested field, applies
all changes while holding the session-manager lock, saves state once, and
returns the resulting `SessionInfo`. System, deleting, and soft-deleted sessions
remain rejected by the update transaction. Because updates serialize under the
same lock, concurrent partial updates retain fields omitted by either caller.

Terminal overlay and Swift GUI toggles continue to present Star/Unstar actions,
but send `update` with `starred` set to the desired value. `StarMsg`,
`UnstarMsg`, their handler cases, manager methods, auth-matrix rows, and the two
top-level commands are deleted. The `update` row remains remote-human writable
and subject to the existing self-or-descendant target authorization.

### Proposal 2: Forward the old verbs into update

The old commands and messages could construct an update internally. This would
ease mixed-version operation, but would retain the redundant public surface and
contradict the explicit no-shim requirement. It is rejected.

## Other Notes

### References

- [Issue #1396](https://github.com/d0ugal/graith/issues/1396)
- `internal/cli/update.go`
- `internal/daemon/handler_lifecycle.go`
- `internal/daemon/session_metadata.go`
- `internal/protocol/messages.go`
- `gui/shared/Sources/GraithProtocol/GraithProtocolClient.swift`

### Implementation Notes

No state migration is needed because persisted `SessionState.Starred` is
unchanged. The wire change is deliberately breaking: older star/unstar messages
become unsupported and no aliases remain. The protocol manifest is regenerated
after `UpdateMsg` becomes a Swift-required shape.

### Testing

Tests cover combined and omitted fields, explicit and bare boolean flag forms,
idempotent true/false, ambiguous lookup, deleted/deleting/system sessions,
scenario-owned state, deletion protection, authorization, concurrent partial
updates, persistence, overlay transport, Swift request/response decoding, and
protocol/capability conformance. Focused Go race tests, integration tests, Swift
shared tests, docs generation, and the full Go suite verify the cross-layer
change.
