---
title: "Design Doc: Collector-Based Metrics Shipping"
authors: Codex
created: 2026-08-04
status: Accepted
reviewers: (none yet)
informed: obs-epic-orchestrator
issue: https://github.com/d0ugal/graith/issues/2051
---

# Collector-Based Metrics Shipping

Graith should keep metrics export collector-based: the daemon exposes a local
Prometheus scrape endpoint, and Prometheus, Grafana Alloy, or another
collector owns remote-write delivery. Direct remote write from the daemon stays
out of scope unless a future design proves Graith can take on the operational
responsibilities of a production-grade remote-write sender.

## Background

The telemetry runtime design keeps metrics disabled by default and starts a
local Prometheus-compatible HTTP endpoint only when
`[telemetry.metrics].enabled = true`. The initial daemon metrics design then
adds bounded daemon and session metrics to that endpoint without adding an
outbound metrics exporter.

Prometheus remote write is the standard way for Prometheus-compatible senders
to forward time series to long-term stores such as Mimir. The protocol looks
small at the wire-format level, but the Prometheus docs frame the sender as the
component that scrapes instrumented applications or exporters and then sends
remote-write messages to a server. The current Graith metrics path already
matches that split: Graith instruments itself, exposes the metrics locally, and
lets collector software own shipping.

## Problem

Direct remote write can look like a small feature because the daemon already
has metric values and Go has protobuf/HTTP libraries. That framing misses the
hard part. Once Graith dials a remote-write endpoint itself, it becomes the
sender that must preserve ordering, durability, retry, authentication, and
backpressure behavior while the daemon is also trying to keep PTYs, sessions,
worktrees, and users responsive.

That responsibility is not a good fit for the daemon today. Metrics are useful
for operating Graith, but a slow or unavailable metrics backend must not become
a reason for session creation, attach output, config reload, or daemon shutdown
to stall.

## Goals

- Keep Prometheus scrape plus a collector as the recommended metrics path.
- Record the direct remote-write responsibilities Graith is deliberately not
  taking on in this slice.
- Make future reopening evidence-based rather than preference-based.
- Keep user docs discoverable without copying design-history detail into them.

### Non-Goals

- Implement direct remote write.
- Prototype a remote-write sender.
- Add new telemetry configuration.
- Enable telemetry or ship data from a development machine.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | Users enable the daemon scrape endpoint and configure external collectors from config/docs. |
| iOS | Excluded | Mobile clients do not own daemon metrics shipping or collector configuration. |
| macOS | Excluded | The app may display derived observability later, but it should not own remote-write delivery. |

## Proposals

### Proposal 0: Do Nothing

Leave the earlier telemetry and metrics designs as the only record. This keeps
the implementation unchanged, but it leaves a gap in the design history. A
future issue could rediscover the same direct-remote-write tradeoff and treat
the lack of a sender as accidental rather than deliberate.

### Proposal 1: Keep Metrics Collector-Based (Recommended)

Graith continues to expose metrics through the local scrape endpoint described
in `website/content/docs/configuration/observability.md`. A collector such as
Prometheus or Grafana Alloy scrapes that endpoint and owns remote-write
delivery to Mimir or another remote backend.

This keeps responsibilities separated:

- Graith owns instrumentation, label hygiene, local opt-in listener lifecycle,
  and daemon-safe metric collection.
- The collector owns scrape scheduling, target health, write-ahead logging,
  queue sizing, retry/backoff policy, endpoint authentication, and delivery
  diagnostics.
- The remote backend owns ingestion limits, tenancy, retention, and query-time
  behavior.

The collector path is the safer default because it keeps remote delivery
failure outside the daemon's critical path. If the remote endpoint slows down,
the collector can queue, retry, shed according to its own policy, or surface
its own health metrics without blocking Graith's PTY and session work. If the
collector is absent, Graith still runs normally and the local metrics endpoint
can be inspected with `curl`.

#### Direct shipping responsibilities

A direct sender inside Graith would have to own at least these behaviors:

- Queueing and WAL: Prometheus remote write reads samples from a WAL into
  per-shard memory queues before sending. Grafana Alloy's
  `prometheus.remote_write` component also keeps a WAL to buffer unsent metrics
  during intermittent network failure and repopulate in-memory cache after
  process restart, with retention and truncation policy owned by the collector.
- Retry and backoff: senders must retry server-side failures, may handle rate
  limiting, and must back off so a struggling receiver is not overwhelmed.
- Ordering: remote-write compatible senders must preserve timestamp order for
  samples in each series, even when sending different series concurrently.
- Stale markers: a sender must emit the Prometheus staleness marker when it can
  detect that a series will no longer be appended. Scrape-based collectors
  already have the target and scrape-cycle context needed for that decision.
- Authentication and transport: remote write treats authentication and
  encryption as transport-layer concerns. Prometheus and Alloy expose the
  related HTTP client/auth/TLS surface; Graith would need an equivalent secret,
  redaction, reload, validation, and logging contract.
- Backpressure: queue capacity, shard count, batch size, retry delay, and WAL
  retention are operational controls. In Graith, backpressure would also need a
  product policy: whether metrics enqueue blocks daemon work, drops samples,
  grows disk, or disables export.

None of these are impossible, but together they are collector-grade work. They
also need observable failure modes, upgrade compatibility, and tests that cover
restart recovery, remote outage, rate limiting, partial writes, and shutdown
flush behavior.

#### Go library surface

Existing Go packages reduce parts of the work, but they do not remove the
sender responsibility.

`github.com/prometheus/client_golang/prometheus` and
`github.com/prometheus/client_golang/prometheus/promhttp` are the stable path
Graith already uses: instrument code, register collectors, and expose a scrape
handler. They are sufficient for the current daemon metrics runtime, but they
do not make the daemon a durable remote-write sender.

`github.com/prometheus/client_golang/exp/api/remote` provides experimental
Prometheus remote API bindings, including a remote-write API client and
configurable backoff. It is useful transport code, not a daemon shipping
subsystem. It does not decide what Graith persists, how WAL state survives
restart, how per-series ordering interacts with daemon concurrency, when stale
markers are emitted, or what product behavior follows sustained backpressure.

`github.com/prometheus/prometheus/storage/remote` contains Prometheus'
`WriteStorage` and `QueueManager`, including WAL-watcher and queue-manager
machinery. Embedding that package would mean adopting a large Prometheus server
subsystem with its config model, storage directory, lifecycle, metrics, and
compatibility surface. That can be a valid choice in a collector, but it is not
a small library call inside Graith.

`github.com/prometheus/prometheus/prompb` and the generated v2 protobuf types
help encode requests. Encoding is only the wire-format layer; it does not solve
queueing, WAL durability, retries, ordering, staleness, auth, or backpressure.

### Proposal 2: Add Direct Remote Write

Add `[telemetry.metrics.remote_write]` configuration and have the daemon send
samples directly to a remote-write endpoint.

This removes a process for users who only want Graith metrics in Mimir, but it
puts a production sender into the daemon. The feature would need persistent
state, remote-write configuration and secret handling, queue/WAL resource
limits, local health metrics for the sender itself, retry and rate-limit
behavior, shutdown/drain semantics, and compatibility coverage against
supported remote-write protocol versions. It would also duplicate work already
provided by Prometheus and Alloy.

The tradeoff is not favorable until there is evidence that the collector path
is materially failing Graith users.

## Other Notes

### Reopening criteria

Reopen direct metrics shipping only with concrete evidence that collector-based
shipping is inadequate for Graith. Useful evidence would include:

- Repeated user reports where running Prometheus or Alloy is the blocker, not
  only an extra setup step.
- A supported, narrowly scoped Go sender library that owns durable queue/WAL
  semantics, retry/backoff, ordering, stale markers, auth hooks, health metrics,
  and bounded backpressure without embedding a broad Prometheus server runtime.
- A design that proves daemon responsiveness under remote outage, receiver
  throttling, process restart, disk-full, and shutdown.
- Compatibility evidence against the relevant remote-write spec version and
  the intended backends, including Mimir.
- A product decision for exactly what gets persisted, when samples may be
  dropped, how credentials are configured and redacted, and how users debug
  sender health.

Convenience alone is not enough. The future design must show that Graith can
own the same operational boundary a collector owns today.

### References

- Issue: https://github.com/d0ugal/graith/issues/2051
- Telemetry runtime design: `docs/design/2026-07-31-telemetry-runtime.md`
- Initial daemon metrics design: `docs/design/2026-07-31-initial-daemon-metrics.md`
- User docs: `website/content/docs/configuration/observability.md`
- Prometheus Remote-Write 1.0 specification: https://prometheus.io/docs/specs/prw/remote_write_spec/
- Prometheus Remote-Write 2.0 specification: https://prometheus.io/docs/specs/prw/remote_write_spec_2_0/
- Prometheus remote write tuning: https://prometheus.io/docs/practices/remote_write/
- Prometheus remote write configuration: https://prometheus.io/docs/prometheus/latest/configuration/configuration/#remote_write
- Prometheus Go application instrumentation guide: https://prometheus.io/docs/guides/go-application/
- Grafana Alloy `prometheus.remote_write` component docs: https://grafana.com/docs/alloy/latest/reference/components/prometheus/prometheus.remote_write/
- `client_golang` module docs: https://pkg.go.dev/github.com/prometheus/client_golang
- `promhttp` package docs: https://pkg.go.dev/github.com/prometheus/client_golang/prometheus/promhttp
- `client_golang/exp/api/remote` package docs: https://pkg.go.dev/github.com/prometheus/client_golang/exp/api/remote
- `prometheus/storage/remote` package docs: https://pkg.go.dev/github.com/prometheus/prometheus/storage/remote
