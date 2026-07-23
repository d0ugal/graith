# Architecture policy

`manifest.json` is the enforcement source for package boundaries. Keep every
discovered package classified and owned. Add an allowed rule only when the
dependency is an intentional part of the boundary contract; otherwise the
architecture check must fail. Exceptions are migration records, not a second
baseline: each needs an owner, reason, and future expiry, and expired records
fail CI.

The package graph under `website/data/` is a generated production-import
visualization. It omits test-only and platform-specific relationships. Run
`make package-graph` only when graph inputs change and `make package-graph-check`
to verify drift; do not use the graph as policy input or edit it by hand.

Run `go test ./internal/architecture` after analyzer changes, then
`make architecture-check` and the affected package tests. Changes touching
protocol or integration boundaries also require their corresponding checks.
