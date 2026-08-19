---
title: "Design Doc: Dependency Health Awareness"
authors: Codex
created: 2026-08-19
status: Accepted (implementation issues tracked below)
reviewers: Claude, Codex
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/2213
---

# Dependency Health Awareness

Graith should give agents and users a small, shared signal when a hosted service
they depend on is degraded, unavailable, or recovering. The first version polls
configured Statuspage-compatible sources from the daemon, persists the last
observation, routes explicit service warnings to affected agents, and surfaces
compact warnings in the terminal status bar. This is situational awareness, not
a promise that a status page accurately describes the real service.

## Background

Agents commonly depend on services outside Graith: GitHub for repository
operations and hosted model providers for agent inference. Today a failed
operation looks like an ordinary command or provider error. Agents may retry
repeatedly, and users have to determine independently whether a shared outage
is responsible.

The daemon already owns long-lived background work and persistent state. It also
has daemon-authored inbox/PTY notification delivery (`notifyFromDaemon`) and
returns a `FleetSummary` through the status response. The CLI status bar renders
that summary, while session records retain the configured agent name. These are
the natural first integration points; dependency health need not become a new
agent protocol or a request-by-request network proxy.

This document covers epic [#2212](https://github.com/d0ugal/graith/issues/2212)
and the first design slice in [#2213](https://github.com/d0ugal/graith/issues/2213).

## Problem

When GitHub, OpenAI, Anthropic, Cursor, or another shared dependency has an
incident, several agents can fail at once. Without a daemon-level observation,
each agent treats the failure as local, users see a noisy fleet, and retries can
make an outage harder to reason about. A status page is an imperfect and
occasionally stale signal, but it is useful shared context if Graith labels it
as such and avoids turning incident prose into agent instructions.

## Goals

- Poll configured service status sources with bounded, predictable resource use.
- Persist observations sufficiently to deduplicate incident notifications and
  detect recovery across daemon restarts.
- Route explicit v1 dependencies to the agents that use them: GitHub globally,
  OpenAI to Codex, Anthropic to Claude, and Cursor to Cursor agents.
- Notify affected active agents once on degradation/down and once on recovery.
- Show compact, severity-ordered dependency warnings in the terminal status bar.
- Offer an inspection command so users can distinguish an outage from a stale
  or unreachable status source.
- Give agents a clear instruction to avoid repeated retry loops while the signal
  is active, while leaving the final decision with the agent and user.

### Non-Goals

- Automatically discovering which CLI/API a command uses in v1.
- Treating a status page as authoritative, measuring service health directly, or
  guaranteeing that an incident is detected.
- Automatic retry, request queuing, circuit breaking, or provider failover.
- Scraping arbitrary HTML, parsing incident prose into commands, or executing
  content from a status page.
- Supporting every hosted service, native GUI warning surfaces, or per-operation
  dependency attribution in the first slice.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | The daemon poller, `gr dependency status`, agent notices, and terminal status bar are CLI/control-plane behavior. |
| iOS | Excluded | The native app does not own daemon polling or the terminal status bar; a later API/UI design can consume the signal. |
| macOS | Excluded | The native app does not render the terminal status bar; native presentation can follow once the daemon data shape proves useful. |

## Proposals

### Proposal 0: Do Nothing

Each agent and user continues to diagnose dependency failures independently.
This has no implementation cost, but it guarantees duplicated investigation and
does not prevent agents from retrying a shared outage. It also leaves the epic's
global GitHub signal unavailable.

### Proposal 1: Daemon Poller with Explicit Routing (Recommended)

The daemon owns a small dependency-health worker. It polls enabled configured
sources, normalizes the result into a deliberately small state model, persists
the latest observation, and emits transition notifications. The worker never
blocks a session-manager operation or an agent command.

#### Config schema

Add a daemon-wide `[dependency_health]` block and repeatable service entries:

```toml
[dependency_health]
enabled = true
poll_interval = "5m"
recovery_poll_interval = "30s"
timeout = "5s"

[[dependency_health.service]]
name = "github"
provider = "statuspage"
base_url = "https://www.githubstatus.com"
global = true

[[dependency_health.service]]
name = "openai"
provider = "statuspage"
base_url = "https://status.openai.com"
agent_types = ["codex"]

[[dependency_health.service]]
name = "anthropic"
provider = "statuspage"
base_url = "https://status.anthropic.com"
agent_types = ["claude"]

[[dependency_health.service]]
name = "cursor"
provider = "statuspage"
base_url = "https://status.cursor.com"
agent_types = ["cursor"]
```

`name` is a stable local identifier and must be unique. `provider` is
`statuspage` in v1. `base_url` is an HTTPS origin; the provider appends the
standard `/api/v2/summary.json` and `/api/v2/incidents.json` paths, avoiding
arbitrary request paths in config. `global = true` routes to every active agent;
otherwise `agent_types` is required and matches the session's configured agent
name exactly. A service may not be both global and have `agent_types` in v1.

The defaults above are examples, not implicit network dependencies. The
embedded default config should leave the service list empty (or the feature
disabled) until users opt in. Config validation rejects duplicate names,
unsupported providers, malformed HTTPS URLs, invalid durations, and ambiguous
routing. A bad service entry should fail config load rather than silently poll a
different endpoint.

`poll_interval` is the normal cadence. `recovery_poll_interval` is an optional
shorter cadence used while a service's last fresh `observed_state` is
`degraded` or `down`; it defaults to 30 seconds and must be positive and no
longer than `poll_interval`. The faster cadence is per service, so one provider
incident does not cause every healthy source to be polled more often. Users can
set the two values equal to disable adaptive polling.

The request boundary is deliberately strict: `base_url` must be HTTPS, have no
userinfo, query, or fragment, and use the default HTTPS port (or no explicit
port). IP literals and hosts resolving to loopback, link-local, multicast,
private, or otherwise non-global addresses are rejected. Requests ignore proxy
environment variables, do not follow redirects, require a JSON content type,
and cap each response at a small fixed limit (256 KiB in v1). The provider
resolves and validates the address for each request and uses the validated
connection address, preventing DNS rebinding from changing the destination.
These rules protect the daemon even though the configured URL is user data; a
future private status service needs an explicit security design rather than a
weaker v1 default.

The configured service list is capped at 32 entries and one poll cycle uses at
most four concurrent requests. A slow source therefore cannot create an
unbounded connection burst or indefinitely delay every other source. Provider
incident IDs are treated as untrusted data even though they are structural:
they are trimmed, restricted to a safe identifier character set, bounded to 128
bytes, and escaped/sanitized before persistence, JSON output, status-bar output,
or agent notification. The provider status label is not persisted or surfaced
in v1 because the normalized state is the only useful stable signal.

#### Provider and normalized state

The Statuspage provider reads the summary indicator and unresolved incident
identifiers. It maps `none` to `operational`, `minor` to `degraded`, and
`major`/`critical` to `down`; unknown values are `unknown`. It records only
bounded metadata: service name, normalized state, observed time, source URL,
provider incident IDs, and a short provider status label. Incident titles and
updates are not needed for routing and are not sent to agents.

Each configured service has two independent dimensions: `observed_state`
(`operational`, `degraded`, `down`, or `unknown`) and `source_health`
(`fresh`, `stale`, or `failed`). A successful response always updates
`observed_state` and marks the source fresh. A failed response preserves the
last observed state, marks the source failed, and changes it to stale after
`2 * poll_interval` without a success. The user-visible composite is therefore
unambiguous: `down + stale` still means the last provider observation reported
down, but the status bar displays both the severity and stale marker; it does
not claim a fresh outage. A source that has never succeeded is `unknown +
failed`.

The provider-state transition machine uses only fresh successful observations.
Stale/failed changes never emit outage or recovery notifications. Inspection
shows both dimensions and timestamps, and the status bar renders a fresh
degraded/down warning as `⚠/✗ Service state`, while stale versions render
`? Service state (stale)` in dim styling. This preserves awareness without
converting a polling failure into a new service claim.

Each configured service has this persisted observation model:

```text
operational | degraded | down | unknown
```

`unknown` means a source returned an invalid response. Source health is tracked
separately as described above; polling failure alone never sends a
service-outage notice.

#### Polling, persistence, and failure behavior

The worker performs one bounded poll soon after startup, then polls each
operational service at `poll_interval` and each degraded/down service at
`recovery_poll_interval`, with a small per-daemon jitter. A fresh operational
observation returns that service to the normal cadence. A transport or parse
failure does not change the observed state, so a service last known degraded or
down remains on the recovery cadence while the source is unavailable; it does
not generate another agent notice. The stale threshold remains
`2 * poll_interval`, so a short recovery cadence does not make a source appear
stale merely because one fast request failed.

Each HTTP request uses the configured timeout and the URL rules above, and
honors cancellation during daemon shutdown. Polls run outside the
session-manager lock. One permanent worker snapshots the current config at the
start of each cycle; config reload updates the snapshot without starting a
second worker. Removed services stop being polled but their persisted state
remains for one retention period and pending notifications are allowed to drain.
In-flight requests finish or cancel with the old snapshot. The concurrency cap
of four applies across both normal and recovery polls, and jitter is bounded so
recovery polling remains meaningfully faster than the normal cadence.

Persist a versioned optional `dependency_health` envelope in the established
daemon state file. The envelope has its own schema version and contains one
record per service with observed state, source health, timestamps, incident IDs,
and pending transitions. The health decoder validates this envelope before it
is applied; invalid health data is discarded and logged while the rest of daemon
state remains usable. The top-level daemon state version is bumped only if the
repository's migration rules require it. Older daemons ignore the optional
field, and the implementation must ensure an older daemon rewrite cannot erase
it silently (or use a separate sidecar state file if that cannot be guaranteed).
Missing state is an empty baseline; a fresh unhealthy observation may produce
one notification. Writes use the existing atomic state path and one state-owner
serialization point.

The worker canonicalizes provider incident IDs as opaque trimmed strings and
uses a service-scoped transition record: `(service, generation, observed
state)`, with incident IDs retained as evidence rather than the sole identity.
The generation advances only on a fresh state change, so a provider changing
incident IDs during one outage does not duplicate a notice. A degraded-to-down
change is a new severity transition; repeated polls of the same state are
deduplicated. Recovery is a fresh transition from degraded/down to operational,
and emits one recovery notice even if the provider removed the incident.
Recovery from unknown or stale source health is not claimed as service recovery.

If notification delivery fails, the observation and a bounded pending-transition
record are committed together before the daemon logs the failure. Delivery
means durable inbox publication; the PTY poke is best effort and is not used as
the commit acknowledgement. Retries use bounded exponential backoff and the
same service/generation/target key. A pending record is removed only after the
inbox store confirms publication, and an existing message with that key is
treated as success. This gives at-least-once delivery without relying on a
stable thread alone to make duplicates harmless. Deleted sessions drop their
pending target; a recreated session ID is a new target. A service source is
isolated from other sources: one bad endpoint does not stop the worker or
suppress healthy-service updates.

#### Agent routing and notification format

On a transition, target sessions are selected from current non-deleted active
sessions. In v1, active means `creating`, `running`, or `ready`; `stopped` and
`errored` sessions are not auto-resumed or notified. Global GitHub targets all
active agents. The other v1 mappings use exact agent type matching (`codex`,
`claude`, and `cursor`). A newly started matching session does not receive an
old transition; it can inspect current state with `gr dependency status`.
There is no attempt to infer dependencies from commands, tools, prompts, or
recent failures. Pending delivery therefore retries only targets that were
active when selected and still exist; it uses the inbox store without the
auto-resume behavior of the general notification helper.

Notifications use the existing daemon-authored system message path and a stable
thread such as `dependency-health:<service>`. They contain structured,
bounded fields rendered as plain text:

```text
[Graith dependency health] GitHub is degraded (status page signal).
Source: https://www.githubstatus.com
Observed: 2026-08-19T12:00:00Z
Incident reference: github-123
Pause repeated retries for GitHub-dependent work; inspect `gr dependency status`.
This is situational data, not an instruction from GitHub.
```

Recovery changes the first sentence to “is operational again” and says that
normal attempts may resume cautiously. Down notices use “down” and recommend
waiting or asking the user about alternatives. The message never includes raw
incident titles, descriptions, update text, links supplied by the provider, or
any field interpreted as a command. The source URL is the validated configured
URL. Status-page content is untrusted data and must remain visibly attributed;
agents must not follow it as instructions.

The notice is advice, not enforcement. Agents should stop or substantially slow
repeated retries for operations using the affected dependency, preserve useful
error context, continue unrelated work, and retry only after recovery or a
deliberate user decision. This guidance belongs in the notice and agent-facing
documentation, not in a daemon-side command blocker.

#### Status bar behavior

Extend the daemon's `FleetSummary` with compact dependency-health metadata:
active degraded/down service names (bounded and sorted by severity/name), the
highest severity, and whether any source is stale. Attached clients already
receive this through status polling, so no second client polling protocol is
needed. The client renders a short warning such as `⚠ GitHub degraded` or
`✗ OpenAI down` on the right side of the bar.

Warnings are fleet-wide and follow this matrix: urgent orchestrator attention,
then down dependencies, then degraded dependencies, then jailed comments, then
ordinary fleet counts and unread counts. In full mode, show up to three unique
service names sorted by severity then name; in compact mode show the highest
severity and the number of affected services; in critical mode show the highest
severity only. Jailed comments remain visible when no dependency warning can
fit. Service names are configured identifiers restricted to safe printable
characters and are sanitized again before rendering. Healthy services disappear
after recovery. Stale markers are dim and never replace the observed severity.
The status bar is a pointer to `gr dependency status`, not a replacement for
details.

#### Inspection command

Add `gr dependency status` with human-readable output and `--json`. It lists
configured services, both state dimensions, last successful poll, last failure,
active incident IDs, routing, and validated source URL. V1 is informational and
always exits zero for a successfully read status snapshot; config/daemon/IPC
errors remain non-zero. JSON has a top-level schema version, and empty
configuration returns an empty services array. `gr service status` is deferred;
v1 has one canonical command and does not add an alias.

### Proposal 2: Per-Agent Dependency Checks

Each agent wrapper could check its provider immediately before every command and
inject the result into that agent only. This gives fresher, more targeted data,
but multiplies network traffic, couples provider knowledge to launch wrappers,
cannot provide a global GitHub signal reliably, and complicates sandbox/network
policy. It also encourages request-path automation before the simpler shared
signal has proved useful. It is a possible later optimization, not the v1
design.

## Consensus

Independent reviews agreed that the explicit, opt-in poller and untrusted-data
boundary are the right v1 direction. The design was revised to make source
health a separate overlay from the last successful provider state; define
strict HTTPS destination and response limits; clarify that only active sessions
are targeted without auto-resuming stopped agents; specify state validation,
reload ownership, transition/outbox ordering, and status-bar priority; and bound
and sanitize provider incident IDs. The reviewers also identified provider
partial-response tests, service-count/concurrency limits, and renamed-agent
routing as implementation test cases. No implementation issues should be
created until this revised draft is accepted.

## Other Notes

### References

- [Epic #2212](https://github.com/d0ugal/graith/issues/2212)
- [Issue #2213](https://github.com/d0ugal/graith/issues/2213)
- `internal/config/config.go` — daemon configuration and validation patterns.
- `internal/daemon/run.go` — background task lifecycle and shutdown.
- `internal/daemon/state.go` — persistent atomic daemon state.
- `internal/daemon/notify.go` — daemon-authored agent notifications.
- `internal/protocol/messages.go` — `StatusResponseMsg` and `FleetSummary`.
- `internal/client/statusbar.go` — status-bar priority and compaction.
- `docs/design/2026-08-06-status-bar-attention-signals.md` — related status-bar signals.

### Implementation Notes

The first implementation should keep provider parsing in a small package with a
transport interface so fake JSON and clock tests do not require network access.
State additions should be optional for older daemons and should not require a
destructive migration. The poller should publish a new summary snapshot through
the existing status path rather than make status-bar clients understand provider
responses.

The initial service list should be opt-in and documented with examples. The
GitHub/OpenAI/Anthropic/Cursor mappings are documentation examples plus explicit
config values, not implicit network checks. Users with aliases such as
`my-codex` list that exact name in `agent_types`; this is intentional until an
agent-profile grouping design exists.

### Alternatives considered

- **Use only PTY notices:** easy to add, but missed notices do not persist and
  users have no fleet-wide status-bar signal.
- **Use inbox messages only:** durable, but too easy to miss and not visible
  while the user is attached elsewhere.
- **Treat every poll failure as an outage:** simple, but a local DNS, firewall,
  or status-page incident would create false provider-down warnings.
- **Include provider incident text:** useful context, but violates the trust
  boundary and creates an instruction-injection surface.
- **Allow arbitrary redirects or proxy configuration:** convenient for private
  deployments, but turns a background status worker into an SSRF surface.

### Testing

Unit tests should cover TOML defaults and validation, Statuspage JSON mapping,
unknown values, hostile URL/redirect/DNS cases, response-size and timeout
limits, partial summary/incident responses, incident/state dedupe,
degraded-to-down and recovery transitions, stale-source handling, persistence
across restart and older-daemon rewrites, failed delivery retention, explicit
routing, and bounded notification formatting. Status-bar tests should cover the
priority matrix, compact widths, recovery removal, and stale styling. Handler or
integration tests
should verify `gr dependency status` and JSON output. Focused daemon race tests
should exercise concurrent polling, status reads, config reload, and shutdown;
the full race and integration suites remain required before implementation
merge.

### Open questions

- Should v1 default to no services configured, or ship commented examples only?
- Should a future `--check` flag return a non-zero health exit code, or should
  scripting use a separate command?
- Are the default five-minute normal cadence and 30-second recovery cadence
  appropriate for typical status-page lag and API limits?

### Proposed follow-up issues

The design is accepted, and these implementation slices are now tracked under
epic #2212:

1. [#2216](https://github.com/d0ugal/graith/issues/2216) — **Config and normalized
   dependency-health model:** add validated TOML schema, defaults, routing rules,
   limits, and versioned persisted state.
2. [#2217](https://github.com/d0ugal/graith/issues/2217) — **Statuspage provider
   and adaptive poller:** implement bounded HTTP polling,
   JSON normalization, normal/recovery cadence selection, timeout,
   stale/failure handling, and transition dedupe with fake-clock/server tests.
3. [#2218](https://github.com/d0ugal/graith/issues/2218) — **Daemon notifications
   and routing:** select active sessions, deliver
   daemon-authored degradation/recovery notices, retain pending delivery, and
   enforce the status-page trust boundary.
4. [#2219](https://github.com/d0ugal/graith/issues/2219) — **Status protocol and
   terminal bar:** extend `FleetSummary`, status
   aggregation, warning priority/compaction, and client tests.
5. [#2220](https://github.com/d0ugal/graith/issues/2220) — **Dependency inspection
   CLI and documentation:** add `gr dependency status`
   with stable human/JSON output and document configuration, agent guidance, and
   failure semantics.
6. [#2221](https://github.com/d0ugal/graith/issues/2221) — **Operational
   hardening:** add race/integration coverage, restart tests,
   metrics/logging for poll outcomes, and review whether native GUI surfaces or
   additional providers merit a follow-up design.

Deferred work is tracked separately so it is not lost:

- [#2222](https://github.com/d0ugal/graith/issues/2222) — automatic dependency
  usage detection and attribution.
- [#2223](https://github.com/d0ugal/graith/issues/2223) — additional providers and
  private status sources.
- [#2224](https://github.com/d0ugal/graith/issues/2224) — native GUI dependency
  warnings for iOS and macOS.
- [#2225](https://github.com/d0ugal/graith/issues/2225) — request-level resilience,
  retry suppression, and provider failover.
