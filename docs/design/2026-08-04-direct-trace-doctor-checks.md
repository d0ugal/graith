---
title: "Design Doc: Direct Trace Doctor Checks"
authors: Codex
created: 2026-08-04
status: Implemented
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/2041
---

# Direct Trace Doctor Checks

Graith should make direct OTLP trace export easier to troubleshoot without
turning `gr doctor` into another collector or a backend probe. The shipped
diagnostic validates local config and credential-source readiness, redacts
header values, and leaves remote reachability to daemon logs and backend tools.

## Background

Tracing is already opt-in under `[telemetry.tracing]`. The daemon exports spans
through the OpenTelemetry OTLP gRPC or HTTP/protobuf exporters once tracing is
enabled and the daemon restarts. Header values may come from inline config,
environment variables, or owner-only token files.

## Problem

Direct trace export is much simpler than direct metrics or log shipping, but
misconfigured endpoints and missing credentials are still easy to miss. Before
this change, users had to infer those problems from daemon startup or exporter
log messages.

## Goals

- Add a small, explicit `gr doctor --tracing` diagnostic.
- Reuse the same validation rules that the daemon startup path uses.
- Check environment-variable and token-file header sources without printing
  values.
- Avoid sending test spans or calling remote observability backends.

### Non-Goals

- No Tempo or Grafana Cloud reachability probe.
- No test span emission.
- No new telemetry config keys.
- No changes to metrics or log collection.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | `gr doctor` is the troubleshooting surface for local config. |
| iOS | Excluded | Mobile clients do not manage daemon trace export. |
| macOS | Excluded | The native app can rely on CLI diagnostics for this daemon-owned setting. |

## Proposals

### Proposal 0: Do Nothing

Users keep checking tracing by editing config, restarting, and then looking for
daemon log messages. This preserves the smallest CLI surface, but it makes the
recommended direct trace path harder to debug than the generated Alloy path.

### Proposal 1: Local Config Doctor (Recommended)

Add `gr doctor --tracing`. The check reports enabled state, validates endpoint
syntax and protocol-specific shape, resolves header sources using the current
config directory, and redacts header values from all output. Missing header
sources fail when tracing is enabled and warn when tracing is disabled.

### Proposal 2: Send A Test Span

Doctor could create a one-off exporter and send a diagnostic span. That would
prove backend reachability, but it would also make `gr doctor` ship telemetry
from the user's machine, require clearer privacy controls, and potentially leak
environment-specific labels. That is outside the refined scope.

## Other Notes

### References

- `internal/cli/doctor_tracing.go`
- `internal/config/telemetry.go`
- `website/content/docs/configuration/observability.md`

### Testing

Unit coverage verifies successful diagnostics, disabled tracing, missing
credential sources, and redaction of inline, environment, file, and endpoint
secret material.
