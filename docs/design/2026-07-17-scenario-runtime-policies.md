---
title: "Design Doc: Scenario Runtime Policies"
authors: OpenAI Codex
created: 2026-07-17
status: Implemented
reviewers: (none yet)
informed: Graith maintainers and scenario users
issue: https://github.com/d0ugal/graith/issues/1381
---

# Scenario Runtime Policies

Scenarios gain opt-in, durable timeout, bounded-retry, required-member, and
quorum policies. The daemon evaluates successful work from assigned todo and
required declared result contracts, not from process state, and keeps the
current indefinite/manual behaviour when no policy is configured.

## Background

Scenario startup is an atomic reserve/create/commit operation in
`internal/daemon/scenario.go`. A scenario and its member identities are stored
in `state.json`; assigned todo items live in `todos.sqlite` and currently drive
the computed `complete` status. Session creation and resume both pass through
the launch throttle. Runtime crashes, stopped members, and overdue work are
otherwise left to the orchestrator.

## Problem

A fan-out can remain live forever when a worker stalls or a provider disappears.
There is no durable attempt budget, no distinction between members that gate a
result and redundancy that may be lost, and no declarative point at which a
partial set of successful results is enough. An orchestrator must reconstruct
those decisions after every restart, and a naive retry loop can duplicate work
or bypass the launch throttle.

## Goals

- Preserve legacy behaviour for scenario files without policies.
- Make timeout and bounded-retry decisions durable and restart-safe.
- Define success from result contracts and support required members plus quorum.
- Surface policy progress and failure reasons through CLI and protocol status.
- Reuse session identity, worktrees, conversations, todos, and launch controls.
- Reject impossible or ambiguous policies before any session is created.

### Non-Goals

- Provider/model replacement or fallback.
- Unbounded retries or retry backoff.
- Launch throttling changes.
- Automatically deleting or stopping remaining members at quorum.

## Proposals

### Proposal 0: Do Nothing

Keep orchestrator-authored polling and retry logic. This avoids schema work but
does not survive orchestrator context loss and cannot give exactly-once bounded
retry decisions across daemon restarts.

### Proposal 1: Trigger-Based Recovery

Represent each timeout as a schedule trigger and each retry as a session action.
Triggers lack a result-contract snapshot and member attempt identity, so the
mapping would be indirect and would still need scenario-specific durable state.

### Proposal 2: Daemon-Owned Scenario Policy State (Recommended)

Add policy inputs to scenario TOML and the start/add protocol. The daemon stores
normalized policy and per-member attempt state with the scenario, reconciles it
on a fixed one-second cadence, and performs restarts through the existing
session lifecycle.

The syntax is:

```toml
[scenario.policy]
completion = "quorum"     # "all" (default) or "quorum"
quorum = 2                 # required only for quorum mode
on_exhausted = "fail"     # "wait" (default) or "fail"

[[sessions]]
name = "braw"
repo = "~/Code/croft"
task = "Review the change"

[sessions.policy]
required = true            # default true
timeout = "30m"
retries = 2                # additional attempts; default 0, maximum 10
```

An omitted `[scenario.policy]` and omitted member policies select the legacy
status calculation and no policy loop actions. Supplying either block opts the
scenario into policy semantics. `completion = "all"` means every required
member must succeed. `completion = "quorum"` additionally requires at least
`quorum` successful members; optional successes count toward that threshold,
but every required member still gates completion.

### Time and completion semantics

An initial attempt begins when the atomic scenario start has committed all
members and policy activation is durably recorded. An added member begins when
its addition commits. A retry attempt begins when the daemon durably claims the
single retry path; its deadline is frozen at that instant, before it waits for a
launch slot. Durations use wall-clock time, so daemon downtime and manual stops
consume the deadline. Output, hooks, messages, todo claims, and other activity
do not extend it.

A member succeeds when it has at least one assigned todo or required declared
result, all assigned todos are `done`, and every required result is `available`.
Optional results never gate success. A clean exit, stopped state, or zero exit
code is never success. On a reconcile tick, an already-observed success wins;
once the timeout claim is persisted, later completion belongs to the newly
claimed attempt. Completed and outstanding todo and result state remain valid
across retries.

Every policy-managed member therefore requires a non-empty, seedable task or a
required result declaration. Todo contracts are written before policy
activation; a write failure rolls back start/add so no active member can exist
without its success contract. Adding policy semantics to a legacy scenario
first verifies that each existing task member still has its immutable seeded
todo contract and rejects the opt-in if it cannot be proven.

Scenario completion is terminal and persisted. Reaching quorum does not stop or
delete the remaining sessions. With `on_exhausted = "fail"`, exhaustion of a
required member terminally fails the scenario; optional exhaustion only fails
once it makes the configured quorum mathematically unreachable. `wait` leaves
exhaustion visible for manual action.

### Retry mechanics

Timeout claims the next attempt under the scenario lock and persists the new
attempt number, immutable start/deadline, source launch generation, and pending
action before process work begins. The action uses `Restart`: it stops the old
process and resumes the same graith session, native agent conversation,
worktree, branch, token scope, and assigned todos. It neither recreates the
session nor changes model/provider.

Each successful session launch increments a durable generation. Immediately
before process work, the daemon also persists `retry_dispatched`. After restart,
an undispatched pending claim continues; an advanced generation proves the
launch completed; and a dispatched attempt with neither a generation advance
nor a durable outcome is exhausted as interrupted. This fail-closed ambiguity
rule prevents replay after a failed outcome write. A failed restart exhausts
the newly consumed attempt rather than starting an unbounded launch retry loop.
Members with retries are forced to the resumable PTY driver even when the soft
global headless default is enabled.

`gr scenario stop` suspends automatic actions before stopping members. It does
not move deadlines. `gr scenario resume` clears the suspension after manual
resumes and immediately reconciles elapsed deadlines. Scenario deletion removes
the durable claim; every action rechecks membership after lifecycle work so a
delete during retry cannot recreate or mutate the scenario. Retry lifecycle
work is serialized per scenario, and its stop path escalates through a bounded
SIGTERM/SIGKILL wait, so a wedged member cannot block commands for other
scenarios indefinitely.

The policy loop propagates daemon cancellation through its scenario gate,
per-session launch gate, launch throttle, and final pre-spawn check. This keeps
shutdown from launching a retry after `StopAll` has taken its process snapshot.
If restart finds a durable reserve record that never reached policy activation,
it records a visible terminal startup failure instead of attempting to complete
or retry a partial fleet.

Adding a member with any policy setting opts a legacy scenario into runtime
policy semantics. Existing members become required without timeouts. The add
operation creates an owned member; shared membership is only resolved during
atomic scenario startup.

### Validation and status

Validation runs in both the strict TOML parser and daemon preflight. It rejects
unknown completion/exhaustion values, quorum outside `1..member_count`, quorum
below the required-member count, quorum supplied outside quorum mode, no
required members in all mode, retries outside `0..10`, retries without timeout,
non-positive timeouts, timeouts below the one-second scheduler resolution,
missing or over-limit task contracts when no required result is declared, and
timeout/retry policy on shared members the scenario does not own.

Protocol and JSON status include the normalized policy, terminal outcome and
reason, success/quorum counts, and for each member its required flag, attempt
budget, immutable timestamps, retry-pending flag, success timestamp, and
exhaustion reason. Human `status` and `list` render the same progress compactly.

## Other Notes

### References

- `internal/daemon/scenario.go` — atomic scenario lifecycle and status.
- `internal/daemon/session_control.go` — restart preserving identity/worktree.
- `internal/daemon/launch.go` — shared launch concurrency control.
- `internal/daemon/todostore.go` — assigned result contracts.
- `docs/design/2026-06-22-scenarios.md` — original scenario design.

### Implementation Notes

State v21 adds launch-generation and normalized policy fields after v20's
scenario-member mirror state. Existing sessions receive a non-zero launch
generation; all earlier scenario fields survive unchanged and existing
scenarios remain legacy-policy records. Policy activation happens
only after successful startup. A reserve record left inactive by abrupt daemon
termination is recovered as a terminal startup failure and is never reconciled
as a live policy. The one-second scheduler uses the daemon's injectable loop
clock. Todo retention excludes active policy scopes until their outcome is
observed and durably recorded.

### Testing

Pure validation and policy evaluation tests use fixed clocks. Lifecycle tests
cover initial deadlines, retry claims, launch-generation crash windows,
completion/timeout ordering, required and optional exhaustion, quorum edges,
manual stop/resume, delete during retry, daemon restart, partial startup, and
legacy status. Protocol manifests, CLI parsing/rendering, state migration,
capability generation, race tests, and integration coverage complete the change.
