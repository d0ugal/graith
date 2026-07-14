---
weight: 1200
title: "Inter-Agent Messaging"
description: "Send messages between running agents."
icon: "forum"
toc: true
draft: false
---

graith includes a SQLite-backed messaging system that enables communication between sessions, between sessions and the user, and between agents in a hierarchy.

## Concepts

**Streams** are named message channels. Every message belongs to a stream. Two types:

- **Topic streams** -- created explicitly with `gr msg pub --topic <name>`. Any session can publish or subscribe.
- **Inbox streams** -- named `inbox:<session-id>`, created automatically for each session. Used for direct messages with `gr msg send`.

**Subscribers** track read position per stream. Each session is identified as a subscriber by its `GRAITH_SESSION_ID`. Unread counts are per-subscriber.

**Threads** group related messages within a stream. Pass `--thread <id>` to `pub`/`send` to continue a thread, and `--thread <id>` to `sub` to filter.

**System streams** are prefixed with `_system.` and used internally (e.g. for approval notifications). They are hidden from `gr msg topics` unless `--system` is passed.

## Publishing to topics

```bash
gr msg pub --topic code-review "Found a race condition in handler.go:245"
gr msg pub --topic build-results --file ./test-output.txt
```

Any session can publish to any topic. The sender is auto-detected from `GRAITH_SESSION_ID` and `GRAITH_SESSION_NAME`. When run outside a graith session, `sender_name` is empty and `sender_id` is set to `pid:<pid>` (the process ID).

## Direct messaging

```bash
gr msg send fix-auth-bug "the tests are green now, rebase on main"
gr msg send fix-auth-bug --file ./review-notes.md
gr msg send fix-auth-bug --quiet "silent context update"
```

`send` writes to the target session's inbox stream (`inbox:<session-id>`) and types a notification into the session's PTY by default. Use `--quiet` to skip the PTY notification (the message is still delivered to the inbox).

### Tree messaging

```bash
gr msg send --children "rebase on main and re-run tests"
gr msg send --parent "tests are green, ready for review"
```

`--children` sends to all descendant sessions. `--parent` sends to the parent session. Both auto-detect the current session from `GRAITH_SESSION_ID`.

## Subscribing

```bash
# Read unread messages
gr msg sub --topic code-review

# Read all messages (not just unread)
gr msg sub --topic code-review --all

# Read and acknowledge
gr msg sub --topic code-review --all --ack

# Block until a message arrives
gr msg sub --topic code-review --wait

# Stream continuously
gr msg sub --topic code-review --follow

# Filter to a specific thread
gr msg sub --topic code-review --thread abc123

# Read inbox
gr msg inbox --all --ack
```

### Behavior

- Default: returns unread messages and exits. If no unread messages, prints nothing.
- `--wait`: blocks until at least one message arrives, then exits.
- `--follow`: blocks and streams messages as they arrive, indefinitely.
- `--ack`: marks all returned messages as read.
- `--all`: returns all messages, not just unread.

## Acknowledging

```bash
gr msg ack --topic code-review
```

Marks all messages in the stream as read for the current subscriber.

## Listing topics

```bash
gr msg topics
gr msg topics --system   # include _system.* streams
```

Shows each stream with its total message count and unread count.

## Threading

Threads allow structured conversations within a stream:

```bash
# Start a thread
gr msg pub --topic design "Proposal: new API endpoint for /users"

# Continue the thread (use the message ID from the first message as thread ID)
gr msg pub --topic design --thread msg_abc123 "I agree, but we should add pagination"

# Read only messages in a thread
gr msg sub --topic design --thread msg_abc123

# Set up a reply channel
gr msg send worker-1 "Please review this change" --reply-to review-results
# worker-1 can then publish results to the review-results topic
```

## Message format

In JSON output (`--json` or agent mode), messages have this structure:

```json
{
  "id": "msg_abc123",
  "seq": 1,
  "stream": "code-review",
  "body": "Found a race condition in handler.go:245",
  "sender_id": "session-uuid",
  "sender_name": "fix-auth-bug",
  "thread_id": "",
  "reply_to": "",
  "created_at": "2026-06-17T10:30:00Z"
}
```

## Retention

Configure message retention in `config.toml`:

```toml
[messages]
max_age        = "7d"   # prune messages older than 7 days
max_per_stream = 1000   # keep at most 1000 messages per stream
```

Both are optional. When unset, messages are kept indefinitely.

## Patterns

See [Agent-to-agent communication]({{< relref "patterns/communication.md" >}}) for detailed messaging patterns including pub/sub broadcast, request/reply, coordination barriers, and hierarchical agent communication.
