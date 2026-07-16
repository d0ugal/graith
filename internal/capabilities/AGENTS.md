# Capability manifest contributor instructions

These instructions apply to `internal/capabilities/` in addition to the
repository root `AGENTS.md`.

`capabilities.json` is the hand-maintained source of truth for CLI, iOS, and
macOS capability support. Generated documentation and Swift fixtures are
derived from it; never hand-edit those outputs.

## Changing a capability

1. Add or update the capability in `capabilities.json`, choosing each frontend
   state deliberately (`supported`, `planned`, or `n/a`).
2. If a GUI capability is implemented in the shared feature layer, update
   `sharedAffordances()` in
   `gui/shared/Tests/GraithSessionKitTests/CapabilityConformanceTests.swift`
   with a compile anchor to the real symbol.
3. Use `viewOnlyCapabilities` only for reviewed UI-only support with no shared
   model affordance. Use `knownDivergences` only for an intentional iOS/macOS
   difference. Both lists are checked for stale entries.
4. Confirm the actual iOS and macOS views expose what the manifest claims;
   shared-model conformance cannot prove view wiring.
5. Regenerate both artifacts and run tests:

   ```bash
   go test ./internal/capabilities -update
   go test ./internal/capabilities
   ```

The update command rewrites the marker-delimited capability table in
`website/content/docs/capabilities.md` and the generated GUI fixture at
`gui/shared/Tests/GraithSessionKitTests/Fixtures/capability_manifest.json`.
Commit both with the source manifest.

Run `make -C gui shared-test` when GUI states or affordances change. This needs
to run outside a sandbox that blocks Xcode tooling.
