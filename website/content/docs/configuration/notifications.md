---
weight: 330
title: "Notifications & messages"
description: "Status bar, desktop/push notifications, and messages."
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

The status bar shows session name, status, agent type, branch, git status, unread messages, and fleet summary, updating in real time.

## Notifications

```toml
[notifications]
enabled    = true   # desktop notifications (status changes AND `gr notify`)
on_stopped = false  # notify when a session stops
command    = ""     # custom notification command (optional)

# Proactive `gr notify` push notifications:
backend           = "macos"   # "macos" (helper app, falls back to osascript) or "command"; default "macos"
max_per_hour      = 12         # rolling-hour cap on low/normal pushes (high bypasses)
quiet_hours_start = "22:00"    # suppress low/normal pushes in this window (24h "HH:MM")
quiet_hours_end   = "07:00"    # window may wrap past midnight; high priority bypasses
```

When `command` is set, graith runs it via `sh -c` instead of the system notification API. Status-change notifications pass `GRAITH_SESSION_NAME`, `GRAITH_STATUS`, and `GRAITH_MESSAGE`; `gr notify` push notifications (`backend = "command"`) pass `GRAITH_NOTIFY_TITLE`, `GRAITH_NOTIFY_MESSAGE`, and `GRAITH_NOTIFY_PRIORITY`.

### Proactive push notifications (`gr notify`)

The orchestrator (and triggers) can proactively get your attention — a morning
briefing, a CI failure, a review needed — rather than leaving it silently in an
inbox:

```bash
gr notify "Morning briefing ready" --priority low
gr notify "CI failing on main after 3 retries" --priority high
```

Priority levels: `low`, `normal` (default), and `high`. `high` plays a sound and
**bypasses quiet hours and the rate limit**; `low`/`normal` are subject to both.
Only the orchestrator session and the human can send notifications — plain agent
sessions are rejected to prevent spam. Identical notifications within the
[coalesce window](#timing) (30s by default) are coalesced. Other backends (ntfy,
Pushover, Slack) are planned.

#### The `macos` backend

The `macos` backend prefers a small bundled helper app (`GraithNotifier.app`,
bundle identifier `com.graith.notifier`) that posts via
`UNUserNotificationCenter`. graith then appears as **"Graith"** in *System
Settings > Notifications*, so you can configure its style, sounds, and
Do-Not-Disturb like any other app.

Build the helper with `make notifier` (macOS only — a no-op on Linux) and place
the resulting `macos/build/GraithNotifier.app` where graith can find it:
alongside the `gr` binary, under `<prefix>/libexec/graith/` or
`<prefix>/share/graith/`, in `/Applications`, or in `~/Applications`. Set
`GRAITH_NOTIFIER_APP` to override the location.

If the helper isn't installed — or won't launch — graith falls back to
`osascript`, whose notifications work but appear under "Script Editor" and can't
be configured per-app — the reason the helper exists. One exception: if you've
explicitly turned off notifications for "Graith" in System Settings, graith
honours that and does **not** route around it via `osascript`.

Triggers can fire a notification when their action completes:

```toml
[trigger.action]
type               = "session"
notify_on_complete = true
notify_message     = "Morning briefing ready"   # templated; optional
notify_priority    = "low"                        # low|normal|high; optional
```

### Timing

Low-level notification pacing — override to tune coalescing, backend dispatch,
and PTY injection. The idle timeout and max wait are shared by inbox
notifications and `gr type`, so both avoid colliding with an attached user's
typing under one policy. Every key is optional; leave the table out for the
defaults below.

```toml
[notifications.timing]
coalesce_window      = "30s"   # drop an identical push within this window ("0" disables coalescing)
dispatch_timeout     = "15s"   # per-backend dispatch timeout (osascript / helper app / command)
inbox_idle_timeout   = "10s"   # wait before inbox notifications or `gr type` inject into an attached PTY
inbox_max_wait       = "2m"    # cap that user-idle wait before injecting anyway
inbox_cooldown       = "30s"   # minimum interval between unread-inbox nudges to one session ("0" disables)
inbox_detached_delay = "5s"    # settle delay before notifying a session with no attached client ("0" is immediate)
```

`coalesce_window`, `inbox_cooldown`, and `inbox_detached_delay` accept `"0"` to
disable. `dispatch_timeout`, `inbox_idle_timeout`, and `inbox_max_wait` fall back
to their default when zero or negative (they have no sensible zero). An
unparseable value always falls back to the default.

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

A status set via `gr status` auto-expires after this TTL if the agent produces new output without updating it. Override per-update with `gr status --ttl <duration>`.
