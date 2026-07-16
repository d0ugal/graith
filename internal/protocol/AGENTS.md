# Protocol contributor instructions

These instructions apply to `internal/protocol/` in addition to the repository
root `AGENTS.md`.

## Wire model

Frames have a one-byte channel followed by a four-byte big-endian length:

- `0x00`: JSON control messages.
- `0x01`: raw PTY data.
- `0x02`: MCP proxy traffic.

Control messages use `{"type":"...","payload":{...}}`. Go wire structs live in
`messages.go`; the remote Swift subset lives in
`gui/shared/Sources/GraithProtocol/Messages.swift`.

## Changing messages

For every added or changed exported wire struct:

1. Make the JSON shape and optionality explicit in `messages.go`.
2. Ensure every exported wire struct is registered in `manifest.go`; add new
   types to `registeredTypes`.
3. Classify it in `swiftAnnotations`:
   - `required`: Swift models it now;
   - `planned`: client-relevant but not implemented in Swift yet;
   - `na`: intentionally local/internal and not needed by Swift.
4. For `required`, set the correct Swift type and update Swift decoding/models.
5. Update the handler/client call sites. A new daemon handler case also requires
   a `remoteMessagePolicy` row; see `../daemon/AGENTS.md`.
6. Regenerate the fixture:

   ```bash
   go test ./internal/protocol -run TestManifestUpToDate -update
   ```

7. Run:

   ```bash
   go test ./internal/protocol
   go test -race ./internal/protocol
   ```

   Run relevant integration tests for routing or lifecycle changes. If the
   Swift-required surface changed, run `make -C gui shared-test`; shared Swift
   tests are safe inside the graith sandbox when full Xcode is available.

Never hand-edit
`gui/shared/Tests/GraithProtocolTests/Fixtures/protocol_manifest.json`.
The manifest test suite intentionally fails closed: registry completeness
catches missing, stale, and unclassified types, while manifest generation
catches duplicates and unsupported shapes.

The manifest describes conformance, not identical Go and Swift structs: Swift
may model a subset of fields, but it must decode synthesized required shapes
with compatible types and optionality.

See `docs/design/2026-07-14-protocol-conformance-design.md` for rationale.
