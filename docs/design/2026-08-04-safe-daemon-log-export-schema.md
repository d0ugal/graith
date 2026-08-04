---
title: "Design Doc: Safe Daemon Log Export Schema"
authors: Codex
created: 2026-08-04
status: Draft
reviewers: (none yet)
informed: obs-epic-orchestrator
issue: https://github.com/d0ugal/graith/issues/2049
---

# Safe Daemon Log Export Schema

Graith should not export the existing local daemon `slog` stream verbatim.
The first safe daemon log path should be a collector-only, opt-in stream of
curated daemon events with an allowlisted schema, bounded labels, sanitized
attributes, and deterministic truncation. Raw session scrollback remains outside
the safe daemon log export surface.

## Background

The daemon already writes local structured process logs to `daemon.log` and
rotates them through the `[logging]` settings. Those records are optimized for
local diagnosis: they include paths, user-chosen names, command construction,
configuration diffs, process identifiers, and raw Go errors that can be useful
when debugging a broken daemon on the same machine.

The existing observability runtime keeps metrics and tracing disabled by
default, with explicit opt-in configuration for any listener, exporter, or
telemetry dial. The metrics design also established the important privacy rule
that telemetry labels must stay low-cardinality and omit session IDs, session
names, repository paths, worktree paths, branch names, prompts, message bodies,
and user names.

Safe daemon logs need the same boundary. A future implementation may generate
events from the same daemon lifecycle points as local `slog`, but the export
surface must be separate from the local diagnostic log because the current
fields were not designed for off-machine transport.

## Problem

Directly tailing or pushing the current `daemon.log` would leak data that often
identifies the user, their machine, their repositories, or active work. Current
daemon and PTY log calls include absolute paths, cwd/worktree values, socket and
certificate paths, session names, trigger names, Git branch/ref details,
notification titles, Tailnet users, device labels, command arguments, sandbox
grants, external endpoint values, and raw error strings.

The observability epic needs a credible log/event contract before Graith ships
any supported off-machine daemon log export. Without that contract, a later
exporter would either over-share by forwarding local logs or under-specify what
collectors, dashboards, and alert rules can rely on.

## Goals

- Inventory the current local daemon `slog` fields that would be risky if they
  left the machine.
- Define an allowlist for safe event labels and attributes.
- Define rejected sensitive fields that must not be exported verbatim.
- Decide the v1 transport shape among OTLP logs, Loki push, and
  collector-only export.
- Define truncation behavior for large allowed values.
- Explain why session scrollback is excluded from safe daemon log export.

### Non-Goals

- Add a direct daemon log exporter.
- Add a session-log redaction engine.
- Rewrite existing local daemon logging.
- Guarantee that user-configured collectors pointed at `daemon.log`,
  `daemon.stderr.log`, or session scrollback files are safe.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | Users will opt into any future safe log event stream and collector integration through daemon configuration and docs. |
| iOS | Excluded | Mobile clients do not own daemon process logging or collector setup. |
| macOS | Excluded | The macOS app can display derived observability later, but the safe export boundary belongs to the daemon runtime. |

## Current local daemon log inventory

The inventory below comes from the current daemon, PTY, config watcher, and
telemetry `slog` calls, including `internal/daemon/session_create.go:1056`,
`internal/daemon/session_create.go:1275`,
`internal/daemon/session_resume.go:641`,
`internal/daemon/session_watch.go:211`,
`internal/daemon/handler.go:295`, `internal/daemon/filewatch.go:476`,
`internal/daemon/prwatch.go:909`, `internal/daemon/telemetry.go:63`,
`internal/daemon/run.go:939`, `internal/pty/session.go:911`, and
`internal/pty/scrollback.go:134`.

| Current field family | Examples currently logged | Export policy |
|----------------------|---------------------------|---------------|
| Stable runtime facts | `version`, `commit`, `terminal_backend`, `profile` | Allow as non-label attributes, except profile names are user-chosen and must be mapped to `default` or `custom`. |
| Bounded lifecycle enums | `status`, `from`, `to`, `stop_reason`, `exit_category`, `signal_source`, `signal_request`, `operation`, `stage`, `recovery`, `backend`, `watch_backend`, `sandbox_backend` | Allow only documented enum values. Unexpected enum inputs become `unknown`; deliberately user-defined classes become `custom`. |
| Externally supplied reasons | `reason`, hook report reason strings, trigger/action errors | Reject raw values. Export only a Graith-owned mapped reason enum, `unknown`, or a sanitized `error.kind`. |
| Counts, booleans, and durations | `count`, `sessions`, `helpers`, `capacity`, `queued`, `waited_ms`, `duration_ms`, `startup_ms`, `settle`, `read_only`, `sandboxed`, `attached`, `unread_messages`, `rss_mb`, `cpu_percent`, `open_fds`, `process_count` | Allow as attributes when they are scalar, bounded, and not derived from content. |
| OS process identifiers | `pid`, `pgid`, `exit_code`, `signal`, `peak_rss_mb`, `peak_rss_proc` | Allow as attributes. They are not labels and must not include command names or command lines. |
| Session, scenario, trigger, todo, device, request, and client identifiers | `id`, `session`, `session_id`, `target`, `scenario`, `trigger`, `todo`, `request_id`, `event_id`, `device`, `client_id`, `root`, `member` | Reject raw values. If correlation is required, export a pseudonymous `*.ref` attribute derived with a daemon-local HMAC key and never use it as a label. |
| User-chosen names | `name`, session names, scenario member names, trigger names, notification `title`, device `label`, `tailnet_user`, GitHub login or username values | Reject raw values. Where necessary, export a coarse boolean or enum such as `has_custom_name=true` or `actor_kind=remote_user`. |
| Paths and endpoints | `path`, `cwd`, `workdir`, `worktree`, `marker_worktree`, `root`, `repo`, `repository`, `socket`, `scrollback_path`, `settings`, `hooks_path`, `cert`, `key`, `addr`, `endpoint`, `read_dirs`, `write_dirs`, `unix_sockets`, `watch_dirs`, `samples` | Reject raw values. Export only safe counts, backend names, path-kind enums, or localhost-vs-nonlocal endpoint classification. |
| Commands, arguments, and agent conversation IDs | `command`, `args`, `agent_session_id`, `old_agent_session_id`, `new_agent_session_id`, `fresh_start`, `forced_id_fresh_fallback` | Reject command and ID values. Allow `fresh_start` and `forced_id_fresh_fallback` booleans. Allow a normalized `agent_kind` enum. |
| Git and PR details | `branch`, `default`, `ref`, `old`, `new`, `pr`, `issue`, author counts, dropped counts | Reject branch/ref/SHA/name values and raw PR/issue numbers. Allow cardinality counts such as dropped comment counts and coarse result enums. Do not export PR comment bodies or author logins. |
| Config changes | `key`, `old`, `new`, sandbox grant arrays, telemetry endpoint/header values | Allow `key` only from a documented config-key enum. Reject raw old/new values except booleans and numeric durations for explicitly safe keys. Never export header values. |
| Error and diagnostic strings | `err`, `error`, `cleanup_error`, `detail`, `tasks`, `handlers`, `sandbox_diagnostic`, `resource_samples`, `top_process` | Reject raw strings. Export `error.kind`, `error.class`, `result=error`, and safe numeric context instead. |
| Raw terminal/session content | Session scrollback files, prompt injection results, message bodies, notification messages, PTY output, screen snapshots, terminal query payloads | Always reject from the safe daemon log export surface. |

This inventory does not mean the local log should be rewritten. Local
diagnostics can remain rich because they stay on the machine by default. The
safe export implementation must derive a new event record and drop any local
`slog` attribute that is not explicitly allowed below.

## Proposals

### Proposal 0: Do Nothing

Leave daemon logs as local files and provide no safe export contract. Users can
still point Alloy, promtail, or another collector at `daemon.log`, but Graith
would not be able to call that path safe because current records contain
off-machine-sensitive fields.

This avoids implementation work, but it does not satisfy the observability epic
and encourages downstream collector configs to depend on a local diagnostic log
format that was never a privacy boundary.

### Proposal 1: Collector-Only Safe Event Stream (Recommended)

The first supported daemon log export path should be collector-only. Graith
emits a dedicated, opt-in local stream of safe JSON event records, and a local
collector such as Grafana Alloy tails that stream and forwards it to Loki or an
OTLP logs receiver. Graith does not push logs directly to Loki, does not dial an
OTLP logs endpoint for this first slice, and does not reuse `daemon.log` as the
safe export source.

The event stream is schema-first and transport-neutral. It should map cleanly
to OTLP Logs, Loki labels plus JSON body, or another collector pipeline without
changing the daemon event contract.

The config surface should live under a future `[telemetry.logs]` block,
disabled by default:

```toml
[telemetry.logs]
enabled = false
```

Enabling the stream, disabling it, changing its local output path, changing its
rotation settings, or changing its pseudonymization key should follow the
existing telemetry runtime precedent and require a daemon restart. Disabled
settings may reload without starting a writer. The implementation issue can
choose the final local filename and size/backups defaults, but the stream must
rotate independently from `daemon.log`.

Event writes are best-effort and must not block daemon lifecycle work, hold the
session-manager lock across file I/O, or make session operations fail because a
collector-facing event cannot be written. Write failures should be reported to
the local daemon log with rate limiting and surfaced through safe aggregate
telemetry later, not by exporting the raw write error off-machine.

#### Record shape

Each exported record uses a structured JSON object with stable top-level keys
and an allowlisted `attributes` object:

```json
{
  "schema": "graith.daemon_event.v1",
  "time": "2026-08-04T12:34:56.789Z",
  "severity": "INFO",
  "event.domain": "session",
  "event.name": "session.exited",
  "result": "success",
  "service.name": "graith-daemon",
  "service.version": "dev",
  "graith.commit": "unknown",
  "attributes": {
    "session.driver_kind": "pty",
    "session.status": "stopped",
    "session.exit_code": 0,
    "session.sandboxed": true,
    "duration_ms": 1234
  }
}
```

The exact file path and config subkeys beyond `enabled` belong to the
implementation issue, but the event producer must be distinct from the local
daemon `slog` handler. The implementation should fail closed: if a field is not
in the allowlist, it is omitted rather than exported.

Dotted event-body keys map to underscore collector label names when a backend
requires label names such as Loki's Prometheus-style identifiers. For example,
`service.name` in the event body becomes `service_name` as a label.

#### Allowed labels

Only these fields may become collector labels. Loki label names should use the
underscore form shown here; OTLP processors may also retain the dotted event
body attributes.

| Label | Allowed values |
|-------|----------------|
| `schema` | `graith.daemon_event.v1` |
| `service_name` | `graith-daemon` |
| `severity` | `DEBUG`, `INFO`, `WARN`, `ERROR` |
| `event_domain` | `daemon`, `session`, `attach`, `pty`, `upgrade`, `telemetry`, `config`, `sandbox`, `trigger`, `scenario`, `todo`, `pr_watch`, `git`, `remote`, `notification` |
| `event_name` | A committed per-domain enum such as `session.spawned`, `session.exited`, `telemetry.metrics_started`, `upgrade.stage_completed`, or `trigger.action_failed` |
| `result` | `success`, `error`, `degraded`, `skipped`, `denied`, `timeout`, `unknown` |
| `session_driver_kind` | `pty`, `headless`, `unknown` |
| `agent_kind` | `codex`, `claude`, `cursor`, `opencode`, `agy`, `custom`, `unknown` |
| `sandbox_backend` | `nono`, `safehouse`, `none`, `unknown` |

Labels must never contain raw IDs, hashes, names, paths, branches, usernames,
device labels, endpoints, hostnames, machine identifiers, error strings, or
arbitrary config values.

Unknown value handling is per-field and deterministic. Unexpected or invalid
values for Graith-owned enums become `unknown`. Deliberately user-defined
classes become `custom`, currently limited to profile names other than the
default profile and agent names outside the bundled `codex`, `claude`, `cursor`,
`opencode`, and `agy` set. Event constructors must reject unregistered
`event.name` values rather than exporting `event_name="unknown"`.

The implementation must commit the complete `event.name` enum and the complete
safe config-key enum in code, a generated schema, or both before enabling the
stream. Tests must fail if a constructor attempts to emit an unlisted event
name, unlisted config key, raw config value, or arbitrary string label.

#### Allowed attributes

Attributes can carry more detail than labels, but they are still allowlisted.
The initial allowed attribute families are:

- Scalar counts, byte counts, durations, and booleans.
- OS process IDs and exit data: `process.pid`, `process.pgid`,
  `process.exit_code`, `process.signal`.
- Bounded lifecycle values: session status, stop reason, exit category, signal
  source, upgrade stage, trigger result, watch backend, telemetry protocol.
- Resource facts: `service.version`, `graith.commit`, OS type, architecture,
  daemon start mode, process kind, terminal backend, and profile kind
  (`default` or `custom`).
- Safe config change facts: documented config key enum plus safe boolean,
  integer, and duration values for that key.
- Pseudonymous correlation fields with a `.ref` suffix, only when generated via
  HMAC-SHA256 using a daemon-local export secret and truncated to 32 hex
  characters. Examples: `session.ref`, `scenario.ref`, `trigger.ref`,
  `device.ref`. These are attributes, never labels.
- Sanitized error facts: `error.kind`, `error.class`, `error.retryable`, and a
  bounded `error.code` enum where the code is defined by Graith, not copied from
  an arbitrary error string.

The export secret should be generated locally when safe log export is first
enabled, stored under the daemon data directory with owner-only permissions,
and reused across daemon restarts so refs remain useful for local correlation.
Deleting or rotating that secret intentionally breaks future correlation. The
secret value must never be logged, exported, included in config output, or
shared with metrics/tracing unless a later design explicitly decides that.

#### Rejected fields

The safe export path must reject these values even if they appear in local
daemon logs:

- Raw session IDs, session names, agent conversation IDs, scenario IDs, trigger
  names, todo IDs, request IDs, client IDs, device IDs, and device labels.
- Usernames, Tailnet users, GitHub logins, notification titles/messages, PR
  comment bodies, message bodies, prompts, command output, terminal snapshots,
  terminal query payloads, and scrollback bytes.
- Absolute or relative paths, cwd, worktree roots, repository roots, socket
  paths, certificate/key paths, hook/settings paths, sandbox grant paths, watch
  directory samples, and file names.
- Command names, argv, environment variables, sandbox wrapper argv, configured
  tool commands, and agent launch arguments.
- Git branch names, refs, SHAs, repository names, remote URLs, PR and issue
  numbers, PR author names, and issue titles.
- Telemetry endpoint values, remote addresses, hostnames, machine identifiers,
  HTTP headers, auth material, tokens, certificates, keys, and config values
  that may contain secrets.
- Raw `err.Error()` strings, formatted diagnostics, handler/task snapshots,
  resource sample structs, process names, and top-process strings.

#### Schema compatibility

`graith.daemon_event.v1` permits additive optional attributes and additive
`event.name` enum values when they are committed to the schema and covered by
allowlist tests. Removing or renaming a label, changing the meaning of a label
value, changing pseudonymization semantics, or allowing a previously rejected
sensitive field requires a new schema version. Collectors should treat unknown
optional attributes as ignorable and should not depend on attribute order.

The safe daemon event schema is also independent of the existing
`protocol.EventMsg`/`gr events` stream. That stream can carry session names,
sender identifiers, and message bodies, so a safe log exporter must not forward
it directly for the same reason it must not wrap the local `slog.Handler`.

### Proposal 2: Direct OTLP Logs Export

Add a `[telemetry.logs]` OTLP exporter and send records directly from the
daemon. This aligns with existing trace exporter terminology, but it adds
network dialing, authentication headers, retry/backoff, shutdown flushing, and
reload semantics before the safe event schema has had collector use. It also
creates another route by which Graith can ship data off-machine.

OTLP logs are a good future transport once the collector-only event schema is
validated. They should not be the first implementation.

Issue #2050 intentionally prototypes this transport behind explicit opt-in to
exercise the schema with OTLP logs without changing the supported v1
recommendation above. The prototype must still use the allowlisted
`graith.daemon_event.v1` constructors, keep raw local logs and session
scrollback out of scope, and document itself as experimental behavior.

### Proposal 3: Direct Loki Push

Push daemon log records from Graith directly to Loki. This is convenient for a
single backend, but it couples the daemon to Loki-specific batching, labels,
tenant headers, authentication, retry behavior, and backpressure decisions. It
also makes the label cardinality policy harder to enforce because the exporter
surface and storage backend are the same feature.

Loki should be supported through a local collector in v1.

## Truncation and size policy

The safe event implementation must truncate only after a value has passed the
allowlist. Rejected values are omitted, not truncated.

- Label values cap at 64 UTF-8 bytes and must be enum values, so normal records
  should never need label truncation. Unknown label handling follows the
  deterministic per-field rules in the label section.
- String attributes cap at 256 UTF-8 bytes. Longer values are truncated at a
  valid UTF-8 boundary and accompanied by `<field>.truncated=true` and
  `<field>.original_bytes=<n>`.
- Array attributes must be explicitly allowed, contain only allowed scalar
  element types, cap at 16 elements, and cap each string element at 128 UTF-8
  bytes. Extra elements are dropped with `<field>.truncated=true`.
- Map/object attributes are rejected unless the schema defines their keys
  exactly. Free-form maps such as headers, environment, sandbox grants, config
  diffs, resource sample structs, and task snapshots are not exported.
- A complete serialized event should cap at 8 KiB. If an otherwise allowed
  event exceeds the cap, the producer drops optional attributes in the
  deterministic order defined by that event's schema. If the event schema does
  not define a custom order, optional attributes are dropped by descending key
  name until the record fits. The producer sets `event.truncated=true` and emits
  the record. Required identity fields such as `schema`, `time`, `severity`,
  `event.domain`, `event.name`, and `result` are never dropped.

No v1 attribute is expected to need free-form string or array truncation; these
rules are a defense-in-depth backstop for future allowlisted values, not a
license to admit raw paths, errors, command lines, or message text.

## Session scrollback exclusion

Session scrollback is not a daemon observability event. It is raw terminal
content produced by user commands and agent processes. It can contain prompts,
source snippets, command output, file contents, secrets printed by tools, model
responses, and arbitrary binary or control-sequence data. It is also high
volume and unstructured, so a reliable redaction policy would require a
separate session-log redaction engine, which is explicitly out of scope here.

The safe daemon log export path must therefore exclude:

- `<data_dir>/logs/<session-id>.log` session scrollback files.
- Raw attach replay data and terminal-owned history.
- Screen snapshots and previews.
- PTY output chunks, terminal query payloads, prompt text, message bodies, and
  notification bodies derived from session content.

Users may still deliberately configure their own collector to read session
scrollback files, but that is a raw-content disclosure outside Graith's safe
daemon log export contract.

## Other Notes

### References

- Issue: https://github.com/d0ugal/graith/issues/2049
- Parent epic: https://github.com/d0ugal/graith/issues/2037
- Telemetry runtime design: `docs/design/2026-07-31-telemetry-runtime.md`
- Initial daemon metrics design:
  `docs/design/2026-07-31-initial-daemon-metrics.md`
- Session event stream design:
  `docs/design/2026-07-29-session-event-stream.md`
- Session event forwarding design:
  `docs/design/2026-07-29-session-event-forwarding.md`
- User observability docs:
  `website/content/docs/configuration/observability.md`
- Local daemon log setup: `internal/daemon/run.go`
- Session lifecycle logs: `internal/daemon/session_create.go`,
  `internal/daemon/session_resume.go`, `internal/daemon/session_watch.go`
- PTY diagnostics and scrollback: `internal/pty/session.go`,
  `internal/pty/scrollback.go`

### Implementation Notes

The implementation should build export events from explicit constructors rather
than wrapping the existing `slog.Handler`. A handler-level sanitizer would have
to interpret arbitrary local log fields after the fact, which is fragile and
easy to bypass when new local fields are added.

The same rule applies to `protocol.EventMsg`: safe daemon log events may share
lifecycle observation points with `gr events`, but they must be constructed from
the safe schema rather than forwarding protocol event payloads.

The implementation PR must update `website/content/docs/configuration/observability.md`
and any generated collector examples to distinguish the safe daemon event stream
from raw diagnostic files. Raw `daemon.log`, `daemon.stderr.log`, and session
scrollback collection must remain documented as deliberate raw-content export.

Add tests around the event constructors, not around every local log call. The
tests should prove that known events produce only allowlisted labels and
attributes, unknown enum values collapse according to the per-field rules, raw
paths, PR numbers, issue numbers, and IDs are omitted, pseudonymous refs are
stable for one daemon export key, different keys produce different refs, and
oversized allowed values truncate according to the policy above.

### Testing

This design-only change needs Markdown/link review rather than Go tests. A
future implementation should add focused unit tests for event construction,
redaction policy, truncation, and collector file rotation, then run affected
daemon tests.
