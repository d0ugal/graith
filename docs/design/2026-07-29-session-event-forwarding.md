---
title: "Design Doc: Session Event Forwarding"
authors: Dougal Matthews
created: 2026-07-29
status: Implemented (v1)
reviewers: Issue #1646 decision
informed: Graith CLI users coordinating child sessions
issue: https://github.com/d0ugal/graith/issues/1646
---

# Session Event Forwarding

Graith should add an opt-in rule that lets a parent session receive selected
lifecycle events from one of its direct children. The first event class is `ci`,
forwarding the PR-watch loop's aggregate CI state without adding any extra
GitHub polling or mutating the parent's own PR/CI badge.

## Background

The PR-watch loop already resolves a session's GitHub PR, computes aggregate CI
state, updates runtime display metadata, and sends useful inbox notifications to
the owning session. Coordinating parent sessions do not see that feedback unless
the child explicitly reports it back.

The session tree is also an authorization boundary. A parent may act on its
descendants, but forwarding every descendant's events upward would flood fleet
control sessions and blur source identity.

## Problem

Coordinating agents often spawn child sessions and then wait for completion or
review. A child can receive CI transitions after it stops, while the parent that
owns coordination never sees them. A CI-only propagation flag would solve one
case but would not scale to future review, PR, or lifecycle events.

## Goals

- Provide a generic event-name allowlist rather than one flag per event type.
- Forward exactly one hop from a followed direct child to its current direct
  parent.
- Prevent the config-managed system orchestrator from following child events.
- Persist follow rules across daemon restart and remove them when the source
  session is purged.
- Preserve source attribution and daemon-authored message identity.
- Reuse the existing PR-watch data and inbox notification path.

### Non-Goals

- Forwarding raw GitHub webhook/check-run payloads.
- Cascading forwarded events to grandparents.
- Forwarding untrusted PR comment bodies or changing the existing jail/trust
  boundary.
- Adding iOS or macOS management UI in v1.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | The feature is a coordination primitive for agent sessions and needs creation-time plus management commands. |
| iOS | Excluded | Native mobile does not manage agent-created session trees today. |
| macOS | Excluded | The v1 control surface is CLI-only; native apps still observe ordinary inbox messages. |

## Proposals

### Proposal 0: Do Nothing

Keep child CI notifications local to the child. This leaves parent coordinators
blind to feedback and encourages one-off flags such as `--propagate-ci`.

### Proposal 1: Direct-Child Follow Rules (Recommended)

Store one durable follow rule per source child session. The rule contains only
the event allowlist; the destination is always resolved from the child's current
`ParentID` at delivery time. This makes reparenting explicit: a rule moves to a
new non-orchestrator direct parent, and it is disabled by deletion if the child
is orphaned or reparented under the system orchestrator.

Rules are created atomically during `gr new --follow-events=ci` by reserving the
rule with the child session placeholder before the agent process is launched.
For existing children, `gr events follow`, `gr events unfollow`, and
`gr events following` manage the same state. Only the direct parent session or
the local user may change a relationship.

The `ci` event is emitted from the PR-watch loop after a poll has already fetched
CI data. Deduplication keys the aggregate CI observation by source session, PR
number, head SHA, and aggregate state, so duplicate polls or webhook kicks do not
produce duplicate parent messages. The first observation after registration may
forward the current aggregate state, including `pending`; this intentionally uses
the parent's normal inbox notification path rather than PR-watch's user-facing
notification rate limit because the parent explicitly opted in. The message is
daemon-authored, identifies the source child and PR, includes the head SHA,
aggregate counts, and failing checks, and is terminal: inbox messages are not
themselves eligible for forwarding.

### Proposal 2: Store Explicit Parent Targets

Persist both source and destination session IDs. This makes ownership obvious,
but reparenting can leave stale targets unless every parent update rewrites or
deletes rules. Resolving the current parent from the source session avoids stale
targets and better matches the live session tree.

## Other Notes

### Implementation status

V1 is implemented on top of the daemon event stream introduced by issue #109.
The forwarding rule uses that stream only for structured observation; the
parent-facing agent notification is still the normal daemon-authored inbox path.

### References

- Issue #1646.
- Issue #109.
- `internal/daemon/prwatch.go` for existing PR/CI awareness and deduplication.
- `internal/daemon/notify.go` for daemon-authored inbox delivery.
- `docs/design/2026-06-25-pr-ci-awareness-design.md`.
- `docs/design/2026-07-11-pr-comment-author-trust-design.md`.

### Testing

Coverage should exercise event-name validation, registration, removal, listing,
orchestrator exclusion, one-hop/no-cascade delivery, duplicate suppression,
restart persistence, reparent move-or-disable policy, delete cleanup, and source
attribution in the forwarded CI message.
