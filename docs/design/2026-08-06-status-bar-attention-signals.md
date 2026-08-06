---
title: "Design Doc: Status Bar Attention Signals"
authors: Codex
created: 2026-08-06
status: Implemented
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/2085
---

# Status Bar Attention Signals

Graith's status bar should surface compact, persistent signals for user action
without becoming a dashboard. This design adds two high-priority signals:
unreleased jailed PR comments and an orchestrator-controlled attention request.

## Background

Attached clients already poll `status` and receive the current session plus a
daemon-wide `FleetSummary`. The status bar renders fleet counts on the right,
after current-session state and PR/CI information. PR-watch already quarantines
untrusted PR comments in the message store and exposes them through
`gr msg jail`. The orchestrator can send inbox messages and push notifications,
but neither gives a compact persistent indicator while the user is working in a
different session.

## Problem

Jailed PR comments require user or orchestrator action, but they are easy to
miss once the initial system inbox notice ages out. The orchestrator also needs
a low-friction way to say "come back here" that remains visible from any
attached session, clears when the user arrives, and gives the orchestrator a
fresh prompt only when the request is stale.

## Goals

- Show unreleased jailed PR comments in the status bar with warning treatment.
- Let the orchestrator set a short status-bar attention request with optional
  context.
- Clear the request when the user attaches to the requesting orchestrator.
- Send one stale-arrival inbox prompt to the orchestrator, without repeated
  prompts on later attaches.
- Keep narrow terminals readable by prioritizing urgent/actionable signals.

### Non-Goals

- Replace `gr msg jail list/show/release`; the status bar is only a pointer.
- Add a general dashboard of every daemon health condition.
- Add native GUI controls for jail release or orchestrator attention in this
  iteration.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | The terminal status bar and `gr attention` are CLI control-plane features. |
| iOS | Excluded | Native apps do not render the terminal status bar and do not manage jailed comments. |
| macOS | Excluded | Same as iOS for the native app; terminal users get the CLI behavior. |

## Proposals

### Proposal 0: Do Nothing

The jail remains discoverable only through inbox messages and `gr msg jail`.
The orchestrator continues to rely on transient notifications or ordinary
messages. This does not solve the "user returned much later" case.

### Proposal 1: Extend FleetSummary With Action Signals (Recommended)

Add metadata-only action fields to `FleetSummary`: jail count plus newest
author/repo/PR, and the current orchestrator attention text. Attached clients
already poll `status`, so every attached session sees the same global warning
without a second refresh loop.

The orchestrator sets or clears attention with `orchestrator_attention`, exposed
as `gr attention`. The daemon stores one optional request in state. A successful
attach to the requesting orchestrator clears it. If its age is at least five
minutes, the daemon publishes one system inbox prompt: "You had an outstanding
request for the user. They have arrived." The clear happens before publication,
so repeated attaches do not spam the orchestrator.

Right-side status-bar compaction is priority ordered:

1. Orchestrator attention, red/bold.
2. Jailed comments, yellow/bold; red/bold when the count reaches five.
3. Fleet health counts.
4. Current-session unread inbox count.

Full-width bars show context such as attention text and newest jailed author/PR.
Compact modes drop ordinary fleet text first, then lower-priority action
signals, preserving the strongest actionable token when possible.

### Proposal 2: Poll Jail and Attention Separately

A separate client request could fetch richer status-bar data. That adds another
periodic daemon call for every attached client and more protocol surface while
duplicating the existing status refresh path. It also makes native clients more
likely to drift from the CLI.

## Other Notes

### References

- `internal/client/statusbar.go`
- `internal/daemon/handler.go`
- `internal/daemon/jailstore.go`
- `internal/protocol/messages.go`
- `docs/design/2026-07-13-pr-comment-jail-design.md`

### Implementation Notes

The jail summary query is count/newest metadata only and never includes comment
bodies. The attention request is additive optional state, so no state migration
is needed. The freshness threshold is centralized as
`orchestratorAttentionStaleAfter`.

### Alternatives considered

- **Unread inbox/system notices:** already surfaced per attached session as a
  count; fleet-wide unread counts would be noisy and ambiguous.
- **Failed trigger polls or paused triggers:** useful, but they need a clearer
  severity model; defer to trigger status/doctor for now.
- **Daemon reload/restart failures:** errors already return to the command that
  caused them; persistent surfacing needs an ownership model.
- **Sessions waiting for user input:** promising future signal, but hook
  semantics vary by agent and need more design to avoid false positives.
- **CI failures/merge conflicts:** already shown on session PR badges and in the
  Session Navigator; keep them session-local.
- **Stale/lost sessions after daemon restart:** better suited to `gr doctor`.
- **Exporter/observability health:** only relevant when configured and too broad
  for the status bar.

### Testing

Unit coverage should include jail summary aggregation, attention set/clear
authorization, stale/fresh attach clearing, one-shot stale-arrival prompts, and
status-bar rendering/compaction/color selection.
