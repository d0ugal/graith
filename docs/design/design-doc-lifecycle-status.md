---
title: "Design Doc: Automatic Lifecycle Status Updates"
authors: Dougal Matthews
created: 2026-06-18
status: Draft
---

# Automatic Lifecycle Status Updates

## Background

The overlay session picker shows each session's status — either explicitly set
by the agent via `gr status "..."` or derived from hook reports ("Using Bash").
But lifecycle events that the *daemon* controls — creation, stopping, crashing,
idle timeout — leave no trace in the status. A user opening the overlay sees a
stopped session with no context about why it stopped or who created it.

## Problem

When looking at the overlay, there's no way to tell:

- Why a session stopped (idle timeout? crash? user request? daemon shutdown?)
- Who spawned a session (parent agent? user?)
- Whether a session was just created, resumed, or restarted

The `StopReason` field exists in state but isn't surfaced in the status text.
The `ParentID` field tracks lineage but isn't visible at a glance.

## Goals

- Surface lifecycle context in the overlay status without requiring agent
  cooperation
- Preserve the agent's last status as context when the daemon changes state
- Keep the implementation simple — reuse summary fields, no new protocol
  messages

### Non-Goals

- Changing the overlay rendering (this uses existing status display)
- Adding new fields to `SessionInfo` or the protocol
- Notification/alerting on lifecycle events

## Proposal

### How it works

The daemon writes `SummaryText`/`SummarySetAt`/`SummaryTTL` directly on the
`SessionState` at lifecycle transition points. These statuses show up in the
overlay the same way agent-set statuses do, and they get overwritten when the
agent calls `gr status` with its own message.

### Lifecycle events and status text

| Event | Status text | Where |
|-------|-------------|-------|
| Child session created | `Created by {parent_name}` | `Create()` phase 3, inside lock |
| Session forked | `Forked from {source_name}` | `Fork()` phase 3, inside lock |
| Session resumed | `Resumed` | `Resume()` phase 3, inside lock |
| Session restarted | `Restarted` | `Restart()` via resume with flag |
| Stopped: user | `Stopped (was: {prev})` | `watchSession()`, inside lock |
| Stopped: idle | `Stopped after idle (was: {prev})` | `watchSession()`, inside lock |
| Stopped: shutdown | `Stopped by shutdown (was: {prev})` | `StopAll()`, inside lock (before kill) |
| Stopped: crash (exit != 0) | `Crashed exit {code} (was: {prev})` | `watchSession()`, inside lock |
| Exited cleanly (exit 0, no reason) | `Exited (was: {prev})` | `watchSession()`, inside lock |
| Reconcile: mid-creation | `Interrupted by daemon restart` | `Reconcile()`, direct field set |
| Reconcile: orphaned process | `Lost during daemon restart` | `Reconcile()`, direct field set |
| Reconcile: mid-deletion | `Delete interrupted by restart` | `Reconcile()`, direct field set |
| Adoption failure | `Lost during daemon upgrade` | `AdoptSessions()`, inside lock |

All lifecycle statuses use `SummaryTTL = 0` (config default). For running
sessions, the status fades normally when the agent starts producing output
without updating it. For stopped sessions, the status persists (faded)
indefinitely since there's no agent activity to clear it — which is the desired
behavior.

**System sessions** (`SystemKind != ""`) are skipped for lifecycle statuses —
users don't interact with them directly and "Created by" on an auto-spawned
orchestrator would be noise. This matches `IsSystemSession()` in validate.go
and is forward-compatible with future system kinds beyond orchestrator.

### Interaction with agent-set status

**Start events** (created, forked, resumed, restarted): The daemon sets the
status inside the phase-3 lock, before `saveState()`, before the session
result is returned. The agent process is just starting, so there's no existing
agent status to preserve. The agent's first `gr status` call overwrites the
lifecycle status naturally.

Note: `Resume()` does not clear `SummaryText` — it carries over from the
previous run. Setting "Resumed" explicitly overwrites whatever was there, which
is the desired behavior.

**Stop events** (user, idle, crash, shutdown): The daemon appends context from
the previous `SummaryText`. The format is `{stop_reason} (was: {previous})`.
The previous text may be agent-set (e.g. "Running tests") or daemon-set (e.g.
"Resumed" if the agent never called `gr status`). If no summary was set, the
parenthetical is omitted:
- With prior status: `Stopped after idle (was: Running tests)`
- Without: `Stopped after idle`

The "(was: ...)" context comes from `s.SummaryText` only — not from
hook-derived statuses like "Using Bash". Hook-derived statuses are computed
on-the-fly in `toSessionInfo` from runtime `hookReports` and are never stored
in `SummaryText`. This means a session whose only visible status was
hook-derived will show a bare stop reason. This is acceptable for v1; a future
enhancement could also consult the current hook report when building the
suffix.

To avoid resurrecting stale context, the "(was: ...)" suffix is only included
when the previous summary is still within its **effective TTL** — computed the
same way `toSessionInfo` does (handler.go:879-882): if the previous summary had
a custom `SummaryTTL` (e.g. from `gr status --ttl 30m`), use that; otherwise
use `cfg.Status.TTLDuration()`. The effective TTL must be snapshotted before
`applyLifecycleSummaryLocked` overwrites `SummaryTTL` to 0. If the previous
summary had already expired under its own TTL, the suffix is omitted.

**Top-level sessions created by the user** get no initial lifecycle status —
the user knows they just created it. Only child sessions (where `ParentID` is
set) get a "Created by" status. `ParentID` is always set by the CLI from
`GRAITH_SESSION_ID` (cli/new.go:84), stored unconditionally by the daemon
(daemon.go:510), and resolved from the global `sm.state.Sessions` map — so
this works correctly across repos. The parent's name is looked up under the
same lock; if the parent was already deleted, the lifecycle status is skipped.

### Implementation

Two helpers — one for use inside locked sections, one for Reconcile's direct
field mutation:

```go
// applyLifecycleSummaryLocked sets lifecycle status on a session.
// Caller must hold the lock guarding s. Sanitizes and truncates to fit
// the 100-byte limit. Does not call saveState() — caller must persist.
func applyLifecycleSummaryLocked(s *SessionState, text string) {
    if s.SystemKind != "" {
        return
    }
    text = sanitizeSummaryText(text)
    text = truncateToBytes(text, 100)
    now := time.Now()
    s.SummaryText = text
    s.SummarySetAt = &now
    s.SummaryTTL = 0
}
```

This helper:
- Skips all system sessions (`SystemKind != ""`), matching `IsSystemSession()`
- Sanitizes and truncates text before writing (never relies on error from
  `SetSummary`)
- Does **not** call `saveState()` — the caller persists in the same
  `saveState()` that commits the lifecycle transition, avoiding a double
  disk write
- Is best-effort by nature: if something goes wrong, the lifecycle
  transition still succeeds

For `Reconcile()`, which is a method on `*State` (not `*SessionManager`),
apply the same sanitize+truncate logic and set the fields directly on
`SessionState`. `Reconcile` cannot call `SessionManager` methods because
it runs during `LoadState()` before `sm.state` is assigned.

**Call sites** (9 total):

1. **`Create()` phase 3** — inside the lock (daemon.go:785-830), after
   setting `StatusRunning`, before `saveState()`. If `parentID != ""`, look up
   `sm.state.Sessions[parentID].Name` under the same lock. If the parent
   was deleted, skip.
   ```go
   applyLifecycleSummaryLocked(sessState, "Created by "+parentName)
   ```

2. **`Fork()` phase 3** — inside the lock, after setting `StatusRunning`,
   before `saveState()`.
   ```go
   applyLifecycleSummaryLocked(sessState, "Forked from "+sourceName)
   ```

3. **`resumeWithSummary()` phase 3** — an unexported helper
   `resumeWithSummary(id string, rows, cols uint16, lifecycleSummary string)`
   contains the actual resume logic. Inside the lock (daemon.go:1619-1668),
   after setting `StatusRunning`, before `saveState()`:
   ```go
   applyLifecycleSummaryLocked(sessState, lifecycleSummary)
   ```
   The public `Resume()` delegates with `"Resumed"`, keeping its signature
   `Resume(id string, rows, cols uint16)` unchanged — no churn across
   ~17 call sites in handler.go, orchestrator.go, and tests.

4. **`Restart()`** — calls `resumeWithSummary()` with `"Restarted"`.
   No separate lifecycle write needed. This avoids the race where
   `watchSession` from the old PTY could overwrite a post-resume status.

5. **`watchSession()`** — inside the lock (daemon.go:1205-1232), after
   setting `StatusStopped` and `StopReason`, before `saveState()`. Snapshot
   `prevSummary`, `prevSetAt`, and the effective TTL **before**
   `applyLifecycleSummaryLocked` overwrites them. The effective TTL must
   match how `toSessionInfo` resolves freshness (handler.go:879-882): use
   the previous summary's `SummaryTTL` if set, else `sm.cfg.Status.TTLDuration()`.
   ```go
   prevSummary := s.SummaryText
   prevSetAt := s.SummarySetAt
   prevTTL := sm.cfg.Status.TTLDuration()
   if s.SummaryTTL > 0 {
       prevTTL = time.Duration(s.SummaryTTL) * time.Second
   }
   text := formatStopSummary(s.StopReason, s.ExitCode, prevSummary, prevSetAt, prevTTL)
   applyLifecycleSummaryLocked(s, text)
   ```

6. **`AdoptSessions()`** — inside the lock (daemon.go:227-258), when PTY
   adoption fails and session is marked `StatusStopped`.
   ```go
   applyLifecycleSummaryLocked(sessState, "Lost during daemon upgrade")
   ```

7. **`Reconcile()`** — direct field mutation on `SessionState` (state.go:275).
   Skip system sessions (`SystemKind != ""`) before applying summaries.
   For each reconcile case:
   - `StatusCreating` → `StatusErrored`: "Interrupted by daemon restart"
   - `StatusRunning` + dead PID → `StatusStopped`: "Lost during daemon restart"
   - `StatusDeleting` → `StatusStopped`: "Delete interrupted by restart"

8. **`StopAll()`** — inside the lock (daemon.go:2399-2406), where it
   already sets `StopReasonShutdown` and calls `saveState()`. Write the
   shutdown summary here **before** killing PTYs, so it is persisted even
   if the daemon exits before `watchSession` runs. Snapshot the effective
   TTL per-session (same logic as call site 5) to get the "(was: ...)"
   suffix right.
   ```go
   for _, s := range sm.state.Sessions {
       if s.Status == StatusRunning {
           prevSummary := s.SummaryText
           prevSetAt := s.SummarySetAt
           prevTTL := sm.cfg.Status.TTLDuration()
           if s.SummaryTTL > 0 {
               prevTTL = time.Duration(s.SummaryTTL) * time.Second
           }
           s.StopReason = StopReasonShutdown
           text := formatStopSummary(StopReasonShutdown, nil, prevSummary, prevSetAt, prevTTL)
           applyLifecycleSummaryLocked(s, text)
       }
   }
   ```
   When `watchSession` later acquires the lock for these sessions, it sees
   `StopReason` already set and `SummaryText` already populated. It can
   skip or overwrite — either way the shutdown summary is already persisted.

9. **Restart's manual stop block** (daemon.go:2118-2127) — when `Restart()`
   manually patches `StatusStopped` before calling `Resume()`, skip the
   lifecycle summary here. `watchSession` for the old PTY will see
   `sm.sessions[id] != sess` (stale) and skip its write too. The final
   status comes from `resumeWithSummary()` with "Restarted".

### Stop reason formatting

```go
func formatStopSummary(reason string, exitCode *int, prev string, prevSetAt *time.Time, ttl time.Duration) string {
    var base string
    switch reason {
    case StopReasonUser:
        base = "Stopped"
    case StopReasonIdle:
        base = "Stopped after idle"
    case StopReasonShutdown:
        base = "Stopped by shutdown"
    case StopReasonCrash:
        if exitCode != nil && *exitCode == 0 {
            base = "Exited"
        } else if exitCode != nil {
            base = fmt.Sprintf("Crashed exit %d", *exitCode)
        } else {
            base = "Crashed"
        }
    default:
        base = "Stopped"
    }

    // Only append "(was: ...)" if previous summary is fresh (within TTL)
    if prev == "" || prevSetAt == nil || time.Since(*prevSetAt) > ttl {
        return base
    }
    return truncateWithContext(base, prev, 100)
}

func truncateWithContext(base, prev string, maxBytes int) string {
    suffix := " (was: " + prev + ")"
    full := base + suffix
    if len(full) <= maxBytes {
        return full
    }
    // Truncate prev to fit
    overhead := len(base) + len(" (was: ...)") 
    avail := maxBytes - overhead
    if avail <= 0 {
        return base
    }
    return base + " (was: " + truncateToBytes(prev, avail) + "...)"
}
```

Note: `truncateToBytes` must not split multi-byte UTF-8 sequences. Truncate
at rune boundaries, checking byte length.

### Edge cases

**Summary text length**: All dynamic text (parent names, source names,
previous status) is truncated before writing. Session names can be up to 128
bytes, so "Created by {name}" can exceed 100 bytes — the helper truncates the
name portion. The `applyLifecycleSummaryLocked` helper always truncates the
final text, so `SetSummary`'s 100-byte hard rejection is never hit.

**Exit code 0 without explicit stop**: When a process exits cleanly (code 0)
but no `StopReason` was set, `watchSession` defaults to `StopReasonCrash`.
The formatter distinguishes this: exit 0 → "Exited", non-zero → "Crashed exit
N". This avoids the misleading "Crashed exit 0".

**Rapid state transitions**: A session that is created and immediately receives
an agent status update might briefly flash "Created by X" then switch. This is
fine — the agent's status is more current and more useful.

**Restart flow**: `Restart()` (daemon.go:2104) does NOT call `Stop()`. It:
1. Locks, sets `StopReasonUser`, unlocks
2. Kills PTY, waits on `<-Done()`
3. Locks, manually sets `StatusStopped` (no lifecycle summary here), unlocks
4. Calls `Resume()` which writes "Restarted" atomically in phase 3

In practice, `watchSession` almost always acquires the lock **before** Resume's
phase 3, because `<-ptySess.Done()` unblocks both goroutines simultaneously
but Resume's unlocked phase 2 (hook injection, PTY spawn, git operations)
takes significant time. So the common path is: `watchSession` runs first, sees
`sm.sessions[id]` still pointing to the old PTY (not stale), and writes
`StatusStopped` + a transient stop summary. This is harmless — Resume phase 3
overwrites everything atomically. In the less common path where Resume phase 3
completes first, `watchSession` sees `sm.sessions[id] != sess` (stale) and
skips entirely. Both paths converge on the same final state: "Restarted" set
atomically with the Running transition.

**Reconcile timing**: `Reconcile()` is a method on `*State` (state.go:275),
not `*SessionManager`. It runs during `LoadState()` (daemon.go:222) before
`sm.state` is assigned, so `SessionManager` methods like `SetSummary` are not
available. Write `SummaryText`, `SummarySetAt`, and `SummaryTTL` directly on
the `SessionState` struct, applying the same sanitize+truncate logic. Because
`SummarySetAt` is set to `now()` at reconcile time, these statuses render as
fresh (non-faded) for a full TTL window after every daemon start. This is
intentional — the user should notice sessions that were interrupted.

**Stale "(was: ...)" context**: The suffix is only included when the previous
summary's `SummarySetAt` is within TTL. A summary that faded out hours ago
won't be resurrected.

**Concurrent `gr status` from agent**: At stop time the agent is dead, so no
concurrent writes. At start time, the PTY is spawned during phase 2 (before the
phase-3 lock), so in theory an agent could call `gr status` before phase 3
runs. Phase 3 would then overwrite the agent's status. In practice this is
extremely unlikely — agent processes take seconds to initialize — but if it
becomes an issue, phase 3 can guard with `if s.SummaryText == ""` to avoid
clobbering an agent-set status.

## Other Notes

### References

- `internal/daemon/daemon.go` — `SetSummary()`, `watchSession()`, `Create()`,
  `Fork()`, `Resume()`, `Restart()`, `AdoptSessions()`, lifecycle methods
- `internal/daemon/state.go` — `SessionState` struct, `Reconcile()`
- `internal/daemon/handler.go` — `toSessionInfo()` summary resolution
- `internal/daemon/orchestrator.go` — system session lifecycle
- `internal/cli/new.go` — `ParentID` from `GRAITH_SESSION_ID`

### Implementation Notes

- No state migration needed — writes to existing `SummaryText`/`SummarySetAt`/`SummaryTTL` fields
- No protocol changes — uses direct field mutation under the existing lock
- No config changes — uses existing TTL config for expiration
- Lifecycle statuses are not user-configurable; `gr status` remains
  authoritative while running, daemon owns the stop-time narrative

### Testing

**Unit tests:**
- `formatStopSummary` — all stop reasons (user, idle, crash, shutdown) with
  and without prior summary, with stale vs fresh prior summary, and with
  custom `SummaryTTL` (e.g. `--ttl 30m`) to verify effective TTL resolution
- `truncateWithContext` — byte boundary truncation, long names, multi-byte
  UTF-8 rune safety
- `applyLifecycleSummaryLocked` — system session skip (`SystemKind != ""`),
  sanitization, field mutation
- Exit code 0 → "Exited" vs non-zero → "Crashed exit N"
- Start events: "Created by" only when ParentID present and resolvable

**Integration tests:**
- Create a child session → assert "Created by {parent}" in list
- Stop session by user → assert "Stopped" status appears
- Idle-stop a session → assert "Stopped after idle" with context
- Restart a session → assert final status is "Restarted" (not "Stopped" or
  "Resumed")
- Daemon restart reconcile → sessions show appropriate interrupt messages
- Daemon shutdown → assert "Stopped by shutdown" persisted (survives daemon
  exit without relying on `watchSession`)
- Custom TTL: agent `--ttl 30m`, stop within 30m → "(was: ...)" present;
  stop after 30m → suffix absent

**All tests must pass with `-race` flag.** The locked helper design ensures
no concurrent field access outside `sm.mu`, but `-race` catches any oversight.
