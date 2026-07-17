---
title: "Design Doc: Scenario Completion Actions and Cleanup"
authors: OpenAI Codex
created: 2026-07-17
status: Implemented
reviewers: (pending review)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1379
---

# Scenario Completion Actions and Cleanup

This change makes todo-derived scenario completion a durable trigger source and
adds an opt-in, delayed lifecycle policy. Reporting, archival, and synthesis can
run once per completion epoch, with recoverable cleanup gated on action and
required-delivery success.

## Background

Scenario status is derived in `internal/daemon/scenario.go` from the todo items
assigned to each member. Todo mutations publish best-effort messages, while the
trigger engine has only schedule and file-watch sources. Embedded trigger
definitions and trigger runtime survive daemon restart, and session cleanup is
already recoverable through soft deletion.

Trigger actions presently finish at different points. Commands and messages
deliver synchronously, while a session action is considered dispatched once its
agent is spawned and asks that agent to perform any configured delivery. That
is sufficient for ordinary scheduled automation, but not for cleanup which must
wait for the final work and its required output.

## Problem

A scenario becoming complete has no durable edge event. A coordinator must poll,
run final work, wait for delivery, and then stop or delete members. Crashes can
lose those steps or repeat them. Immediate teardown would also risk destroying
diagnostic state, interrupting output delivery, or touching sessions the scenario
does not own.

## Goals

- Define an idempotent complete edge and a new epoch after reopen/recompletion.
- Treat todo notifications only as hints and reread authoritative todo state.
- Persist action and cleanup state before every externally visible transition.
- Reuse normal trigger actions and delivery routes inside scenario boundaries.
- Gate cleanup on terminal actions and required delivery.
- Use stop plus soft delete, respecting shared, starred, system, and ownership
  boundaries, with cleanup disabled unless explicitly configured.
- Make interrupted and failed work visible and retryable.

### Non-Goals

- A tribunal-specific synthesis verb or action type.
- Destructive purge, automatic unstar, or cleanup of arbitrary descendants.
- Exactly-once execution of an arbitrary external command. A crash after the
  command side effect but before its terminal state is persisted is recorded as
  an interrupted failure and requires an explicit retry; it is never silently
  replayed.

## Proposals

### Proposal 0: Do Nothing

Keep todo events best-effort and require a permanent coordinator. This leaves
restart loss, duplicate polling, and unsafe teardown unsolved.

### Proposal 1: Durable completion epochs on the scenario (Recommended)

Scenario files gain an embedded completion source:

```toml
[[trigger]]
name = "archive"

[trigger.completion]
event = "complete"
session = "reporter" # execution/mirror context; required for command actions

[trigger.action]
type = "command"
command = "./scripts/archive-report"

[trigger.action.deliver]
store = "shared:reports/{scenario_name}-{completion_epoch}.md"
required = true
```

`completion` is legal only on an embedded scenario trigger. Its optional
`session` names a non-shared member and supplies the source session/worktree for
command or mirrored session actions. Literal inbox targets continue to be
limited to scenario members or the orchestrator. Global completion triggers,
scenario/tracker actions, and external action repos are rejected.

The scenario lifecycle block is separate from any one action:

```toml
[scenario.lifecycle]
cleanup = "on_success" # omitted/"off" (default), "on_success", or "always"
delay = "30m"
```

The daemon stores an observed-complete bit, monotonically increasing epoch,
per-trigger action records, and a cleanup record on `ScenarioState`. A todo
mutation wakes the reconciler but does not assert completion. The reconciler
queries the todo store again, compares the result with the persisted observed
bit, and commits a new epoch with pending actions before dispatch.

Each action transitions `pending -> running -> succeeded|failed`, with each
transition saved atomically. A daemon restart resumes pending work. A non-session
action left running is marked as an interrupted failure instead of replayed,
which avoids duplicate external effects. A completion-spawned session carries
the scenario/epoch/action/attempt key in its durable creation reservation; after restart
the daemon can adopt it and finish the action when that session reaches a clean
terminal state. `gr trigger run scenario:<id>:<name>` explicitly retries a failed
action in the current complete epoch.

Required command/message delivery is synchronous and delivery errors fail the
action. A required session delivery adds a must-deliver-before-exit instruction;
the completion action remains running until the spawned session exits cleanly.
Cleanup cannot be scheduled before all actions are terminal. `on_success`
requires all actions to succeed, while `always` requires only terminality.

Reopening cancels pending/running work and any scheduled cleanup for that epoch;
recompletion creates the next epoch and a fresh action set. Manual scenario stop
does the same before stopping members. Manual scenario delete cancels first,
then follows its existing explicit lifecycle semantics. Daemon shutdown leaves
durable in-flight state for startup recovery rather than treating shutdown as a
user cancellation.

At the cleanup deadline the daemon rechecks that the same epoch is still
complete, then calls soft delete for only the scenario's non-shared member IDs
whose `ScenarioID` still matches. It never unstars and never follows parent or
trigger ownership edges. Soft deletion stops running agents and preserves their
state/worktrees for restore; retention disabled is a cleanup failure, not a
fallback to purge.

`ScenarioRecord` reports the epoch, action records, and cleanup state. Trigger
history also records the completion cause. CLI status renders these fields, and
the required Swift protocol model decodes them for GUI status consumers.

### Proposal 2: Convert completion into a synthetic schedule

A short schedule could poll scenario status and invoke the existing executor.
It would blur edge epochs with polling instants, consume rate-limit budget, and
still need a second durable state machine for cleanup. It also cannot prevent a
polling race from firing after reopen.

### Proposal 3: Add a dedicated synthesis action

A daemon-specific synthesis verb could spawn a particular agent and archive its
answer. That couples lifecycle to one workflow and duplicates the existing
command/session/message vocabulary. Completion should be a source and gate, not
a special action.

## Other Notes

### References

- `docs/design/2026-07-11-triggers-design.md`
- `docs/design/2026-07-16-todo-list.md`
- `docs/design/soft-delete.md`
- `internal/daemon/todo.go` — mutation hints
- `internal/daemon/trigger.go` — shared executor and status
- `internal/daemon/scenario.go` — authoritative derived status

### Implementation Notes

The persisted schema is additive but receives a state-version migration so an
older daemon fails closed on downgrade. Completion variables add
`{scenario_id}`, `{scenario_name}`, and `{completion_epoch}`. The executor must
not hold the session-manager lock across todo queries, commands, process waits,
message/store delivery, or soft deletion.

### Testing

Tests cover every persisted transition, restart adoption/interruption, duplicate
hints, reopen/recompletion, explicit retry, delayed cleanup, required-delivery
failure, manual cancellation, retention zero, and ownership protections for
shared, starred, system, replaced, descendant, and unrelated trigger sessions.
Protocol manifest, Swift decoding, CLI rendering, capability generation, race
tests, and integration coverage protect the cross-layer behavior.
