---
title: "Design Doc: Telemetry Runtime"
authors: Codex
created: 2026-07-31
status: Implemented (v1)
reviewers: (none yet)
informed: obs-epic-orchestrator, obs-alloy-docs
issue: https://github.com/d0ugal/graith/issues/1959
---

# Telemetry Runtime

Graith gains an explicit, daemon-owned telemetry configuration surface for
optional metrics and tracing. The first slice keeps both disabled by default,
starts no telemetry network endpoint unless the matching feature is enabled,
and makes telemetry runtime changes require a daemon restart.

## Background

The daemon is the long-lived process that owns sessions, PTYs, worktrees,
state, process-level logs, and reloadable configuration. Observability for the
daemon and session lifecycle needs stable configuration before later work can
add concrete metrics, trace exporters, and instrumentation.

Logs already remain local through the existing daemon and session log files.
Metrics and tracing are different because they can add listeners, exporters, or
outbound network dials. They need an explicit opt-in boundary that later
instrumentation can reuse without changing the user-facing contract.

## Problem

There is no first-class place to configure daemon metrics or tracing. Without a
small shared contract, later observability work would either hard-code
endpoints, depend on ambient OpenTelemetry environment variables, or add
multiple incompatible knobs. That would make it too easy for a daemon to expose
a listener or ship telemetry unexpectedly.

## Goals

- Keep metrics and tracing disabled by default.
- Require explicit config before starting any metrics listener, trace exporter,
  telemetry network endpoint, or telemetry dial.
- Validate metrics bind addresses and trace export endpoints with clear errors.
- Preserve ordinary graith operation with no Grafana, Alloy, Mimir, Loki, Tempo,
  or OpenTelemetry collector dependency.
- Provide stable config names for follow-up metrics, tracing, and Alloy docs.
- Make reload behavior explicit and safe.

### Non-Goals

- Add daemon/session metric instruments.
- Configure logs for Loki shipping.
- Add iOS or macOS UI for telemetry settings.
- Support hot-reloading telemetry runtime endpoints in this slice.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | Users configure telemetry through `config.toml` and daemon lifecycle commands. |
| iOS | Excluded | Mobile clients do not own the daemon runtime or telemetry endpoints. |
| macOS | Excluded | The app can benefit from daemon telemetry later, but the first contract is daemon configuration only. |

## Proposals

### Proposal 0: Do Nothing

Leave observability to ad hoc code in each future instrumentation task. This
keeps the current daemon behavior unchanged, but it does not create a clear
opt-in boundary and makes it likely that metrics, tracing, and docs drift apart.

### Proposal 1: Static Daemon-Owned Telemetry Runtime (Recommended)

Add a `[telemetry]` config block with independent `[telemetry.metrics]` and
`[telemetry.tracing]` sub-blocks. The daemon starts a telemetry runtime during
startup only when at least one sub-feature is enabled. Disabled telemetry
returns no runtime, starts no listener, creates no exporter, and performs no
telemetry dial.

Metrics use a local Prometheus scrape endpoint:

```toml
[telemetry.metrics]
enabled = false
bind_address = "127.0.0.1:4824"
path = "/metrics"
```

Tracing defines the OTLP exporter contract for later instrumentation:

```toml
[telemetry.tracing]
enabled = false
endpoint = ""
protocol = "grpc"
insecure = false
timeout = "10s"
sampling_ratio = 1.0
queue_size = 2048
max_export_batch_size = 512
schedule_delay = "5s"
compression = "none"

[telemetry.tracing.headers]
```

`protocol` is deliberately small: `grpc` endpoints are `host:port`, and
`http/protobuf` endpoints are full `http` or `https` trace URLs that include
the OTLP traces path and are used verbatim by later exporter work. The
`insecure` setting applies to `grpc`; `http/protobuf` uses the URL scheme.
Headers are accepted as a map so credentials can be provided without embedding
them in URLs; config rendering redacts their values.
The trace provider uses parent-based ratio sampling, bounded batch processing,
and optional gzip compression for `http/protobuf` export. Defaults preserve the
initial all-root-spans behavior while matching OpenTelemetry's bounded batcher
defaults.

Telemetry runtime changes are restart-only. `gr daemon reload` and the config
watcher compare the old and new active telemetry runtime shape before mutating
the live config. Enabling or disabling metrics or tracing, or changing settings
for an enabled sub-runtime, returns a restart-required error and leaves the
running generation untouched. Changes to disabled sub-runtime values can publish
without starting listeners or exporters; those values affect runtime only after
the feature is enabled and the daemon restarts.

The main trade-off is that changing telemetry endpoints requires a daemon
restart. That is acceptable for the first slice because it avoids partial
exporter/listener replacement semantics, keeps reload failure modes simple, and
does not block ordinary reloadable settings from remaining hot-reloadable.

### Proposal 2: Hot-Reload Metrics and Tracing

Allow reload to replace the metrics listener and tracing exporter in place.
This is more convenient, but it adds a two-phase runtime replacement problem:
the daemon must stop or drain old exporters, bind or connect new endpoints,
decide rollback semantics when one sub-runtime succeeds and another fails, and
avoid losing spans during the swap. This is better deferred until actual
metrics and tracing exporters exist.

## Other Notes

### References

- Issue: https://github.com/d0ugal/graith/issues/1959
- Config: `internal/config/config.go`
- Embedded defaults: `internal/config/default_config.toml`
- Runtime: `internal/daemon/telemetry.go`
- Reload guard: `internal/daemon/session_config.go`

### Implementation Notes

The tracing runtime installs an OpenTelemetry SDK tracer provider and OTLP
exporter only when `[telemetry.tracing] enabled = true`. It always passes the
configured endpoint to the exporter and does not rely on `OTEL_*` environment
variables for exporter endpoint, headers, TLS certificates, compression,
timeout, sampler, batcher, or proxy settings, so enabling tracing without an
endpoint fails validation rather than falling back to an ambient exporter.
Telemetry values are validated even when the matching sub-runtime is disabled,
except that the tracing endpoint is required only when tracing is enabled; this
catches invalid saved settings before the restart that activates them.

The metrics runtime starts an HTTP server only when
`telemetry.metrics.enabled` is true. Tests may call the runtime directly with
port `0` to get an ephemeral port, but user configuration rejects port `0` so a
saved config never depends on an unknown scrape target. The metrics path is
handled as a literal request path rather than a `net/http.ServeMux` pattern, so
valid URL paths cannot be reinterpreted as wildcard patterns or panic during
daemon startup. If the configured metrics address cannot bind, daemon startup
fails clearly because the user explicitly opted into that listener.

### Alternatives considered

Using OpenTelemetry environment variables as the primary config was rejected
because it would make telemetry activation depend on inherited environment
rather than explicit graith config. A single top-level `telemetry.enabled` flag
was rejected because metrics and tracing need to be independently enabled for
local development and staged rollout.

### Testing

Coverage should prove default config keeps telemetry disabled, valid telemetry
config loads, invalid bind addresses and endpoints fail clearly, tracing header
values are redacted, disabled daemon telemetry starts no listeners, enabled
metrics starts a scrape endpoint, metrics paths are served literally,
tracing-only config starts no listener in this slice, metrics bind failures fail
daemon runtime startup, runtime-affecting telemetry changes are rejected during
config reload without publishing the new config, and inactive telemetry edits
can still publish without starting runtime.
