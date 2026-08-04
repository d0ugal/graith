---
title: "Design Doc: Alloy Doctor Checks"
authors: Codex
created: 2026-08-04
status: Implemented
reviewers: (none yet)
informed: obs-epic-orchestrator
issue: https://github.com/d0ugal/graith/issues/2043
---

# Alloy Doctor Checks

Graith adds opt-in `gr doctor --alloy` checks that diagnose local Grafana Alloy
collection setup without starting services, reading credential files, or calling
remote observability backends. The checks focus on the local collector binary,
generated or supplied config validation, Graith log readability, loopback
metrics scraping, generated backend URL environment variable shape, and
non-root service-status hints.

## Background

Graith already exposes local observability surfaces: logs stay in the data
directory, metrics bind a Prometheus-compatible endpoint only when
`[telemetry.metrics] enabled = true`, and traces export through OTLP only when
`[telemetry.tracing] enabled = true`. Issue #2042 added `gr config alloy`,
which renders Alloy config from resolved Graith paths and telemetry settings.
The generated config uses `sys.env(...)` references for Loki, Mimir, and Tempo
backend details instead of embedding credentials.

Alloy setup still has several local failure modes before any backend is
involved: the Alloy binary may be absent or too old, the generated config may
not validate with the installed Alloy release, selected log files may not be
readable by the collector user, metrics may be enabled but not reachable on the
configured scrape endpoint, and the platform service may not be running.

## Problem

When collection fails, users currently have to guess whether the fault is in
Graith, Alloy, file permissions, the metrics listener, service state, or a
remote Loki/Mimir/Tempo endpoint. A generic health check that always probes
Alloy would be noisy for users who have no collector, while a diagnostic that
reads secret files or calls backend APIs would cross the no-telemetry-by-default
boundary.

## Goals

- Detect Alloy on `PATH` or at a caller-provided executable path.
- Report the local Alloy version without requiring a daemon or collector
  service.
- Validate a supplied Alloy config path, or a temporary `gr config alloy`
  rendering, when the installed Alloy CLI supports local validation.
- Check Graith's local metrics scrape endpoint only when the metrics signal is
  selected and Graith metrics are enabled.
- Check selected Graith log files exist and are readable by the current user.
- Validate common generated backend URL environment variables without printing
  secrets or making backend calls.
- Surface likely macOS or Linux service state when detectable without root.
- Keep diagnostics bounded to local filesystem, process, and loopback HTTP
  checks.

### Non-Goals

- Start, stop, install, enable, reload, or manage Alloy.
- Discover or inspect credential files.
- Print token, password, userinfo, query, or fragment values.
- Call Loki, Mimir, Tempo, Grafana Cloud, or other remote backend APIs.
- Prove that remote credentials are valid or that data reached a backend.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | `gr doctor` is the existing local diagnostics surface. |
| macOS | Targeted through CLI | The CLI can inspect PATH, files, loopback metrics, `launchctl` hints, and Homebrew service status without root. |
| Linux | Targeted through CLI | The CLI can inspect PATH, files, loopback metrics, and `systemctl --user` or package `systemctl` status without root. |
| iOS | Excluded | iOS is a remote frontend and cannot inspect the local collector host. |

## Proposals

### Proposal 0: Do Nothing

Keep Alloy troubleshooting in documentation. This preserves the local-only
boundary, but users still have to manually run several commands and interpret
which failures belong to Graith versus Alloy.

### Proposal 1: Opt-In `gr doctor --alloy` (Recommended)

Extend the existing doctor command with Alloy-specific flags:

```text
gr doctor --alloy
gr doctor --alloy --alloy-binary /opt/homebrew/bin/alloy
gr doctor --alloy --alloy-config ./config.alloy
gr doctor --alloy --alloy-signals daemon-logs,metrics
```

The checks run only when `--alloy` is set, so users without a collector do not
get warnings in ordinary `gr doctor` output. `--alloy-binary` accepts either an
executable path or a command name; an empty value searches `PATH` for `alloy`.
`--alloy-config` points at a supplied or previously generated config. When that
flag is empty, doctor renders a temporary config with the same pure renderer and
default signal set as `gr config alloy` and validates the temporary file.

Doctor first resolves the Alloy binary, runs `--version`, and checks for the
documented `validate` command. If validation is unsupported, doctor warns and
continues with the remaining local checks. If validation is supported and the
config is invalid, doctor fails but does not print Alloy stderr or stdout,
because config diagnostics may include user-authored snippets or values.

For log collection, the selected `daemon-logs` signal checks only
`daemon.log` and `daemon.stderr.log`, matching the generated config. Session
scrollback remains excluded. For metrics, doctor only dials loopback or wildcard
addresses translated through the generator's scrape-target logic. Non-loopback
binds are reported as not locally checked instead of sending traffic to an
unknown host. For backend URLs, doctor checks the generated environment variable
names (`GRAITH_LOKI_URL`, `GRAITH_MIMIR_URL`,
`GRAITH_TEMPO_OTLP_ENDPOINT`) for common shape issues. The Loki check runs only
when the `daemon-logs` signal is selected. Output names the variable and
sanitized shape only; it never prints userinfo, query strings, fragments,
usernames, tokens, or passwords.

Service checks are best-effort and read-only. On macOS, doctor tries
`brew services list --json` for Homebrew Alloy and `launchctl print
gui/<uid>/homebrew.mxcl.alloy`; on Linux it tries `systemctl --user is-active
alloy.service` and then `systemctl is-active alloy.service`. Missing tools or
inconclusive states warn rather than fail because Graith does not own these
services in this slice.

### Proposal 2: Always Run Alloy Checks

Doctor could always search for Alloy and warn when it is absent. That makes
collection diagnostics more discoverable, but it turns an optional external
collector into noise for every Graith user. It also risks implying Alloy is a
Graith dependency. The opt-in flag is a clearer boundary.

### Proposal 3: Persistent Alloy Doctor Config

Graith could add `[observability.alloy]` settings for binary path, config path,
and selected signals. That may be useful later, but it is premature for this
local diagnostic slice. Flags keep #2043 small and avoid freezing another
configuration schema before the basic generated-config path has real usage.

## Other Notes

### References

- Issue: https://github.com/d0ugal/graith/issues/2043
- Parent epic: https://github.com/d0ugal/graith/issues/2037
- Alloy config generator: `docs/design/2026-08-04-alloy-config-generation.md`
- User observability docs:
  `website/content/docs/configuration/observability.md`
- Grafana Alloy validate command:
  https://grafana.com/docs/alloy/latest/reference/cli/validate/

### Testing

Unit tests should use fake Alloy executables, local temporary files, and
`httptest` loopback servers. Coverage should prove binary resolution, version
rendering, generated and supplied config validation, unsupported validation
handling, log readability checks, metrics reachability, backend URL redaction,
and service-status command parsing. Tests must not require a real Alloy install,
root, or network access beyond loopback.
