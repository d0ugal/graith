---
weight: 320
title: "Session behavior"
description: "Headless sessions, delete retention, the launch throttle, and git pull."
icon: "tune"
toc: true
draft: false
---

## Headless sessions

**Experimental.** A headless session runs the agent in Claude Code's stream-json mode instead of an interactive PTY — for fire-and-forget work such as review judges and one-shot helpers. graith parses the typed event stream, so `gr logs -f` renders it and the run's cost/token usage is captured from the result envelope. v1 is Claude-only, one-shot, requires a prompt, and is **incompatible with the sandbox** (headless requires `sandbox.enabled = false`, or a per-agent sandbox that resolves to disabled).

```toml
[headless]
experimental = false  # master gate: headless is inert unless this is on
default      = false  # once enabled, whether new sessions go headless by default
```

`experimental` is the master switch. While it is off, headless is inert and sessions use the PTY driver. It is gated because the underlying control protocol is undocumented and may change between Claude Code releases. Trigger-spawned headless sessions are not implemented yet; see [Automation → Headless session actions]({{< relref "automation.md#headless-session-actions-planned" >}}).

v1 is **Claude-only** and **one-shot** (one prompt, run to completion, exit). Only agents marked `headless_capable = true` can run headless — see [Agent definitions]({{< relref "agents.md#agent-definitions" >}}). Requesting headless for an agent that can't do it is an error, not a silent fallback to PTY.

`gr attach` on a headless session offers to convert it to an interactive PTY,
preserving the conversation, worktree, branch, and environment. Pass `--yes` to
skip the confirmation. Use `gr logs -f` to inspect a headless session read-only
without converting it (see [Session Lifecycle → Headless sessions]({{< relref "/docs/sessions.md#headless-sessions" >}})).

A headless session drives Claude Code over its stdin control protocol, giving graith a clean interrupt and inline tool-approval handling. It has no human to answer prompts, so its approval policy must be **non-blocking**: `yolo` auto-allows, a non-blocking `[approvals]` backend decides, and anything that would queue for a human is denied (escalated once to the orchestrator inbox). See [Session Lifecycle → Headless sessions]({{< relref "/docs/sessions.md#headless-sessions" >}}).

## Delete retention

```toml
[delete]
retention = "24h"            # how long soft-deleted sessions are kept before purge
purge_startup_delay = "30s"  # delay before the first purge sweep after the daemon starts
purge_interval = "10m"       # how often the purge sweep runs thereafter
```

`gr delete` is a soft delete: it stops the agent and hides the session but keeps its worktree, branch, and state for this window, so `gr restore` can bring it back. A background loop hard-deletes sessions once their retention expires. Setting `retention = "0"` disables soft delete, so `gr delete` refuses and directs the user to `gr purge`; it never silently becomes destructive. `gr purge` is the only immediate, destructive verb.

`purge_startup_delay` and `purge_interval` tune only the sweep cadence, not whether a session is recoverable: a session's recovery deadline is frozen when it is deleted (`retention` at delete time), so no timing value can turn soft delete into an immediate hard delete. The cadence is deliberately coarse because the retention window is measured in hours. Both must be positive durations; a `gr reload` retunes the running purge timer on its next tick. `gr doctor` reports the effective cadence and the last/next sweep times.

## Orphan garbage collection

```toml
[gc]
orphan_min_age = "5m"  # minimum age before an orphaned worktree/scratch dir is GC-eligible
```

`gr gc` reclaims worktree and scratch directories left behind by sessions that are no longer in state. `orphan_min_age` is the minimum age a directory must reach before GC will remove it, so a directory belonging to an in-flight `gr new` (created before the session is committed to state) is never destroyed. Setting `orphan_min_age = "0"` opts out of the age floor and makes directories eligible immediately — only safe when no sessions are being created concurrently.

## Git pull

```toml
[git_pull]
enabled  = false  # periodically fast-forward maintenance repos' default branches
interval = "1h"   # how often to pull (minimum: 1 minute)
```

When enabled, the daemon fetches and fast-forward merges the default branch of each repo registered with `git maintenance`. The first pull runs shortly after the daemon starts, then on the configured `interval` — so a daemon restart doesn't leave repos stale for a full interval before the next pull.

Sessions run in their own worktrees on feature branches, which share only the object store with the source checkout, so fast-forwarding the default branch cannot disturb them — those sessions do **not** block the pull. A repo is only skipped when a session works directly on the source checkout (in-place) or has the default branch itself checked out in its worktree. This keeps default branches up to date for future session creation without ever pulling into an active worktree.

## Launch throttle & startup watchdog

Launching several sessions at once can overwhelm the machine: heavyweight agent runtimes (Claude Code loads a ~400MB Node process) all initialise simultaneously and the tail of the burst can stall for minutes — or hang indefinitely at ~9MB RSS (only the sandbox wrapper loaded), producing no output and never connecting. The `[launch]` block bounds this and recovers stuck launches automatically.

```toml
[launch]
max_concurrent  = 3      # max agent spawns in their startup window at once (< 1 => default 3)
startup_timeout = "3m"   # kill + restart a session stuck with no output past this ("0" disables the watchdog)
settle_timeout  = "10s"  # how long a launch holds its throttle slot waiting for first output ("0" => release right after spawn)
```

**Throttle.** A launch acquires one of `max_concurrent` slots just before spawning the agent and holds it across the risky startup window, releasing it as soon as the session produces its first output or `settle_timeout` elapses (whichever comes first). This bounds how many agents are *initialising* at once — the actual source of the stampede — so a burst starts cleanly in sequence rather than all at once. `gr new` still returns promptly; the slot is released in the background.

**Watchdog.** A background loop looks for sessions that are running but have never produced output, sit at `agent_status: unknown`, and have been up longer than `startup_timeout`. Each is killed and restarted fresh (the restart uses a fresh `--session-id` rather than resuming a conversation that was never persisted). A per-session cap prevents restart storms for a permanently-broken session; the counter resets once the session produces output. Set `startup_timeout = "0"` to disable the watchdog entirely.

`max_concurrent` and `startup_timeout` are re-read on config reload (`SIGHUP` / edit-and-save), so you can tune them without restarting the daemon.

## Detection & status classification

The daemon decides each session's `agent_status` (`active`, `ready`, `approval`, `unknown`) from two sources: authoritative reports from agent hooks, and, when no fresh hook report exists, a periodic scrape of the PTY scrollback. The `[detection]` block exposes the timing policy behind both so you can tune how often the daemon looks and how long each signal is trusted.

```toml
[detection]
scan_interval        = "500ms"  # how often PTY scrollback is scanned for agent status
fetch_interval       = "5m"     # how often `git fetch` refreshes remote tracking refs
fetch_timeout        = "30s"    # per-repo `git fetch` timeout (a slow remote can't stall the pass)
silent_threshold     = "20s"    # zero PTY output past this warns the session is silent
adopted_grace        = "60s"    # after a daemon upgrade, keep the prior status this long ("0" disables)
recent_output_window = "3s"     # recent PTY output alone implies "active" when scraping is inconclusive ("0" disables)
hook_start_window    = "5s"     # a SessionStart hook stays authoritative this long
hook_activity_window = "30s"    # tool-use hooks (prompt / pre / post) stay authoritative this long
hook_terminal_window = "30m"    # ready / approval hooks (Stop, idle, permission) stay authoritative this long
```

**Scanning.** `scan_interval` is the scrollback poll cadence — the shortest window in which a status change can be noticed from PTY text. `fetch_interval` governs the much slower background `git fetch` that keeps `origin/<base>` fresh so the diverged-from-base count doesn't go stale after remote merges, and `fetch_timeout` bounds each repo's fetch so one slow remote can't stall the pass for other sessions.

**Hook authority.** When an agent hook reports a status, that report is trusted over PTY scraping until its window elapses. `hook_start_window`, `hook_activity_window`, and `hook_terminal_window` are the windows for start, tool-use, and terminal (ready/approval) events respectively — longer for terminal states, which are stable, and short for the noisy activity events.

**Scraping fallbacks.** `silent_threshold` is how long a running session may produce zero PTY output before the daemon logs a "silent session" warning. `recent_output_window` treats a session that produced output very recently as `active` even when the scraped text is inconclusive; `adopted_grace` keeps a session's previous status for a short window after a daemon upgrade re-attaches to a surviving agent, so a freshly adopted PTY isn't misread as `unknown`. Setting either window to `"0"` disables that fallback.

Empty or non-positive values fall back to the defaults shown above. These values are read live on each detection pass (and each hook report), so a config reload (`SIGHUP` / edit-and-save) takes effect without restarting the daemon; `scan_interval` and `fetch_interval` set the loop tickers at startup, so changing those two requires a `gr daemon restart`.

## Update check

`gr list` and `gr doctor` check GitHub for a newer graith release and cache the result. The `[updates]` block makes this configurable — turn it off for downstream forks, packaged or offline installs, or if you simply don't want the network call.

```toml
[updates]
enabled    = true            # check GitHub for newer graith releases
repository = "d0ugal/graith"  # GitHub "owner/repo" whose latest release is checked
interval   = "1h"            # how long a cached result stays fresh
timeout    = "5s"            # HTTP request timeout for the release check
```

Setting `enabled = false` disables all update-check network activity: `gr list` skips it silently and `gr doctor` reports the check as disabled rather than claiming you are up to date. The check never runs for a `dev` build regardless of this setting. Point `repository` at a fork to track its releases instead. An empty `interval` or `timeout` falls back to the built-in defaults (1h and 5s); a value that is set but unparseable, or a `repository` not in `owner/repo` form, is rejected at config load.
