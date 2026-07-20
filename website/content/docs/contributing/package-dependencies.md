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

The documentation build runs `go list` against `cmd/graith`, writes temporary
Hugo data, and renders that data as Mermaid. The data file is ignored by Git and
is recreated from the commit being built; there is no generated diagram to
commit or keep in sync.

Build the documentation locally from the repository root:

```bash
make docs
```

For a live preview that regenerates the graph once before starting Hugo:

```bash
make docs-serve
```
