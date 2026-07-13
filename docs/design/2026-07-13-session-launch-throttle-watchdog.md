---
title: "Design Doc: Session-launch throttle & startup watchdog"
authors: fix-1092-launch
created: 2026-07-13
status: Accepted
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1092
---

# Session-launch throttle & startup watchdog

When several agent sessions are launched in a short window, some agent
processes stall for minutes (occasionally tens of minutes) during startup: the
sandbox wrapper is running but the agent binary never finishes initialising, so
it produces no output, never connects, and sits at `agent_status: unknown`
indefinitely. This doc adds two defences: a **launch throttle** that bounds how
many agent spawns are in their heavy-init window at once, and a **startup
watchdog** that detects a session stuck with no output past a threshold and
restarts it fresh.

## Background

`SessionManager.Create` / `Fork` / `resumeWithSummaryAndPrompt`
(`internal/daemon/daemon.go`) each end in a `grpty.NewSession(...)` call that
forks the (usually sandboxed) agent under a PTY. Once forked, the agent binary
(for Claude, a ~400MB node process) initialises on its own; graith learns it is
alive by scraping PTY output or receiving hook status reports. A background
`RunDetectionLoop` polls each running session every 500ms and records
`LastOutputAt` and a derived `AgentStatus`.

There is currently no coordination between concurrent launches: N simultaneous
`gr new` calls all fork their agents at once and all initialise at once.

## Problem

Under a burst (issue #1092), the tail of concurrent launches blows up. Observed:
the first 4 sessions in a burst went active in 2–8s; the 5th took 23 minutes;
the 6th took 23 minutes then was killed; the 7th never started. A stalled
session sits at ~9MB RSS (sandbox wrapper only — the agent's node runtime never
loaded), emits zero output, never connects, and shows `agent_status: unknown`
for its whole life. Once such a zombie is eventually signalled, resume can also
fail (#1091), leaving it unrecoverable.

This is not the sandbox backend: running the sandbox invocation concurrently
with a trivial command completes fine. It is resource contention from many
heavyweight agent runtimes initialising simultaneously.

## Goals

- Bound concurrent agent-startup contention so a burst starts cleanly instead of
  stampeding.
- Detect a session stuck in startup (no output, `unknown`) past a configurable
  threshold and recover it automatically rather than leaving a zombie.
- Make both knobs configurable (throttle limit, watchdog timeout).
- Emit structured logs across the launch/startup path so future incidents are
  diagnosable from logs alone (the incident above had to be reconstructed from
  sparse lines).

## Non-Goals

- Diagnosing the root cause inside the agent runtime / OS (an API rate limit, a
  node startup lock, etc.). The throttle and watchdog are mitigations robust to
  whatever the underlying contention is.
- A full resume-fallback rework — that is #1091. This doc only ensures the
  watchdog's restart uses a fresh start (`FreshStart`) so a killed pre-connect
  session recovers rather than dying on `--resume` against a never-persisted
  conversation.

## Proposals

### Proposal 0: Do Nothing

Leave launches uncoordinated. Rejected: the tail latency under bursts is
severe (tens of minutes) and produces permanently-stuck sessions.

### Proposal 1: Throttle + watchdog (Recommended)

**Launch throttle.** A weighted semaphore (`launchThrottle`, capacity
`[launch] max_concurrent`) is acquired immediately before each `NewSession`
spawn. The slot is *not* released when the fork returns — it is held across the
risky init window and released when the session produces its first output
(`LastOutputAt` becomes non-zero) or a `settle_timeout` safety cap elapses,
whichever comes first. That bounds the number of agents *initialising* at once,
not just the number of forks, which is what actually matters (the burst
evidence shows the stalls come from concurrent init, not the fork itself).
Releasing happens in a background goroutine so `Create`/`Resume` still return
promptly. On spawn error the slot is released immediately.

**Startup watchdog.** `RunStartupWatchdogLoop` ticks periodically and looks for
sessions that are `StatusRunning`, have a live PTY that has produced no output,
carry an `unknown`/empty `AgentStatus`, and have been running longer than
`[launch] startup_timeout`. Each such session is killed and restarted with
`FreshStart = true` (so a forced-id agent uses `--session-id` rather than
`--resume`, dovetailing with #1091). A per-session counter caps consecutive
watchdog restarts so a permanently-broken session is marked errored instead of
looping forever; the counter resets once the session produces output.

**Config.** A new `[launch]` table: `max_concurrent` (default 3),
`startup_timeout` (default `3m`, `"0"` disables the watchdog), `settle_timeout`
(default `10s`, how long a slot waits for first output before releasing).

**Logging.** Slot acquire logs queue-wait and in-flight/capacity; slot release
logs time-to-first-output; the watchdog logs each kill/restart with full session
context (id, name, age, peak RSS, agent_status, PID, attempt).

Chosen because it is robust to the unknown root cause, needs no protocol
change, and reuses the existing detection-loop and Restart machinery.

### Proposal 2: Serialize launches completely (max_concurrent = 1)

A special case of Proposal 1. Rejected as the *default* — the evidence shows
~4 concurrent startups are fine, so fully serial startup needlessly slows the
common case. It remains reachable via config.

## Consensus

Not yet reviewed.

## Other Notes

The throttle capacity is re-read on config reload by swapping the semaphore
channel; slots held against the old channel release harmlessly against it. The
watchdog skips the orchestrator session, which has its own supervisor
(`orchestrator.go`) that already handles fresh-start restarts.
</content>
