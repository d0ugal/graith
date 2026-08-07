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

In terminal-owned attach, this position also selects the Graith-owned chrome
row. With a visible status bar or read-only indicator, Graith reserves that row
outside the child PTY viewport when the terminal has at least two rows; one-row
terminals suppress the chrome row so the child keeps the line.

## Notifications

```toml
[notifications]
enabled    = true   # desktop notifications (status changes AND `gr notify`)
on_stopped = false  # notify when a session stops
command    = ""     # custom notification command (optional)

# Proactive `gr notify` push notifications:
backend           = "macos"   # "macos" (required helper app) or "command"; default "macos"
max_per_hour      = 12         # rolling-hour cap on low/normal pushes (high bypasses)
quiet_hours_start = "22:00"    # suppress low/normal pushes in this window (24h "HH:MM")
quiet_hours_end   = "07:00"    # window may wrap past midnight; high priority bypasses
```

When `command` is set, status-change notifications run it via `sh -c` instead
of the system notification API and pass `GRAITH_SESSION_NAME`, `GRAITH_STATUS`,
and `GRAITH_MESSAGE`. `gr notify` push notifications use the command only when
`backend = "command"`; they pass `GRAITH_NOTIFY_TITLE`,
`GRAITH_NOTIFY_MESSAGE`, and `GRAITH_NOTIFY_PRIORITY`.

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
Only the orchestrator session and the user can send notifications — plain agent
sessions are rejected to prevent spam. Identical notifications within the
[coalesce window](#timing) (30s by default) are coalesced. Other backends (ntfy,
Pushover, Slack) are planned.

#### Native macOS notifications

Both default session-status notifications (such as `on_stopped`) and the
`macos` push backend prefer a small bundled helper app (`GraithNotifier.app`,
bundle identifier `com.graith.notifier`) that posts via
`UNUserNotificationCenter`. They therefore appear as **"Graith"** in *System
Settings > Notifications*, where you can configure their style, sounds, and
Do-Not-Disturb behavior like any other app. Stable and `graith-dev` Homebrew
installations install this helper automatically on macOS; Linux packages do not
contain it. Managed macOS daemon services retain the packaged helper beside
their private `Graith.app` service generation, so the `macos` backend still uses
Graith's native helper after the daemon starts from its service copy.

Build the helper with `make notifier` (macOS only — a no-op on Linux) and place
the resulting `macos/build/GraithNotifier.app` where graith can find it:
alongside the `gr` binary, under `<prefix>/libexec/graith/` or
`<prefix>/share/graith/`, in `/Applications`, or in `~/Applications`. Set
`GRAITH_NOTIFIER_APP` to override the location.

The helper is required for native macOS delivery. If it isn't installed or
can't launch, the dispatch fails and graith logs/reports the failure; it does
not route the notification through another application. If you've explicitly
turned off notifications for "Graith" in System Settings, graith honours that
choice as a suppressed notification.

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
dispatch_timeout     = "15s"   # per-backend dispatch timeout (helper app / command)
inbox_idle_timeout   = "10s"   # wait before inbox notifications or `gr type` inject into an attached PTY
inbox_max_wait       = "2m"    # cap that user-idle wait before injecting anyway
inbox_cooldown       = "30s"   # minimum interval between unread-inbox nudges to one session ("0" disables)
inbox_detached_delay = "5s"    # settle delay before notifying a session with no attached client ("0" is immediate)
```

`coalesce_window`, `inbox_cooldown`, and `inbox_detached_delay` accept `"0"` to
disable. `dispatch_timeout`, `inbox_idle_timeout`, and `inbox_max_wait` fall back
to their default when zero or negative (they have no sensible zero). An
unparseable value always falls back to the default.

### System notification guidance

Add top-level `[[notification_instruction]]` rules to prepend trusted local
guidance to matching PR-watch and released jailed-comment system notices sent to
agents. With no rules configured, system notification bodies are unchanged.
Repeat `[[notification_instruction]]` blocks to define multiple rules.

```toml
[[notification_instruction]]
name = "reviewer-guidance"
kinds = ["github_pr_comment", "github_pr_review"]
owners = ["my-user", "example-org"]
repos = ["example-org/customer-rollout"]
authors = ["alice", "bob", "review-bot[bot]"]
template = """
Reviewer feedback guidance:
- Treat maintainer review comments as actionable feedback.
- Keep the PR scoped to the requested change unless the user says otherwise.
- If generated files are involved, verify the matching source config before editing.
"""
```

Each rule must include at least one condition field. All configured condition
fields must match; values inside one field are alternatives. For example, the
rule above matches only PR conversation or inline review-comment notices for
`example-org/customer-rollout` under one of the listed owners when at least one
source comment author is one of the listed logins. If multiple rules match,
Graith prepends them in config order.

`owners` matches the owner or user portion of an `owner/repo` repository slug;
use it to match every repository under one account or organization. `repos`
accepts either `owner/repo` or a repository basename and never means "all repos
owned by this name." `authors` matches GitHub logins case-insensitively when the
notification source has authors. `session_names` and `session_repos` can further
restrict rules to session metadata when available; `session_repos` uses the same
`owner/repo` or basename matching as `repos`, falling back to basename matching
when owner metadata is not available.

Current PR-watch notification kinds are `github_ci_failure`,
`github_ci_complete`, `github_ci_recovery`, `github_pr_merge_conflict`,
`github_pr_lifecycle`, `github_pr_review_decision`, `github_pr_review`
(inline code-review comments), and `github_pr_comment` (PR conversation
comments).

Templates use `{{kind}}`, `{{repo}}`, `{{author}}`, `{{pr_number}}`, `{{url}}`,
`{{session_name}}`, and `{{session_repo}}`. These variables are metadata only;
comment bodies and reviewer text are never available as template variables.
Graith renders the matched guidance in a `Trusted guidance from local Graith
config` section before the existing notification payload, followed by a
metadata block labeled as data, not instructions. The notification payload is
then labeled as external data, not instructions.

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
