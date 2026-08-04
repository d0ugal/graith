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

## Logs

Graith always writes logs to local files. It does not ship logs to Loki,
Grafana Cloud, or any other service unless you configure an external collector
such as Grafana Alloy to read those files.

The files listed here are raw local diagnostics, not a redacted telemetry
stream. Daemon logs can include absolute paths, worktree names, session names,
branch or PR details, command arguments, notification text, remote user or
device labels, and raw error strings. Daemon stderr can include panic traces,
race-detector output, and process diagnostics. Export these files only when
that raw diagnostic content is acceptable for your backend and retention policy.

The default data directory is `~/.local/share/graith` on Linux and other XDG
platforms, and `~/Library/Application Support/graith` on macOS unless
`XDG_DATA_HOME` is set. Set `data_dir` in the global config to move it. When
`GRAITH_PROFILE=<profile>` is set, the app name becomes `graith-<profile>`, so
the default data directory changes with that profile.

Collect these raw diagnostic files when you want Graith local logs in Loki:

| Path | Contents |
|------|----------|
| `<data_dir>/daemon.log` | Raw structured daemon diagnostics in JSON format |
| `<data_dir>/daemon.log.N` | Rotated raw daemon diagnostic backups, controlled by `[logging]` |
| `<data_dir>/daemon.stderr.log` | Raw daemon stderr, including panic tracebacks, `SIGQUIT` goroutine dumps, and race-detector output |
| `<data_dir>/logs/<session-id>.log` | Raw per-session scrollback logs |

The local file table in the [configuration reference]({{< relref "/docs/configuration/_index.md#file-locations" >}})
lists the default Linux/XDG paths. Use the resolved data directory, not the
literal examples, when you have a custom `data_dir`, macOS install, or named
profile.

Per-session scrollback logs contain raw terminal output. They may include
prompts, source snippets, command output, or secrets printed by programs running
inside a session. Sending those files to Loki is a deliberate off-machine
disclosure; collect them only when that exposure is acceptable. Raw daemon logs
are also a deliberate diagnostic export, not the future safe daemon event
schema. Rotated daemon logs are useful for local inspection or manual backfill,
but a live Alloy tail should usually read only `<data_dir>/daemon.log` so
rotation does not re-ingest old backups.

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
path.

Graith deliberately keeps metrics collector-based: a collector such as
Prometheus or Alloy owns remote-write delivery to Mimir or other long-term
storage. See the
[collector-based metrics shipping decision](https://github.com/d0ugal/graith/blob/main/docs/design/2026-08-04-collector-based-metrics-shipping.md)
for why.

The metric set includes daemon/session reliability signals and focused local
attach latency segments:

| Metric | Type | Labels |
|--------|------|--------|
| `graith_daemon_info` | gauge | `version`, `commit` |
| `graith_daemon_uptime_seconds` | gauge | none |
| `graith_daemon_attached_clients` | gauge | none |
| `graith_sessions` | gauge | `status`, `driver_kind` |
| `graith_session_launch_duration_seconds` | histogram | `operation`, `driver_kind`, `result` |
| `graith_session_lifecycle_transitions_total` | counter | `from`, `to` |
| `graith_session_input_events_total` | counter | `operation`, `result` |
| `graith_session_input_bytes_total` | counter | `operation`, `result` |
| `graith_session_input_duration_seconds` | histogram | `operation`, `result` |
| `graith_session_input_readback_latency_seconds` | histogram | `operation` |
| `graith_pty_output_read_duration_seconds` | histogram | `result` |
| `graith_pty_screen_update_duration_seconds` | histogram | `result` |
| `graith_pty_attach_fanout_duration_seconds` | histogram | `result` |
| `graith_attach_output_queue_delay_seconds` | histogram | `mode` |
| `graith_attach_output_write_duration_seconds` | histogram | `mode`, `result` |
| `graith_screen_snapshot_requests_total` | counter | `kind` |
| `graith_screen_snapshot_duration_seconds` | histogram | `kind` |
| `graith_messages_published_total` | counter | `stream_kind`, `sender_kind` |

Histogram metrics also expose the standard Prometheus `_bucket`, `_sum`, and
`_count` series. Label values are intentionally bounded. `status`, `from`, and
`to` use `creating`, `running`, `stopped`, `errored`, `deleting`, or `unknown`.
`driver_kind` uses `pty`, `headless`, or `unknown`; launch `operation` uses
`create`, `fork`, `orchestrator_create`, `resume`, or `unknown`; input
`operation` uses `attach`, `type`, `type_no_newline`, or `unknown`; `result`
uses `success` or `error`; snapshot `kind` uses `full`, `delta`, or `unknown`;
attach output `mode` uses `raw`, `coalesced`, or `unknown`;
`stream_kind` uses `topic`, `inbox`, `system`, or `unknown`; and `sender_kind`
uses `session`, `device`, `system`, or `unknown`.

`graith_session_lifecycle_transitions_total` counts published session
status-change events. It can collapse transient internal busy states. For
`graith_session_input_duration_seconds`, `operation="type"` includes the
configured `lifecycle.input_delay` between writing the input bytes and
submitting the trailing carriage return. `graith_session_input_readback_latency_seconds`
records one pending attach input at a time, from the successful PTY write
attempt to the next eligible PTY output read. `mode="coalesced"` on attach
output metrics measures daemon delivery of the terminal-owned attach hint frame,
not the client's final terminal draw.

Graith does not put session IDs, session names, repository paths, worktree
paths, branch names, prompts, message bodies, or user names in metric labels.

### Local latency diagnostic

From a source checkout, you can run a local terminal-owned attach latency
diagnostic without enabling telemetry export:

```bash
GRAITH_INPUT_LATENCY_DIAGNOSTIC=1 go test ./internal/daemon -run TestTerminalOwnedAttachInputLatencyDiagnostic -count=1
```

Set `GRAITH_INPUT_LATENCY_SAMPLES` to change the sample count.

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
# Add backend-required auth headers here.
```

`enabled` must be set to `true` before the daemon installs an OpenTelemetry
tracer provider and OTLP exporter. `endpoint` is required when tracing is
enabled; Graith passes the configured endpoint to the exporter and does not fall
back to `OTEL_*` environment variables for endpoint, headers, TLS certificates,
compression, timeout, sampler behavior, or proxy settings. Credentials belong in
`[telemetry.tracing.headers]`, not in the endpoint URL, and header values are
redacted from daemon config responses and local `gr config show`/`diff` output.

Supported protocols:

| `protocol` | `endpoint` format |
|------------|-------------------|
| `"grpc"` | `host:port`, for example `127.0.0.1:4317` |
| `"http/protobuf"` | full `http` or `https` trace URL used verbatim, for example `http://127.0.0.1:4318/v1/traces` |

`timeout` uses the same duration syntax as other Graith config values and must
be greater than zero when set. It bounds exporter setup, export requests, and
shutdown flushing. `insecure` disables TLS for the `grpc` exporter only; HTTP
endpoints use the scheme in the configured trace URL.

Tracing export is optional runtime plumbing for direct OTLP trace export or for a
collector such as Alloy, which can forward traces to Tempo. Export failures are
reported in the daemon log and do not stop the daemon. Graith does not require
Grafana Cloud, Alloy, Mimir, Loki, Tempo, or any collector to run normally.

When tracing is enabled, Graith emits focused attach/input/render spans:

| Span | Notes |
|------|-------|
| `graith.attach.input` | attached client input accepted by the daemon |
| `graith.pty.input.write` | daemon write into the PTY |
| `graith.pty.output.read` | PTY output read after readiness notification |
| `graith.pty.screen.update` | scrollback append and daemon terminal model update |
| `graith.pty.attach.fanout` | daemon fanout to attached output writers |
| `graith.session.input.readback` | next eligible PTY output after a successful attach input write |
| `graith.attach.output.queue_delay` | time an attach output frame waits in the daemon writer queue |
| `graith.attach.output.write` | daemon write of an attach output frame or terminal-owned hint frame |

Latency span attributes are intentionally bounded: byte counts, attach writer
counts, attach output mode, and input operation. Graith does not put session IDs,
session names, repository paths, worktree paths, branch names, prompts, message
bodies, or user names in span attributes.

Attach latency tracing is per local interactive event and per PTY output chunk.
High-output sessions can therefore produce many spans while tracing is enabled;
run it with a local collector that is sized for that volume, or prefer metrics
when you only need aggregate latency distributions.

### Direct trace export

Direct trace export points Graith's trace exporter straight at an OTLP trace
backend. It is trace-only: it does not tail Graith log files, expose or scrape
metrics, start Alloy, or forward anything to Loki or Mimir. Use the
[Alloy collector example](#collect-with-grafana-alloy) when you want collector
buffering, enrichment, sampling, redaction, log collection, or metric scraping.

The opt-in boundary is unchanged. Graith still ships no telemetry until you set
`enabled = true` under `[telemetry.tracing]` with a valid endpoint and restart
the daemon (`gr daemon restart`).

For a self-hosted Tempo OTLP gRPC receiver:

```toml
[telemetry.tracing]
enabled = true
endpoint = "127.0.0.1:4317"
protocol = "grpc"
insecure = true
timeout = "10s"
```

The gRPC endpoint is `host:port`; do not add a URL scheme or an OTLP HTTP path
such as `/v1/traces`. Use `insecure = true` only for a plaintext receiver, such
as a loopback Tempo endpoint. Leave it `false` when the gRPC endpoint uses TLS.
With TLS, Graith validates the server certificate against the host system trust
store; there is no Graith setting for a custom CA bundle or client certificate.
If your self-hosted Tempo requires extra headers, such as `X-Scope-OrgID` for
multi-tenant Tempo, put them under `[telemetry.tracing.headers]`.

For a self-hosted Tempo OTLP HTTP receiver:

```toml
[telemetry.tracing]
enabled = true
endpoint = "http://127.0.0.1:4318/v1/traces"
protocol = "http/protobuf"
timeout = "10s"
```

The `http/protobuf` endpoint must be the full trace URL and is used exactly as
configured. Tempo commonly accepts OTLP/gRPC on port `4317`; enable and expose
the Tempo OTLP HTTP receiver before using port `4318`. For `http/protobuf`,
omit `insecure` or leave it `false`; the URL scheme controls TLS.

For Grafana Cloud OTLP HTTP, copy the OTLP gateway details from your stack's
OpenTelemetry tile and use the full trace URL:

```toml
[telemetry.tracing]
enabled = true
endpoint = "https://otlp-gateway-prod-us-east-0.grafana.net/otlp/v1/traces"
protocol = "http/protobuf"
timeout = "10s"

[telemetry.tracing.headers]
# Authorization = "Basic <value from your Grafana Cloud OpenTelemetry tile>"
```

Replace the endpoint with the exact URL for your stack and region; Grafana Cloud
hosts can differ by region and stack age. If the OpenTelemetry tile shows a
generic `OTEL_EXPORTER_OTLP_ENDPOINT` base URL ending in `/otlp`, keep that
stack-specific base URL and append `/v1/traces`; Graith uses the configured
HTTP trace URL verbatim. Put Grafana Cloud auth in `[telemetry.tracing.headers]`,
not in the endpoint URL. If the tile shows an
`OTEL_EXPORTER_OTLP_HEADERS` value such as `Authorization=Basic ...`, copy only
the header value after `Authorization=` into TOML and use a literal space after
`Basic`, not a percent-encoded space. Use a token with `traces:write`
permission, and keep the real value out of examples, screenshots, and bug
reports. Graith redacts header values in daemon config responses and local
`gr config show`/`diff` output, but the config file still contains whatever
secret value you save there.

Endpoint details are documented by
[Grafana Tempo](https://grafana.com/docs/tempo/latest/set-up-for-tracing/instrument-send/set-up-collector/otel-collector/),
[Grafana Cloud OTLP](https://grafana.com/docs/grafana-cloud/observe-and-act/send-data/otlp/send-data-otlp/),
and the
[OpenTelemetry OTLP exporter configuration](https://opentelemetry.io/docs/languages/sdk-configuration/otlp-exporter/).

## Collect with Grafana Alloy

This example keeps Graith's own defaults local. Graith exposes metrics on
loopback only and exports traces to a loopback Alloy receiver only after you
enable those features. Alloy is the process that tails files and sends data to
Loki, Mimir, and Tempo.

First, enable metrics and tracing in `config.toml`:

```toml
[telemetry.metrics]
enabled = true
bind_address = "127.0.0.1:4824"
path = "/metrics"

[telemetry.tracing]
enabled = true
endpoint = "127.0.0.1:4317"
protocol = "grpc"
insecure = true
timeout = "10s"
```

Restart the daemon after saving the config:

```bash
gr daemon restart
```

The tracing settings above point Graith at Alloy's local OTLP gRPC receiver.
`insecure = true` is required for this plaintext local gRPC example. To use the
OTLP HTTP receiver instead, set `protocol = "http/protobuf"` and omit
`insecure` or leave it `false`; `insecure = true` is rejected for
`http/protobuf` because the URL scheme controls plaintext HTTP versus HTTPS.

```toml
[telemetry.tracing]
enabled = true
endpoint = "http://127.0.0.1:4318/v1/traces"
protocol = "http/protobuf"
insecure = false
timeout = "10s"
```

Then configure Alloy. This example intentionally exports the raw diagnostic log
files described above. Replace every uppercase placeholder with the full URL,
username, instance ID, or token for your backend, and set the referenced
environment variables in Alloy's service environment. The example uses Linux
default log paths; on macOS use paths under
`/Users/YOU/Library/Application Support/graith`, and for named profiles use
`graith-<profile>` instead of `graith`.

```alloy
local.file_match "graith_logs" {
  path_targets = [
    {
      "__path__"  = "/home/YOU/.local/share/graith/daemon.log",
      "job"       = "graith",
      "component" = "daemon",
    },
    {
      "__path__"  = "/home/YOU/.local/share/graith/daemon.stderr.log",
      "job"       = "graith",
      "component" = "daemon-stderr",
    },
    {
      "__path__"  = "/home/YOU/.local/share/graith/logs/*.log",
      "job"       = "graith",
      "component" = "session",
    },
  ]
}

loki.source.file "graith" {
  targets    = local.file_match.graith_logs.targets
  forward_to = [loki.write.graith.receiver]
}

loki.write "graith" {
  endpoint {
    // Example Grafana Cloud URL:
    // https://logs-prod-REGION.grafana.net/loki/api/v1/push
    url = "LOKI_PUSH_URL"

    basic_auth {
      username = "LOKI_USERNAME_OR_INSTANCE_ID"
      password = sys.env("LOKI_API_TOKEN")
    }
  }
}

prometheus.scrape "graith" {
  job_name = "graith"

  targets = [{
    "__address__" = "127.0.0.1:4824",
  }]

  metrics_path = "/metrics"
  forward_to   = [prometheus.remote_write.mimir.receiver]
}

prometheus.remote_write "mimir" {
  endpoint {
    // Example Grafana Cloud URL:
    // https://prometheus-prod-REGION.grafana.net/api/prom/push
    url = "MIMIR_REMOTE_WRITE_URL"

    basic_auth {
      username = "MIMIR_USERNAME_OR_INSTANCE_ID"
      password = sys.env("MIMIR_API_TOKEN")
    }
  }
}

otelcol.receiver.otlp "graith" {
  grpc {
    endpoint = "127.0.0.1:4317"
  }

  http {
    endpoint = "127.0.0.1:4318"
  }

  output {
    traces = [otelcol.exporter.otlphttp.tempo.input]
  }
}

otelcol.exporter.otlphttp "tempo" {
  client {
    // Grafana Cloud usually uses an /otlp endpoint. A local Tempo HTTP
    // endpoint is usually http://tempo:4318.
    endpoint = "TEMPO_OTLP_HTTP_ENDPOINT"
    auth     = otelcol.auth.basic.tempo.handler
  }
}

otelcol.auth.basic "tempo" {
  client_auth {
    username = "TEMPO_USERNAME_OR_INSTANCE_ID"
    password = sys.env("TEMPO_API_TOKEN")
  }
}
```

This Alloy file is only an example. Graith does not start Alloy, read these
environment variables, or send any telemetry to the placeholder endpoints.

## Troubleshooting collection

If logs are missing, confirm the data directory and profile first. `data_dir`
and `GRAITH_PROFILE` change every log path. Check that the daemon has started,
that `daemon.log` or `daemon.stderr.log` exists, and that the Alloy process can
read the files. For session logs, make sure the glob points at
`<data_dir>/logs/*.log`. If Alloy starts after large files already exist,
consider `loki.source.file` position handling and whether you want to tail from
the end.

If metrics are missing, confirm `[telemetry.metrics].enabled = true` and that
you restarted the daemon after changing telemetry settings. Curl the exact
local target from the Alloy host:

```bash
curl http://127.0.0.1:4824/metrics
```

If that fails, check the daemon log for startup errors. A metrics bind failure
prevents daemon startup after you opt in. If curl succeeds but Mimir has no
samples, inspect the Alloy `prometheus.scrape` and `prometheus.remote_write`
components, the `metrics_path`, and the remote-write URL and credentials.

If traces are missing, confirm `[telemetry.tracing].enabled = true`, restart
the daemon, and match Graith's protocol and endpoint to Alloy's receiver:
`protocol = "grpc"` uses `127.0.0.1:4317` with `insecure = true` for the
plaintext local example, while `protocol = "http/protobuf"` uses a full URL
such as `http://127.0.0.1:4318/v1/traces`. Graith does not read `OTEL_*`
environment variables for tracing exporter settings. The gRPC exporter dials
lazily, so the receiver may not see a connection until a span is exported.
Startup, export, and shutdown issues appear in the daemon log as
`telemetry tracing exporter started`, `telemetry tracing exporter error`, or
`telemetry tracing exporter shutdown failed`. Search Tempo for
`service.name = "graith-daemon"`. Graith emits startup and session lifecycle
spans, but tracing is batched, so spans may not appear immediately after daemon
restart or after an operation completes.

If a reload appears to do nothing, remember that runtime-affecting telemetry
changes are restart-only. `gr daemon reload` rejects enabling or disabling
metrics or tracing, and rejects changes to settings for a telemetry runtime that
is currently enabled. Disabled telemetry values may reload, but they still do
not start listeners or exporters until you enable the feature and restart.
