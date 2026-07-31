---
title: "Design Doc: Initial Daemon Metrics"
authors: Codex
created: 2026-07-31
status: Implemented (v1)
reviewers: (none yet)
informed: obs-epic-orchestrator, obs-alloy-docs
issue: https://github.com/d0ugal/graith/issues/1960
---

# Initial Daemon Metrics

Graith exposes a small Prometheus-compatible daemon metrics surface when the
metrics runtime from the telemetry config is explicitly enabled. The first
slice covers daemon uptime/build info, aggregate session state, attach count,
launch/input/snapshot latency, status transitions, and message publish volume
without adding per-session labels or tracing spans.

## Background

The daemon owns session state, PTY and headless process lifecycles, attach
clients, message stores, and the optional telemetry runtime. The telemetry
runtime design in `docs/design/2026-07-31-telemetry-runtime.md` establishes the
user-facing opt-in contract: metrics are disabled by default, use
`[telemetry.metrics]`, and start a local HTTP scrape endpoint only when
`enabled = true`.

That runtime initially exposed only a placeholder response. Reliability and
latency work now need a minimal set of real daemon/session metrics that can be
scraped by a local collector such as Grafana Alloy.

## Problem

Without concrete metrics, operators cannot answer basic daemon questions from
a scrape target: how long the daemon has been up, how many sessions are
running or stopped, whether launches are failing or slow, whether input writes
or screen snapshots are accumulating latency, and whether message traffic is
flowing. Adding all possible PTY, renderer, worker, and queue metrics in one
change would make the surface larger and harder to keep low-cardinality.

## Goals

- Expose useful daemon/session metrics only when metrics are explicitly enabled.
- Reuse the telemetry runtime/config contract from issue #1959.
- Keep metric names stable enough for early dashboards and Alloy docs.
- Keep labels bounded and free of session IDs, names, paths, branches, prompts,
  message bodies, or user names.
- Instrument the existing daemon call sites without adding new background work
  or listener lifecycle code.

### Non-Goals

- Add tracing spans or OpenTelemetry exporters.
- Export per-session metrics with session-specific labels.
- Instrument every PTY read/write/render internals path.
- Hot-reload an active metrics runtime.
- Add iOS or macOS UI for telemetry settings.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | Users enable metrics through `config.toml` and daemon lifecycle commands. |
| iOS | Excluded | Mobile clients do not own daemon telemetry endpoints. |
| macOS | Excluded | The daemon metrics endpoint is sufficient for this first slice; app UI can consume derived observability later. |

## Proposals

### Proposal 0: Do Nothing

Keep the metrics endpoint as a placeholder. This preserves the new telemetry
runtime boundary, but it leaves the observability epic without usable daemon
signals and forces Alloy/dashboard work to guess at a future metric surface.

### Proposal 1: Daemon-Owned Prometheus Registry (Recommended)

Create a Prometheus registry only when `[telemetry.metrics].enabled` is true
and pass it into the existing metrics HTTP runtime. The runtime remains
responsible for listener lifecycle and path handling; the session manager owns
registry population and instrumentation.

The first metric families are:

| Metric | Labels |
|--------|--------|
| `graith_daemon_info` | `version`, `commit` |
| `graith_daemon_uptime_seconds` | none |
| `graith_daemon_attached_clients` | none |
| `graith_sessions` | `status`, `driver_kind` |
| `graith_session_launch_duration_seconds` | `operation`, `driver_kind`, `result` |
| `graith_session_lifecycle_transitions_total` | `from`, `to` |
| `graith_session_input_events_total` | `operation`, `result` |
| `graith_session_input_bytes_total` | `operation`, `result` |
| `graith_session_input_duration_seconds` | `operation`, `result` |
| `graith_screen_snapshot_requests_total` | `kind` |
| `graith_screen_snapshot_duration_seconds` | `kind` |
| `graith_messages_published_total` | `stream_kind`, `sender_kind` |

Aggregate gauges read session and attach state at scrape time under the
existing session-manager lock. Counters and histograms are updated at existing
behavior boundaries: process spawn attempts, session status event publication,
session input writes, screen snapshot handling, and message publish success.

Unknown or future values collapse to `unknown`, preserving bounded labels while
making unexpected inputs visible. The main trade-off is that the first slice is
coarse: it cannot distinguish one problematic session from another via labels.
That is deliberate because high-cardinality session labels would make the
metrics surface expensive and leak user/worktree details.

### Proposal 2: OpenTelemetry Metrics SDK

Use the OpenTelemetry metrics SDK and export OTLP metrics directly to a
collector. This would align with future tracing, but it adds exporter
configuration, temporality choices, network dialing, and dependency decisions
that are not needed for the local Alloy-to-Mimir path. Prometheus scrape keeps
activation local and matches the existing #1959 runtime.

## Other Notes

### References

- Issue: https://github.com/d0ugal/graith/issues/1960
- Telemetry runtime design: `docs/design/2026-07-31-telemetry-runtime.md`
- Runtime hook: `internal/daemon/telemetry.go`
- Metric definitions: `internal/daemon/metrics.go`
- Session launch hooks: `internal/daemon/session_create.go`,
  `internal/daemon/session_fork.go`, `internal/daemon/session_resume.go`, and
  `internal/daemon/orchestrator.go`
- User docs: `website/content/docs/configuration/observability.md`

### Implementation Notes

`SessionManager.metrics` is an atomic pointer. It remains nil when metrics are
disabled, when only tracing is enabled, and when the metrics listener fails to
start. Instrumentation methods load the pointer and return immediately when it
is nil.

The custom `graith_sessions` collector emits aggregate live, non-soft-deleted
session counts by status and driver kind. Empty `driver_kind` is treated as
`pty` for backward compatibility with older state.

### Testing

Coverage proves disabled telemetry starts no listener and creates no metrics
registry, enabled metrics exposes the expected Prometheus metric names, tracing
alone does not create a metrics listener or registry, listener startup failure
does not retain runtime state, reload still rejects telemetry changes, and
synthetic high-cardinality session/message values do not appear in metric
labels.
