# NEEDS iOS VALIDATION (issue #628, iOS track — Tasks 18–20)

This file tracks everything a human must build and test in **Xcode on a Mac**
(with an iOS Simulator / device and code signing) that **cannot be verified in
the graith worktree** (macOS host, no iOS SDK/simulator, no signing).

The iOS code lives in `gui/ios/` as an isolated SwiftPM package and depends on a
documented boundary protocol (`GraithClientAPI`) rather than on the shared
`GraithProtocolClient` / `GraithTerminalCore` (macOS track, Phases 2–3). Items
marked **INTEGRATION** are blocked on those shared packages landing.

Legend: ☐ = todo for the human validator, ✔ = done, ⚠ = known drift / risk.

---

## Build & project setup

- ✔ A launchable iOS app target now exists: `GraithMobileApp` (@main SwiftUI App
  → shared `RootView`). Built for the `iphonesimulator` SDK and bundled into an
  ad-hoc-signed `.app` by `build-ios-app.sh` (no `.xcodeproj` — no xcodegen/tuist
  here); `make run` installs + launches it. Verified launching on an iPhone 17
  Pro sim (iOS 26.5) — `RootView` renders. Identity uses the real Keychain when
  signed with `Resources/GraithMobile.entitlements`, else an in-memory fallback.
- ☐ For a **distributable / Keychain-backed** build, create the universal SwiftUI
  Xcode app (design §C.0: SwiftUI multiplatform, **not** Mac Catalyst) with a
  real signing identity + entitlements, that links `GraithTerminalCore` +
  `GraithProtocol` (shared) and this tree's `GraithMobileUI` /
  `GraithTerminalUIKit`. The SPM-bundle path above is the dev/simulator route.
- ☐ `gui/ios` is a standalone SwiftPM package for isolation. It cannot compile
  here (no iOS SDK). Open it in Xcode and confirm all five targets type-check
  on iOS 16+ and macOS 14+.
- ☐ Wire `.package(path: "../shared")` to the shared core's `GraithProtocol` +
  `GraithTerminalCore` products (now exposed by `gui/shared` after the
  shared/macos/ios split), then delete the mock adapters where the real types
  are available. This is the main remaining iOS integration step.
- ⚠ **INTEGRATION** `GraithClientAPI.DeviceKeySigner` is defined here for now;
  agreed with the macOS agent to re-home it into `GraithProtocol` at
  integration and bridge with a `typealias`.

## Crypto / device identity (Task 18)

- ☐ `DeviceIdentity` uses CryptoKit `Curve25519.Signing`. Verify the key is
  created once and persisted in the Keychain (see Keychain items below), and
  that `publicKeyRaw()` returns the 32-byte raw representation and `sign(_:)`
  a 64-byte raw signature.
- ⚠ **OPEN QUESTION for design-628 (daemon PoP verifier):** confirm the daemon
  verifies **raw** ed25519 (32-byte pubkey, 64-byte signature, base64 in JSON),
  **not** DER/SPKI-wrapped. If the daemon expects DER, `DeviceIdentity` must
  switch to `.x963`/DER encoding. Tracked in code comments in
  `GraithMobileKit/DeviceIdentity.swift`.
- ☐ Consider Secure Enclave for the private key. CryptoKit's
  `SecureEnclave.P256` is EC P-256, **not** ed25519 — if the daemon requires
  ed25519 the key cannot live in the Secure Enclave; document the trade-off and
  keep the ed25519 key as a Keychain item with
  `kSecAttrAccessibleWhenUnlockedThisDeviceOnly`.

## Keychain (Task 18)

- ☐ Verify `KeychainStore` round-trips on device (simulator Keychain differs).
  Items: device private key, and per-host client tokens.
- ☐ Confirm the access group / `kSecAttrAccessible` policy is correct for a
  universal app and that tokens do **not** sync to iCloud Keychain
  (`kSecAttrSynchronizable = false`).
- ☐ Confirm token deletion on host-remove and on a `pair_revoke`-driven wipe.

## Tailnet reachability (Task 18, design §C.5)

- ☐ v1 relies on the **official Tailscale iOS app** for the tunnel. Verify the
  "not connected to tailnet" state renders and clears correctly when Tailscale
  is toggled. `NWPathMonitor` + a probe connect is used; validate the probe
  does not produce false "unreachable" on cellular/Wi-Fi transitions.
- ☐ Validate the optional Shortcuts hand-off to bring Tailscale up (if kept).
- ☐ MagicDNS resolution of `graith-<host>.ts.net` works app-wide once the
  tunnel is up (no in-app resolver).

## Pairing flow (Task 18, design §B.2)

- ☐ End-to-end: fresh device → enter MagicDNS host → `pair_request` → local
  human runs `gr pair approve <id>` → app stores token + SPKI pin + profile →
  reconnect does PoP challenge/response. **INTEGRATION** (needs the daemon
  remote listener from Phase 1 Task 9 + `GraithProtocolClient`).
- ☐ Confirm the SPKI fingerprint shown in the app matches what `gr pair` prints
  locally (TOFU confirmation).
- ☐ `pair_request` rejected / rate-limited paths render sensibly.

## Read + approvals milestone (Task 19)

- ☐ From a phone on the tailnet, see all sessions across **≥2 daemons** and
  approve/deny a real tool call **without kicking a desktop attach**. This is
  the Task 19 acceptance criterion. **INTEGRATION** (needs `approval_subscribe`
  handler — design-628 Task 8 — and the remote listener).
- ☐ Multi-host aggregated sidebar: host → repo → session tree renders; per-host
  connection state and errors are visible.
- ☐ `repo_list`-backed create picker: pick a repo, create a session remotely,
  see it appear.
- ☐ Log tail + `screen_snapshot` peek render without attaching.

## Full interactive attach (Task 20)

- ☐ `BaseTerminalUIView`: hardware keyboard (`pressesBegan/Ended`), IME marked
  text (US English + **Japanese** compose/commit/cancel), on-screen key row
  (esc/ctrl/alt/arrows/tab + sticky modifiers), selection + scroll gestures.
- ☐ Resize via control message resizes the remote PTY; `vim`/`less` reflow.
- ☐ Copy/paste incl. bracketed-paste framing.
- ☐ Reattach after data-channel EOF; detached-while-backgrounded UX (iOS
  suspends sockets when backgrounded) — verify reconnect on foreground.
- ☐ iPad split-view / two windows must **not** show the same session twice
  (single-attach guard) — verify the guard blocks the second pane.
- ⚠ **INTEGRATION** the Metal renderer (`MetalTerminalRenderer`) is in the
  shared `GraithTerminalCore` and must be de-AppKit'd for iOS. The UIKit view
  here drives a `TerminalCoreDriving` seam + a `TerminalRenderer` seam; wiring
  the real renderer + the `GhosttyTerminalState: TerminalCoreDriving` adapter is
  an integration step (see the adapter table below).
- ⚠ **Double-handling risk (on-device):** printable hardware-keyboard keys can
  arrive via BOTH `pressesBegan` and `UIKeyInput.insertText`. `UIKeyMapping`
  returns nil for plain characters (so they flow through text input, preserving
  IME/layout) and only returns a stroke for special keys and modifier chords —
  but confirm on-device there's no double-emit of Return/Tab/arrows, and adjust
  the split if a key double-fires.
- ☐ Verify `UITextInput` conformance drives real IME correctly on-device — the
  implementation exposes only the composition buffer as the document (terminal
  has no persistent editable text) and commits on `unmarkText`. Geometry methods
  (`caretRect`/`selectionRects`/`firstRect`) are stubs since the grid draws its
  own cursor; confirm the system loupe/autocorrect bar behave acceptably or
  disable them.
- ☐ `UIMenuController.showMenu` (copy on selection) is deprecated on iOS 16+;
  confirm the `UIEditMenuInteraction` path on iOS 16+ if the deprecation bites.
- ☐ Verify the display-link 60 Hz redraw + `renderIfNeeded` coalescing matches
  gui-poc's model once the real Metal renderer is wired.

## Integration with the shared package (macOS track — LANDED)

`GraithProtocolClient` + `GraithTerminalCore` are implemented on branch
`d0ugal/graith/apple-macos` (commit c32d9b5, 25 tests green) and exposed as
library products. The iOS tree consumes them through the `GraithClientAPI`
boundary via **thin adapters** written at merge time (they can't compile until
a `.package(path: "..")` dependency is added). Adapter mapping:

| Boundary type (this tree) | Shared type (macOS track) | Adapter notes |
|---|---|---|
| `GraithHostClient` | `actor GraithProtocolClient` | wrap; `approvalStream()`→`subscribeApprovals()`, `attach(sessionID:)`→`attach(sessionID:cols:rows:)` |
| `TerminalAttachSession` | `AttachSession` | wrap `output`/`send`/`resize`/`detach`; ignore `events`/`session`/`close` or surface as needed |
| `GraithPairing` | `client.pairRequest(deviceLabel:)` | construct a pre-auth client, call `pairRequest`, map `PairResponseMsg`→`PairResponse` |
| `HostClientFactory` | `GraithProtocolClient(transport:profile:clientID:token:signer:)` | build client per host from `HostEntry` + `HostCredentials` + `DeviceIdentity` |
| `TerminalCoreDriving` | `GhosttyTerminalState` | `extension GhosttyTerminalState: TerminalCoreDriving` mapping `TerminalKey`/`TerminalModifiers`→ported `KeyMapping`/ghostty enums; `feedOutput`→`write`, `encode`→`encodeKey`, scroll/selection 1:1 |
| `TerminalRenderer` | `MetalTerminalRenderer` (iOS-ported) | conform the ported renderer to the seam (`cellSize`/`layout`/`renderIfNeeded`/`setNeedsRender`) |

- ☐ Add the path dependency, write the 6 adapters above, delete the mock
  factory/pairing where the real ones are wired.
- ⚠ **Semantics to confirm with apple-macos** (asked on `apple-track-628`):
  (1) `AttachSession.output` finishes on detach/EOF/kick (drives reattach);
  (2) `subscribeApprovals()` emits an initial snapshot on subscribe;
  (3) attach initial cols/rows vs a follow-up `resize()` (the UIView sizes in
  `layoutSubviews`, post-attach).
- **Confirmed by apple-macos** (`apple-track-628`): (1) `AttachSession.output`
  finishes on detach/EOF/kick, and a kick surfaces a `detached` reason on
  `AttachSession.events` — the `AttachSessionAdapter` should drain `events` and
  feed the reason into `TerminalAttachViewModel.phase = .detached(reason)`.
  (2) `subscribeApprovals()` relays daemon pushes only; the daemon itself sends
  an `approval_notification` snapshot on subscribe (design-628 Task 8), so no
  client-side priming is needed. (3) The adapter passes a best-guess `80x24` to
  `attach(cols:rows:)`, then `layoutSubviews` sends the real `resize()`.

## libghostty-vt (Task 13, upstream dependency)

- ⚠ `gui/Libraries/libghostty-vt.a` is a **stale, unpinned, macOS-arm64-only**
  archive (per design-628). It cannot link into an iOS target. A human must
  build the SHA-pinned `.xcframework` (macOS + iOS device + iOS simulator) per
  Task 13 before either front-end ships. Blocks all on-device terminal tests.

## POC / core drift found while building

- ⚠ The old `gui/macos/Sources/GraithGUI/Session.swift` model is **out of date** vs
  current `internal/protocol/messages.go` `SessionInfo` (missing PR/CI, scenario
  id/name, includes, yolo, migrated_from, config_stale, exit_signal). The iOS
  `GraithClientAPI.SessionInfo` is written fresh against the current wire
  contract and includes them. The macOS `Session.swift` should be reconciled
  when it moves into `GraithTerminalCore`.
- ⚠ Frame constants verified against `internal/protocol/frame.go`: channels
  `0x00` control / `0x01` data / `0x02` MCP, 5-byte header (1 channel byte +
  big-endian uint32 length), `MaxPayload = 4 MiB`. The transport client
  (macOS track) must match these.
