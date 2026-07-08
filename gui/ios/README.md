# graith universal app — iOS track (issue #628)

This subtree holds the **iOS-specific** work for the universal (iOS + macOS)
app: Tasks 18–20 of `docs/plans/2026-07-07-universal-app-remote-control.md`.

It is deliberately **isolated** from the shared core (`gui/shared`) and the
macOS app (`gui/macos`, owned by the macOS track, Phases 2–3) so the two
tracks can move in parallel without merge conflicts. Everything here depends on
a small, documented **boundary protocol** (`GraithClientAPI`) rather than on the
macOS agent's concrete `GraithProtocolClient` / `GraithTerminalCore`. When those
shared packages land, we replace the mock adapters in `GraithMobileMock` with
thin adapters onto the real types — the UI and app logic above the boundary do
not change.

## Why a boundary protocol

Per the plan (Phase 4/5 depend on the shared package from Phase 2):

> Until the shared API stabilizes, build the iOS-specific, independent pieces
> first ... against a small documented protocol interface you agree with the
> macOS agent; integrate with the real shared package as it lands.

`GraithClientAPI` is that interface. It is transport-agnostic and mirrors the
graith framed-protocol message set (`internal/protocol/messages.go`) for exactly
the subset the mobile app needs. See `Sources/GraithClientAPI/Boundary.swift`.

## Modules

| Module | Owns | Depends on |
|--------|------|-----------|
| `GraithClientAPI` | The boundary contract: `GraithHostClient`, `TerminalAttachSession`, `GraithPairing`, wire-message Codables, `TerminalCoreDriving` | — |
| `GraithMobileKit` | `HostRegistry`, Keychain store, ed25519 `DeviceIdentity`, pairing flow, tailnet reachability, multi-host aggregation store | `GraithClientAPI` |
| `GraithMobileUI` | SwiftUI universal shell: host→repo→session sidebar, create picker, approval prompt, log tail, screen peek | `GraithClientAPI`, `GraithMobileKit` |
| `GraithTerminalUIKit` | Task 20: `BaseTerminalUIView` (`UIKeyInput`/`UITextInput`), on-screen key accessory row, attach view-model | `GraithClientAPI` |
| `GraithMobileMock` | In-memory mock `GraithHostClient` + `TerminalCoreDriving` for SwiftUI previews and tests | `GraithClientAPI` |

## Build / validation

**Builds and runs on the iOS Simulator** with full Xcode — the live libghostty
terminal renders (see `../README.md` for build/run commands). On-device install,
signed distribution, and IME/hardware-keyboard validation remain (done in Xcode
with an Apple ID).

The eventual integration target is a single **SwiftUI multiplatform** Xcode app
(design §C.0) that links `GraithTerminalCore` + `GraithProtocolClient` (shared)
and this tree's iOS views. This SwiftPM manifest exists so the code is
organised, type-checked in Xcode, and unit-testable in isolation.
