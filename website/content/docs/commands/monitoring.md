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

List all sessions with status, current activity, and the summary set by
`gr status`. Human-readable output shows the parent-child hierarchy by default;
use `--flat` for the legacy repo/name ordering. Summaries are truncated to the
configured `[terminal].summary_width` display-cell budget (40 by default).

| Flag | Description |
|------|-------------|
| `--repo <path>` | Filter by repo path |
| `--flat` | Show sessions in flat repo/name order |
| `--tree` | Deprecated no-op retained for compatibility; hierarchy is the default |
| `--children <name-or-id>` | Filter to descendants of a session |
| `--starred` | Show only starred sessions |
| `--label <label>` | Filter by exact label; repeat for AND matching |
| `-q`, `--quiet` | Output session names only (or IDs with `--json`) |
| `--wide` | Show all columns, including per-session token usage |
| `--summary-width <cells>` | Override the SUMMARY display-cell limit for this invocation (must be positive) |
| `--full-summary` | Show the complete SUMMARY without a display-cell limit; mutually exclusive with `--summary-width` |
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
or the Session Navigator (`ctrl+b w`) for an interactive view.

`--label` compares case-insensitively and composes with `--repo`, `--children`,
`--starred`, and `--deleted`. Repeating it requires every requested label; it
never consults GitHub or infers labels from session content.

`--tokens` composes with the selection flags (`--repo`, `--children`,
`--starred`, `--label`, `--deleted`) and the display modes (`--flat`,
`--tree`), but is mutually exclusive with `--quiet` and `--wide`. `--flat` and
`--tree` cannot be supplied together.

`--wide` does not change the SUMMARY limit. Use `--summary-width` or
`--full-summary` when more context is needed. JSON output remains the complete,
untruncated session shape; `--quiet` and `--tokens` retain their focused
projections and do not add the SUMMARY table column.

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

`--json` and agent mode always use this full `SessionInfo` shape and daemon
ordering, even with `--flat`, `--tree`, or `--tokens` — display flags do not
change its schema or omit sessions, and there's no separate flat token schema.
Each row's `cwd` is the persisted working directory assigned to the agent;
`worktree_path` remains the Git worktree/source path and can differ for mirrors
and system sessions. Its `labels` field is always the complete array (including
`[]` for an unlabelled session). USD cost isn't shown, a planned opt-in via a
user-supplied price table.

### `gr search <query>`

Search conversation transcripts across sessions. V1 supports Claude Code and
Codex transcripts and uses the same canonical transcript readers as migration
and token accounting, so hidden reasoning and other excluded provider fields are
not indexed. Search is local to the daemon; query text and matched transcript
bodies are not sent to external services.

```console
$ gr search "permission denied"
braw (braw-id)  claude/user  graith  2026-07-29T10:00:00Z
  ...got [permission denied] opening the cache...
  locator: s:braw-id:a:claude:n:sess-braw:t:4
```

| Flag | Description |
|------|-------------|
| `--session <name-or-id>` | Search one session |
| `--children` | With `--session`, include descendants of that session |
| `--repo <name-or-path>` | Filter by repo name or path |
| `--agent <name>` | Filter by agent name |
| `--kind <kind>` | Filter by message kind: `user`, `assistant`, `tool`, or `context`; repeat or comma-separate |
| `--since <time>` | Include messages at or after an RFC3339 timestamp or `YYYY-MM-DD` date |
| `--until <time>` | Include messages at or before an RFC3339 timestamp or `YYYY-MM-DD` date |
| `--state <state>` | Filter by session state: `all` (default), `active` (`running`/`creating`), or `stopped` (`stopped`/`errored`) |
| `--deleted` | Include soft-deleted sessions |
| `--limit <n>` | Result count; defaults to `[search] default_limit` (20) and is capped by `[search] max_limit` (200) |
| `--cursor <cursor>` | Continue from a previous response's `next_cursor` |

Search is literal and case-insensitive. It is not fuzzy or semantic. Results are
ordered deterministically by newest message timestamp when available, then
session creation time, session ID, migrated/current generation, agent, native
agent session ID, and transcript turn index. Time filters only match turns whose
transcript record provided a parseable timestamp.

Cold transcript parses are bounded per source: by default search reads at most
16 MiB and keeps at most 10,000 turns from one transcript source. These and
other search resource limits are tunable in `[search]`. Responses set
`truncated` when pagination or resource bounds mean more matching content may
exist outside the returned window. When a source bound is hit, v1 searches the
oldest records read from that source and omits later transcript content.

Each result includes the owning session, agent, message kind, optional timestamp,
a bounded UTF-8 snippet, match ranges, and an opaque locator. Snippets strip
ANSI and terminal control sequences before matching and display. The locator is
for clients to reopen the owning context; do not parse it in scripts. Use
`--json` for the stable structured form:

```console
$ gr search "bothy" --json | jq '.results[0]'
{
  "session_id": "braw-id",
  "session_name": "braw",
  "repo_path": "/Users/me/Code/graith",
  "repo_name": "graith",
  "agent": "claude",
  "agent_session_id": "sess-braw",
  "kind": "user",
  "timestamp": "2026-07-29T10:00:00Z",
  "snippet": "fix the bothy",
  "matches": [{"start": 8, "end": 13}],
  "locator": "s:braw-id:a:claude:n:sess-braw:t:0"
}
```

Unsupported agents are reported in `unsupported_agents` rather than silently
counting as zero results. Soft-deleted sessions are excluded unless `--deleted`
is set. Purged sessions are absent from state and are no longer searchable.
When a session has cross-agent migration or fork provenance, search includes
both the current transcript and the persisted `migrated_from` source transcript.
Those generations are not coalesced; if the same phrase appears in both, both
results are returned with distinct `agent`, `agent_session_id`, and `locator`
values.

Search uses bounded on-demand scanning with an in-memory cache keyed by
transcript path, size, and mtime. Appends, replacement, truncation, resume, and
migration become searchable on the next query that observes the changed
fingerprint. A daemon restart drops the cache and rebuilds it on demand. The
daemon bounds scanner line size through the `[transcript]` config, stores at
most 32 MiB of parsed search text in memory by default, truncates any single
turn cached for search at 128 Ki runes, returns snippets of at most 240 runes,
and caps one paginated query window at 1,000 results. These defaults are
controlled by the `[search]` config block.

### `gr events`

Stream live daemon events until interrupted. Use it in supervisors that would
otherwise poll `gr list` to notice session readiness, stops, deletes, or public
topic messages.

```console
$ gr events --json
{"type":"status_change","at":"2026-07-29T12:00:00Z","session_id":"abc123","session":"fix-auth","status_kind":"agent","from":"active","to":"ready"}
{"type":"message","at":"2026-07-29T12:00:02Z","topic":"review-ready","message_id":"msg_abc","seq":7,"sender_id":"abc123","sender":"fix-auth","body":"PR ready"}
{"type":"session_deleted","at":"2026-07-29T12:00:04Z","session_id":"abc123","session":"fix-auth"}
```

Without `--json`, the command prints compact human-readable lines. Agent-mode
clients automatically enable JSON output.

`status_change` events use `status_kind: "agent"` for detected agent states
such as `active`, `ready`, or `error`, and `status_kind: "session"` for daemon
lifecycle states such as sessions becoming `running`, stopping, or erroring.

`message` events include public topic publishes only. They include
`system: true` when the sender is a daemon-authored system notice. Direct inbox
messages (`inbox:<id>`) and `_system.*` streams are not emitted; use
`gr msg inbox --wait` or `gr msg inbox --follow` for private direct messages.

`session_deleted` is emitted when a session is hidden by soft delete, or when a
live/non-deleted session is hard-deleted directly. A later purge of an already
soft-deleted session does not emit a second deletion event.

The stream is best-effort and not replayable. If a subscriber disconnects or is
too slow, use `gr list --json` and `gr msg sub --topic <name> --all` to rebuild
current state. The stream is fleet-wide for authenticated users and sessions,
matching the visibility of `gr list` plus public topic subscriptions.

### `gr events follow <child> --events <events>`

Register a durable follow rule so the child session's direct parent receives
selected child events. The only event class in v1 is `ci`:

```console
$ gr events follow bairn --events ci
Following ci from bairn -> ben
```

`--events` accepts a comma-separated list or repeated values. Re-running
`follow` for the same child replaces that child's followed event set. Unknown
event classes are rejected. Only the direct parent session, or the local user,
may create or change a follow rule. The config-managed system orchestrator
cannot follow child events; commands that would target it fail and no event is
delivered.

`gr new --follow-events=ci` creates the same rule atomically while creating a
child session. If the follow rule is invalid, session creation fails before the
child starts.

Follow rules are keyed by source child and persisted in daemon state. Delivery
is exactly one hop to the source child's current direct parent; forwarded events
are terminal and are never forwarded again to a grandparent. Reparenting moves
the rule to the new direct parent when that parent is valid. Reparenting to no
parent, to a deleted parent, or to the system orchestrator disables the rule.
Purging a source session removes its rule.

### `gr events unfollow <child> [--events <events>]`

Remove selected event classes from a direct-child follow rule:

```console
$ gr events unfollow bairn --events ci
Unfollowed none from bairn -> ben
```

Omit `--events` to remove the whole follow rule.

### `gr events following`

List active follow rules visible to the caller:

```console
$ gr events following
ci from bairn -> ben
```

Use `--json` for the stable structured form:

```json
{
  "rules": [
    {
      "child_session_id": "bairn-id",
      "child_session": "bairn",
      "parent_session_id": "ben-id",
      "parent_session": "ben",
      "events": ["ci"],
      "created_at": "2026-07-30T12:00:00Z",
      "updated_at": "2026-07-30T12:00:00Z"
    }
  ]
}
```

### Forwarded `ci` events

The `ci` class forwards the aggregate PR-watch CI transitions graith already
computes: `pending`, `failing`, and `passing`. The first matching PR-watch
observation after a rule is registered can therefore forward the current
aggregate state. It does not forward raw GitHub `check_run` or `check_suite`
webhook payloads, does not start extra GitHub polling, and does not treat
unknown CI as passing.

The parent receives a daemon-authored inbox message and `gr events` subscribers
also see a `session_event` payload:

```json
{
  "type": "session_event",
  "event_class": "ci",
  "forwarded": true,
  "session_id": "ben-id",
  "session": "ben",
  "source_session_id": "bairn-id",
  "source_session": "bairn",
  "destination_session_id": "ben-id",
  "destination_session": "ben",
  "pr_number": 1646,
  "pr_url": "https://github.com/d0ugal/graith/pull/1646",
  "head_ref_oid": "abc123def456...",
  "ci_state": "failing",
  "failing_checks": ["test"],
  "ci_pending": 1,
  "ci_passed": 3,
  "ci_total": 5,
  "system": true
}
```

The event identifies the source child and PR, includes the child PR head SHA,
aggregate check counts, and failing-check names when known, and preserves
daemon/system authorship. It does not mutate or masquerade as the parent's own
PR or CI state.

Forwarded `ci` messages use the parent's normal inbox notification path, so a
new forwarded transition can notify or auto-resume the parent session just like
other daemon-authored inbox messages. They are deduplicated by source session,
PR number, head SHA, and aggregate state, but they do not use PR-watch's
user-facing notification rate limit because they are explicit child-to-parent
follow events. Raw GitHub comments still use the existing trust and quarantine
paths instead of event forwarding.

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

Pass `--alloy` to add local Grafana Alloy collection checks. This opt-in
section finds Alloy on `PATH` or at `--alloy-binary`, reports `alloy
--version`, validates either a supplied `--alloy-config` path or a temporary
`gr config alloy` rendering when the installed Alloy supports `alloy validate`,
checks selected daemon log files and the loopback metrics scrape endpoint, and
reports likely local service state when macOS or Linux exposes it without root.
It does not start Alloy, read credential files, print backend secrets, or call
Loki, Mimir, Tempo, or Grafana Cloud.

The **Daemon** section reports the active terminal-screen backend. The stable
values are `libghostty-helper` for the
process-isolated native backend. Unsupported builds fail closed. For scripts, use the top-level
`terminal_backend` field:

```bash
gr doctor --json | jq -r .terminal_backend
```

The daemon-owned value is also present at `diagnostics.terminal_backend` in the
same JSON document.

When the daemon is reachable, plain output adds a **Purge** section with the effective startup delay, sweep interval, and last/next sweep times; before the first sweep it shows `Last sweep: not yet run` and `Next sweep: awaiting first sweep`. The same values appear under `diagnostics.purge` in `--json`.

Plain output also includes **Watcher Resources** for file-watch triggers. It
reports the current reserved estimated watch backend units versus
`triggers.advanced.watch_max_directories`, warns when the budget is near or
blocked by an exhausted binding, and attributes active registrations to trigger
bindings by session. The section also separates live registrations from stale
reservations left behind by removed directories or backend-dropped watches; stale
reservations are pruned automatically on reconcile, and persistent stale counts
can be cleared by rebuilding watcher state with `gr daemon restart`. The same
data appears under `diagnostics.watchers` in `--json`; degraded bindings report
zero active registrations and include retry state.

The on-disk size walk is opt-in — it can take tens of seconds on a large install (worktrees full of `node_modules` and `.git` objects). Pass `--disk` to size the data dir, tmp repos, and orphaned worktrees; when the default run finds leftover artifacts worth sizing (orphaned worktrees, a legacy directory) it recommends re-running with `--disk`. In `--json`, `disk_measured` indicates whether sizes were computed.

| Flag | Description |
|------|-------------|
| `--autofix` | Automatically fix issues |
| `--alloy` | Run local Grafana Alloy collection checks |
| `--alloy-binary <path-or-command>` | Alloy executable to inspect (default searches `PATH` for `alloy`) |
| `--alloy-config <path>` | Alloy config file or directory to validate (default validates generated config) |
| `--alloy-signals <list>` | Alloy signals to check: `daemon-logs`, `metrics`, `traces`, or `all`; default is `metrics,traces` |
| `--disk` | Measure on-disk sizes (walks the data dir; can be slow on large installs) |

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

Set a status summary, shown in the Session Navigator and `gr list`. Run inside a graith session, it auto-detects the session.

| Flag | Description |
|------|-------------|
| `--clear` | Clear the status summary |
| `--ttl <duration>` | Override TTL for this status update (e.g. `10m`, `1h`) |

### `gr notify <message>`

Send a desktop/push notification via the configured `[notifications]` backend — unlike an inbox message, it grabs the user's attention. Only the orchestrator session and the user can send them; plain agent sessions are rejected.

| Flag | Description |
|------|-------------|
| `--title <text>` | Notification title (default `graith`) |
| `--priority <level>` | `low`, `normal` (default), or `high`; `high` plays a sound and bypasses quiet hours and the rate limit |

```bash
gr notify "Morning briefing ready" --priority low
gr notify "CI failing on main after 3 retries" --priority high
```

With `--json`, `gr notify` prints a structured result:

```json
{
  "delivered": false,
  "reason": "suppressed by quiet hours (22:00–07:00); use --priority high to override"
}
```

`reason` is an empty string when the notification was delivered, and explains
suppression or backend failure when `delivered` is `false`. Suppression and
delivery failure are reported in the JSON result rather than by a non-zero exit
status.

See [Configuration → Notifications]({{< relref "/docs/configuration/notifications.md#notifications" >}}) for backends, rate limiting, and quiet hours.
