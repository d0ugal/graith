---
title: "Design Doc: Session Event Stream"
authors: Codex
created: 2026-07-29
status: Implemented (v1)
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/109
---

# Session Event Stream

`gr events` provides a live JSON stream for supervisors that need to react when
sessions change state or publish coordination messages. It replaces polling
`gr list` for common orchestration loops while keeping private inbox messages
out of the global stream.

## Background

The daemon already observes session state from multiple places: hook reports,
PTY status detection, process-exit watchers, and delete paths. Messaging has a
durable SQLite store plus per-stream subscribers, but there is no single stream
that reports "something changed" across sessions. Supervisors therefore poll
`gr list` and occasionally pair it with `gr msg sub`.

## Problem

Polling wastes work and adds latency for supervisory agents. The cost is visible
in scenario-like workflows that only need to know when a session becomes ready,
stops, is deleted, or publishes a public topic message.

## Goals

- Add a daemon-backed `gr events` command for real-time event consumption.
- Emit machine-readable events for agent status changes, lifecycle status
  changes, soft deletion, and public topic messages.
- Avoid leaking direct inbox message bodies through a global stream.
- Keep the stream best-effort and non-durable; missed events can still be
  recovered by reading `gr list` or message history.

### Non-Goals

- Durable event replay or cursor management.
- A native GUI event-stream surface.
- Streaming private inbox messages; use `gr msg inbox --wait/--follow` for
  direct messages.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | Supervisory agents and scripts consume this stream. |
| iOS | Excluded | Native apps already poll their own fleet model and should not expose orchestration-only topic bodies in v1. |
| macOS | Excluded | Same as iOS; GUI support can be added later if the fleet model needs push updates. |

## Proposals

### Proposal 0: Do Nothing

Keep requiring `gr list` polling and ad hoc message subscriptions. This avoids
new protocol surface, but it leaves the latency and wasted-work problem
unsolved.

### Proposal 1: In-Memory Event Bus (Recommended)

Add an in-memory pub/sub bus owned by `SessionManager`. State-transition paths
publish compact `EventMsg` values after the relevant state mutation is durable.
The new `events_sub` control message registers the connection as a subscriber
and streams `event` envelopes until the client detaches or disconnects.

Events are best-effort. Slow subscribers may miss bursts rather than blocking
daemon lifecycle work. The authoritative state remains `gr list`, and durable
message history remains `gr msg sub`.

The stream is deliberately fleet-wide for authenticated users and session
tokens. That matches the existing visibility of `gr list` plus public
`gr msg sub` topics, while direct inboxes and `_system.*` topics remain excluded
from the global stream.

### Proposal 2: Reuse Message Store System Streams

Lifecycle events could be persisted into `_system.*` topics and consumed through
`gr msg sub --follow`. That reuses existing code, but it makes transient state
changes durable chat messages, overloads message retention policy, and makes it
harder to present a typed event shape without parsing message bodies.

## Other Notes

### References

- Issue: https://github.com/d0ugal/graith/issues/109
- `internal/daemon/session_detection.go` for PTY-derived agent status changes.
- `internal/daemon/handler_messaging.go` for topic message publication.
- `internal/daemon/session_watch.go` and `session_delete.go` for lifecycle
  transitions.

### Testing

Focused tests cover subscribing, delivery of status and message events, direct
lifecycle transitions without PTY watchers, private inbox and `_system.*`
filtering, detach cleanup, protocol registration, authorization matrix
coverage, CLI rendering, and docs for the new command.
