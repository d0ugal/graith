---
title: "Design Doc: Tracker-Poll → Session Dispatch (a triggers action)"
authors: Dougal Matthews
created: 2026-07-16
status: Accepted
reviewers: (none yet)
issue: https://github.com/d0ugal/graith/issues/643
---

# Tracker-Poll → Session Dispatch

Keep live sessions in sync with an issue tracker: on a schedule, poll the tracker
for issues in an *active* state, spawn a session (seeded from the issue body) for
each active issue that has none, and stop/clean up the session when its issue
leaves the active state. The tracker is the source of truth; the daemon
reconciles the live session set against it every tick. This is expressed as a new
`tracker` **action** in the existing triggers framework (#1013), driven by a
`[trigger.schedule]` source — not a bespoke poll loop.

## Background

The triggers framework (`docs/design/2026-07-11-triggers-design.md`, shipped v1)
already gives us everything a tracker poller needs *except* the reconcile step:

- A **schedule source** (`[trigger.schedule]`, cron / `every`) with a durable,
  restart-safe run-state machine, overlap/rate-limit policy, and a daemon-wide
  concurrency cap (`internal/daemon/trigger.go`).
- A **session action** that spawns a session parented to the orchestrator, tagged
  with the owning trigger (`TriggerID` on `SessionState`), with `auto_cleanup` /
  `idle_timeout` lifecycle knobs (`internal/daemon/trigger_actions.go`,
  `actionSession`). This already proves "spawn a session from a trigger" (the
  #71 spawn-from-issue half).
- A precedent for **reaping on external state** — `pr_watch` auto-resumes /
  notifies a session when its PR changes, and #114 soft-deletes a session when
  its PR merges. Reconciling "session should exist iff the tracker says the issue
  is active" generalises that.
- A **`gh` reader** that shells out to the GitHub CLI with a short timeout and
  `GH_PROMPT_DISABLED` (`internal/daemon/ghpr.go`, `ghRunner`). GitHub Issues is
  the same CLI surface (`gh issue list`), so the first tracker target reuses it.

What is missing is a single verb that, each tick, computes the *desired* set of
sessions from the tracker and drives the live set toward it — spawning what's
missing and reaping what's obsolete — idempotently and without thrashing.

## Problem

Today, turning a tracker's "in progress" column into running graith sessions is
manual: a human reads the board, runs `gr new` per issue, and later remembers to
`gr delete` the ones whose issues are done. There is no way to say "for every
issue labelled `in-progress`, keep a session alive; when the label comes off,
tear it down." External cron scripting `gr new` per issue can't dedup across
runs (it respawns every poll), has no grace window (a brief column move kills
work), and has no graith identity to own or reap the sessions.

The triggers `session` action spawns *one* session per fire — it has no notion of
"one session per tracker issue, reconciled." Firing it on a schedule would spawn
a fresh session every tick. Reconciliation — the spawn-and-reap-to-match-state
half — is the genuinely new piece.

## Goals

- A scheduled trigger that polls an issue tracker and **reconciles** live sessions
  against the set of active issues: spawn one per newly-active issue, reap the
  session when its issue leaves the active state.
- **GitHub Issues** as the first (v1) provider, reusing the `gh` reader.
- **Idempotent across ticks** — one session per issue, keyed by a durable tag, so
  a session is never respawned while it (or a stopped instance of it) still
  exists.
- **Reconciliation grace** — a configurable window before reaping, so an issue
  briefly moved out of the active state (a mislabel, a column bounce) does not
  kill in-flight work.
- **Prompt seeding** — the spawned session's prompt is templated with the issue's
  number/title/body/url/labels.
- **A concurrency cap** — a per-trigger ceiling on how many tracker sessions run
  at once, so a large backlog can't spawn dozens of agents in one tick.
- Reuse the framework's schedule loop, overlap/rate-limit policy, run-state,
  `gr trigger` CLI, and authorization — no new loop, protocol message, or CLI.

### Non-Goals

- **Non-GitHub trackers** (Linear, Jira). The config carries a `provider` field
  so they can be added, but v1 implements only `github`. Others are a runtime
  error until implemented.
- **Push / webhook source** (#604, NOT_PLANNED). v1 polls on the schedule cadence.
- **Per-state concurrency lanes** (`max_concurrent_by_state`, symphony-style) —
  that depends on execution lanes (#603, open). v1 offers a single per-trigger
  `max_concurrent` cap.
- **Draft/prompt-staging UI** (#608). v1 seeds the prompt directly from the issue
  body at spawn time.
- **Bidirectional sync** — graith never writes back to the tracker (no closing
  issues, no comments). The tracker is read-only source of truth.
- **Reaping unfinished work safely beyond a grace window** — reap is a stop or a
  *soft* delete (recoverable within the retention window), never a hard purge.

## Proposals

### Proposal 0: Do Nothing

Users keep running `gr new` per issue by hand, or script external cron around it.

**Cons:** the status quo the issue rejects — no dedup (respawns every poll), no
grace window, no reap, no graith identity to own the sessions. Reconciliation
never happens; the human *is* the reconciler.

### Proposal 1: A `tracker` action reconciling against the schedule source (Recommended)

A new `action.type = "tracker"`, valid only with a `[trigger.schedule]` source.
Each fire runs one **reconcile pass** (not a single dispatch):

1. **Poll.** Query the provider for issues currently in the active state. For
   GitHub: `gh issue list --repo <slug> --state <state> [--label ...]
   [--assignee ...] --json number,title,body,url,labels --limit N`, via the
   existing `ghRunner`. Each issue becomes an `issueRef{key, number, title, body,
   url, labels}` where `key` is the stable identity (`gh:<slug>#<number>`).

2. **Take stock.** Enumerate this trigger's live tracker sessions — those tagged
   `TriggerID == <trigger>` with a non-empty `TrackerIssue` that are not
   soft-deleted. This is the durable dedup set (survives daemon restart), keyed by
   `TrackerIssue == issueRef.key`.

3. **Plan (pure function).** `reconcileTracker(active, existing, graceState, opts,
   now)` returns `{spawn []issueRef, reap []string /*session ids*/}`:
   - **spawn**: an active issue with no live session — bounded by `max_concurrent`
     (count existing + already-planned spawns; stop at the cap, log the rest).
   - **reap**: a live session whose issue is no longer active, *and* has been
     inactive at least `grace`. Grace is tracked in an in-memory
     `map[triggerName+key]firstSeenObsoleteAt`; an issue reappearing active clears
     its entry. A session whose issue is still active clears any stale mark.
   - The function is total and side-effect-free (grace map mutation returned as a
     new map / applied by the caller), so it is exhaustively unit-testable — the
     framework's "extract the pure logic" discipline.

4. **Apply.** Spawn planned sessions via the existing `createTriggerSession`
   path (orchestrator-parented, `TriggerID` + new `TrackerIssue` tag set in the
   same durable reservation), prompt expanded with the issue vars. Reap planned
   sessions per `reap` policy: `stop` (default) stops the agent, `delete`
   soft-deletes it, `none` leaves it (report only). Return a summary
   (`"spawned 2, reaped 1, active 5"`) recorded in the run history and,
   optionally, delivered.

**Why an action, not a source.** The issue frames this as source + action +
reconcile. But the *source* is already the generic schedule source — nothing
tracker-specific about "fire every 5m." The tracker-specific behaviour is
entirely in *what the fire does*: poll + reconcile. Modelling it as an action
reuses the schedule loop, overlap guard (a slow reconcile never overlaps itself
under the default `overlap = "skip"`), rate limit, run-state, and CLI wholesale.
A bespoke source would duplicate all of that.

**Config surface.** A `[trigger.action.tracker]` sub-block plus the existing
`agent`/`model`/`prompt` (for spawned sessions) and `deliver` (for the summary):

```toml
[[trigger]]
name = "issue-sessions"

[trigger.schedule]
every = "5m"

[trigger.action]
type   = "tracker"
agent  = "claude"
prompt = "Work on GitHub issue #{issue_number}: {issue_title}\n\n{issue_body}\n\n{issue_url}"

[trigger.action.tracker]
provider       = "github"          # v1: github (default)
repo           = "~/Code/graith"   # resolves the gh slug + is the spawn repo
active_state   = "open"            # open | closed | all (default open)
active_labels  = ["in-progress"]   # active iff the issue has one of these (empty = any)
assignee       = "@me"             # optional gh assignee filter
grace          = "10m"             # inactive this long before reaping (default 5m)
max_concurrent = 3                 # cap on live tracker sessions (0 = unlimited)
reap           = "stop"            # stop | delete | none (default stop)
limit          = 50                # max issues fetched per poll (default 50)

[trigger.action.deliver]           # optional: the reconcile summary
inbox = "orchestrator"
```

**New template vars** (`config.TriggerVars`): `{issue_number}`, `{issue_title}`,
`{issue_body}`, `{issue_url}`, `{issue_labels}` — empty for non-tracker triggers,
following the existing unknown-token-is-error discipline. The prompt is expanded
per-issue at spawn time.

**Session tag.** `SessionState.TrackerIssue` (and `CreateOpts.TrackerIssue`),
mirroring `TriggerID` — set in the same durable reservation. This is the dedup
key; robust against renames the way name-guessing is not (same rationale as the
`ensure` reactor's `TriggerID` tag).

**Validation** (`config/trigger.go`): `tracker` requires `[action.tracker]` with
a `repo` and a schedule source; `provider` in `{github, ""}`; `active_state` in
`{open, closed, all, ""}`; `reap` in `{stop, delete, none, ""}`; `grace`/`limit`
parse and are non-negative; like `session`/`scenario` it requires `[orchestrator]
enabled` (it owns spawned sessions) and its `repo` must pass `allowed_repo_paths`.

### Proposal 2: A dedicated `RunTrackerLoop` poll loop

Model it on `RunPRWatchLoop` — a standalone tracker poller with its own tick,
cursor, and config section, outside the triggers framework.

**Pros:** total control over cadence and per-issue scheduling; closest to the
issue's "in-tree prior art: `prwatch.go`" note.

**Cons:** duplicates the schedule loop, run-state machine, overlap/rate-limit
policy, concurrency cap, and `gr trigger` observability that #1013 built exactly
so tracker-poll wouldn't need its own. The issue itself re-scopes to "an
extension of the unified triggers framework, not a standalone poller." Rejected
for that reason.

## Consensus

(none yet — to be reviewed via the ship-it tribunal.)

## Other Notes

### References

- `docs/design/2026-07-11-triggers-design.md` — the parent framework.
- `internal/daemon/trigger.go` — schedule loop, `fireAction`, overlap/rate-limit.
- `internal/daemon/trigger_actions.go` — `actionSession`, `createTriggerSession`,
  `autoCleanupStopped`, `idleSeconds`.
- `internal/daemon/ghpr.go` — `ghRunner`, `repoSlug`, `parseGitHubRemote` (reused).
- `internal/daemon/daemon.go` — `Create` / `CreateOpts`, `Stop`, `SoftDelete`.
- Issues: #643 (this), #1013/#592 (framework), #71 (spawn-from-issue, done),
  #114 (reap-on-merge, done), #603 (lanes, open), #608 (draft sessions, open),
  #604 (webhook, NOT_PLANNED).

### Implementation Notes

- **State.** `TrackerIssue` on `SessionState` is a no-op state migration (new
  omitempty field). Grace tracking is in-memory on `triggerState`
  (`trackerObsolete map[string]time.Time`), rebuilt from live sessions — losing
  it on restart is fail-safe (re-observe → restart the grace clock → reap later,
  never sooner).
- **Idempotency / restart-safety.** Dedup is by the durable `TrackerIssue` tag, so
  a restart mid-reconcile never double-spawns. Overlap `skip` (the default) means
  a slow reconcile can't run twice concurrently.
- **Reap safety.** `delete` is a *soft* delete (retention window applies); a
  reaped session's work is recoverable via `gr restore`. Starred / system
  sessions are never reaped (mirrors `autoCleanupStopped`).
- **Read-only tracker.** The daemon never mutates the tracker; `gh` calls are
  list-only, short-timeout, `GH_PROMPT_DISABLED`.

### Testing

- **Config validation** (table-driven): tracker requires schedule + repo +
  `[action.tracker]`; provider/state/reap enums; orchestrator-required; repo
  allow-list; grace/limit parsing.
- **`reconcileTracker` (pure)**: newly-active issue → spawn; obsolete-past-grace →
  reap; obsolete-within-grace → held; reappear-active clears grace; `max_concurrent`
  caps spawns; existing live session → no respawn; soft-deleted → respawn; each
  `reap` policy. Fake `now`, injected grace map.
- **gh issue parsing**: `gh issue list --json` shape → `issueRef`s; empty; label
  extraction; `issueRefKey` stability.
- **Template vars**: issue vars expand; unknown `{var}` errors.
- Scots fixtures (repo `croft`, sessions `braw`/`dreich`, labels `thrawn`).
- All `-race` clean (grace map under `triggerState.mu`, dispatch off-lock).

### Open questions

- **Reap on issue *close* vs *label removal*.** v1 treats "not in the active set"
  uniformly (whatever `active_state`/`active_labels` select). A future refinement
  could distinguish "closed → delete" from "delabelled → stop."
- **Per-state concurrency lanes** — deferred to #603.
- **Delivering per-issue events** (spawned/reaped notifications) vs a single
  reconcile summary — v1 delivers the summary only.
</content>
</invoke>
