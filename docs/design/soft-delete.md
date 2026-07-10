---
title: "Design Doc: Soft Delete with Recovery Window"
authors: Dougal Matthews
created: 2026-07-10
status: Draft
reviewers: (none yet)
informed: (TBD)
---

# Soft Delete with Recovery Window

## Background

`gr delete` (alias `gr rm`) is destructive and immediate. Today the CLI sends a
`delete` control message, the handler routes it through `handleSessionLifecycle`,
and `SessionManager.Delete(id)` (`internal/daemon/daemon.go`) runs synchronously:

1. Kill the PTY / agent process (detach, kill, force-kill after a 5s grace).
2. Tear down the worktree and branch via `git.TeardownSession` (`git worktree
   remove --force` + `DeleteBranch`), or `os.RemoveAll` for `--no-repo`/shared
   scratch dirs. In-place sessions leave the repo untouched.
3. Reparent any children, remove the session from `sm.state.Sessions`, drop its
   auth token from `tokenIndex`, delete the log file, nono profile, and safehouse
   fragment, and `saveState`.

After that the session is gone. There is no undo. The worktree directory, the git
branch (including any un-pushed commits on it), and the `state.json` record are
all removed.

This is a sharp edge that has drawn blood. The most common failure is deleting a
session that was merely *stopped* — mistaking a paused-but-valuable session for
one whose work has already landed on `main`. Because a stopped session and a
"done, safe to bin" session look similar in `gr list`, a single `gr delete braw`
can vaporise un-pushed commits and an entire worktree with no confirmation beyond
the existing unsaved-work prompt (which is itself skipped by `--force` and by
batch deletes).

The codebase already contains most of the primitives a recovery window needs:

- **Time-boxed fields on sessions.** `SessionState.SummaryTTL int` /
  `SummarySetAt *time.Time` and the runtime `IdleSince *time.Time` (compared
  against a timeout in `checkIdleSession`) are working examples of "this field
  expires at a computed time".
- **A versioned, migratable state file.** `state.json` carries a `Version`
  (`CurrentStateVersion = 13`) and a `migrations` chain; additive fields are a
  version bump plus a no-op migration.
- **Config-driven durations.** `config.ParseDurationWithDays` parses `"24h"`,
  `"7d"`, `"7d12h"`. `StatusConfig.TTL`/`TTLDuration()` and
  `Messages.MaxAge`/`MaxAgeDuration()` are the established "duration string in
  TOML with a Go accessor and a default" pattern.
- **Background maintenance loops.** `RunMessageCleanupLoop` (hourly ticker) and
  `RunGitPullLoop` (startup-delay timer, then config interval) are templates for
  a periodic reaper that survives daemon restarts.
- **A single `list` chokepoint.** Both `gr list` and the overlay session picker
  fetch sessions through the same `list` control message, so filtering
  server-side hides deleted sessions everywhere at once.

## Problem

Deletion is irreversible, and the point of no return arrives with no grace
period. Users want the ergonomics of `git reflog` or a desktop trash can: delete
freely, and get a window to change your mind before anything is actually
destroyed. Concretely:

1. `gr delete` should stop being immediately destructive by default. It should
   *retire* a session — hide it, stop its agent — while keeping the worktree,
   branch, and state around for a retention window (default 24h).
2. There must be a way to *see* what has been soft-deleted and how long is left
   before it is purged.
3. There must be a way to *bring one back* within the window, landing it in a
   resumable state.
4. There must still be a way to delete *right now* for the cases where you know
   you are done and want the disk back.
5. Purging expired sessions must happen automatically, reliably, and without a
   running client — i.e. in the daemon.

## Goals

- `gr delete` performs a **soft delete** by default: the session is marked
  deleted, its agent is stopped, and it disappears from `gr list` and the
  overlay, but its worktree, branch, and `state.json` record are preserved until
  the retention window elapses.
- `gr restore <name>` **un-deletes** a soft-deleted session within its window,
  returning it to `stopped` so it can be `gr resume`d.
- `gr list --deleted` shows soft-deleted sessions and their **expiry time**.
- `gr delete --force` / `gr delete --purge` **hard-deletes immediately**,
  bypassing the window (today's behaviour).
- The daemon runs a **purge loop** that hard-deletes soft-deleted sessions once
  their window expires, catching up on restart for windows that elapsed while it
  was down.
- Retention is configurable via `[delete] retention`, with `"0"` disabling soft
  delete entirely (every `gr delete` is a hard delete).
- Backward compatible: existing `state.json` files load unchanged; existing
  scripts calling `gr delete --force` keep their exact meaning.

### Non-Goals

- **Compressing or archiving worktrees.** Soft-deleted worktrees stay on disk
  as-is (see [Worktree handling](#worktree-handling)). Reclaiming that disk is a
  possible future optimisation, not part of this design.
- **Recovering after the window.** Once purged, a session is gone. The window is
  the only recovery mechanism; we are not adding a separate long-term archive.
- **A general-purpose undo for other operations** (stop, rename, fork). This is
  scoped to delete.
- **Changing scenario teardown.** `gr scenario stop/delete` and internal
  rollback call `SessionManager.Delete` directly and must stay hard deletes —
  scenario lifecycle owns its worktrees and expects them gone.

## Proposal

### The marker: `DeletedAt`, not a new status

A soft-deleted session is marked by a new nullable timestamp on
`SessionState`, **not** by a new `SessionStatus` value:

```go
type SessionState struct {
    // ... existing fields ...
    DeletedAt *time.Time `json:"deleted_at,omitempty"` // nil = live; set = soft-deleted
}
```

`nil` means live; a non-nil value is the instant the soft delete happened. The
purge deadline is `DeletedAt.Add(retention)`, computed from the *current* config
at purge time (not frozen at delete time — see
[Config](#config-delete-retention)).

Why a timestamp field and not a `StatusDeleted` enum value:

- **`StatusDeleting` already exists** as a *transient* marker used during the
  synchronous hard-delete teardown, and `State.Reconcile()` reverts any session
  stuck in `deleting` back to `stopped` on daemon restart ("Delete interrupted by
  restart"). A persistent `deleted` status would collide with that reconciliation
  logic and force careful disambiguation everywhere status is switched on.
- A soft-deleted session is semantically a **stopped** session that happens to be
  hidden and scheduled for purge. Keeping `Status = stopped` means resume/restore
  semantics, exit-code handling, and status formatting all keep working without
  special cases. The only new axis is "is it in the trash", which is exactly one
  boolean's worth of information — carried by whether `DeletedAt` is set.

A helper centralises the check:

```go
func (s *SessionState) IsSoftDeleted() bool { return s.DeletedAt != nil }
```

### State transitions

Soft delete slots into the existing lifecycle as a hidden terminal-ish state that
can loop back to `stopped`:

```
                gr delete (retention > 0)
  running ──stop agent──▶ stopped ──set DeletedAt──▶ [soft-deleted]
     │                       ▲                            │  │
     │                       │  gr restore (clear         │  │
     │                       └─── DeletedAt)──────────────┘  │
     │                                                       │ purge loop
     │ gr delete --force / --purge                           │ (now-DeletedAt
     ▼                                                       ▼  >= retention)
  ─────────────────── hard delete (Delete) ───────────────────▶ gone
```

- **`running → soft-deleted`**: `gr delete` on a running session stops the agent
  (the same PTY teardown Stop/Delete already do) and sets `DeletedAt`. Status
  becomes `stopped`.
- **`stopped → soft-deleted`**: sets `DeletedAt`; agent is already stopped.
- **`soft-deleted → stopped`**: `gr restore` clears `DeletedAt`. The session
  reappears in `gr list` as a normal stopped session, ready for `gr resume`.
- **`soft-deleted → gone`**: the purge loop (or `gr delete --purge`) runs the
  existing hard `Delete`.
- **`* → gone`**: `gr delete --force`/`--purge` and all non-CLI callers
  (scenarios) go straight to hard `Delete`, unchanged.

The daemon — not the CLI — decides soft vs hard, based on
`!Force && retention > 0`. The CLI does not know the retention value (it lives in
daemon-side config), so it simply forwards whether `--force`/`--purge` was
given. See [Who decides](#who-decides-soft-vs-hard).

### CLI UX

#### `gr delete` (default: soft)

```
$ gr delete braw
Soft-deleted braw. Recoverable until 2026-07-11 06:53 (in 24h).
  gr restore braw        to bring it back
  gr delete braw --purge to remove it now
```

The existing **unsaved-work confirmation** is preserved: if the worktree has
uncommitted or un-pushed work, `gr delete` still prompts `Delete anyway? [y/N]`
unless `--force`. This is deliberately kept even for soft delete — it is
protective regardless of whether the daemon ultimately soft- or hard-deletes
(e.g. with `retention = "0"` a soft delete *is* a hard delete), and it costs
nothing when there is no unsaved work. Because the CLI doesn't know the retention
value, the prompt wording stays generic ("Delete …?"); the success line printed
afterward reflects what actually happened, driven by the daemon's response.

#### `gr delete --force` / `gr delete --purge`

Both mean **hard-delete now, bypassing the window, and skip the confirmation
prompt**. They are aliases with one nuance:

- `--force` retains its historical meaning ("don't prompt") and *additionally*
  now means "don't soft-delete". Existing scripts using `--force` keep working
  and get the same immediate destruction they always did.
- `--purge` is the discoverable, self-documenting name for "really delete now",
  and is the flag we advertise in the soft-delete success message. It also works
  on an already-soft-deleted session (empty the trash for one entry).

They are equivalent flags; `--purge` is sugar. Supplying both is not an error.

#### `gr restore <name>`

```
$ gr restore braw
Restored braw (stopped). Resume it with: gr resume braw
```

Restore clears `DeletedAt`, leaves `Status = stopped`, and returns the
`SessionInfo`. Auth mirrors delete (`authSelfOrDescendant`). Shell completion for
`gr restore` queries the **deleted** list (a new `completeDeletedSessionNames`),
since live-name completion would never see soft-deleted sessions.

#### `gr list --deleted`

```
$ gr list --deleted
NAME    REPO     AGENT   STATUS   DELETED           EXPIRES
braw    graith   claude  stopped  2026-07-10 06:53  in 23h
dreich  graith   codex   stopped  2026-07-09 20:10  in 9h
```

`--deleted` shows *only* soft-deleted sessions, with `DELETED` (when) and
`EXPIRES` (relative time until purge) columns in place of the usual live-only
columns. Default `gr list` (and the overlay, and `--json`, and every other
`list` caller) shows only live sessions. The two views are mutually exclusive;
there is no combined view by default, keeping the common case uncluttered.

### Server-side hiding via the `list` message

All session listing funnels through the `list` control message, which today is
sent with an empty `struct{}{}` by ~15 CLI callsites and the overlay. We add a
request field:

```go
type ListMsg struct {
    Deleted bool `json:"deleted,omitempty"` // false = live only; true = soft-deleted only
}
```

The handler's `case "list"` filters `sm.state.Sessions` by `IsSoftDeleted()`:
`Deleted == false` returns live sessions (the default everywhere), `Deleted ==
true` returns only soft-deleted ones. Filtering in one place means the overlay,
`gr list`, tree view, quiet mode, and JSON output all hide soft-deleted sessions
without touching each caller. Fleet-summary counts (the overlay's
running/stopped tallies) must likewise exclude soft-deleted sessions.

`toSessionInfo` gains the `DeletedAt` value (and a derived expiry) so
`--deleted` and `--json` can render it. For live listings `DeletedAt` is always
nil, so nothing changes.

`gr restore` and `gr delete --purge` need to resolve names that only exist in the
deleted list. `resolveSessionInfo` (used by most commands) searches the live list
only, which is correct for `info`/`path`/`resume`/`stop` — a soft-deleted session
is intentionally "gone" from normal operations. A new
`resolveDeletableSessionInfo` tries the live list first, then the deleted list,
so `--purge` works on both, and `gr restore` resolves against the deleted list.

### Control messages

Following the established three-file pattern (protocol struct → handler case →
CLI command):

- **`ListMsg`** (above) — extend the existing `list` request; response is the
  unchanged `SessionListMsg` (now filtered).
- **`DeleteMsg`** gains `Purge bool` (the CLI sets it when `--force`/`--purge` is
  given). The handler passes it through to the daemon, which chooses soft vs
  hard.
- **`RestoreMsg { SessionID string }`** — new `restore` control message; handler
  `case "restore"` authorizes with `auth.checkTarget(..., authSelfOrDescendant)`
  and calls `sm.Restore(id)`, replying `restored` with a `SessionInfo` (mirrors
  `resume.go`).

### Daemon: `SoftDelete`, `Restore`, and the purge loop

`SessionManager.Delete` is unchanged and remains the hard-delete implementation.
We add:

```go
// SoftDelete marks a session deleted, stops its agent, keeps everything on disk.
func (sm *SessionManager) SoftDelete(id string) (SessionState, error)

// Restore clears the soft-delete marker, returning the session to stopped.
func (sm *SessionManager) Restore(id string) (SessionState, error)
```

`SoftDelete` reuses the front half of `Delete`'s validation and the agent-kill
path of `Stop`:

1. Under lock: look up the session; reject the same cases `Delete` rejects —
   config-managed **system/orchestrator** sessions, **starred** sessions ("unstar
   it first"), and sessions in `StatusCreating`. Reject an **already
   soft-deleted** session with an error pointing at `gr restore` / `--purge`.
2. Remove the PTY from `sm.sessions` **before** killing it, exactly as `Delete`
   does. This preserves a critical invariant: `watchSession` treats a session as
   stale when it is no longer in `sm.sessions`, so the exit watcher will not race
   in and clobber `DeletedAt`/`Status` when the agent process exits.
3. Stop the agent (detach → kill → 5s grace → force-kill), the same teardown
   `Stop` performs. **Do not** touch the worktree or branch.
4. Set `DeletedAt = now`, `Status = stopped`, `StatusChangedAt = now`; write a
   summary ("Soft-deleted, recoverable until …"); `saveState`.

The routing decision lives in the handler's delete path: if `Purge` is set or
`retention == 0`, call `Delete`/`DeleteWithChildren` (hard); otherwise call
`SoftDelete`/`SoftDeleteWithChildren`.

`Restore`:

1. Under lock: look up the session; error if not found or not soft-deleted.
2. Clear `DeletedAt`, set `StatusChangedAt = now`, write a summary ("Restored");
   `saveState`. Status is already `stopped`.

The worktree still exists on disk (soft delete never removed it), so a subsequent
`gr resume` re-launches the agent in place with no worktree work — the normal
resume path.

#### `RunPurgeLoop`

Modeled on `RunGitPullLoop` (startup handling + ticker, survives restarts):

```go
func (sm *SessionManager) RunPurgeLoop(ctx context.Context) {
    // One sweep shortly after startup: catch windows that expired while the
    // daemon was down. Then a coarse ticker — retention is measured in hours,
    // so a 10-minute cadence is plenty precise and cheap.
    timer := time.NewTimer(purgeStartupDelay)
    defer timer.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-timer.C:
            sm.purgeExpired()
            timer.Reset(purgeInterval) // e.g. 10m
        }
    }
}
```

`purgeExpired` reads the current `retention` from config each pass (so config
reloads take effect), snapshots the soft-deleted session IDs whose
`now - DeletedAt >= retention` under a read lock, then hard-deletes each by
calling the existing `Delete(id)` — reusing all its teardown, reparenting, token
cleanup, and error handling. If `retention == 0`, there should be no soft-deleted
sessions to purge, but any that exist (created before a config change) are purged
immediately. Started alongside the other loops in the daemon's `Run`, so it stops
cleanly on context cancel during graceful shutdown.

Error handling and logging follow the existing loops: a failed `Delete` is logged
(slog, JSON) and left in place; the next sweep retries. Because purge goes
through the same `Delete` that already handles "PTY kill failed → mark errored,
keep for retry", a transiently un-killable session is not lost — it simply
survives to the next pass.

## Storage

**Decision: keep soft-deleted sessions in the same `state.json`, distinguished by
`DeletedAt`, and leave their worktrees in place.** No separate graveyard
directory, no moved files.

Options considered:

| Option | Pros | Cons |
|---|---|---|
| **A. Flag in place (chosen)** | Zero data movement; delete is O(1) and can't half-fail; restore is trivially a flag flip; survives daemon crash with no reconciliation; reuses `Delete` verbatim for purge | Deleted sessions share the `Sessions` map; every listing path must filter |
| B. Separate `Trash map[string]*SessionState` in state | Clear separation; iteration over live sessions untouched | Two maps to keep consistent; move-on-delete and move-on-restore can partially fail; `Reconcile`, token index, reparenting, and ~every `sm.state.Sessions` access must learn about the second map |
| C. Separate graveyard *directory* on disk (move worktree + a per-session json) | Frees the live state file; "trash" is browsable on disk | Moving a worktree breaks `git worktree`'s recorded path (the main repo's `.git/worktrees/<id>/gitdir` points at the old location) — restore would need `git worktree repair`; move is non-atomic across filesystems; large worktrees are slow/expensive to move twice |

Option A wins because the expensive, failure-prone work in deletion is the
*worktree teardown*, and soft delete's entire value proposition is **not doing
that work** until the window closes. Moving files around (B's map-shuffle risk,
C's worktree-path breakage) reintroduces exactly the failure surface we are
trying to defer. Filtering all listings through one server-side chokepoint (the
`list` handler) makes A's one real downside — a shared map — a single small
change rather than a scattered one.

## Worktree handling

Soft-deleted worktrees stay on disk **unchanged**, in their original location
(`<DataDir>/worktrees/<repoName>/<repoHash>/<id>`), with their branch intact.
This is what makes instant, lossless restore possible: `gr restore` + `gr resume`
finds the worktree exactly where the daemon left it.

The cost is disk. A soft-deleted session consumes the same space it did while
live for up to the retention window. This is an accepted trade-off:

- The default window is 24h, bounding worst-case accumulation to "everything
  deleted in the last day".
- Users who want the disk back immediately have `gr delete --purge`.
- Users who never want the overhead can set `retention = "0"`.

**Archiving/compression is explicitly out of scope** (a Non-Goal). It is a
plausible future enhancement — e.g. `git bundle` the branch and `tar.zst` the
worktree on soft delete, unpack on restore — but it trades restore latency and
implementation complexity for disk, and the 24h default already bounds the
exposure. `gr list --deleted` gives users visibility into what is pending purge,
which is the pragmatic mitigation for disk pressure (see Edge cases).

## Config: `[delete] retention`

New config sub-struct, following the `StatusConfig.TTL` pattern (duration string
in TOML, Go accessor with a default, parsed by `ParseDurationWithDays`):

```toml
[delete]
retention = "24h"   # how long soft-deleted sessions are kept before purge
# retention = "0"   # disable soft delete: every `gr delete` is a hard delete
# retention = "7d"  # days are supported (ParseDurationWithDays)
```

```go
type DeleteConfig struct {
    Retention string `toml:"retention"`
}

func (d DeleteConfig) RetentionDuration() time.Duration {
    if strings.TrimSpace(d.Retention) == "" {
        return 24 * time.Hour // default when unset
    }
    v, err := config.ParseDurationWithDays(d.Retention)
    if err != nil {
        return 24 * time.Hour // fail SAFE: never silently disable recovery
    }
    return v
}
```

Semantics, and the two edge cases worth calling out explicitly:

- **Unset** (`retention` absent, or the whole `[delete]` block absent): default
  **24h**. `config.Default()` sets this via `default_config.toml`.
- **`"0"` (or `"0s"`)**: soft delete is **disabled** — every `gr delete` is a
  hard delete. This is the one value that means "off", and it must be
  distinguishable from "unset". Because unset → 24h, we cannot treat the Go zero
  value as "off"; the parse of the literal string `"0"` yields a zero
  `time.Duration`, and the daemon's soft-vs-hard test is `retention > 0`, so a
  parsed zero correctly disables soft delete while an *unset* field takes the 24h
  default before ever reaching that test.
- **Unparseable** (e.g. `retention = "banana"`): fall back to **24h**, not to
  disabled. A typo in the retention string must never silently turn off recovery
  — that would recreate the exact data-loss footgun this feature exists to
  remove. `Config.Validate()` additionally surfaces an unparseable non-empty
  value as a validation error (joined via `errors.Join`, like the other
  sub-struct validations) so the mistake is visible, but the runtime behaviour
  is still fail-safe if validation is bypassed.

Retention is read fresh on each delete and each purge sweep, so `gr daemon
restart`-free config reloads (SIGHUP → `ReloadConfig`) change the window for
future deletes and future purges without affecting the recorded `DeletedAt` of
already-deleted sessions.

## State model & migration

`state.json` gains one optional field (`SessionState.DeletedAt`, `omitempty`).
This is additive and backward compatible: old state files simply have the field
absent, which unmarshals to `nil` (= live), exactly the desired default.

Following the established migration discipline, bump `CurrentStateVersion`
`13 → 14` and add a `migrateV13ToV14` entry that is a **documented no-op** (the
new field defaults correctly; the migration exists to keep the chain intact and
to keep the "reject a state file newer than this binary" guard meaningful). A
v13 file loaded by the new binary is migrated to v14 in memory and rewritten on
the next `saveState`; a v14 file loaded by an *old* binary is rejected by the
newer-than-me guard (it would ignore `deleted_at` and could purge nothing) —
which is the correct fail-closed behaviour.

`State.Reconcile()` (run on startup) needs **no change** for soft delete:
soft-deleted sessions have `Status = stopped`, which Reconcile leaves alone, and
`DeletedAt` persists across restarts in `state.json`. The startup purge sweep
(above) is what catches windows that expired while the daemon was down — not
Reconcile.

## Edge cases

- **Delete a running session.** Soft delete stops the agent first (same PTY
  teardown as `Stop`), then marks `DeletedAt`. It does *not* leave a running
  agent attached to a hidden session.
- **Delete an already soft-deleted session.** `SoftDelete` errors with a message
  pointing at `gr restore <name>` (to recover) or `gr delete <name> --purge` (to
  finish it off). `gr delete --purge` on an already-soft-deleted session hard
  deletes it (resolved via `resolveDeletableSessionInfo`).
- **Restore after the window / restore a purged session.** Once purged the
  session is gone from state; `gr restore` reports "not found". Within the
  window, restore always succeeds.
- **Restore when the base branch moved.** Restore does not touch git at all — it
  only clears a flag. The worktree and its branch are exactly as they were at
  delete time; the base branch having advanced on `origin` is irrelevant until
  the user chooses to rebase, same as for any long-lived stopped session. No
  special handling needed.
- **Restore when the worktree path conflicts / worktree missing.** The worktree
  was never moved, so its path is still registered with git and still on disk;
  there is nothing to conflict with. If a user *manually* deleted the worktree
  directory out from under the daemon during the window, restore still succeeds
  (flag flip), but the subsequent `gr resume` fails the same way resuming any
  session with a missing worktree fails today — this is not a new failure mode
  and not one soft delete promises to fix.
- **Name reuse during the window.** graith does **not** enforce globally-unique
  session names — `resolveSessionInfo` matches by name *or* ID and takes the
  first match. A soft-deleted session keeps its name "occupied" in state, but
  because default listings hide it, a user can create a *new* session with the
  same name during the window. Chosen policy: **allow it, disambiguate by ID.**
  Live-name resolution only ever sees the new (live) session, so normal commands
  are unambiguous. `gr restore <name>` resolves against the deleted list; if a
  same-named soft-deleted session exists it restores that one. If restoring would
  produce two live sessions with the same name, that is already a supported state
  in graith (names aren't unique) and both remain addressable by ID. We do not
  block create-during-window, because the deleted session is meant to be
  invisible to normal workflow.
- **Soft delete with `--children`.** `SoftDeleteWithChildren` marks each
  descendant soft-deleted. It is simpler than hard `DeleteWithChildren`: there is
  no worktree teardown and therefore **no need for the concurrent-create sweep**
  that hard delete performs (that sweep exists to catch descendants created
  *during* the slow teardown so their worktrees aren't orphaned — soft delete
  does no teardown, so a racing new child simply isn't marked and remains live,
  which is acceptable). Each descendant gets its own `DeletedAt` and its own
  independent purge deadline. Reparenting is deferred to purge time (the existing
  `Delete` reparents), so the tree structure is preserved for the whole window
  and restore of a parent re-exposes a coherent subtree.
- **Scenario teardown.** `gr scenario stop/delete` and two-phase rollback call
  `SessionManager.Delete` directly and stay **hard deletes** — scenario lifecycle
  owns and expects to reclaim its worktrees. Soft delete is reached *only* via
  the CLI `delete` path. This is enforced by routing: only the delete handler
  consults retention; internal callers invoke `Delete` explicitly.
- **`gr info` / `gr path` / `gr resume` / `gr stop` on a soft-deleted session.**
  These resolve against the live list only and report "not found" — a
  soft-deleted session is intentionally absent from normal operations. To act on
  it you first `gr restore` (then it's a normal stopped session) or
  `gr delete --purge` (to finish deleting).
- **Disk pressure.** The mitigation is visibility + manual purge:
  `gr list --deleted` shows what's pending and its size implicitly (one row per
  retained worktree), and `gr delete <name> --purge` reclaims any single one
  immediately. Lowering `[delete] retention` shortens the window for future
  deletes. Auto-purge under disk pressure is a possible future enhancement but is
  not in scope; the bounded default window is the primary guard.
- **Daemon restarts mid-purge.** Purge hard-deletes one session at a time via
  `Delete`, which is itself crash-safe (it sets `StatusDeleting`, does teardown
  off-lock, and `Reconcile` reverts a stuck `deleting` back to `stopped` on
  restart). If the daemon dies between two sessions, the already-purged ones are
  gone and the rest are picked up by the next startup sweep. A session caught
  mid-`Delete` is reverted to stopped by Reconcile *but keeps its `DeletedAt`*
  (Delete only clears state at the very end), so it is still soft-deleted and
  will be re-purged — no session is silently resurrected as live.

## Testing strategy

- **Unit — daemon.** `SoftDelete` sets `DeletedAt`/`Status=stopped` and preserves
  the worktree on disk; rejects system/starred/creating/already-deleted; removes
  the PTY from `sm.sessions` before kill (the exit-watcher-race invariant).
  `Restore` clears `DeletedAt` and errors on not-found/not-deleted.
  `SoftDeleteWithChildren` marks the whole subtree.
- **Unit — routing.** Handler chooses soft vs hard from `Purge` and retention:
  `retention>0 && !Purge` → soft; `Purge` → hard; `retention==0` → hard. Table
  test the four combinations.
- **Unit — config.** `RetentionDuration()`: unset → 24h; `"0"` → 0 (disabled);
  `"7d"`/`"7d12h"` parse; `"banana"` → 24h (fail-safe) *and* `Validate()`
  surfaces the error. Assert the unset-vs-`"0"` distinction explicitly.
- **Unit — listing.** `list` with `Deleted=false` hides soft-deleted; with
  `Deleted=true` shows only them; fleet-summary counts exclude soft-deleted.
  `resolveDeletableSessionInfo` finds live-then-deleted; `resolveSessionInfo`
  never finds soft-deleted.
- **Unit — migration.** A v13 fixture loads and migrates to v14 with `DeletedAt`
  nil; a synthesized v14-with-`deleted_at` round-trips; a v15 file is rejected by
  the newer-than-me guard.
- **Purge loop.** Extract the decision — `shouldPurge(session, retention, now)` —
  as a pure function and unit-test it exhaustively (before/at/after the deadline,
  nil `DeletedAt`, zero retention) with an **injected `now`**, so the loop itself
  is a thin scheduler around tested logic (mirrors `checkIdleSession`). Test
  `purgeExpired` against a state with mixed expired/unexpired sessions asserting
  only the expired are `Delete`d. Do not sleep on wall-clock in tests.
- **Integration** (`internal/integration/`, real daemon): delete → session hidden
  from `gr list`, visible under `--deleted`, worktree still on disk; restore →
  reappears stopped, resume works; `--purge` → gone immediately; set a tiny
  retention and assert the startup sweep purges an already-expired fixture.
- **Regression test for the originating bug:** deleting a *stopped* session no
  longer destroys its worktree/branch; it is recoverable via `gr restore` within
  the window. This test should fail against today's hard-delete code and pass
  with the change.

Per the repo's fixture convention, tests use Scots-word session/repo names —
`braw`/`bonnie` for happy paths, `dreich`/`thrawn`/`scunner` for
rejection/expiry/error cases, `bide` for the restore-after-restart persistence
test, `croft` for repo names.

## Alternatives considered

- **New `StatusDeleted` enum value instead of `DeletedAt`.** Rejected: collides
  with the transient `StatusDeleting` and `Reconcile`'s handling of it, and
  scatters "is it really stopped or is it trashed?" branching across every place
  that switches on status. A nullable timestamp is strictly more information
  (it carries *when*, needed for the window) with less blast radius.
- **CLI decides soft vs hard.** Rejected: retention lives in daemon-side config;
  making the CLI authoritative would require shipping the retention value to
  every client (or a config round-trip) just to pick a code path, and would let a
  stale client disagree with the daemon. The daemon is the single source of truth
  for the window; the CLI only expresses intent (`--purge` or not). The one cost
  — the CLI can't tailor its confirmation wording to soft/hard — is handled by a
  generic prompt plus a result-driven success message.
- **Separate graveyard directory / move worktrees on delete.** Rejected: moving a
  worktree breaks git's recorded worktree path and needs `git worktree repair` on
  restore, the move is non-atomic and slow for large trees, and it reintroduces
  the very teardown-style failure surface soft delete exists to avoid. Leaving
  worktrees in place makes restore a flag flip. (See [Storage](#storage).)
- **Archive/compress soft-deleted worktrees.** Deferred (Non-Goal): trades
  restore latency and complexity for disk that the 24h default already bounds. A
  clean future extension if disk pressure proves real.
- **Client-side "trash" (CLI moves files, no daemon involvement).** Rejected: the
  daemon owns PTYs, state, and worktree lifecycle; a client-side trash couldn't
  stop the agent, couldn't survive to purge on a schedule, and would race the
  daemon's own state writes.
- **Do nothing / rely on `git reflog`.** Rejected: reflog recovers *commits* on a
  branch, but hard delete removes the branch and the worktree — there is often no
  ref left to reflog, and un-committed working-tree changes are unrecoverable
  regardless. The pain is real and reflog does not address it.

## Other Notes

### References

- `internal/daemon/daemon.go` — `SessionManager.Delete`, `DeleteWithChildren`,
  `Stop`/`stopWithReason`, `Resume`, `Create`, `checkIdleSession`,
  `RunMessageCleanupLoop`, `RunGitPullLoop`, `StopAll`.
- `internal/daemon/state.go` — `SessionState`, `SessionStatus`, `State`,
  `LoadState`/`SaveState`, `migrations`/`CurrentStateVersion`, `Reconcile`.
- `internal/daemon/handler.go` — `case "delete"`/`"list"`/`"resume"`,
  `handleSessionLifecycle`, `toSessionInfo`, `authSelfOrDescendant`.
- `internal/protocol/messages.go` — `DeleteMsg`, `SessionInfo`, `SessionListMsg`.
- `internal/config/config.go` — `ParseDurationWithDays`, `StatusConfig.TTL`/
  `TTLDuration`, `Messages.MaxAge`/`MaxAgeDuration`, `Config.Validate`,
  `Default()`/`default_config.toml`.
- `internal/cli/delete.go`, `internal/cli/list.go`, `internal/cli/resume.go`,
  `internal/cli/completion.go` — CLI surfaces to extend.
- Prior art: `git reflog` (commit recovery), desktop trash/recycle bins
  (retention + restore + empty-now), Kubernetes graceful deletion
  (`deletionTimestamp` + grace period — directly analogous to `DeletedAt` +
  retention).

### Implementation notes

The bulk of a working branch already exists (protocol + handler + daemon
`SoftDelete`/`Restore`/`RunPurgeLoop` + config + CLI, building and vetting clean)
from the #994 implementation spike; this doc locks down the decisions it left
open — the `DeletedAt` marker over a new status, daemon-side soft-vs-hard routing,
`--force`≡`--purge`, the name-reuse policy, soft-`--children` semantics, and the
fail-safe unparseable-retention behaviour. Remaining work per the coverage
requirement is the test matrix above.

### Migration / rollout

Ships behind config with a safe default: users who do nothing get a 24h recovery
window (a strict safety improvement). Users who want the old behaviour set
`[delete] retention = "0"`. State files are forward-migrated (v13→v14) and old
binaries fail closed on new state, so a daemon downgrade is safe (it refuses the
newer state rather than mishandling it).
