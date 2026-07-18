---
title: "Design Doc: Collision-Safe Scenario Name Templates"
authors: OpenAI Codex
created: 2026-07-17
status: Implemented
reviewers: (pending implementation review)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1417
---

# Collision-Safe Scenario Name Templates

Saved scenario definitions may use a small, daemon-rendered template vocabulary
to create a unique scenario instance namespace and apply it consistently to
member names and every scenario-local member reference.

## Background

`gr scenario start` parses TOML into a `ScenarioStartMsg`, and the daemon
preflights the scenario before reserving its record and member session IDs.
Scenario display names and non-shared session names are global live-name keys.
Member names are also referenced by `mirror`, `depends_on`, completion actions,
literal trigger inbox deliveries, manifests, results, and lifecycle commands.

Today all those values are literal. The daemon allocates a scenario ID during
start, but definitions cannot use it to form collision-safe names. Scenario
records persist rendered runtime state, while the authored naming intent and
start-time identity context are not retained.

Issue #1415 proposes an orchestrator-mediated start request. That path needs to
distinguish the authenticated caller, the orchestrator parent that owns created
members, and the original initiating session.

## Problem

A reusable scenario file cannot be started twice concurrently unless an
external tool rewrites its scenario name, every owned member name, and all
references to those members. Rewriting only `[scenario].name` still leaves
member collisions. Ad hoc rewriting also makes diagnosis and restart behavior
hard to explain because the daemon records only the rewritten values.

## Goals

- Render one collision-safe instance namespace from one immutable context.
- Render member names and every scenario-local member reference exactly once.
- Preserve caller, parent, and initiator as distinct identities.
- Reject unknown tokens and invalid or overlong rendered names before mutation.
- Persist authored-to-rendered mappings and context for restart and diagnosis.
- Keep lifecycle operations, results, manifests, triggers, and additions
  unambiguous after rendering.

### Non-Goals

- General-purpose text templating, expressions, conditionals, or user variables.
- Automatic sanitization, truncation, or collision-driven renaming.
- Implementing the mediated request transport from issue #1415.
- Re-rendering an existing scenario after daemon restart.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | The CLI loads authored TOML and reports rendered start/status data. |
| iOS | Excluded | Scenario authoring and lifecycle controls are not currently exposed in the iOS client. |
| macOS | Excluded | Scenario authoring and lifecycle controls are not currently exposed in the macOS client. |

## Proposals

### Proposal 0: Do Nothing

Keep scenario files literal and require callers or skills to rewrite TOML. This
preserves the current implementation but prevents safe reuse and concurrent
starts and duplicates naming logic outside the daemon.

### Proposal 1: Daemon-Owned One-Pass Rendering (Recommended)

The daemon allocates and transiently reserves a scenario ID, snapshots the UTC
time and three session identities, then renders the scenario name before the
rest of the graph. The fixed vocabulary is:

| Token | Value |
|-------|-------|
| `{caller}` | authenticated submitting session name |
| `{parent}` | orchestrator/owner session name |
| `{initiator}` | original requesting session name; direct starts use caller |
| `{date}` | UTC `YYYYMMDD` |
| `{time}` | UTC `hhmmss` |
| `{datetime}` | UTC `YYYYMMDDthhmmssz` |
| `{scenario_id}` | full stable ID, such as `sc-a1b2c3d4` |
| `{short_id}` | the ID's eight hexadecimal random characters |
| `{scenario}` | fully rendered scenario name; unavailable in the scenario name itself |

Expansion is a single pass. Values introduced by another token are never
interpreted as templates. There is no sanitization or truncation: the rendered
scenario and member names must satisfy the existing validators and 128-byte
limits. A collision is a preflight error. The ID allocator retries boundedly if
its random candidate is already persisted or reserved by a concurrent start.

After rendering the scenario name, the daemon renders every member name,
`mirror`, each `depends_on` entry, completion `session`, and literal or mixed
`action.deliver.inbox` target. Trigger fire-time variables such as
`{session_name}` remain deferred; overlapping instance tokens such as `{date}`
are fixed at scenario start. The daemon then runs all existing graph, trigger,
result, policy, repository, and collision validation against the rendered copy.
No session or scenario state is created until this preflight succeeds.

`ScenarioState` persists the immutable render context, authored scenario name,
authored/rendered member mappings, and authored/rendered reference mappings.
Status/list JSON and manifests expose the same diagnostic metadata. The normal
record, manifest, environment, result destination, todo, trigger, and lifecycle
paths use only rendered values. Restart loads those values and never re-renders.

Successful `scenario_start` already returns a `ScenarioRecord`; its `name` and
session names remain the rendered names, with the render metadata alongside.
Human CLI output therefore reports the actual selectors immediately.

`gr scenario add` is deliberately post-instantiation: its new member name and
`depends_on` selectors must be literal rendered names. Braced member names fail
normal name validation and a braced dependency cannot resolve. The daemon adds
an identity authored/rendered mapping for the new literal member so later
manifests remain complete.

Lifecycle and query commands (`status`, `stop`, `resume`, `delete`, result
selection) accept the rendered scenario name or stable ID where already
supported; the authored template is diagnostic metadata, not an alias.

The protocol carries explicit caller, parent, and initiator IDs. The direct
handler forces all three from authentication. Daemon-internal trigger starts
can retain their source session as initiator, and a future #1415 handler can set
the authenticated ordinary caller, authoritative orchestrator parent, and
durable original initiator without changing rendering semantics.

Trade-offs are a small persistent metadata expansion and a protocol manifest
change. In return, all start paths share one authoritative renderer and
preflight, and runtime code continues operating on ordinary literal names.

### Proposal 2: CLI-Side Rendering

The CLI could allocate a suffix and rewrite the graph before sending it. This
would miss daemon-native trigger starts, make uniqueness coordination racy, and
let clients disagree about token semantics. It also cannot authoritatively
snapshot caller/parent identity. The daemon must own rendering.

## Other Notes

### References

- [Issue #1417](https://github.com/d0ugal/graith/issues/1417)
- [Issue #1415](https://github.com/d0ugal/graith/issues/1415)
- `internal/daemon/scenario.go` — authoritative start preflight and lifecycle
- `internal/scenariofile/scenariofile.go` — strict TOML parsing
- `internal/protocol/messages.go` — start and status wire shapes
- `docs/design/2026-06-22-scenarios.md` — base scenario design

### Implementation Notes

Template-aware file parsing defers only member-identity graph checks that need a
render context. The daemon always repeats those checks against the rendered
graph, so alternate clients and daemon-native starts cannot bypass validation.
Result store templates keep their separate existing vocabulary; their
`{session_name}` input is naturally the rendered member name.

### Testing

Pure renderer tests cover the vocabulary, one-pass behavior, unknown variables,
invalid syntax, mixed trigger/runtime variables, and authored mappings. Daemon
tests cover same-second and concurrent starts, ID candidate collisions, rendered
name collisions, shared initiators, mirror/dependency/completion/delivery
references, invalid and overlong names, results, additions, persistence, and
restart. Protocol, CLI, race, integration, and documentation builds provide the
cross-layer checks.
