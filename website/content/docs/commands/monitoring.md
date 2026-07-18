---
weight: 420
title: "Monitoring & interaction"
description: "Inspect sessions and drive a running session remotely."
icon: "monitoring"
toc: true
draft: false
---

## Information and monitoring

### `gr list` (alias: `ls`)

List all sessions with status.

| Flag | Description |
|------|-------------|
| `--repo <path>` | Filter by repo path |
| `--tree` | Show parent-child hierarchy |
| `--children <name-or-id>` | Filter to descendants of a session |
| `--starred` | Show only starred sessions |
| `--wide` | Show all columns, including per-session token usage |
| `--tokens` | Show the detailed token-usage projection and aggregate totals |

The `--wide` view adds a **Tokens** column with the compact total token usage
for each session's current agent (a trailing `~` marks an approximate count).

Use `--tokens` for the detailed input, output, cache-read, cache-write, other,
and total counts:

```console
$ gr ls --tokens
SESSION  REPO    AGENT   INPUT   OUTPUT  CACHE-R    CACHE-W  OTHER  TOTAL      COUNTED
braw     graith  claude  12,431  48,209  1,204,882  96,004   0      1,361,526  8s ago
canny    graith  codex   69,131  3,517   756,224    0        0      828,872    11s ago
TOTAL                     81,562  51,726  1,961,106  96,004   0      2,190,398  2/2 known
```

The detailed projection composes with the normal list selection flags, including
`--repo`, `--children`, `--starred`, `--deleted`, and `--tree`. `--quiet` and
`--wide` are each mutually exclusive with `--tokens` because all three select
an output projection.

Counts reflect the session's **current agent** and are updated by a background
poll, so they normally lag by up to the configured poll interval (30 seconds by
default). **Counted** is the age of the last successful observation; if a later
poll cannot read a transcript, the last count is retained and its age continues
to grow rather than being replaced with a false zero. Agents without a
transcript reader (currently anything other than Claude Code and Codex) show
`(unsupported)`, while a supported session that has not been observed shows
`(unknown)`. A present all-zero row is a genuine observed zero. A trailing `~`
marks an approximate/degraded count.

The input, output, cache-read, cache-write, and other categories are mutually
exclusive; **Total** does not add cache or reasoning fields a second time. The
aggregate includes known rows only and its **Counted** cell reports coverage
(for example, `2/4 known`) so a partial total is not presented as fleet-wide.

`gr ls --json` is the canonical structured form. Token data is nested under each
session's `tokens` field, including `counted_at` and the optional `degraded`
marker:

```console
$ gr ls --json | jq '.sessions[] | {name, tokens}'
{
  "name": "braw",
  "tokens": {
    "input": 12431,
    "output": 48209,
    "cache_creation": 96004,
    "cache_read": 1204882,
    "total": 1361526,
    "counted_at": "2026-07-18T12:00:00Z"
  }
}
```

`--json` and agent mode always use this full `SessionInfo` shape, even when
`--tokens` is also present; there is no separate flat token JSON schema. USD
cost is not shown (a planned opt-in via a user-supplied price table).

### `gr logs <name-or-id>` (alias: `l`)

Show session output without attaching.

| Flag | Description |
|------|-------------|
| `-f, --follow` | Follow output (like `tail -f`) |
| `-n, --lines <num>` | Number of lines to show (`0`, the default, uses the server's `[limits]` `log_lines`, normally 300) |

### `gr info`

Show info for the current session. Auto-detects the session by matching the current working directory against session worktree paths.

### `gr dashboard`

Live-updating TUI dashboard of all sessions. Supports inline attach, stop, delete, and resume.

### `gr approvals`

List sessions waiting for approval.

### `gr doctor` (alias: `doc`)

Run health checks and diagnostics. Checks daemon status, safehouse availability, orphaned worktrees, oversized scrollback files, and stale PID files.

When the daemon is reachable, plain output includes a **Purge** section with the effective startup delay and sweep interval, plus the last and next sweep times. Before the daemon's first sweep, the section says `Last sweep: not yet run` and `Next sweep: awaiting first sweep`. The same values remain available under `diagnostics.purge` in `--json` output.

By default `gr doctor` avoids walking the data dir to measure on-disk sizes — that walk can take tens of seconds on a large install (worktrees full of `node_modules` and `.git` objects), so it's opt-in. Pass `--disk` to report the size of the data dir, tmp repos, and orphaned worktrees. When it finds leftover artifacts whose size is worth knowing (orphaned worktrees, a legacy directory), the default run recommends re-running with `--disk`. In `--json` output, the `disk_measured` field indicates whether sizes were computed.

| Flag | Description |
|------|-------------|
| `--autofix` | Automatically fix issues |
| `--disk` | Measure on-disk sizes (walks the data dir; can be slow on large installs) |

### `gr sandbox explain`

Explain, predictively, whether the configured sandbox would allow or deny a filesystem or network access, without launching an agent. Builds the profile graith would generate from config and queries the backend's policy oracle. Needs an oracle → the `nono` backend (on a `safehouse` config it errors and points at `gr sandbox watch`).

| Flag | Description |
|------|-------------|
| `--path <p>` | Filesystem path to check (use with `--op`) |
| `--op <read\|write\|readwrite>` | Operation for `--path` |
| `--host <h>` | Network host to check (e.g. `github.com`) |
| `--port <n>` | Network port for `--host` (default 443) |
| `--agent <name>` | Resolve the merged (global + per-agent) policy for this agent |

```bash
gr sandbox explain --path ~/.ssh/id_rsa --op read
gr sandbox explain --host github.com --port 443
```

### `gr sandbox watch [session]`

Show the sandbox denials the OS actually recorded — live-tail by default, or a recent window with `--recent`. Reads the macOS unified log (Seatbelt), so it works for both the `safehouse` and `nono` backends on macOS. macOS-only; run it from your normal shell (not inside a sandboxed session — `/usr/bin/log` refuses to run sandboxed).

| Flag | Description |
|------|-------------|
| `--recent` | Show a recent aggregated window instead of live-tailing |
| `--follow`, `-f` | Force a live tail even when output is piped or in `--json` mode |
| `--since <dur>` | Window for `--recent` (a `log show --last` duration, e.g. `5m`, `1h`); implies `--recent` |
| `--proc <substr>` | Filter denials to processes whose name contains this substring |

Live-tail is the default on a terminal; when output is piped or in `--json` (agent) mode it defaults to `--recent` so it can't hang — pass `--follow` to override.

An optional `[session]` positional scopes denials to that session's process tree. See [Diagnostics & limitations]({{< relref "/docs/sandbox/debugging.md" >}}) for the full guide.

```bash
gr sandbox watch                 # live-tail
gr sandbox watch --recent --since 1h
gr sandbox watch my-session --proc node
```

## Remote interaction

### `gr type <name-or-id> <text>` (alias: `t`)

Type text into a session's PTY stdin. Appends a newline by default.

When a user is attached to the target session, graith waits for their input to
go idle before injecting. The shared `inbox_idle_timeout` and `inbox_max_wait`
settings under `[notifications.timing]` control that wait; after the maximum,
graith warns in the daemon log and injects anyway. See
[Notification timing]({{< relref "/docs/configuration/notifications.md#timing" >}}).

| Flag | Description |
|------|-------------|
| `--no-newline` | Do not append a newline after the text |

### `gr status [session] <message>`

Set a status summary for a session, visible in the session picker overlay and `gr list`. When run inside a graith session, the session is auto-detected.

| Flag | Description |
|------|-------------|
| `--clear` | Clear the status summary |
| `--ttl <duration>` | Override TTL for this status update (e.g. `10m`, `1h`) |

### `gr notify <message>`

Send a proactive desktop/push notification via the configured `[notifications]` backend. Unlike an inbox message, a notification proactively gets the human's attention. Only the orchestrator session and the human may send notifications (plain agent sessions are rejected).

| Flag | Description |
|------|-------------|
| `--title <text>` | Notification title (default `graith`) |
| `--priority <level>` | `low`, `normal` (default), or `high`; `high` plays a sound and bypasses quiet hours and the rate limit |

```bash
gr notify "Morning briefing ready" --priority low
gr notify "CI failing on main after 3 retries" --priority high
```

See [Configuration → Notifications]({{< relref "/docs/configuration/notifications.md#notifications" >}}) for backends, rate limiting, and quiet hours.
