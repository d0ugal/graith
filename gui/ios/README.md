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
