---
title: "Design Doc: Alloy Config Generation"
authors: Codex
created: 2026-08-04
status: Implemented
reviewers: (none yet)
informed: obs-epic-orchestrator
issue: https://github.com/d0ugal/graith/issues/2042
---

# Alloy Config Generation

Graith adds a local CLI command that renders Grafana Alloy configuration from
the same resolved config and path model used by the daemon. This keeps the
observability setup deterministic across Linux, macOS, profiles, and custom
data directories without enabling telemetry or shipping data by itself.

## Background

The telemetry runtime design established opt-in metrics and tracing settings,
and the observability docs already describe how Alloy can collect daemon logs,
scrape metrics, and receive traces. The current documentation still contains a
static Alloy file. Static examples are fragile because Graith paths vary by OS,
`GRAITH_PROFILE`, and `data_dir`, while tracing and metrics endpoints may also
be configured away from their defaults.

## Problem

Users have to copy an Alloy example, replace paths and endpoints by hand, and
remember that session scrollback logs are sensitive. A wrong path silently drops
logs. A stale metrics path breaks scraping. Including session log globs by
default can ship raw terminal output off the machine.

## Goals

- Render Alloy config for daemon logs, metrics, and traces.
- Use Graith's resolved path model, including Linux, macOS, profiles, and
  custom `data_dir`.
- Use current telemetry metrics bind/path and tracing protocol/endpoint values.
- Keep session scrollback logs out of generated output by default.
- Keep backend URLs and credentials as Alloy environment references or obvious
  placeholders, never values read from Graith config.
- Make the renderer deterministic and covered by golden tests.

### Non-Goals

- Install, bundle, start, stop, or supervise Alloy.
- Enable Graith metrics or tracing.
- Include session log collection by default.
- Validate a user's remote Grafana Cloud, Loki, Mimir, or Tempo credentials.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | The command belongs beside `gr config show` because it renders local configuration state. |
| iOS | Excluded | Mobile clients do not own the local daemon data directory or Alloy process. |
| macOS | Targeted through CLI | The macOS daemon uses the same CLI config and path resolver; no app UI is required for this slice. |

## Proposals

### Proposal 0: Do Nothing

Keep the static Alloy example in the docs. This preserves the current behavior,
but leaves path and endpoint substitution manual and keeps session log globs in
the easiest copy/paste path.

### Proposal 1: `gr config alloy` Renderer (Recommended)

Add `gr config alloy` with a `--signals` comma list. The command loads the
effective local Graith config, resolves paths, applies a configured `data_dir`
the same way the root command does, and writes Alloy text to stdout. The render
logic is pure: tests pass a `config.Config`, `config.Paths`, and selected
signals, then compare exact golden output.

Daemon logs use `<data_dir>/daemon.log` and `<data_dir>/daemon.stderr.log`.
Session scrollback logs under `<data_dir>/logs/*.log` are deliberately omitted.
Metrics use `telemetry.metrics.BindAddressOrDefault()` and
`PathOrDefault()`. Traces use `telemetry.tracing.ProtocolOrDefault()` and the
configured endpoint when that endpoint is a concrete loopback listener for the
local Alloy process. A configured remote tracing exporter target is rejected
with guidance because Alloy cannot bind a remote collector address. When no
endpoint is configured yet, the generator renders the standard local OTLP
endpoint for the selected protocol and comments how to make Graith match it
before enabling tracing.

Backend destinations remain environment-driven. The generated Loki, Mimir, and
Tempo blocks use `sys.env(...)` for URLs, usernames, and tokens, so Graith never
reads or inlines backend secrets.

### Proposal 2: Keep the Renderer in Documentation Tooling

A docs-only generator could produce a better static snippet during site builds,
but it would still not reflect a user's actual profile, OS, or `data_dir`. It
also would not help operators inspect the config they should run locally.

## Other Notes

### References

- Telemetry runtime: `docs/design/2026-07-31-telemetry-runtime.md`
- Initial daemon metrics: `docs/design/2026-07-31-initial-daemon-metrics.md`
- CLI config commands: `internal/cli/config.go`
- Config path resolver: `internal/config/paths.go`
- Observability docs: `website/content/docs/configuration/observability.md`

### Testing

Golden tests cover representative Linux, macOS, profile/custom-path output, and
the gRPC TLS receiver branch. Focused unit tests cover signal parsing,
command-level `data_dir` and profile resolution, wildcard metrics scrape
targets, local tracing endpoint validation, disabled telemetry comments, and
the default exclusion of session log globs.
