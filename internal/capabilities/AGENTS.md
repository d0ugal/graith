# Capability manifest contributor instructions

These instructions apply to `internal/capabilities/` in addition to the
repository root `AGENTS.md`.

`capabilities.json` is the hand-maintained source of truth for CLI, iOS, and
macOS capability support. Generated documentation and Swift fixtures are
derived from it; never hand-edit those outputs.

## Changing a capability

1. Add or update the capability in `capabilities.json`, choosing each frontend
   state deliberately: `supported` is shipped, `planned` is targeted but not
   shipped, and `n/a` is deliberately excluded. An `n/a` cell or intentional
   GUI divergence requires `platform_decision` pointing to the feature design's
   exact `## Platform support` section.
2. Decide CLI, iOS, and macOS scope in the design doc before implementation.
   Keep parity across targeted GUI surfaces; do not use `n/a` for a temporary
   implementation gap.
3. If a GUI capability is implemented in the shared feature layer, update
   `sharedAffordances()` in
   `gui/shared/Tests/GraithSessionKitTests/CapabilityConformanceTests.swift`
   with a compile anchor to the real symbol.
4. Use `viewOnlyCapabilities` only for reviewed UI-only support with no shared
   model affordance. Platform exclusions belong only in the manifest and design
   doc, not in a second Swift exception list.
5. Confirm the actual iOS and macOS views expose what the manifest claims;
   shared-model conformance cannot prove view wiring.
6. Regenerate both artifacts and run tests:

   ```bash
   go test ./internal/capabilities -update
   go test ./internal/capabilities
   ```

The update command rewrites the marker-delimited capability table in
`website/content/docs/capabilities.md` and the generated GUI fixture at
`gui/shared/Tests/GraithSessionKitTests/Fixtures/capability_manifest.json`.
Commit both with the source manifest.

Run `make -C gui shared-test` when GUI states or affordances change. Shared
Swift tests are safe inside the graith sandbox when full Xcode is available.
