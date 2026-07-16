# graith universal app — iOS track (issue #628)

This subtree holds the **iOS-specific** work for the universal (iOS + macOS)
app: Tasks 18–20 of `docs/plans/2026-07-07-universal-app-remote-control.md`.

Originally this subtree was **isolated** from the shared core behind a local
boundary protocol (`GraithClientAPI`) plus local copies of the RemoteKit types
(`GraithMobileKit`) and the real client (`GraithMobileReal`), so the iOS and
macOS tracks could move in parallel. **As of #1131 that boundary, the RemoteKit
copies, and the real client have been folded into `gui/shared`** — the
session/feature layer both apps bind to now lives once in
`shared/GraithSessionKit` (built on `GraithProtocol` + `GraithRemoteKit`). iOS
is a thin consumer: it keeps only genuinely UIKit-specific code.

## Modules

| Module | Owns | Depends on |
|--------|------|-----------|
| `GraithMobileApp` | `@main` SwiftUI App; composition root (builds the real `FleetModel`) | shared + the below |
| `GraithMobileUI` | SwiftUI shell: host→repo→session sidebar, create picker, approval prompt, pairing, session detail | `GraithSessionKit`, `GraithRemoteKit` (shared) |
| `GraithTerminalUIKit` | `BaseTerminalUIView` (`UIKeyInput`/`UITextInput`), on-screen key accessory row | `GraithSessionKit` (shared) |
| `GraithMobileRealTerminal` | libghostty `TerminalCoreDriving` adapter + Metal renderer (iOS) | `GraithSessionKit`, `GraithTerminalCore` |
| `GraithMobileMock` | In-memory mock `GraithHostClient` + `TerminalCoreDriving` for previews/tests | `GraithSessionKit`, `GraithRemoteKit` |

The capability boundary (`GraithHostClient`, `TerminalAttachSession`,
`HostClientFactory`), the per-host `HostConnection`, the multi-host `FleetModel`,
`AttachRegistry`, `TerminalAttachViewModel`, `TailnetReachability`, and the real
client (`RealHostClient`) all live in `shared/GraithSessionKit`. Host / pairing /
identity live in `shared/GraithRemoteKit`. See
`docs/design/2026-07-14-shared-session-feature-layer.md`.

## Tunable gesture physics (issue #1255)

The interactive terminal's touch gestures — two-finger scrollback momentum /
overscroll, the space-key drag → arrow-key navigation, and the long-press to
select — read their **feel** knobs from `TerminalGestureConfig`
(`shared/GraithSessionKit`). Defaults match the previously hard-coded values, and
`BaseTerminalUIView` loads overrides from `UserDefaults.standard` at view
creation, so the feel can be retuned without rebuilding (e.g. via `defaults
write` on a key below, or a future settings screen). Out-of-range values are
clamped.

| `UserDefaults` key | Default | Meaning |
|--------------------|---------|---------|
| `graith.gesture.scrollFriction` | `4.5` | Momentum decay rate (1/s); higher stops a flick sooner |
| `graith.gesture.scrollMomentumCutoff` | `24` | Momentum halts below this speed (points/s) |
| `graith.gesture.scrollSpringStiffness` | `220` | Overscroll spring constant; higher snaps back harder |
| `graith.gesture.scrollSpringDamping` | `26` | Overscroll spring damping; higher settles flatter |
| `graith.gesture.spaceActivationThreshold` | `22` | Points of travel before a space-drag registers a direction |
| `graith.gesture.spaceInitialRepeatDelay` | `0.5` | Seconds a direction is held before the first arrow auto-repeat |
| `graith.gesture.spaceRepeatInterval` | `0.1` | Seconds between arrow auto-repeats |
| `graith.gesture.spaceDirectionHysteresis` | `1.5` | How far the off-axis must beat the held axis before the arrow flips (≥1) |
| `graith.gesture.selectionLongPressDuration` | `0.3` | Seconds a single finger is held before selection begins |

**Physical invariants** (correct for the platform, not a matter of taste) stay as
documented constants in the owning components and are *not* configurable: the
`UIScrollView`-matching rubber-band constant (`0.55`), the spring settle epsilons,
the `0.05`s frame-time clamp, the `36`pt minimum scroll thumb, the two-finger
scroll shape, the immediate (`0`s) space-key recognizer, and the `60`fps display
link. See `TerminalGestureConfig`'s doc comment for the rationale.

## Build / validation

**Builds and runs on the iOS Simulator** with full Xcode — the live libghostty
terminal renders (see `../README.md` for build/run commands). On-device install,
signed distribution, and IME/hardware-keyboard validation remain (done in Xcode
with an Apple ID).

The eventual integration target is a single **SwiftUI multiplatform** Xcode app
(design §C.0) that links `GraithTerminalCore` + `GraithProtocolClient` (shared)
and this tree's iOS views. This SwiftPM manifest exists so the code is
organised, type-checked in Xcode, and unit-testable in isolation.

## Xcode project

Two XcodeGen specs can generate a `.xcodeproj` (both gitignored — the specs are
the source of truth):

- **`../project.yml`** (recommended) → `gui/graith.xcodeproj`: the umbrella
  project with *both* the iOS `graith` app and the macOS `GraithGUI` app.
  Generate with `make -C gui xcodeproj`.
- **`project.yml`** (this dir) → `gui/ios/graith-ios.xcodeproj`: an isolated
  iOS-only project. Generate with `xcodegen generate` from `gui/ios/`.

Both need `brew install xcodegen`. See `../README.md` for details.
