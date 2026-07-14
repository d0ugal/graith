---
title: "Design Doc: Shared session/feature view-model layer (GraithSessionKit)"
authors: Dougal Matthews
created: 2026-07-14
status: Accepted
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1131
---

# Shared session/feature view-model layer (GraithSessionKit)

The macOS and iOS SwiftUI apps share code only up to the protocol/transport
level (`GraithProtocol`, `GraithRemoteKit`, `GraithTerminalCore`, `GraithDesign`).
Everything above the wire — the session store, the per-host connection
view-model, approvals aggregation, host/pairing management — is written twice,
once per platform, in mutually incompatible shapes. This doc introduces a single
shared session/feature layer, `GraithSessionKit`, that both apps bind to, so a
new capability is wired once and appears on both platforms by construction.

## Background

The universal app (#628) is three Swift packages: `gui/shared` (cross-platform
libraries), `gui/macos` (the AppKit/SwiftUI desktop app), and `gui/ios` (the
UIKit/SwiftUI mobile app). `gui/shared` today ships `GraithProtocol` (the framed
wire client `GraithProtocolClient` + Codable wire messages), `GraithRemoteKit`
(the multi-host substrate: `Host`, `HostRegistry`, `DeviceIdentity`,
`KeychainStore`/`SecretStore`, `PairingCoordinator`, `RealPairing`), plus
`GraithTerminalCore` and `GraithDesign`.

Above that line the two apps diverge completely:

- **macOS** binds a single `@MainActor class SessionStore` (`gui/macos/.../SessionStore.swift:44`)
  directly to the concrete `GraithProtocolClient` actor. One store owns every
  host's client, the merged session list, refresh/polling, per-host errors, and
  single-attach coordination. `ApprovalMonitor` subscribes across hosts and
  drives the Dock badge + notifications. `WindowState` holds per-window
  selection/split state.
- **iOS** built a protocol *boundary* — `GraithClientAPI` — declaring
  `GraithHostClient`, `TerminalAttachSession`, `HostClientFactory`,
  `GraithPairing`, plus its own Codable mirrors of the wire messages
  (`SessionInfo`, `ApprovalInfo`, `RepoEntry`, `CreateRequest`, …). Above the
  boundary sit `AppModel` (multi-host aggregator), one `HostConnection` per host
  (connection lifecycle + session list + approval subscription + mutations), and
  `TerminalAttachViewModel`/`AttachRegistry`. The real transport is bridged in by
  `GraithMobileReal` (`RealHostClient` wrapping `GraithProtocolClient`,
  `ModelMapping` translating `GraithProtocol.* → GraithClientAPI.*`), and a
  `GraithMobileMock` supplies previews/tests.

Crucially, iOS **re-declares** the entire `GraithRemoteKit` surface in its own
`GraithMobileKit` target (`HostRegistry`, `SecretStore`, `KeychainStore`,
`DeviceIdentity`, `PairingCoordinator`) — a straight copy that has already
drifted from the shared canonical version (the shared copies carry a
`HostRegistry.unknownHost` orphan-token fix, a `PairingCoordinator` generation
cancel-guard, and a `setDeviceID`-on-confirm step that the iOS copies lack).

## Problem

There is no shared definition of "what a session app can do." Every capability —
list/create/stop/resume/rename/star/fork/migrate a session, respond to an
approval, add/remove a host, pair a device — is implemented independently on each
platform. Concretely:

- **Active, drifting duplication.** `gui/ios/.../GraithMobileKit/{HostRegistry,
  SecretStore,KeychainStore,DeviceIdentity,PairingCoordinator}.swift` duplicate
  `gui/shared/.../GraithRemoteKit/*` and have already diverged in behaviour and
  bug-fix level. `GraithClientAPI/WireMessages.swift` duplicates the
  `GraithProtocol` wire structs field-for-field, forcing a whole
  `ModelMapping.swift` translation layer that exists only to bridge the two.
- **Parity is by discipline, not construction.** A capability added on one
  platform must be independently re-wired on the other, or the apps drift. This
  is exactly why iOS is currently ahead of macOS (#1130) and why the parity
  effort keeps regressing (#1128).
- **Two review surfaces for one behaviour.** A bug fixed in one store's refresh
  or approval-subscription loop is not fixed in the other; the retry/backoff
  logic in `HostConnection.startApprovalSubscription` and macOS
  `ApprovalMonitor` are hand-copied prose, not shared code.

## Goals

- One shared session/feature view-model layer in `gui/shared` that both apps bind
  to: a per-host connection view-model, a multi-host aggregator/store, approvals
  aggregation, host and pairing management, and the single-attach registry.
- Exactly one definition of the capability surface (the `GraithHostClient`
  boundary) and one set of wire model types (`GraithProtocol.*`) — no per-platform
  mirrors, no `ModelMapping` translation.
- Fold the duplicated `GraithRemoteKit` types out of iOS entirely; iOS consumes
  the shared canonical versions (gaining the fixes it was missing).
- Adding a capability requires touching shared once; the platform apps supply
  only chrome (windows, menus, UIKit input, Dock/notification presentation).
- No behavioural regression on either platform; the change is compile-verifiable
  (`swift build` for both apps) and unit-tested for the shared logic.

### Non-Goals

- Redesigning the wire protocol or the daemon (that's #1129, one layer down).
- Unifying the *terminal rendering* stack beyond what already lives in
  `GraithTerminalCore` — the UIKit (`GraithTerminalUIKit`) and AppKit terminal
  views stay platform-specific; only the platform-agnostic
  `TerminalAttachViewModel`/`TerminalCoreDriving`/`AttachRegistry` move to shared.
- Unifying app lifecycle, menus, window/scene management, `graith://` URL/Handoff
  glue, or the Dock-badge/local-notification *presentation* — these are genuinely
  platform-specific and stay in `gui/macos` / `gui/ios`.
- Runtime/device validation in this change — no simulator or device is available
  in the build environment; correctness rests on compile verification, shared
  unit tests, and review. Device smoke-testing is tracked separately.

## Proposals

### Proposal 0: Do Nothing

Keep two parallel stacks and enforce parity by review. This is the status quo and
the thing #1131 identifies as the structural root cause of drift: it has already
failed (the iOS `GraithMobileKit` copies drifted from `GraithRemoteKit`; macOS
lags iOS on features). Rejected.

### Proposal 1: `GraithSessionKit` shared layer, both apps rewired (Recommended)

Add a new library target `GraithSessionKit` to `gui/shared`, depending only on
`GraithProtocol` + `GraithRemoteKit` (deliberately *not* `GraithTerminalCore`, so
it builds without libghostty and stays unit-testable on any host). It contains
the session/feature layer, lifted from iOS's already-clean design and generalised
to serve both platforms:

**The capability boundary.** The `GraithClientAPI` boundary protocols move into
`GraithSessionKit` and are retyped onto the shared models:

- `protocol GraithHostClient: Actor` — the one definition of a session app's
  capabilities (connect/disconnect, `listSessions`, `status`, `repoList`, `logs`,
  `screenSnapshot`, `create`, `stop`/`resume`/`restart`/`interrupt`/`delete`,
  `rename`, `star`/`unstar`, `fork`, `migrate`, `approvalStream`,
  `respondApproval`, `attach`).
- `protocol TerminalAttachSession: Actor`, `protocol HostClientFactory`,
  `protocol GraithPairing`.
- Small app-level value types that aren't wire messages: `ApprovalDecision`,
  `StatusResponse`, `FleetSummary`, `GraithClientError`, `ControlType`.

The per-platform wire mirrors in `GraithClientAPI/WireMessages.swift`
(`SessionInfo`, `PRInfo`, `CIInfo`, `IncludedRepoInfo`, `RepoEntry`,
`CreateRequest`, `ApprovalInfo`, `ScreenSnapshot`, `PairResponse`, …) are
**deleted** in favour of the canonical `GraithProtocol.*` types. `CreateRequest`
becomes `GraithProtocol.CreateMsg` (which is a superset). `ScreenSnapshot`
becomes `ScreenSnapshotResponseMsg`. UI conveniences (`isYolo`,
`isScenarioMember`, `shortBranch`, `isRunning`/`isStopped`/…) consolidate into a
single `SessionInfo` extension in `GraithSessionKit`. Because the boundary now
speaks `GraithProtocol.*` directly, `ModelMapping.swift` collapses to identity
and is removed.

**The view-models.** These move into `GraithSessionKit`:

- `HostConnection` — the per-host `ObservableObject` (connection state, session
  list, approvals, the exponential-backoff approval-subscription loop, and every
  mutation). Retyped from iOS's `HostEntry` to the canonical `Host`.
- `FleetModel` — the multi-host aggregator that unifies macOS `SessionStore`'s
  cross-host merge/refresh/polling/per-host-error/single-attach logic with iOS
  `AppModel`'s connection aggregation. Owns `[HostConnection]`, the merged
  session feed, `SessionRef`/`HostedSession`/`HostedApproval`, repo/host grouping,
  and single-attach coordination via `AttachRegistry`.
- `AttachRegistry` — single-attach-per-session coordination (generalised so the
  owner is an opaque token rather than macOS `WindowState`).
- `TerminalAttachViewModel` + `TerminalCoreDriving` — the platform-agnostic attach
  view-model and its terminal-driving seam.
- `TailnetReachability` — moved from iOS `GraithMobileKit` (it uses `Network`,
  available on both platforms).

**The real client.** `RealHostClient`, `RealHostClientFactory`, `RealPairing`,
`RealAttachSession`, and `ErrorMapping` move from iOS `GraithMobileReal` into
`GraithSessionKit`. They are Foundation-only (they wrap `GraithProtocolClient`)
and are the production `GraithHostClient` for *both* apps.

**iOS becomes a thin consumer.** The `GraithClientAPI`, `GraithMobileKit`, and
`GraithMobileReal` targets are removed; their surviving code lives in shared.
`GraithMobileUI` deletes `AppModel.swift`/`HostConnection.swift` and binds its
SwiftUI views to the shared `FleetModel`/`HostConnection`. `GraithMobileMock`
retargets its mocks onto the shared protocols. iOS keeps only genuinely
UIKit-specific code (`GraithTerminalUIKit`, `GraithMobileRealTerminal`'s Metal
renderer, and the `#if os(iOS)` bits of `PairingView`).

**macOS binds to the shared layer.** `SessionStore` is replaced by the shared
`FleetModel` (with a thin macOS-side extension for the `WindowState`-typed
single-attach owner and the font-size/renderer terminal-presentation state, which
are macOS chrome). `ApprovalMonitor` keeps only the macOS *presenter* (Dock badge
via `NSApp.dockTile`, `UNUserNotificationCenter`) and consumes the shared
approvals aggregation. `WindowState`, menus, and `graith://`/Handoff glue stay
macOS-side.

**Trade-offs.** A large one-shot diff (~40 files across both apps) that cannot be
runtime-tested here. Mitigated by: (1) both apps compile with `swift build` in
this environment, so the change is fully compile-verified; (2) the shared logic
gets real unit tests (`GraithSessionKitTests`) runnable via `make test-clt`; (3)
the shared `GraithRemoteKit` copies iOS adopts are the *fixed* superset, so the
fold-back is a net correctness gain, not just a move.

### Proposal 2: Shared layer + iOS fold-back now, macOS later

Ship `GraithSessionKit` + the iOS rewire + the `GraithRemoteKit` fold-back first,
and port macOS in a follow-up. Smaller, safer per-PR, and still removes the
duplicated types from iOS immediately. Rejected for this change only because the
maintainer opted for the complete rewire in one PR so the acceptance criteria are
met at once and macOS doesn't sit half-migrated; the phasing here is preserved as
the internal build order (see Implementation Notes) rather than as separate PRs.

### Proposal 3: Share code by making `GraithProtocolClient` conform to a boundary in `GraithProtocol`

Put `GraithHostClient` in `GraithProtocol` and conform the actor directly, no
`RealHostClient` wrapper. Rejected: the boundary needs app-level shaping the raw
client doesn't provide (a non-throwing `approvalStream`, a synthesized `status`
from `list`, `CreateRequest`-style creation, `ApprovalDecision` typing), and
keeping the boundary in `GraithSessionKit` preserves the mock seam
(`GraithMobileMock`) that previews/tests depend on. The thin `RealHostClient`
adapter is worth its keep.

## Other Notes

### References

- Issue #1131 (this), #1128 (capability matrix), #1130 (macOS UI gaps), #1129
  (protocol conformance), #628 (universal app), #885 (macOS remote).
- Key code: `gui/macos/.../SessionStore.swift`, `.../ApprovalMonitor.swift`,
  `.../WindowState.swift`; `gui/ios/.../GraithClientAPI/{Boundary,WireMessages}.swift`,
  `.../GraithMobileUI/{AppModel,HostConnection}.swift`,
  `.../GraithMobileReal/*`, `.../GraithMobileKit/*`; `gui/shared/.../GraithRemoteKit/*`.

### Implementation Notes

Build order (repo stays compiling at each checkpoint):

1. Design doc (this).
2. `GraithSessionKit` target + boundary protocols + model conveniences; build
   `gui/shared`.
3. Add `HostConnection`, `FleetModel`, `AttachRegistry`, `TerminalAttachViewModel`,
   `TerminalCoreDriving`, `TailnetReachability`, and the `Real*` client; add
   `GraithSessionKitTests`; `swift build` + `test-clt` on `gui/shared`.
4. Rewire `gui/ios`: delete the three duplicated targets, retarget the rest,
   delete `AppModel`/`HostConnection`; `swift build` on `gui/ios`.
5. Rewire `gui/macos`: `SessionStore → FleetModel`, `ApprovalMonitor → shared
   aggregation + macOS presenter`; `swift build` on `gui/macos`.

Reconciliations to preserve behaviour:

- **`HostEntry` → `Host`.** iOS's remote-only `HostEntry` (String `lastSeen`)
  becomes the canonical `Host` (`kind`-tagged, `Date` `lastSeen`, local support).
  iOS never constructs `.local`. `markSeen` moves from ISO-String to `Date`.
- **Error taxonomy.** `GraithClientError` (the app-facing vocabulary iOS uses)
  moves to shared; `ErrorMapping` continues to translate `ControlError`/
  `TransportError` into it. macOS surfaces `.localizedDescription` as before.
- **`KeychainStore.service`.** iOS used `com.graith.mobile`, macOS
  `com.graith.app`; the shared type keeps `service` a constructor parameter so
  each app preserves its Keychain namespace (no credential loss on upgrade).
- **`GraithMobileApp` composition root** constructs `HostRegistry(keychain:)`
  with no `localHost`; the shared init requires `localHost: Host`. iOS passes a
  sentinel/omits the local host from its registry view (it never lists local).

### Testing

- `GraithSessionKitTests` (new, shared): `FleetModel` cross-host merge and
  stable-ordering, per-host-error isolation, single-attach claim/release/steal,
  session grouping/roots/children/descendantCount, `HostConnection` state machine
  and the approval-subscription retry/backoff (driven against `GraithMobileMock`
  or a local mock), `SessionRef`/`HostedSession` identity.
- Existing `GraithRemoteKitTests` already cover the canonical `HostRegistry`/
  `DeviceIdentity`/`PairingCoordinator` iOS now adopts; the redundant
  `GraithMobileKitTests` are removed (their `TailnetReachability` test moves to
  shared).
- Both apps must `swift build` clean; CI's gui Swift jobs (gated to `gui/`
  changes) run the XCTest/swift-testing suites under full Xcode.

### Open questions

- Whether `FleetModel` should keep macOS's `Timer`-based 2s polling as-is or
  expose a pluggable ticker (iOS drives refresh on connect/foreground). Starting
  with the shared timer, overridable, to avoid behaviour change.
- Whether the terminal-presentation state (`fontSize`/`renderer`) stays on the
  macOS `FleetModel` extension or moves to a separate shared terminal-settings
  model. Kept macOS-side for now (it is chrome, and iOS sizes differently).
