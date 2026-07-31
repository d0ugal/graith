---
weight: 355
title: "Observability"
description: "Optional metrics and tracing configuration."
icon: "monitoring"
toc: true
draft: false
---

Graith keeps logs local by default. Metrics and tracing are also disabled by
default, and the daemon starts no telemetry listener, exporter, network
endpoint, or telemetry dial unless you explicitly enable the matching setting.

Telemetry runtime settings are read when the daemon starts. Enabling or
disabling metrics or tracing, or changing settings for a feature that is
currently enabled, requires a daemon restart:

```bash
gr daemon restart
```

`gr daemon reload` and the config-file watcher reject a config generation that
contains runtime-affecting telemetry changes, so unrelated edits in the same
save are not published either. The running daemon stays on the previous
telemetry runtime. Settings for disabled telemetry features can be edited, but
they do not start listeners or exporters until the feature is enabled and the
daemon restarts.
Graith validates telemetry values even when the matching feature is disabled,
except that `telemetry.tracing.endpoint` is only required when tracing is
enabled.

## Metrics

When enabled, the metrics runtime exposes a Prometheus-compatible scrape
endpoint for a local collector such as Grafana Alloy:

```toml
[telemetry.metrics]
enabled = false
bind_address = "127.0.0.1:4824"
path = "/metrics"
```

`enabled` must be set to `true` before the daemon binds the metrics listener.
`bind_address` must be a TCP `host:port` address with a numeric port from `1`
through `65535`; port `0` is rejected in saved configuration so the scrape
target is stable. The default binds loopback only. If the listener cannot bind,
the daemon refuses to start. Binding to `0.0.0.0`, `[::]`, or an empty host such
as `:4824` exposes the unauthenticated scrape endpoint on every reachable
interface. `path` must start with `/`, cannot include a query string or
fragment, and is matched as a literal URL path.

When enabling metrics for local collection, point Alloy or another Prometheus
scraper at `http://127.0.0.1:4824/metrics` unless you changed the address or
path. The first telemetry slice starts the scrape endpoint only; daemon and
session metric series are added by follow-up instrumentation work.

## Tracing

Tracing is configured independently from metrics:

```toml
[telemetry.tracing]
enabled = false
endpoint = ""
protocol = "grpc"
insecure = false
timeout = "10s"

[telemetry.tracing.headers]
# Authorization = "Bearer ..."
```

`enabled` must be set to `true` before the daemon creates trace-export runtime
state. The first telemetry slice stores validated exporter configuration only;
OTLP export and spans are added by follow-up tracing work. `endpoint` is
required when tracing is enabled; Graith does not fall back to `OTEL_*`
environment variables. Credentials belong in
`[telemetry.tracing.headers]`, not in the endpoint URL, and header values are
redacted from daemon config responses and local `gr config show`/`diff` output.

Supported protocols:

| `protocol` | `endpoint` format |
|------------|-------------------|
| `"grpc"` | `host:port`, for example `127.0.0.1:4317` |
| `"http/protobuf"` | full `http` or `https` trace URL used verbatim, for example `http://127.0.0.1:4318/v1/traces` |

`timeout` uses the same duration syntax as other Graith config values and must
be greater than zero when set. `insecure` applies only to the `grpc` protocol;
for `http/protobuf`, choose `http://` or `https://` in the endpoint URL.

Tracing instrumentation and OTLP export are optional runtime plumbing. Graith
does not require Grafana Cloud, Alloy, Mimir, Loki, Tempo, or any collector to
run normally.
