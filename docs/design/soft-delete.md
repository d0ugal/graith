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
- `gr purge <name>` **hard-deletes immediately**, bypassing the window — the one
  explicitly destructive verb (works on a live or already-trashed session).
- The daemon runs a **purge loop** that hard-deletes soft-deleted sessions once
  their window expires, catching up on restart for windows that elapsed while it
  was down.
- Retention is configurable via `[delete] retention`, with `"0"` disabling soft
  delete entirely (every `gr delete` is a hard delete).
- Backward compatible: existing `state.json` files load unchanged; existing
  `gr delete --force` scripts keep working — with a safety upgrade (they now
  soft-delete and stay recoverable rather than destroying immediately).

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
    ExpiresAt *time.Time `json:"expires_at,omitempty"` // purge deadline, frozen at delete time
}
```

`DeletedAt == nil` means live. When a session is soft-deleted, `DeletedAt` records
*when*, and **`ExpiresAt` is frozen to `DeletedAt + retention` at delete time** —
using the retention value in effect at that moment. The purge deadline is
`ExpiresAt`, **not** recomputed from current config on each sweep.

This freezing is deliberate and load-bearing: the delete success line promises a
concrete "Recoverable until `<ExpiresAt>`". If purge instead recomputed
`DeletedAt + current_retention` every sweep, then lowering `retention` (or setting
`"0"`) would retroactively shorten — or eliminate — a window the user was *already
told* was safe, purging a session 30 seconds after they were promised 24h. Storing
`ExpiresAt` means a config change only affects *future* deletes, exactly matching
what was printed. This cuts **both** ways and is intentional: *raising* retention
does **not** extend the window of an already-soft-deleted session either — its
`ExpiresAt` is fixed at delete time. A session shows the deadline it was promised,
no more and no less; this is a feature (predictability), not a bug to file. Both
the purge predicate and `Restore`'s window check test `now >= ExpiresAt`, so they
can never disagree (see
[SoftDelete/Restore](#daemon-softdelete-restore-and-the-purge-loop)).

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
  running ──stop agent──▶ stopped ──set DeletedAt+ExpiresAt──▶ [soft-deleted]
     │                       ▲                                    │  │
     │                       │  gr restore (clear DeletedAt       │  │
     │                       └───  + ExpiresAt)───────────────────┘  │
     │                                                               │ purge loop
     │ gr purge                                                      │ (now >=
     ▼                                                               ▼  ExpiresAt)
  ─────────────────────── hard delete (Delete) ───────────────────────▶ gone
```

- **`running → soft-deleted`**: `gr delete` on a running session stops the agent
  (using `Delete`'s kill path — see [SoftDelete](#daemon-softdelete-restore-and-the-purge-loop))
  and sets `DeletedAt`. Status becomes `stopped`.
- **`stopped → soft-deleted`**: sets `DeletedAt`; agent is already stopped.
- **`soft-deleted → stopped`**: `gr restore` clears `DeletedAt`, *if still within
  the window*. The session reappears in `gr list` as a normal stopped session,
  ready for `gr resume`.
- **`soft-deleted → gone`**: the purge loop (or `gr purge`) runs the existing hard
  `Delete`.
- **`* → gone`**: `gr purge` and all non-CLI callers (scenarios) go straight to
  hard `Delete`, unchanged. `gr delete` never lands here (unless `retention = 0`).

#### Who decides: the daemon, not the CLI

The daemon — not the CLI — decides soft vs hard, based on `!Purge && retention >
0`. The CLI does not know the retention value (it lives in daemon-side config),
so it simply forwards a single `Purge` bool — set **only** by the `gr purge`
command. `gr delete` always sends `Purge = false`, so it soft-deletes whenever
`retention > 0` and hard-deletes only when the operator has set `retention = 0`.
The daemon-side predicate is named after the wire field (`Purge`), which maps
one-to-one to the `gr purge` verb.

**Hiding is not the same as unreachability.** Filtering `list` (below) removes
soft-deleted sessions from *client-side* name resolution, but the daemon protocol
acts on raw session IDs, so hiding alone does **not** stop a soft-deleted session
from being resumed, restarted, renamed, or attached. The daemon must therefore
carry explicit `IsSoftDeleted()` guards on the ID-addressable lifecycle/metadata
operations — see [Daemon-side guards](#daemon-side-guards-on-id-addressable-operations).

### CLI UX

The model is **two verbs plus restore**: `gr delete` trashes (recoverable),
`gr purge` destroys (immediate), `gr restore` brings back. Destructiveness lives in
the *verb*, not in a flag — so there is no dangerous flag to fat-finger and no
overloaded `--force`.

#### `gr delete` — always soft, one step

```
$ gr delete braw
Soft-deleted braw. Recoverable until 2026-07-11 06:53 (in 24h).
  gr restore braw   to bring it back
  gr purge braw     to remove it now
```

`gr delete` is now **safe by design**, and that changes its behaviour in one
important way: it needs **no confirmation and no `--force`**. If the session is
running, delete stops the agent and soft-deletes it *in a single step* — the exact
"delete a running agent without a separate stop" job `--force` used to do — because
nothing is destroyed: the worktree, branch, and un-pushed commits all survive the
recovery window. There is no unsaved-work prompt on `gr delete`, because there is
no unsaved work at risk.

This is the payoff of the whole design: delete stops being the dangerous verb.
`--force` is therefore **implied** and no longer needed. For backward compatibility
`gr delete --force` (and `-y`) are still accepted, but they are **no-ops /
deprecated aliases** — `gr delete` already does what they asked for, just
recoverably. Existing `gr delete --force` scripts keep working and get a strict
*safety upgrade*: where they used to destroy immediately, they now soft-delete and
stay recoverable for the window (the only change being that disk is reclaimed at
purge time, not instantly — the retention trade-off the feature is built on).

The success line is driven by the daemon's response (see
[Control messages](#control-messages)) — the delete handler's bare `{session_id}`
reply is extended so the CLI can render "Recoverable until …". With
`retention = "0"` the daemon hard-deletes and the line reads
"Deleted braw (permanently)" instead (see [Config](#config-delete-retention)).

#### `gr purge` — destroy now (the only dangerous verb)

```
$ gr purge braw
braw has uncommitted changes and 2 unpushed commits. Purge anyway? [y/N]
```

`gr purge <name>` hard-deletes immediately, bypassing the window — worktree and
branch gone, unrecoverable. It works on a **live** session or one already in the
trash (so it doubles as "empty this one trash entry"). Because purge is the verb
that can actually lose work, this is where the **unsaved-work confirmation now
lives**: purge prompts `Purge anyway? [y/N]` when the worktree is dirty or has
un-pushed commits, and `-y` (or the deprecated `--force`) skips that prompt. In
JSON / non-TTY mode with unsaved work, purge errors unless `-y` is given — the same
guard `delete` used to carry, now attached to the operation that deserves it.
`gr purge` is the CLI command that sets `DeleteMsg.Purge = true`; `gr delete` never
does.

**Batch follows the verbs.** Both commands accept the existing batch filters (e.g.
`--stopped`) via `deleteBatchRun`. `gr delete --stopped` soft-deletes each match
(recoverable, no prompt). `gr purge --stopped` hard-deletes each and prompts once
for the batch (or errors in JSON/non-TTY) unless `-y`. `gr purge` sets
`Purge = true` on every `DeleteMsg` it sends; `gr delete` never does.

#### `gr restore <name>`

```
$ gr restore braw
Restored braw (stopped). Resume it with: gr resume braw
```

Restore clears `DeletedAt`/`ExpiresAt`, leaves `Status = stopped`, and returns the
`SessionInfo`. Auth mirrors delete (`authSelfOrDescendant`). Shell completion for
`gr restore` queries the **deleted** list (a new `completeDeletedSessionNames`),
since live-name completion would never see soft-deleted sessions. **`gr purge
<TAB>` unions live + deleted names** (purge acts on both a live session and a
trashed one); `gr delete <TAB>` offers live names only.

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
sent with an empty `struct{}{}` by ~22 CLI/overlay callsites. We add a request
field:

```go
type ListMsg struct {
    Deleted bool `json:"deleted,omitempty"` // false = live only; true = soft-deleted only
}
```

The handler's `case "list"` filters `sm.state.Sessions` by `IsSoftDeleted()`:
`Deleted == false` returns live sessions (the default everywhere), `Deleted ==
true` returns only soft-deleted ones. This covers `gr list`, the overlay, tree
view, quiet mode, `--json`, shell completion, and CLI name resolution — every
path that goes through the `list` message (`gr list`, completion, and
`resolveSessionInfo`) and the callsites that send `struct{}{}` today (which
decodes to `ListMsg{Deleted:false}` — backward compatible). Note the **MCP
server**'s list paths (`internal/mcp/server.go`) must also stay live-only.

**Version skew belt-and-braces.** An *old* daemon that predates `ListMsg` ignores
`Deleted:true` and returns the **live** list — so a new `gr list --deleted` against
a stale daemon would render live sessions in the trash view with nil expiry (a
lying view). Cheap guard: the CLI additionally filters the rendered `--deleted`
rows on `DeletedAt != nil` (it has the field). A stale daemon then yields an empty
trash view rather than a misleading one.

**But `list` is not the only place sessions are enumerated**, and this is a
correctness requirement, not a nicety. Several daemon features iterate
`sm.state.Sessions` directly and would silently include soft-deleted sessions
unless audited. Each such iterator must make an explicit choice — live-only,
deleted-only, or all-sessions:

- `fleetSummary` counts (overlay running/stopped tallies, status responses) →
  **exclude** soft-deleted.
- diagnostics / `gr doctor` session enumeration → typically **exclude** (or count
  separately).
- `availableRepos` (its comment already says "live sessions") → **exclude**.
- `RunPRWatchLoop` targets (stopped sessions are polled) → **exclude**.
- `--share-worktree` source lookup (iterates all sessions by name/id) → **exclude**
  soft-deleted sources, else a hidden session can be picked as a worktree source.
- scenario create / `scenario add` name-uniqueness checks (see
  [name reuse](#edge-cases)) → decide explicitly.

The design mandates: **any new or existing `sm.state.Sessions` iterator states its
soft-delete semantics.** A grep for `range sm.state.Sessions` is the audit
checklist for implementation and review.

`toSessionInfo` gains the `DeletedAt` value (and a derived `ExpiresAt`) **only when
`DeletedAt != nil`**, so `--deleted` and `--json` can render it. For live listings
`DeletedAt`/`ExpiresAt` are nil, so live output and existing `--json` consumers are
unchanged.

`gr restore` and `gr purge` need to resolve names that only exist in the deleted
list. `resolveSessionInfo` (used by most commands) searches the live list only,
which is correct for `info`/`path`/`resume`/`stop` — a soft-deleted session is
intentionally "gone" from normal operations. A new `resolveDeletableSessionInfo`
tries the live list first, then the deleted list, so `gr purge` works on both, and
`gr restore` resolves against the deleted list. If
a name matches **more than one** session (e.g. a live session and a soft-deleted
one, or two soft-deleted ones from delete/recreate cycles — names are not unique),
the resolver reports the ambiguity and requires an explicit ID rather than acting
on an arbitrary first match.

### Control messages

Following the established three-file pattern (protocol struct → handler case →
CLI command):

- **`ListMsg`** (above) — extend the existing `list` request; response is the
  unchanged `SessionListMsg` (now filtered).
- **`DeleteMsg`** gains `Purge bool`. Both CLI verbs send this one message over
  the existing `delete` control type: `gr delete` sends `Purge = false`, the new
  `gr purge` command sends `Purge = true`. Two verbs, one wire message — no new
  control type needed. The handler passes `Purge` through to the daemon, which
  chooses soft vs hard.
- **Delete response.** Today `delete` goes through the shared
  `handleSessionLifecycle`, which replies with a bare `{session_id}` (or
  `{session_id, deleted:[…]}` for `--children`). That cannot express soft-vs-hard
  or an expiry, so the delete case gets its own response, a new
  `DeleteResultMsg`:

  ```go
  type DeleteResultMsg struct {
      SessionID string           `json:"session_id"`
      Soft      bool             `json:"soft"`                 // true = soft-deleted, false = hard-purged
      DeletedAt *time.Time       `json:"deleted_at,omitempty"` // set when Soft
      ExpiresAt *time.Time       `json:"expires_at,omitempty"` // frozen deadline, set when Soft
      Affected  []DeleteResultMsg `json:"affected,omitempty"`  // FLAT list of descendants for --children
  }
  ```

  This is what lets the CLI print "Soft-deleted braw. Recoverable until … (in
  24h)." vs "Deleted braw (permanently)." for a purge, and drives `--json` for
  delete. `Affected`
  is a **flat** list (one entry per affected descendant), mirroring
  `DeleteWithChildren`'s existing flat `[]string` return — it is not a nested tree,
  so a caller reads soft-vs-purged per session without recursing.
- **`RestoreMsg { SessionID string }`** — new `restore` control message; handler
  `case "restore"` authorizes with `auth.checkTarget(..., authSelfOrDescendant)`
  and calls `sm.Restore(id)`, replying `restored` with a `SessionInfo` (mirrors
  `resume.go`).

#### Daemon-side guards on ID-addressable operations

Filtering `list` hides soft-deleted sessions from *name* resolution, but the
daemon accepts raw session IDs on many control messages, and a soft-deleted
session has `Status = stopped` — so without explicit guards it would happily be
resumed, restarted, or mutated. Today `Resume` only rejects
`StatusDeleting`/`StatusRunning`/`StatusCreating`; a stopped-and-soft-deleted
session passes straight through and gets relaunched with `DeletedAt` still set —
producing a *running, hidden* agent that `gr list` won't show and that the purge
loop will later hard-delete out from under the user. The attach handler
auto-resumes stopped/errored sessions on attach, hitting the same path;
`restart` ends by calling resume; and `scenario_resume` resumes stopped scenario
sessions by stored ID.

The design therefore adds an `IsSoftDeleted()` guard (returning a clear
`"session %q is soft-deleted; gr restore it first"` error) to every ID-addressable
operation that would otherwise act on a stopped session:

- **Reject**: `Resume`, `Restart`, the attach auto-resume branch, `scenario_resume`
  (skip soft-deleted members), `fork` (a `fork` acts on a raw `SourceSessionID`; a
  soft-deleted source has `Status=stopped` and would otherwise fork fine —
  violating "only restore/purge act on the trash"), `rename`, `star`/`unstar`,
  `update`, `type`/shell input, and `--share-worktree` sourcing.
- **Allow** (these are the only ways to act on a soft-deleted session): `restore`,
  `purge`, `list --deleted`, and read-only `logs`/`wait`/`info`. Because
  CLI name resolution is live-only, these read-only ops reach a soft-deleted
  session **only by its raw ID** (copied from `gr list --deleted`), not by name —
  the doc states this rather than implying `gr logs <name>` works on the trash.

This is defense-in-depth: client-side hiding is the ergonomic layer, the daemon
guards are the guarantee.

### Daemon: `SoftDelete`, `Restore`, and the purge loop

`SessionManager.Delete` is unchanged and remains the hard-delete implementation.
We add:

```go
// SoftDelete marks a session deleted, stops its agent, keeps everything on disk.
func (sm *SessionManager) SoftDelete(id string) (SessionState, error)

// Restore clears the soft-delete marker, returning the session to stopped.
func (sm *SessionManager) Restore(id string) (SessionState, error)
```

`SoftDelete` reuses `Delete`'s validation and its **agent-kill path** — the
`detach → kill → 5s grace → force-kill → Close` sequence in `Delete`
(`daemon.go`), **not** `Stop`'s. (This distinction matters: `Stop`/`stopWithReason`
sends a single SIGTERM and returns, relying on `watchSession` to finalize
`Status=stopped` later; it does not detach, grace-wait, force-kill, or remove the
PTY from `sm.sessions`. Using `Stop`'s path would be incompatible with step 2
below, which removes the PTY first.) `SoftDelete` is `Delete`'s teardown **minus
the git teardown and minus the final state removal**:

1. **Under lock**: look up the session; reject config-managed
   **system/orchestrator** sessions and **starred** sessions ("unstar it first"),
   matching `Delete`'s rejections. Reject **`StatusDeleting`** (as `Delete` does)
   and an **already soft-deleted** session (error pointing at `gr restore` /
   `gr purge`). `StatusCreating` is also rejected — but note this is *not* "the
   same as `Delete`": `Delete` handles a creating session *specially* (it removes
   the placeholder so the in-flight create's Phase 3 detects the deletion and
   cleans up). Soft-deleting a half-created session is not meaningful, so
   `SoftDelete` rejects it and tells the user to wait or `gr purge`.
2. **Detach and kick any attached client** and remove the PTY from `sm.sessions`
   **before** killing it — exactly as `Delete` does (`Delete` removes the client
   from `sm.attachedClients` up front and kicks it after teardown). Removing the
   PTY first preserves a critical invariant: `watchSession` treats a session as
   stale when it is no longer in `sm.sessions`, so the exit watcher will not race
   in and clobber `DeletedAt`/`Status` when the agent process exits.
3. **Persist the marker *before* the blocking kill (crash-safety).** Read the
   current `retention`, set `DeletedAt = now`, `ExpiresAt = now + retention` (frozen
   here), `Status = stopped`, `StatusChangedAt = now`, write a summary
   ("Soft-deleted, recoverable until `<ExpiresAt>`"), and `saveState` **while still
   holding the intent** — mirroring how hard `Delete` writes `StatusDeleting`
   before its blocking teardown. If instead we killed first and saved `DeletedAt`
   after, a daemon crash mid-kill would leave a session with no `DeletedAt`;
   `Reconcile` would then find a dead PID and mark it a live stopped session,
   silently *undoing* the delete. Persisting first means a crash leaves the session
   correctly soft-deleted.
4. **Off-lock**: run the agent-kill sequence. **Do not** touch the worktree or
   branch. No further state write is needed (the marker is already durable);
   record the exit if useful.

**Crash between persist and kill.** There is a narrow window where step 3 has
persisted `Status=stopped`+`DeletedAt` but the step-4 kill has not completed. If
the daemon dies here, the agent process may still be alive, attached to a hidden
(soft-deleted) session — and `Reconcile` only re-checks liveness for
`StatusRunning`, so it will skip this stopped session and leave the orphan running
and invisible. To close this, the **startup purge sweep re-kills any soft-deleted
session that still has a live verified PID** (reusing `killVerifiedProcess`) before
doing anything else. This is strictly better than the hard-delete analogue, which
leaves the same orphan visible-but-stopped.

The routing decision lives in the handler's delete path: if `Purge` is set or
`retention == 0`, call `Delete`/`DeleteWithChildren` (hard); otherwise call
`SoftDelete`/`SoftDeleteWithChildren`. Scenario teardown and rollback call
`Delete` directly and are unaffected (always hard).

**Token handling.** Because state is preserved, the session's auth token stays in
`tokenIndex` and remains valid — this is intentional, so `gr restore` (and the
session's own descendants) keep working. The daemon-side guards above are what
prevent a still-valid token from *operating* a soft-deleted session; the token is
only good for `restore`/`purge`/read-only ops until the session is restored.

`Restore`:

1. **Under lock**: look up the session; error if not found or not soft-deleted.
   **Check the window** with the *same* predicate purge uses —
   `shouldPurge(session, now, fallbackExpiry)` i.e. `now >= session.ExpiresAt` (see
   below). If it returns true the window has closed — return a clear "expired, already scheduled
   for purge" error rather than silently resurrecting a session past its advertised
   deadline. (Within the coarse purge cadence a just-expired session may still be
   in state; restore must not undelete it.) Sharing the one predicate guarantees
   restore and purge can never disagree about whether a session is recoverable.
2. Clear `DeletedAt` **and `ExpiresAt`**, set `StatusChangedAt = now`, write a
   summary ("Restored — resume to continue"); `saveState`. Status is already
   `stopped`.

The worktree still exists on disk (soft delete never removed it), so a subsequent
`gr resume` re-launches the agent in place with no worktree work — the normal
resume path.

#### `RunPurgeLoop`

Modeled on `RunGitPullLoop` (startup handling + ticker, survives restarts):

```go
func (sm *SessionManager) RunPurgeLoop(ctx context.Context) {
    // One sweep shortly after startup: catch windows that expired while the
    // daemon was down, and re-kill any orphaned live agent on a soft-deleted
    // session (see crash-safety above). Then a coarse ticker — expiry is frozen
    // and measured in hours, so a 10-minute cadence is plenty precise and cheap.
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

The decision is a single pure function, injectable `now`, shared with `Restore`:

```go
func shouldPurge(s *SessionState, now, fallbackExpiry time.Time) bool {
    if !s.IsSoftDeleted() {
        return false
    }
    exp := fallbackExpiry            // corrupt/hand-edited state: DeletedAt set, ExpiresAt nil
    if s.ExpiresAt != nil {
        exp = *s.ExpiresAt
    }
    return !now.Before(exp)
}
```

The `fallbackExpiry` guards a corner the naive `ExpiresAt != nil` check gets
wrong: a soft-deleted session with `DeletedAt` set but `ExpiresAt` **nil** (a
hand-edited or corrupt `state.json`, or an interrupted older delete) would
otherwise be hidden *and* never purged — the trash silently accumulating
unreachable entries. The sweep passes `fallbackExpiry = DeletedAt + current
retention` (or, if `DeletedAt` is also nil, `now` — purge immediately), and logs
that it fell back, so such a session is still eventually reaped and the anomaly is
visible. `Restore` uses the same helper, so a nil-`ExpiresAt` session is treated
consistently by both paths.

`purgeExpired` snapshots the sessions where `shouldPurge(s, now, fallback)` under a read lock
as `(id, expiresAt)` pairs, then hard-deletes each via a **compare-and-delete**
step, not a bare `Delete(id)`:

- Re-acquire the write lock, re-read the session, and verify it is *still*
  soft-deleted **and** its `ExpiresAt` still equals the snapshot value and
  `shouldPurge` is still true. Only then proceed to `Delete`.

This closes a race the naive "snapshot then `Delete`" has: between the snapshot
and the hard delete, a concurrent `gr restore` could clear the marker (or a
delete/restore/re-delete could change `ExpiresAt`), and an unconditional `Delete`
would then purge a session the user just recovered. The compare step makes purge a
no-op for any session whose marker changed under it. `Delete` itself reuses all
its teardown, reparenting, token cleanup, and error handling.

Note purge keys off the **frozen `ExpiresAt`**, not current config — so lowering
`retention` (or setting `"0"`) does *not* retroactively purge sessions before the
deadline they were promised; it only affects future deletes. If `retention == 0`,
no *new* soft deletes are created (delete goes hard), but any pre-existing
soft-deleted sessions still purge on their own frozen schedule. Started alongside
the other loops in the daemon's `Run`, so it stops cleanly on context cancel during
graceful shutdown.

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
trying to defer. Filtering listings through the `list` handler makes A's main
downside — a shared map — small, though it is *not* a single chokepoint: every
direct `sm.state.Sessions` iterator must also declare its soft-delete semantics
(see [Server-side hiding](#server-side-hiding-via-the-list-message)). That audit
is the real cost of Option A, and it is bounded and greppable.

## Worktree handling

Soft-deleted worktrees stay on disk **unchanged**, in their original location
(`<DataDir>/worktrees/<repoName>/<repoHash>/<id>`), with their branch intact.
This is what makes instant, lossless restore possible: `gr restore` + `gr resume`
finds the worktree exactly where the daemon left it.

The cost is disk. A soft-deleted session consumes the same space it did while
live for up to the retention window. This is an accepted trade-off:

- The default window is 24h, bounding worst-case accumulation to "everything
  deleted in the last day".
- Users who want the disk back immediately have `gr purge`.
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
    v, err := ParseDurationWithDays(d.Retention)
    if err != nil {
        return 24 * time.Hour // fail SAFE: never silently disable recovery
    }
    return v
}
```

Semantics, and the edge cases worth calling out explicitly:

- **Unset** (`retention` absent, or the whole `[delete]` block absent): default
  **24h**, supplied by the `RetentionDuration()` accessor's empty-string branch
  (the same way `StatusConfig.TTLDuration()` defaults in code). We *also* set
  `retention = "24h"` in the embedded `default_config.toml` so the value is
  documented and visible, but the accessor's fallback is the true source of the
  default — a hand-edited config that omits the field still gets 24h.
- **`"0"` (or `"0s"`)**: soft delete is **disabled** — every `gr delete` is a
  hard delete. This is the one value that means "off", and it must be
  distinguishable from "unset". Because unset → 24h, we cannot treat the Go zero
  value as "off"; the parse of the literal string `"0"` yields a zero
  `time.Duration`, and the daemon's soft-vs-hard test is `retention > 0`, so a
  parsed zero correctly disables soft delete while an *unset* field takes the 24h
  default before ever reaching that test.
- **Unparseable** (e.g. `retention = "banana"`): two layers, both fail-safe.
  First, `Config.Validate()` surfaces an unparseable non-empty value as a
  validation error (joined via `errors.Join`, like the other sub-struct
  validations). Because config load runs `Validate()` and propagates its error,
  the *practical* effect of a typo is that **config load fails** (the CLI pre-run
  errors out) or, on a running daemon, **SIGHUP reload logs the error and keeps
  the previous config** — the user is told, loudly, rather than silently getting
  the wrong window. Second, if validation is ever bypassed, the
  `RetentionDuration()` accessor still falls back to **24h**, never to disabled —
  a typo must not silently turn off recovery, which would recreate the exact
  data-loss footgun this feature exists to remove.

Retention is read **once, at delete time**, and frozen into the session's
`ExpiresAt`. A `gr daemon restart`-free config reload (SIGHUP → `ReloadConfig`)
therefore changes the window only for *future* deletes — it never moves the
deadline of an already-soft-deleted session, so the "Recoverable until …" the user
was shown always holds. (This is the fix for the otherwise-broken promise where a
purge recomputing `DeletedAt + current_retention` could bin a session before its
advertised time; see [State model](#state-model--migration).)

## State model & migration

`state.json` gains two optional fields (`SessionState.DeletedAt` and
`SessionState.ExpiresAt`, both `omitempty`). This is additive and backward
compatible: old state files simply have them absent, which unmarshals to `nil`
(= live), exactly the desired default.

Following the established migration discipline, bump `CurrentStateVersion`
`13 → 14` and add a `migrateV13ToV14` entry that is a **documented no-op** (the
new field defaults correctly; the migration exists to keep the chain intact and
to keep the "reject a state file newer than this binary" guard meaningful). A
v13 file loaded by the new binary is migrated to v14 in memory and rewritten on
the next `saveState`.

**Downgrade is *not* currently fail-closed, and the design should fix that.** The
low-level `LoadState` *does* return an error for a state file newer than the
binary (the newer-than-me guard), but the daemon's `Run` today only **logs**
`sm.LoadState()` errors and continues (`daemon.go`) — and `NewSessionManager` has
already initialized an empty `NewState`. So an old daemon reading a v14 file would
not refuse to start; it would come up with **empty in-memory state**, orphaning
running agents/worktrees and operating against the wrong picture. This is a
pre-existing behaviour, but bumping the version makes it reachable. The design
therefore requires a small change: **`Run` must hard-fail (refuse to start) when
`LoadState` reports a newer-than-me state file**, rather than logging and
continuing. With that change, downgrade is genuinely safe (the old binary refuses
the new state instead of silently discarding it). Without it, the rollout section
below must not claim downgrade safety.

`State.Reconcile()` (run on startup) needs **no change** for soft delete:
soft-deleted sessions have `Status = stopped`, which Reconcile leaves alone, and
`DeletedAt` persists across restarts in `state.json`. The startup purge sweep
(above) is what catches windows that expired while the daemon was down — not
Reconcile.

## Edge cases

- **Delete a running session.** `gr delete` kicks any attached client and stops
  the agent first (using `Delete`'s kill sequence), then marks `DeletedAt` — all in
  one step, no confirmation, because it is recoverable. It does *not* leave a
  running agent, or an attached client, on a hidden session. (This is the job that
  used to require `gr delete --force`; it is now just what `gr delete` does.)
- **Delete an already soft-deleted session.** `gr delete` on a trashed session is
  a no-op-ish "already deleted" message pointing at `gr restore <name>` (recover)
  or `gr purge <name>` (destroy now). `gr purge` on a trashed session hard-deletes
  it (resolved via `resolveDeletableSessionInfo`).
- **Deleting with unsaved work is safe and silent.** Because `gr delete` is
  recoverable, it does **not** prompt or error on un-pushed work — an agent (JSON
  mode) or a piped script just gets a soft delete, no `--force` needed. This is the
  fix for the primary-audience footgun: the everyday path is recoverable by
  default, and destruction (`gr purge`) is a different, explicit verb. The dirty-work
  confirmation lives on `gr purge`, which is the only thing that can actually lose
  it.
- **`gr delete` with `retention = "0"`.** When the operator has explicitly disabled
  soft delete, `gr delete` hard-deletes — there is no soft state to fall back to.
  This is a deliberate, documented consequence of the opt-out config, not a
  surprise: an admin who sets `retention = "0"` has chosen destroy-on-delete
  globally. To keep it unmissable, the success line is driven by
  `DeleteResultMsg.Soft`: a hard delete prints **"Deleted braw (permanently)"**,
  never the "Recoverable until …" line — so even a scripted caller's output makes
  the destructive outcome explicit. (This is the one case where `gr delete` is
  destructive, and it is the operator's own configuration choice.)
- **Restore after the window / restore a purged session.** Once purged the
  session is gone from state; `gr restore` reports "not found". Before purge but
  after `ExpiresAt`, restore is refused with an "expired" error (same predicate as
  purge). Comfortably within the window, restore always succeeds.
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
- **Name reuse during the window.** For ordinary `gr new`, graith does **not**
  enforce globally-unique names — `Create` validates the name format but does not
  check uniqueness (both the worktree path *and* the branch name embed the unique
  session id: `<prefix>/<name>-<id>`), and `resolveSessionInfo` matches by name
  *or* ID, first match. A soft-deleted session keeps its name "occupied" in state,
  but because default listings hide it, a user can create a *new* session with the
  same name during the window. Chosen policy: **allow ordinary create, disambiguate
  by ID.** Live-name resolution only sees the new (live) session; there is no path
  or branch collision. Restore resolves against the deleted list and, if a name is
  ambiguous (multiple deleted sessions from delete/recreate cycles), **requires an
  explicit ID** rather than restoring an arbitrary first match.

  **Exception — scenarios enforce uniqueness.** `scenario start` and `scenario
  add` *do* reject a name that already exists in `sm.state.Sessions`. Since
  soft-deleted sessions remain in that map, a hidden deleted session would block a
  scenario session reusing its name. Chosen policy: **scenario uniqueness checks
  ignore soft-deleted sessions** (they treat a soft-deleted name as free), so a
  scenario is not blocked by trash. This is one of the audited direct-iterator
  callsites.
- **Soft delete with `--children`.** `SoftDeleteWithChildren` marks each
  descendant soft-deleted; each gets its own `DeletedAt` and independent purge
  deadline. It skips hard delete's concurrent-create sweep — that sweep exists to
  catch descendants created *during* the slow worktree teardown so they aren't
  orphaned, and soft delete does no teardown. **But leaving a racing new child
  live under a hidden parent is surprising** (tree view would render it as a root
  because the parent is filtered out, and purge would later reparent it). Chosen
  policy: keep a *lightweight* sweep — cheap because it only **re-marks** (sets
  `DeletedAt`/`ExpiresAt`), never tears down — so a child created mid-operation is
  also soft-deleted, keeping the subtree coherent. **Restore must mirror this:**
  because a `--children` delete can hide a whole subtree, restoring *only the
  parent* would leave the children hidden. The design adds **`gr restore
  --children`**, defined concretely as: *restore this session plus **all**
  soft-deleted descendants of it, regardless of when each was deleted.* We do
  **not** try to reconstruct "the same delete operation" — the data model carries
  no delete-group id, and per-descendant `DeletedAt`/`ExpiresAt` values differ, so
  "same operation" is not derivable. Restoring the whole soft-deleted subtree is
  both well-defined from the data on hand and the behaviour a user expects. A bare
  `gr restore <parent>` restores just that one session and warns if it has
  soft-deleted descendants (pointing at `--children`). Each restored descendant is
  re-checked against its own frozen `ExpiresAt`, so an already-expired child is
  skipped rather than resurrected.
- **Scenario teardown.** `gr scenario stop/delete` and two-phase rollback call
  `SessionManager.Delete` directly and stay **hard deletes** — scenario lifecycle
  owns and expects to reclaim its worktrees. Soft delete is reached *only* via
  the CLI `delete` path. This is enforced by routing: only the delete handler
  consults retention; internal callers invoke `Delete` explicitly.
- **Direct-ID operations on a soft-deleted session.** `gr info` / `gr path` /
  `gr resume` / `gr restart` / `gr rename` / `gr stop` / `star` / `update` /
  `type` all report "not found" via live-only name resolution *and* are rejected
  by the daemon-side `IsSoftDeleted()` guards even when addressed by raw ID (see
  [Daemon-side guards](#daemon-side-guards-on-id-addressable-operations)). To act
  on a soft-deleted session you first `gr restore` (then it's a normal stopped
  session) or `gr purge` (to finish deleting).
- **Overlay has no trash view.** The interactive session picker (`ctrl+b w`)
  offers no way to see or restore soft-deleted sessions — `gr list --deleted` and
  `gr restore` are CLI-only. This is a deliberate scoping decision for v1 (keep the
  picker focused on live work); an overlay "trash" view is a plausible follow-up.
- **Disk pressure.** The mitigation is visibility + manual purge:
  `gr list --deleted` shows what's pending and its size implicitly (one row per
  retained worktree), and `gr purge <name>` reclaims any single one
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

- **Unit — daemon.** `SoftDelete` sets `DeletedAt`/`Status=stopped`, preserves the
  worktree on disk, and **persists the marker before the blocking kill** (assert
  the state write ordering — a crash after kill-start must still leave `DeletedAt`
  set); rejects system/starred/creating/deleting/already-deleted; kicks the
  attached client; removes the PTY from `sm.sessions` before kill (the
  exit-watcher-race invariant). `Restore` clears `DeletedAt`, errors on
  not-found/not-deleted, and **errors when the window has expired** (restore-after-
  expiry). `SoftDeleteWithChildren`/restore-with-children mark/clear the subtree.
- **Unit — daemon guards.** `Resume`, `Restart`, the attach auto-resume path,
  `fork` (by raw `SourceSessionID`), `rename`, `star`/`unstar`, `update`, and
  `--share-worktree` sourcing all reject a soft-deleted session by raw ID;
  `scenario_resume` skips soft-deleted members.
- **Unit — CLI verbs.** `gr delete` soft-deletes with **no prompt** even with
  unsaved work / a running agent (recoverable); `gr delete --force`/`-y` behave
  identically (deprecated no-op aliases). `gr purge` hard-deletes and **prompts**
  on unsaved work (errors in JSON/non-TTY) unless `-y`. `gr purge` sets
  `DeleteMsg.Purge=true`; `gr delete` never does.
- **Unit — frozen expiry.** `SoftDelete` sets `ExpiresAt = DeletedAt + retention`;
  lowering retention (or `"0"`) afterward does **not** change an existing
  `ExpiresAt`, and purge honours the frozen value (a session promised 24h is not
  binned early). `Restore` clears both `DeletedAt` and `ExpiresAt`.
- **Unit — crash re-kill.** A soft-deleted session with a live verified PID is
  re-killed by the startup sweep (simulate: `DeletedAt` set, `Status=stopped`,
  fake live PID).
- **Unit — routing.** Handler chooses soft vs hard from `Purge` and retention:
  `retention>0 && !Purge` → soft; `Purge` → hard; `retention==0` → hard. Table
  test the four combinations, single **and batch** (`gr delete --stopped` soft;
  `gr purge --stopped` hard).
- **Unit — config.** `RetentionDuration()`: unset → 24h; `"0"` → 0 (disabled);
  `"7d"`/`"7d12h"` parse; `"banana"` → 24h (accessor fail-safe) *and*
  `Config.Validate()` returns an error for it (so load/reload fails loudly).
  Assert the unset-vs-`"0"` distinction explicitly.
- **Unit — listing.** `list` with `Deleted=false` hides soft-deleted; with
  `Deleted=true` shows only them; `fleetSummary` and other direct
  `sm.state.Sessions` iterators exclude soft-deleted. `resolveDeletableSessionInfo`
  finds live-then-deleted and **reports ambiguity on duplicate names**;
  `resolveSessionInfo` never finds soft-deleted.
- **Unit — migration & downgrade.** A v13 fixture loads and migrates to v14 with
  `DeletedAt` nil; a synthesized v14-with-`deleted_at` round-trips; a v15 (newer)
  file is rejected by the newer-than-me guard **and the daemon `Run` refuses to
  start** (the downgrade fail-closed change) rather than coming up with empty
  state.
- **Purge loop.** Test the shared pure predicate
  `shouldPurge(session, now, fallbackExpiry)` exhaustively (before/at/after
  `ExpiresAt`; nil `DeletedAt`; **nil `ExpiresAt` → fallback used**, both with and
  without `DeletedAt`) with an **injected `now`**, and assert `Restore`'s window
  check calls the *same* function (they can't drift). The loop is then a thin scheduler around tested
  logic (mirrors `checkIdleSession`). Test `purgeExpired` against a state with
  mixed expired/unexpired sessions asserting only the expired are `Delete`d, and
  the **compare-and-delete race**: a session restored (or re-deleted with a new
  `ExpiresAt`) between snapshot and delete is **not** purged. Do not sleep on
  wall-clock in tests.
- **Integration** (`internal/integration/`, real daemon): `gr delete` → session
  hidden from `gr list`, visible under `--deleted`, worktree still on disk;
  `gr restore` → reappears stopped, resume works; `gr purge` → gone immediately;
  resume/restart by raw ID on a soft-deleted session is rejected; set a tiny
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
  for the window; the CLI only expresses intent (`gr delete` vs `gr purge`,
  i.e. `Purge` false/true).
- **Destructiveness as a flag (`gr delete --hard`/`--purge`) instead of a second
  verb.** Rejected in favour of `gr delete` + `gr purge`. A flag on a single verb
  keeps the dangerous operation one fat-fingered `--hard` away from the safe one
  and forces `--force`-style overloading; two verbs put the destructive intent in
  the command name, where it can't be confused, and let `gr delete` shed its
  confirmation entirely (it is recoverable, so it needs none). This also
  dissolves the earlier "`--force` re-creates the footgun" problem: there is no
  destructive flag on `delete` to misuse. `--force`/`-y` survive only as
  deprecated, accepted-but-inert aliases on `gr delete` for script compatibility.
- **`--yes` as a separate "skip prompt but stay soft" flag.** Considered, then
  dropped: once `gr delete` is unconditionally recoverable it needs no prompt to
  skip, so a `--yes` on `delete` has nothing to do. The prompt (and thus `-y`)
  belongs on `gr purge`, the only verb that can lose work.
- **Trash-metaphor vocabulary (`gr trash`/`gr untrash`/`gr trash --empty`).**
  Considered; rejected because it would make `gr delete` the *permanent* verb,
  which inverts the issue's requirement that `gr delete` become the safe default
  and would surprise every existing user's muscle memory. `delete` = safe/trash,
  `purge` = destroy keeps `delete` where users expect it while making the
  destructive path explicit.
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

- `internal/daemon/daemon.go` — `SessionManager.Delete`, `DeleteWithChildren`
  (concurrent-create sweep), `Stop`/`stopWithReason` (single SIGTERM — *not*
  SoftDelete's kill path), `Resume`, `Restart`, `Create`, `Run` (loop startup +
  `LoadState` error handling), `checkIdleSession`, `RunMessageCleanupLoop`,
  `StopAll`, `fleetSummary`, `availableRepos`, `--share-worktree` source lookup.
- `internal/daemon/gitpull.go` — `RunGitPullLoop` (the reaper template).
- `internal/daemon/prwatch.go` — `RunPRWatchLoop` (a direct-iterator to audit).
- `internal/daemon/scenario.go` — scenario name-uniqueness checks, `scenario_resume`.
- `internal/daemon/state.go` — `SessionState`, `SessionStatus`, `State`,
  `LoadState`/`SaveState`, `migrations`/`CurrentStateVersion`, `Reconcile`.
- `internal/daemon/handler.go` — `case "delete"`/`"list"`/`"resume"`/`"restart"`,
  `handleSessionLifecycle`, `toSessionInfo`, `authSelfOrDescendant`.
- `internal/daemon/auth.go` — `tokenIndex`/`resolveAuth` (soft-deleted tokens).
- `internal/protocol/messages.go` — `DeleteMsg`, `SessionInfo`, `SessionListMsg`.
- `internal/config/config.go` — `ParseDurationWithDays`, `StatusConfig.TTL`/
  `TTLDuration`, `Messages.MaxAge`/`MaxAgeDuration`, `Config.Validate`,
  `Default()`/`default_config.toml`.
- `internal/cli/delete.go` (incl. `confirmDelete`, `deleteBatchRun`) and a new
  `internal/cli/purge.go` (the `gr purge` verb) + `internal/cli/restore.go`;
  `internal/cli/batch.go` (`--force`/`-y`), `internal/cli/list.go`,
  `internal/cli/resume.go`, `internal/cli/completion.go`, `internal/cli/root.go`
  (`registerCommands`), `internal/mcp/server.go` (MCP list paths) — CLI and MCP
  surfaces to add/extend.
- Prior art: `git reflog` (commit recovery), desktop trash/recycle bins
  (retention + restore + empty-now), Kubernetes graceful deletion
  (`deletionTimestamp` + grace period — directly analogous to `DeletedAt` +
  retention).

### Implementation notes

A parallel implementation spike exists on a separate branch (`feat/soft-delete`,
not this worktree — a grep here finds no `SoftDelete`/`RunPurgeLoop`/`DeleteConfig`
yet), covering protocol + handler + daemon `SoftDelete`/`Restore`/`RunPurgeLoop` +
config + CLI at a first-cut level. This doc locks down the decisions it left open
and, critically, adds the items the design review surfaced that the spike did
**not** yet cover: the daemon-side `IsSoftDeleted()` guards on ID-addressable
operations, the `DeleteResultMsg` response contract, crash-safe marker ordering,
restore-after-expiry, the purge compare-and-delete race guard, attached-client
kicking, restore-with-children, scenario/share-worktree filtering, and the
downgrade fail-closed change to `Run`. The spike should be reconciled to this doc
before it ships, and the full test matrix above added (tests are a hard
requirement, not a follow-up).

### Migration / rollout

Ships behind config with a safe default: users who do nothing get a 24h recovery
window (a strict safety improvement). Users who want the old behaviour set
`[delete] retention = "0"`. State files are forward-migrated (v13→v14). Downgrade
safety **depends on the `Run` change** described in
[State model & migration](#state-model--migration): today an old daemon reading a
newer state file logs the error and starts with empty state, so this design
requires `Run` to hard-fail on a newer-than-me state file. With that change a
daemon downgrade is safe — the old binary refuses the newer state rather than
silently discarding it; without it, downgrade is *not* safe and the version bump
should not ship alone.
