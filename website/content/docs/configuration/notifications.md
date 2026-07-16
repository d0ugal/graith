---
weight: 330
title: "Notifications & approvals"
description: "Status bar, desktop/push notifications, approvals, and messages."
icon: "notifications"
toc: true
draft: false
---

## Status bar

```toml
[status_bar]
enabled  = true      # show a status bar while attached
position = "bottom"  # "bottom" or "top"
```

The status bar shows the session name, status, agent type, branch, git status, unread messages, and fleet summary. It updates in real time.

## Notifications

```toml
[notifications]
enabled     = true   # desktop notifications (status changes AND `gr notify`)
on_approval = true   # notify when a session needs approval
on_stopped  = false  # notify when a session stops
command     = ""     # custom notification command (optional)

# Proactive `gr notify` push notifications:
backend           = "macos"   # "macos" (helper app, falls back to osascript) or "command"; default "macos"
max_per_hour      = 12         # rolling-hour cap on low/normal pushes (high bypasses)
quiet_hours_start = "22:00"    # suppress low/normal pushes in this window (24h "HH:MM")
quiet_hours_end   = "07:00"    # window may wrap past midnight; high priority bypasses
```

When `command` is set, graith executes it via `sh -c` instead of using the system notification API. For status-change notifications the command receives `GRAITH_SESSION_NAME`, `GRAITH_STATUS`, and `GRAITH_MESSAGE`; for `gr notify` push notifications (`backend = "command"`) it receives `GRAITH_NOTIFY_TITLE`, `GRAITH_NOTIFY_MESSAGE`, and `GRAITH_NOTIFY_PRIORITY`.

### Proactive push notifications (`gr notify`)

The orchestrator (and triggers) can proactively get your attention — a morning
briefing, a CI failure, a review needed — rather than leaving it sitting silently
in an inbox:

```bash
gr notify "Morning briefing ready" --priority low
gr notify "CI failing on main after 3 retries" --priority high
```

Priority levels: `low`, `normal` (default), and `high`. `high` plays a sound and
**bypasses quiet hours and the rate limit**; `low`/`normal` are subject to both.
Only the orchestrator session and the human may send notifications — plain agent
sessions are rejected to prevent spam. Identical notifications within the
[coalesce window](#timing) (30s by default) are coalesced. Other backends (ntfy,
Pushover, Slack) are planned follow-ups.

#### The `macos` backend

The `macos` backend prefers a small bundled helper app (`GraithNotifier.app`,
bundle identifier `com.graith.notifier`) that posts via
`UNUserNotificationCenter`. This makes graith appear as **"Graith"** in *System
Settings > Notifications*, so you can configure its notification style, sounds,
and Do-Not-Disturb behaviour like any other app.

Build the helper with `make notifier` (macOS only — a no-op on Linux) and place
the resulting `macos/build/GraithNotifier.app` where graith can find it:
alongside the `gr` binary, under `<prefix>/libexec/graith/` or
`<prefix>/share/graith/`, in `/Applications`, or in `~/Applications`. Set
`GRAITH_NOTIFIER_APP` to override the location explicitly.

If the helper isn't installed — or is installed but fails to launch — graith
falls back to `osascript`, whose notifications work but appear under "Script
Editor" (and can't be configured per-app) — the reason the helper exists. The
one exception is when you've explicitly turned off notifications for "Graith" in
System Settings: graith honours that and does **not** route around it via
`osascript`.

Triggers can fire a notification when their action completes:

```toml
[trigger.action]
type               = "session"
notify_on_complete = true
notify_message     = "Morning briefing ready"   # templated; optional
notify_priority    = "low"                        # low|normal|high; optional
```

### Timing

Low-level notification pacing. These were formerly fixed constants; override them
to tune coalescing, backend dispatch, and how inbox notifications are injected
into a session's PTY. Every key is optional — leave the table out and the
defaults below apply.

```toml
[notifications.timing]
coalesce_window      = "30s"   # drop an identical push within this window ("0" disables coalescing)
dispatch_timeout     = "15s"   # per-backend dispatch timeout (osascript / helper app / command)
inbox_idle_timeout   = "10s"   # wait for an attached session's PTY to be idle this long before injecting
inbox_max_wait       = "2m"    # cap on the user-idle wait before injecting anyway
inbox_cooldown       = "30s"   # minimum interval between unread-inbox nudges to one session ("0" disables)
inbox_detached_delay = "5s"    # settle delay before notifying a session with no attached client ("0" is immediate)
```

`coalesce_window`, `inbox_cooldown`, and `inbox_detached_delay` accept `"0"` to
disable that behaviour. `dispatch_timeout`, `inbox_idle_timeout`, and
`inbox_max_wait` fall back to their default when set to zero or a negative value
(they have no sensible zero). An unparseable value always falls back to the
default.

## Approvals

```toml
[approvals]
backend  = ""        # who decides (see below); default "" = always prompt the human
timeout  = "10m"     # how long to wait for a human decision
auto_pop = false     # auto-open the approval overlay when a request is queued
command  = ""        # required for backend "command"/"external"; path override for "localmost"

command_timeout   = "5s"  # bounds one "command"/"external" backend invocation
localmost_timeout = "5s"  # bounds one "localmost" binary check

[approvals.builtin]
config   = ""        # localmost-format config.json (backend "builtin")
```

The approval system integrates with agent hooks. When an agent requests approval (e.g. for a dangerous tool call), the `backend` decides who resolves it:

| `backend` | Who decides |
|-----------|-------------|
| `""` (default, equivalent to `"prompt"`) | Always prompt the human via the overlay |
| `"command"` / `"external"` | Delegate to `command` over graith's JSON contract (one JSON object on stdin — `{tool_name,tool_input,session_id,session_name}` — and one on stdout — `{decision:allow\|block\|deny\|defer,reason}`) |
| `"localmost"` | Delegate to the real localmost binary via its native protocol (`command` optionally overrides the binary path) |
| `"builtin"` | graith's built-in localmost-compatible engine — configured via `[approvals.builtin] config` (a localmost-format `config.json` path) **or** inline rules (`allow`, `deny`, `allowSafeXargs`, `askNoninteractive`) |

`mode` is deprecated. With no `backend` set, legacy `mode = "command"`, `mode = "external"`, and `mode = "localmost"` all resolve to `backend = "command"` (graith's JSON contract) for compatibility — `mode = "localmost"` does **not** select the native-protocol `backend = "localmost"`. Set `backend = "localmost"` explicitly to run the real localmost binary. See `ResolveBackend` in `internal/config/config.go` for the full resolution order.

### Backend execution timeouts

For an interactive session, an automated backend runs first and may then defer to the human queue. The worst-case server bound is therefore **backend execution + human wait**: `command_timeout` or `localmost_timeout`, followed by up to `timeout`. For a headless session the backend is instead enclosed by the caller-side `timeout`. The `command`/`external` and `localmost` backends spawn a subprocess to make their decision; the other backends decide in-process.

Both backend timeouts default to `5s` when unset. Each must be a positive duration, at most `60s`, and **strictly shorter** than the human `timeout` — a backend timeout at or above that main policy is rejected at config load, because mismatched approval deadlines have caused approval-behaviour bugs in the past. `gr doctor` prints the complete effective hierarchy: backend execution + human wait = server bound < hook operation deadline.

The hook connection uses `[connection] handshake_timeout` only for its handshake. After `handshake_ok` it installs a fresh, nonzero operation deadline equal to the complete server bound plus one minute of response-delivery grace; this is separate from `dial_timeout`. A human-wait expiry is a daemon decision and blocks the tool with a reason naming that deadline. If the connection itself fails or its operation deadline expires, the longstanding hook-edge policy remains fail-open, but the allow result now includes a visible reason naming the failed phase instead of silently allowing.

> **Upgrade note (v0.69.1):** the timeout hierarchy is validated fail-closed. A previously loadable command/localmost configuration with `timeout <= 5s` may now be rejected because the effective default backend timeout is `5s`. Raise `timeout` above `5s`, or explicitly choose a shorter `command_timeout` / `localmost_timeout`.

## Messages

```toml
[messages]
max_age        = ""  # prune messages older than this, e.g. "7d", "168h" (empty = keep forever)
max_per_stream = 0   # cap messages per stream (0 = unlimited)
```

Duration strings support days: `7d`, `30d`, `1d12h`.

## Status

```toml
[status]
ttl = "5m"  # default TTL for status updates
```

When an agent sets a status via `gr status`, it auto-expires after this TTL if the agent produces new output without updating the status. Override per-update with `gr status --ttl <duration>`.
