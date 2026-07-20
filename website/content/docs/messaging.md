---
weight: 1200
title: "Inter-Agent Messaging"
description: "Send messages between running agents."
icon: "forum"
toc: true
draft: false
---

graith's SQLite-backed messaging connects sessions, sessions and the user, and agents in a hierarchy.

## Concepts

**Streams** are named message channels; every message belongs to one. Two types:

- **Topic streams** -- created explicitly with `gr msg pub --topic <name>`. Any session can publish or subscribe.
- **Inbox streams** -- named `inbox:<session-id>`, created automatically per session. Used for direct messages with `gr msg send`.

**Subscribers** track read position per stream, identified by `GRAITH_SESSION_ID`; unread counts are per-subscriber.

**Threads** group related messages within a stream. Pass `--thread <id>` to `pub`/`send` to continue a thread, to `sub` to filter.

**System streams** are prefixed `_system.` for status and daemon notifications, hidden from `gr msg topics` unless `--system` is passed.

## Publishing to topics

```bash
gr msg pub --topic code-review "Found a race condition in handler.go:245"
gr msg pub --topic build-results --file ./test-output.txt
gr msg pub --topic updates --no-reply "Morning report is ready"
```

Any session can publish to any topic. The sender is auto-detected from `GRAITH_SESSION_ID` and `GRAITH_SESSION_NAME`. Outside a graith session, `sender_name` is empty and `sender_id` becomes `pid:<pid>`.

## Direct messaging

```bash
gr msg send fix-auth-bug "the tests are green now, rebase on main"
gr msg send fix-auth-bug --file ./review-notes.md
gr msg send fix-auth-bug --quiet "silent context update"
gr msg send --no-reply --parent "Morning briefing complete"
```

`send` writes to the target's inbox stream (`inbox:<session-id>`) and types a notification into its PTY. `--quiet` skips the notification; the message still reaches the inbox.

`--no-reply` marks a one-way message. It's delivered and resumes a stopped
recipient normally, but recipient hints say **No reply expected** and suggest no
reply command. The choice is stored with the message, appears as
`"no_reply": true` in JSON, and works with topic publishes too.

`--no-reply`, `--reply-to`, and `--quiet` are independent: reply-expectation, a
route if someone does respond, and PTY suppression respectively. Daemon-authored
system notices have a separate automated identity and omit reply suggestions
without relying on `no_reply`.

### Tree messaging

```bash
gr msg send --children "rebase on main and re-run tests"
gr msg send --parent "tests are green, ready for review"
```

`--children` sends to all descendants, `--parent` to the parent. Both auto-detect the current session from `GRAITH_SESSION_ID`.

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

- Default: returns unread and exits; prints nothing if none.
- `--wait`: blocks until a message arrives, then exits.
- `--follow`: streams messages as they arrive, indefinitely.
- `--ack`: marks all returned messages as read.
- `--all`: returns all messages, not just unread.

## Acknowledging

```bash
gr msg ack --topic code-review
```

Marks every message in the stream as read for the current subscriber.

## Listing topics

```bash
gr msg topics
gr msg topics --system   # include _system.* streams
```

Shows each stream with total and unread message counts.

## Threading

Threads structure conversations within a stream:

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

In JSON output (`--json` or agent mode):

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
  "no_reply": true,
  "created_at": "2026-06-17T10:30:00Z"
}
```

`no_reply` is omitted when false, preserving the default that an ordinary message is replyable.

## From the GUI

The macOS and iOS apps send to and read a session's inbox without the CLI:

- **macOS** — right-click a session in the sidebar and choose **Messages…**.
- **iOS** — open a session and pick **Messages** from the toolbar menu.

The Messages view shows the direct-message conversation (received and sent), a
compose field, and a **mark-as-read** action that acks the inbox. System notices
(PR/CI notifications) are marked *automated* so they read distinctly from
session/human messages.

Topic publish/subscribe (`gr msg pub` / `gr msg sub`) stays CLI-only for now.

## Retention

Configure message retention in `config.toml`:

```toml
[messages]
max_age        = "7d"   # prune messages older than 7 days
max_per_stream = 1000   # keep at most 1000 messages per stream
```

Both are optional; unset keeps messages indefinitely. An empty `max_age` or an
explicit `"0"` is the "retain forever" sentinel. A non-empty value that doesn't
parse, or a negative duration, is rejected at config load and reload — a typo
mustn't silently disable cleanup and let messages and jailed comments grow unbounded.

## Operational limits

The `[messages]` table also exposes the message log's operational limits. Each
key is optional and falls back to the default shown; a value above its hard ceiling
is rejected at config load.

```toml
[messages]
conversation_page_size = 500   # page size when a conversation request omits a limit (default 500)
conversation_max_limit = 2000  # hard cap on messages a single conversation sorts (default 2000; ceiling 100000)
jail_list_limit        = 2000  # max quarantined comments a jail listing returns (default 2000; ceiling 100000)
subscriber_buffer      = 64    # per-subscriber pub/sub channel capacity (default 64; ceiling 65536)
busy_timeout           = "5s"  # SQLite busy/operation timeout for the messages DB ("" => 5s; explicit value must be 1ms–5m)
```

Lowering `conversation_max_limit` alone is always safe: the effective page size
clamps down to the new maximum. Only an *explicit* `conversation_page_size` larger
than `conversation_max_limit` is rejected at load, as a contradiction of intent.
The conversation paging bounds and jail cap are read per request, so changes take
effect on the next `gr daemon reload`. `subscriber_buffer` and `busy_timeout` are
fixed when the database opens, so they're **restart-only** (`gr daemon restart`).
SQLite's `busy_timeout` pragma has **millisecond resolution**, so a positive value
below `1ms` is rejected at load; otherwise it would collapse to `busy_timeout(0)`
and disable lock waiting entirely.

> **Note.** Only `subscriber_buffer` is exposed here (bursty fan-out is a real
> load-tuning case). Other internal channel capacities (daemon control kicks,
> signal-request buffers) stay code-owned and aren't configurable.

## Patterns

See [Agent-to-agent communication]({{< relref "patterns/communication.md" >}}) for messaging patterns: pub/sub broadcast, request/reply, coordination barriers, and hierarchical communication.
