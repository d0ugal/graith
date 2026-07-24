---
weight: 100
title: "Package dependencies"
description: "Generated import relationships between the Go packages in the graith executable."
icon: "account_tree"
toc: true
draft: false
---

This graph shows the Go packages compiled into the `gr`/`graith` executable and
their direct internal imports. An arrow from package A to package B means that A
imports and uses B.

Use the toolbar, mouse wheel, trackpad, or touch gestures to zoom and pan the
graph. The full-screen view is useful when tracing longer dependency paths.

{{< package-dependencies >}}

The graph is generated for the canonical `linux/amd64` build with CGO disabled.
Standard-library and third-party dependencies are omitted, as are test-only and
integration-test packages. Runtime communication that is not represented by a
Go import—such as framed socket traffic between the client and daemon—belongs
in the main [architecture overview]({{< relref "/docs/architecture.md" >}}).

## Generation

The canonical Hugo data is committed at
`website/data/package_dependencies.json` so package-structure changes have a
reviewable diff. Regenerate it from the repository root whenever packages or
their imports change:

```bash
make package-graph
```

CI runs `make package-graph-check` and rejects stale output without modifying
the tracked file. Documentation builds consume the committed data as-is:

```bash
make docs
```

The component dependency contract is checked separately because it includes
test-only imports and ownership metadata that the visualization omits. Run
`make architecture-check` after changing package boundaries. The checked-in
policy is `internal/architecture/manifest.json`, and every package must have a
category and accountable owner. A new package is not complete until it is
classified there. The current ratchet is zero forbidden edges and zero active
exceptions; an import that crosses a forbidden boundary fails CI. Exceptions
are temporary migration records only: they must name an owner, explain the
contract being migrated, and have a future expiry date. Expired exceptions
fail the check rather than silently becoming baseline debt.

The manifest and analyzer are the enforcement source. The generated graph is
only a canonical, production-import visualization: it intentionally omits
test-only and platform-specific relationships and must never be edited by
hand. Update it with `make package-graph` when its inputs change, then run
`make package-graph-check` to verify drift. For boundary changes, run both
checks plus the affected package tests; protocol, integration, and Swift
changes require their corresponding checks as well.

For a live preview:

```bash
make docs-serve
```

## Refreshing native libghostty artifacts

Linux bundles are immutable release assets named
`libghostty-vt-linux-amd64.tar.gz` and `libghostty-vt-linux-arm64.tar.gz`.
The lock records each URL and SHA-256. CI uses
`scripts/libghostty-native.sh prepare-linux-artifact`, which verifies the
digest before extraction and then checks the manifest, target, archive member
types, static-library contents, and pkg-config metadata.

To refresh a pin, update the lock and let the trusted `Publish pinned Linux
libghostty artifacts` workflow build both targets from that reviewed commit.
It refuses to overwrite an existing release asset. Confirm both assets exist,
then record the exact URLs and SHA-256 values printed by the workflow before
merging the lock update. A mutable Actions cache is never a native-code trust
boundary; use `GRAITH_LIBGHOSTTY_SOURCE_BUILD=1` only for local comparison.
