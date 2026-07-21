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
| `--label <label>` | Filter by exact label; repeat for AND matching |
| `-q`, `--quiet` | Output session names only (or IDs with `--json`) |
| `--wide` | Show all columns, including per-session token usage |
| `--tokens` | Show the detailed token-usage projection and aggregate totals |
| `--no-color` | Disable coloured status output |
| `--deleted` | Show recoverably deleted sessions and their expiry |

`--wide` adds a **Tokens** column with the current agent's compact total;
`--tokens` breaks out per-category counts:

```console
$ gr ls --tokens
SESSION  REPO    AGENT   INPUT   OUTPUT  CACHE-R    CACHE-W  OTHER  TOTAL      COUNTED
braw     graith  claude  12,431  48,209  1,204,882  96,004   0      1,361,526  8s ago
canny    graith  codex   69,131  3,517   756,224    0        0      828,872    11s ago
TOTAL                     81,562  51,726  1,961,106  96,004   0      2,190,398  2/2 known
```

`gr dashboard` was removed with no forwarding alias — use `gr ls` for snapshots
or the attached-session picker (`ctrl+b w`) for an interactive view.

`--label` compares case-insensitively and composes with `--repo`, `--children`,
`--starred`, and `--deleted`. Repeating it requires every requested label; it
never consults GitHub or infers labels from session content.

`--tokens` composes with the selection flags (`--repo`, `--children`,
`--starred`, `--label`, `--deleted`, `--tree`) but is mutually exclusive with `--quiet` and
`--wide`.

Counts reflect the **current agent** from a background poll, lagging by up to the
poll interval (default 30 seconds). **Counted** is the age of the last successful
observation; if a later poll can't read a transcript, the last count is kept and
its age grows rather than falling to a false zero. Agents without a transcript
reader (anything but Claude Code and Codex) show `(unsupported)`; a supported but
unobserved session shows `(unknown)`. An all-zero row is a genuine observed zero;
a trailing `~` marks an approximate/degraded count.

The input, output, cache-read, cache-write, and other categories are mutually
exclusive, so **Total** doesn't double-count cache or reasoning fields. The
aggregate counts known rows only; its **Counted** cell reports coverage (e.g.
`2/4 known`) so a partial total isn't shown as fleet-wide.

`gr ls --json` is the canonical structured form; token data nests under each
session's `tokens` field with `counted_at` and the optional `degraded` marker:

```console
$ gr ls --json | jq '.sessions[] | {name, labels, tokens}'
{
  "name": "braw",
  "labels": ["Urgent", "release"],
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

`--json` and agent mode always use this full `SessionInfo` shape, even with
`--tokens` — there's no separate flat token schema. Each row's `cwd` is the
persisted working directory assigned to the agent; `worktree_path` remains the
Git worktree/source path and can differ for mirrors and system sessions. Its
`labels` field is always the complete array (including `[]` for an unlabelled
session). USD cost isn't shown, a planned opt-in via a user-supplied price table.

### `gr logs <name-or-id>` (alias: `l`)

Show session output without attaching.

| Flag | Description |
|------|-------------|
| `-f, --follow` | Follow output (like `tail -f`) |
| `-n, --lines <num>` | Number of lines to show (`0`, the default, uses the server's `[limits]` `log_lines`, normally 300) |

### `gr info`

Show info for the current session, auto-detected by matching the working directory against session worktree paths.

### `gr doctor` (alias: `doc`)

Run health checks and diagnostics: daemon status, safehouse availability, orphaned worktrees, oversized scrollback files, and stale PID files.

The **Daemon** section reports the active terminal-screen backend. The stable
values are `charm` for the pure-Go backend and `libghostty-helper` for the
process-isolated native backend. For scripts, use the top-level
`terminal_backend` field:

```bash
gr doctor --json | jq -r .terminal_backend
```

The daemon-owned value is also present at `diagnostics.terminal_backend` in the
same JSON document.

When the daemon is reachable, plain output adds a **Purge** section with the effective startup delay, sweep interval, and last/next sweep times; before the first sweep it shows `Last sweep: not yet run` and `Next sweep: awaiting first sweep`. The same values appear under `diagnostics.purge` in `--json`.

The on-disk size walk is opt-in — it can take tens of seconds on a large install (worktrees full of `node_modules` and `.git` objects). Pass `--disk` to size the data dir, tmp repos, and orphaned worktrees; when the default run finds leftover artifacts worth sizing (orphaned worktrees, a legacy directory) it recommends re-running with `--disk`. In `--json`, `disk_measured` indicates whether sizes were computed.

| Flag | Description |
|------|-------------|
| `--autofix` | Automatically fix issues |
| `--disk` | Measure on-disk sizes (walks the data dir; can be slow on large installs) |

### `gr sandbox policy`

Inspect the optional shell command restriction layer without launching an agent.
`check` reads a command from stdin; `validate` checks the configured built-in
rules.

```bash
printf '%s\n' 'git status' | gr sandbox policy check
gr sandbox policy validate
```

### `gr sandbox explain`

Predict whether the configured sandbox would allow or deny a filesystem or network access, without launching an agent. Builds the profile graith would generate and queries the backend's policy oracle, which needs the `nono` backend (on a `safehouse` config it errors and points at `gr sandbox watch`).

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

Show the sandbox denials the OS actually recorded. It reads the macOS unified log (Seatbelt), covering both the `safehouse` and `nono` backends. macOS-only; run it from your normal shell, not a sandboxed session — `/usr/bin/log` refuses to run sandboxed.

| Flag | Description |
|------|-------------|
| `--recent` | Show a recent aggregated window instead of live-tailing |
| `--follow`, `-f` | Force a live tail even when output is piped or in `--json` mode |
| `--since <dur>` | Window for `--recent` (a `log show --last` duration, e.g. `5m`, `1h`); implies `--recent` |
| `--proc <substr>` | Filter denials to processes whose name contains this substring |

On a terminal live-tail is the default; piped or `--json` (agent) mode defaults to `--recent` so it can't hang. An optional `[session]` positional scopes denials to that session's process tree. See [Diagnostics & limitations]({{< relref "/docs/sandbox/debugging.md" >}}) for the full guide.

```bash
gr sandbox watch                 # live-tail
gr sandbox watch --recent --since 1h
gr sandbox watch my-session --proc node
```

## Remote interaction

### `gr type <name-or-id> <text>` (alias: `t`)

Type text into a session's PTY stdin. Appends a newline by default.

When a user is attached, graith waits for their input to go idle before
injecting. The `inbox_idle_timeout` and `inbox_max_wait` settings under
`[notifications.timing]` control that wait; past the maximum it warns in the
daemon log and injects anyway. See
[Notification timing]({{< relref "/docs/configuration/notifications.md#timing" >}}).

| Flag | Description |
|------|-------------|
| `--no-newline` | Do not append a newline after the text |

### `gr status [session] <message>`

Set a status summary, shown in the session picker overlay and `gr list`. Run inside a graith session, it auto-detects the session.

| Flag | Description |
|------|-------------|
| `--clear` | Clear the status summary |
| `--ttl <duration>` | Override TTL for this status update (e.g. `10m`, `1h`) |

### `gr notify <message>`

Send a desktop/push notification via the configured `[notifications]` backend — unlike an inbox message, it grabs the human's attention. Only the orchestrator session and the human can send them; plain agent sessions are rejected.

| Flag | Description |
|------|-------------|
| `--title <text>` | Notification title (default `graith`) |
| `--priority <level>` | `low`, `normal` (default), or `high`; `high` plays a sound and bypasses quiet hours and the rate limit |

```bash
gr notify "Morning briefing ready" --priority low
gr notify "CI failing on main after 3 retries" --priority high
```

See [Configuration → Notifications]({{< relref "/docs/configuration/notifications.md#notifications" >}}) for backends, rate limiting, and quiet hours.
