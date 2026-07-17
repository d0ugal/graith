---
title: "Design Doc: Message Reply Expectation"
authors: OpenAI Codex
created: 2026-07-17
status: Implemented
reviewers: (pending implementation review)
informed: Graith maintainers and messaging clients
issue: https://github.com/d0ugal/graith/issues/1374
---

# Message Reply Expectation

Graith messages may explicitly declare that no reply is expected, allowing
one-way reports and hand-offs to avoid presenting a misleading reply command.
The declaration is durable message metadata and is separate from reply routing,
notification suppression, and daemon-authored system identity.

## Background

`gr msg send` and `gr msg pub` publish a `MsgPubMsg` through the daemon into the
SQLite-backed message store. Direct inbox delivery normally injects a PTY hint
which includes a `gr msg send` reply command when the sender is a session. A
stopped recipient is resumed and receives unread inbox context through its
startup hook. Messages already carry `reply_to` routing metadata and direct
sends accept `--quiet` to suppress the immediate notification.

Daemon-authored notifications are identified separately as system messages.
Their hints omit a reply command because the synthetic sender is not an
addressable session.

## Problem

Some ordinary session messages are deliberately one-way. A scheduled session
may report to its parent and then delete itself, making the ordinary reply hint
both misleading and unusable. Neither existing field expresses intent:
`reply_to` says where a reply should go if one is sent, while `quiet` controls
whether a recipient is notified at all.

## Goals

- Let senders explicitly mark direct and topic messages as needing no reply.
- Preserve that choice through the protocol, SQLite, inbox/topic reads, JSON,
  MCP, and required Swift protocol models.
- Give running and resumed recipients clear `No reply expected` wording and no
  reply command for marked messages.
- Preserve existing delivery, notification, and auto-resume behavior.
- Keep omitted metadata fully backward compatible.

### Non-Goals

- Adding relay, forwarding, presentation, or acknowledgement semantics.
- Inferring reply expectation from message body, sender lifetime, `reply_to`,
  `quiet`, or topic choice.
- Replacing the system-message identity marker.

## Proposals

### Proposal 0: Do Nothing

One-way senders could put instructions in every body and recipients could learn
to ignore the generated reply hint. This leaves contradictory injected context
in place and continues to encourage replies to ephemeral sessions.

### Proposal 1: Optional `no_reply` Metadata (Recommended)

Add `no_reply: true` to the publish wire shape and durable message shape, exposed
by `--no-reply` on both `gr msg send` and `gr msg pub`. The SQLite column is a
non-null integer with a false default; opening an older database adds it with
that default. Required Swift types and MCP publish/read shapes model the same
optional semantic.

False and absent values retain existing behavior. A true value suppresses the
reply command and adds `No reply expected` in direct PTY hints, resumed-session
inbox context, and human-readable inbox/topic output. It does not suppress
delivery, notification, or auto-resume. `reply_to` remains legal alongside the
field because it supplies a route without asserting that a response is wanted.

System identity remains an independent reason not to offer a reply path. System
messages therefore need not set `no_reply`; their existing identity-based
rendering continues to apply. Likewise, `quiet` may be combined with
`no_reply`: the former suppresses the immediate PTY notification while the
latter remains visible when the stored message is later read.

The trade-off is a schema and cross-client protocol addition. The boolean is
small, additive, omittable on the wire, and defaults safely for existing rows,
making that cost preferable to overloading an unrelated field.

### Proposal 2: Positive `reply_expected` Metadata

A positive field reads naturally but makes zero-value compatibility awkward:
an omitted Go boolean would mean no reply expected, reversing current behavior.
It would require a pointer/tri-state representation throughout the pipeline or
special decoding rules. `no_reply` makes the existing behavior the zero value.

## Other Notes

### References

- Issue [#1374](https://github.com/d0ugal/graith/issues/1374)
- `internal/protocol/messages.go` — publish and conversation wire shapes
- `internal/daemon/msgstore.go` — durable message schema and reads
- `internal/daemon/notify.go` — running and resumed recipient hints
- `internal/cli/check_inbox.go` — resumed-session inbox context

### Implementation Notes

The database migration must run after creating the base schema and must preserve
all existing rows as replyable. Store queries and conversation projections must
select the new column consistently. The publish response and subscription
frames already serialize the stored message type, so extending that type exposes
the value without a new control message or authorization-policy row.

### Testing

Cover default and explicit no-reply publish round trips, old-database migration,
inbox and conversation reads, CLI/MCP/Swift JSON shapes, system messages, quiet
messages, and both live-PTY and resumed-session hint formatting. Run protocol
fixture generation, Go package/race/integration suites, shared Swift tests, docs
build, vet, and lint.
