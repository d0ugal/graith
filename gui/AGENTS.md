# GUI contributor instructions

These instructions apply to `gui/` in addition to the repository root
`AGENTS.md`.

The GUI has a shared Swift package (`gui/shared`) consumed by the macOS and iOS
apps. Put protocol transport, host/session models, feature view models, and
cross-platform behavior in shared code when possible; keep platform code for
presentation and platform integrations.

## Build and test

```bash
make -C gui shared-build
make -C gui shared-test
make -C gui macos-build
make -C gui ios-app
make -C gui build
```

Shared build/test and app-build targets are sandbox-safe. iOS XCTest, simulator,
and run targets call `xcodebuild`/`simctl` and need a normal terminal or a graith
session with sandboxing disabled. Full Xcode is required for iOS work.
`gui/project.yml` is the source for the generated Xcode project; do not commit
the generated project.

## Protocol changes

Go is the source of truth for the complete wire-shape manifest. Required remote
messages are modelled by hand in
`shared/Sources/GraithProtocol/Messages.swift` and checked by
`ManifestConformanceTests`.

Do not edit
`shared/Tests/GraithProtocolTests/Fixtures/protocol_manifest.json` manually.
Regenerate it from the repository root:

```bash
go test ./internal/protocol -run TestManifestUpToDate -update
```

From the repository root, read `internal/protocol/AGENTS.md` when changing Swift
protocol models.

## Capabilities and parity

`internal/capabilities/capabilities.json` is the source of truth for advertised
support. Shared feature wiring is registered with compile anchors in
`sharedAffordances()` inside `CapabilityConformanceTests.swift`.

When support changes:

1. Implement/remove the behavior and verify the actual app views.
2. Update the manifest and shared affordance or a reviewed exception.
3. Run `go test ./internal/capabilities -update` from the repository root.
4. Run shared and affected app tests.

Do not hand-edit the generated capability fixture. Prefer iOS/macOS parity in
the shared layer; declare a `knownDivergences` entry only when the difference is
intentional and reviewed.

From the repository root, see `internal/capabilities/AGENTS.md`,
`gui/README.md`, and `docs/design/2026-07-14-shared-session-feature-layer.md`
for details.
