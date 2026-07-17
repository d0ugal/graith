---
title: "Design Doc: Near-instant PR detection via a git-refs watch"
authors: fix-pr-detect (agent)
created: 2026-07-14
status: Implemented
reviewers: (none yet)
informed: (TBD)
---

# Near-instant PR detection via a git-refs watch

The PR/CI watcher (`internal/daemon/prwatch.go`) resolves each session's PR by
polling `gh` on a timer. When an agent creates a branch, pushes, and opens a PR,
graith only notices on the next poll — up to ~15s plus batch-cap and
negative-cache latency — so the PR badge and any early CI-failure / review
notifications lag behind reality. This doc adds a cheap local filesystem watch
shared by sessions from the same repository. It *kicks* immediate PR re-polls
the moment a push/commit/checkout touches Git state, keeping the poll loop as the
fallback.

## Background

`RunPRWatchLoop` (`prwatch.go:107`) wakes every `prWatchTick` (15s), builds the
eligible target list (`prWatchTargets`), and polls at most `prWatchBatchCap` (3)
sessions per tick whose `nextPoll` is due. `pollSession` shells out to `gh pr
list --head <branch>` (`ghpr.go:resolvePR`) and, on a hit, writes PR/CI display
state and diffs against a per-session cursor to emit notifications.

Two existing behaviours matter here:

- **Branch drift (#1008).** `reconcileBranch` (`prwatch.go:236`) re-reads the
  worktree's live HEAD every tick. When the local branch changes (e.g. the agent
  ran `git checkout -b feature` or `gh pr checkout`), it clears the cursor and
  `nextPoll` so the next tick re-matches the PR against the branch the worktree is
  actually on. This already makes detection *work* for the common "agent made its
  own branch" case — it's just gated on the 15s tick.
- **Negative cache.** A branch with no PR is re-resolved at most every
  `prNoPRNegCache` (5m). Drift detection clears `nextPoll`, so a *branch change*
  bypasses the negative cache; but a push to the *same* branch that first gains a
  PR still waits out the ordinary cadence.

The trigger subsystem already runs an fsnotify-based watcher
(`RunFileWatchLoop` / `filewatch.go`) over session worktrees, with recursive
directory registration and debounce. That code watches *source files* and
explicitly ignores `.git/`; it is bound to user `[[trigger]]` config, so it is
the wrong vehicle, but its fsnotify patterns are a proven reference.

## Problem

Detection latency is functional, not cosmetic. Until graith associates the PR:

- the `gr list` / overlay PR+CI badges are blank or stale;
- **early PR activity is missed** — a CI failure, a merge conflict, or a review
  comment that lands in the association gap is silently baselined by the
  prime-on-first-observation logic (`diffAndBuild`, `prwatch.go:436`) rather than
  delivered. A stopped agent that would have been auto-resumed by a CI-failure
  directive just… isn't.

Real report: three `deployment_tools` sessions each pushed a branch and opened a
PR; graith took noticeably long to attach the PR to the session. The polling
cadence is the cause.

## Goals

- Detect a session's PR **within ~1s** of the push/commit that makes it
  resolvable, not on the next 15s tick.
- Reuse the existing poll loop for matching/notification — the watch only
  *triggers* a poll, it does not re-implement PR resolution.
- Keep the GitHub poll as an always-on fallback (covers pushes from outside the
  worktree, missed fsnotify events, and platforms where fsnotify degrades).
- Cheap: no busy-polling, no watching the object store, bounded watch count.
- Fail-open: if the watch can't be established, behaviour is exactly today's
  poll-only path.

### Non-Goals

- **Backfilling pre-discovery *human-intent* events** (review comments, PR
  comments, review decisions, lifecycle). Priming deliberately baselines these to
  avoid dumping a whole backlog when re-discovering an old PR after a daemon
  restart or `gh pr checkout`. Mechanical broken state (failing CI, merge
  conflict) is *already* surfaced on first discovery (`prwatch.go:466-522`) and
  stays that way. The near-instant watch shrinks the miss window to ~1s, which is
  the right fix; unconditional comment backfill is a separate, riskier change and
  is out of scope. (Confirmed with the orchestrator.)
- Matching a PR whose *remote* head-branch name differs from the local branch
  (`git push origin HEAD:other-name` with no local branch). That is a distinct
  branch-name-matching gap in `resolvePR`; the watch fires a re-poll but the poll
  still can't name-match it. Noted as a follow-up.
- New config surface. The watch is an internal accelerator of an
  already-configured feature; it lives under the same `[pr_watch] enabled` gate.

## Proposals

### Proposal 0: Do Nothing

Leave detection on the 15s poll. Rejected: the report is explicit that the lag is
functional (missed early CI/review notifications), and a filesystem watch is
cheap and well-precedented in this codebase.

### Proposal 1: A dedicated git-refs watch that kicks the poll loop (Recommended)

Add a lightweight watcher, owned by the PR-watch loop, that watches the
ref-bearing parts of each active repository and its eligible worktrees and, on a
change, asks the existing poll loop to re-poll the affected session(s) now.

#### Mechanism

A buffered `kick chan string` (session IDs) is added to `prWatchState`.
`RunPRWatchLoop` selects on it alongside the 15s ticker:

```
select {
case <-ctx.Done():        return
case <-time.After(tick):  runPRWatchTick(...)          // unchanged
case id := <-kick:        pollKicked(ctx, cfg, id)     // new: immediate targeted poll
}
```

`pollKicked` re-snapshots the one session, runs `reconcileBranch` + `pollSession`
for it (bypassing `nextPoll`/batch-cap gating), and applies a short per-session
kick cooldown (`prKickCooldown`, ~3s) so a burst of ref writes can't hammer `gh`.
Because it funnels through the existing `pollSession`, all downstream
gates/rate-limits/cursor logic are unchanged — a kick just moves *when* a poll
happens, never *what* it does.

The watcher itself (`prrefwatch.go`) mirrors `filewatch.go`'s shape but is much
smaller because it watches Git metadata rather than whole worktrees:

- A reconcile tick (~2s) diffs running, non-mirror, non-in-place sessions against
  live memberships. Sessions are grouped by their canonical `--git-common-dir`,
  with exactly one fsnotify backend per group.
- The repository backend registers the common-dir top level (`packed-refs` and
  the primary worktree's `HEAD`) plus the recursive `<common>/refs` and
  `<common>/logs` trees exactly once. Common events fan out to every member.
- A linked worktree contributes only its own gitdir top level (`HEAD`,
  `ORIG_HEAD`) and local `logs` tree to that backend. Ownership is ref-counted,
  so stopping or deleting the session removes those paths without disturbing
  the shared repository watch. The object store is never watched.
- Events are coalesced with a short per-session debounce (~750ms) and session IDs
  are sent non-blocking to `kick`. New nested ref/log directories inherit their
  parent's common or local ownership and are registered once.

#### Why this path set

A `git commit` writes `<gitdir>/logs/HEAD` and `refs/heads/<b>`; `git checkout
-b` writes `<gitdir>/HEAD` and the reflog; `git push` updates
`refs/remotes/origin/<b>` (and its reflog) in the common dir. Watching the
gitdir top + `logs` + the common `refs`/`logs` + `packed-refs` covers commit,
checkout, and push without touching the high-churn `objects/` tree. Multiple
sessions on one repo share the common registration; a common ref or reflog event
kicks all of them, while a linked `HEAD` event kicks only its owner. Only the
session whose branch now has a PR actually matches, and extra polls remain cheap
and rate-limited.

#### Trade-offs

- **Pro:** near-instant; reuses all poll-side logic; no new config; fail-open;
  common metadata costs are paid once per repository rather than per worktree.
- **Con:** a few redundant polls when several sessions share a repo (bounded by
  cooldown + rate-limit). fsnotify semantics vary by OS, but the poll fallback
  covers any missed event — the watch is a pure accelerator.

### Proposal 2: Shorten the poll tick / drop the negative cache

Poll every 1-2s instead. Rejected: it multiplies `gh` invocations across every
session continuously (rate-limit and CPU cost) to cut latency that a filesystem
watch removes for free, and still can't beat "poll exactly when something
changed."

### Proposal 3: Reuse the trigger `RunFileWatchLoop`

Drive PR re-polls off the existing watch infra. Rejected: it's bound to user
`[[trigger]]` config and explicitly ignores `.git/`; watching whole worktrees to
catch ref changes is far more churn than watching the ref subtree directly.

## Other Notes

### The negative-cache interaction (added after review)

A kick fires on a *ref* change, but the first PR on a branch is created by `gh pr
create` — a GitHub API call that runs *after* the push and writes no local ref,
so it produces no kick. The realistic flow is `git push` (→ kick) then `gh pr
create` seconds later. If a kicked poll that finds no PR installed the ordinary
`prNoPRNegCache` (5m) backoff, the session would be parked past the moment the PR
actually appears — leaving detection no better, and briefly worse, than the tick
baseline. `pollSession` therefore takes a `kicked` flag: a kicked no-PR miss uses
a short `prKickedNoPRBackoff` (20s) so the timer re-checks promptly and catches
the just-created PR, while a timer-driven miss still uses the full negative cache.
Relatedly, a *dropped* kick (kick channel saturated under fan-out) clears the
session's `nextPoll` so the next tick re-polls it rather than leaving it stranded
on a long backoff.

### kqueue resource bound (issue #1402)

On macOS/BSD, fsnotify's kqueue backend opens a real descriptor for every watched
path and for directory contents. Recursively registering the same common
`refs`/`logs` tree once per worktree therefore multiplied both descriptors and
memory by active session count. Grouping by canonical common dir makes growth
`repository Git metadata + small linked-worktree gitdirs`, rather than
`sessions × repository Git metadata`. Tests assert that two linked worktrees
have one backend and one unique common registration set; platform-independent
ownership assertions are used because descriptor accounting itself is
kqueue-specific. Any path registration failure is logged and leaves the timer
poll active, so descriptor/watch limits degrade latency instead of correctness.

### References

- `internal/daemon/prwatch.go` — `RunPRWatchLoop`, `reconcileBranch`,
  `pollSession`, `diffAndBuild` (priming).
- `internal/daemon/ghpr.go` — `resolvePR`.
- `internal/daemon/filewatch.go` — fsnotify reconcile/debounce reference.
- Issues #1008 (branch-drift re-match) and #1402 (shared repository watchers),
  plus the PR-comment author-trust design docs.

### Testing

- `resolveGitRefWatchPaths` / `gitRefWatchDirs` resolve the right common and
  local directories for a normal repo and a linked worktree.
- Two worktrees from one repository have one fsnotify backend and a unique union
  of common plus worktree-local registered directories. Removing one membership
  releases its local paths; removing the last closes the backend.
- Commits and pushes through the common refs/reflogs fan out to all repository
  sessions, while a linked worktree's `HEAD` change targets only its owner.
- `pollKicked` honours the cooldown (a second immediate kick is a no-op) and, via
  a stubbed `ghRunner`, drives a real notification through the unchanged
  `pollSession` path.
- Fail-open: a session with no resolvable git dir produces no watcher and no
  panic. `-race` clean (the watcher goroutines touch `prWatch` under its mutex).
</content>
</invoke>
