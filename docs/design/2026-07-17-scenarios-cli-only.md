---
title: "Design Doc: Keep scenarios out of the native GUIs"
authors: Dougal Matthews
created: 2026-07-17
status: Implemented
reviewers: (none yet)
informed: (TBD)
---

# Keep scenarios out of the native GUIs

Scenarios are an advanced orchestration facility intended to coordinate agent
sessions through an orchestrator and the `gr scenario` command family. The
native iOS and macOS apps will stop exposing scenario grouping, status, and
lifecycle controls. Scenario member sessions remain ordinary attachable
sessions in the apps, while orchestration stays in the CLI and orchestrator.

## Background

The scenario subsystem starts and coordinates groups of sessions from a TOML
definition. It includes roles and dependencies, inter-session messaging,
completion actions, declared results, and cleanup policy. Those concepts are
documented for an orchestrator-driven workflow in
`website/content/docs/scenarios.md`.

The native apps currently fetch scenario records during every host refresh,
group scenario members separately in the sidebar, show a dedicated scenarios
sheet, and expose stop, resume, and delete actions. Supporting that surface also
requires scenario-specific Swift protocol models, client methods, shared fleet
state, mocks, and tests.

## Problem

Scenario authoring and operation are configuration- and automation-heavy. A
partial native presentation cannot express the full workflow cleanly, while the
especially constrained phone UI pays substantial navigation and state-model
complexity for an expert feature. Maintaining a read/lifecycle subset in two
apps also suggests that scenarios are a general interactive UI concept when the
intended control plane is the orchestrator.

The apps still need to list and attach to the sessions that an orchestrator
creates. They do not need to understand the scenario graph in order to do that.

## Goals

- Keep scenario creation, inspection, and lifecycle management in the CLI and
  orchestrator workflow.
- Remove scenario-specific controls, grouping, and badges from both native apps.
- Stop fetching scenario records from native clients.
- Preserve ordinary session visibility and terminal attachment for scenario
  member sessions.
- Record iOS and macOS as deliberately excluded in the capability manifest.

### Non-Goals

- Removing scenarios from the daemon, CLI, protocol, or orchestrator.
- Hiding sessions merely because they were created by a scenario.
- Changing scenario persistence, authorization, completion actions, results, or
  cleanup behavior.
- Preventing another non-Swift remote client from using scenario messages.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | Scenario configuration and lifecycle commands remain the human and automation interface. |
| iOS | Excluded | The graph and lifecycle surface is too advanced and space-intensive for the phone UI; member sessions remain usable normally. |
| macOS | Excluded | Keeping the native apps conceptually aligned is more valuable than maintaining a partial desktop-only orchestration UI. |

All scenario capability rows (`scenarios.manage`,
`scenarios.completion-automation`, `scenarios.results`, and
`scenarios.runtime-policy`) use this decision.

## Proposals

### Proposal 0: Keep the current native scenario UI

This preserves an already-built surface but keeps scenario-specific networking,
state, navigation, and lifecycle actions in both apps. It also makes the phone
UI carry concepts that are more naturally expressed in configuration and an
orchestrator session.

### Proposal 1: CLI/orchestrator-only scenarios (Recommended)

Remove the dedicated scenario views and all sidebar entry points and grouping
from iOS and macOS. Scenario-created sessions continue to arrive through the
ordinary session list and remain selectable and attachable.

Remove scenario operations from `GraithHostClient`, `HostConnection`, and
`FleetModel`, and stop refreshing scenario records. Delete their native-client
mocks and shared feature tests. Remove the Swift protocol models and request
helpers that existed solely for the native UI, and classify all scenario wire
types as Swift `na` in the Go protocol manifest. The Go wire structs and daemon
handlers remain unchanged for CLI/orchestrator use.

Set all iOS and macOS cells for the scenario capabilities to `n/a`, linked
to this document. The platform-scoped parity check then excludes both GUIs while
continuing to enforce the manifest and the CLI documentation.

Update the user documentation to state that scenarios are operated through the
CLI/orchestrator and that native apps show their member sessions as ordinary
sessions.

### Proposal 2: Keep scenarios on macOS only

The desktop has enough space for the existing presentation, but this would
create a permanent product-level difference between the two native apps and
continue carrying most of the Swift scenario stack. The feature remains a
partial orchestration UI even on macOS. Rejected.

## Other Notes

### References

- `website/content/docs/scenarios.md` — scenario product documentation.
- `gui/ios/Sources/GraithMobileUI/ScenariosView.swift` — current iOS surface.
- `gui/macos/Sources/GraithGUI/ScenariosView.swift` — current macOS surface.
- `gui/shared/Sources/GraithSessionKit/HostConnection.swift` and
  `FleetModel.swift` — current shared scenario state and actions.
- `internal/protocol/manifest.go` — Swift wire-model expectations.

### Implementation Notes

Optional `scenario_id` and `scenario_name` fields can remain on the Go
`SessionInfo` wire shape without corresponding Swift properties; Swift decoding
already ignores unknown JSON keys. This lets the daemon and CLI retain scenario
metadata without leaking scenario presentation back into the apps.

### Testing

- Shared Swift builds and tests prove the boundary and real/mock clients no
  longer require scenario methods or models.
- macOS and iOS builds prove no deleted scenario view remains referenced.
- Protocol conformance proves no Swift decoder remains registered for a
  scenario type classified `na`.
- Capability tests prove both GUI cells are design-linked exclusions.
- Existing Go scenario unit and integration coverage remains unchanged.
