---
weight: 320
title: "Session behavior"
description: "Headless sessions, delete retention, the launch throttle, and git pull."
icon: "tune"
toc: true
draft: false
---

## Headless sessions

**Experimental.** A headless session runs the agent in Claude Code's stream-json mode instead of an interactive PTY — for fire-and-forget sessions no human will attach to (tribunal judges, trigger briefings). graith parses the typed event stream, so `gr logs -f` renders it and the run's cost/token usage is captured from the result envelope. v1 is Claude-only, one-shot, requires a prompt, and is **incompatible with the sandbox** (headless requires `sandbox.enabled = false`, or a per-agent sandbox that resolves to disabled).

```toml
[headless]
experimental = false  # master gate: headless is inert unless this is on
default      = false  # once enabled, whether new sessions go headless by default
```

`experimental` is the master switch. While it is off, headless is inert: `gr new --headless` and `[trigger.action] headless = true` have no effect. It is gated because the underlying control protocol is undocumented and may change between Claude Code releases.

v1 is **Claude-only** and **one-shot** (one prompt, run to completion, exit). Only agents marked `headless_capable = true` can run headless — see [Agent definitions]({{< relref "agents.md#agent-definitions" >}}). Requesting headless for an agent that can't do it is an error, not a silent fallback to PTY.

`gr attach` on a headless session is not supported yet; use `gr logs -f` to inspect one read-only (see [Session Lifecycle → Headless sessions]({{< relref "/docs/sessions.md#headless-sessions" >}})). Convert-to-interactive on attach is planned.

A headless session drives Claude Code over its stdin control protocol, giving graith a clean interrupt and inline tool-approval handling. It has no human to answer prompts, so its approval policy must be **non-blocking**: `yolo` auto-allows, a non-blocking `[approvals]` backend decides, and anything that would queue for a human is denied (escalated once to the orchestrator inbox). See [Session Lifecycle → Headless sessions]({{< relref "/docs/sessions.md#headless-sessions" >}}).

## Delete retention

```toml
[delete]
retention = "24h"  # how long soft-deleted sessions are kept before purge
```

`gr delete` is a soft delete: it stops the agent and hides the session but keeps its worktree, branch, and state for this window, so `gr restore` can bring it back. A background loop hard-deletes sessions once their retention expires. Setting `retention = "0"` makes soft delete keep sessions **forever** (never auto-purged) — it does not turn `gr delete` into a destructive delete. `gr purge` is the only immediate, destructive verb; it bypasses the window.

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
