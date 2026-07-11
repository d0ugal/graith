---
title: "Design Doc: File-Watcher Triggers"
authors: Dougal Matthews
created: 2026-07-11
status: Draft (revised after a 2-judge source-verified review — see Consensus)
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/593
---

# File-Watcher Triggers

> **Note on code references.** `file:line` citations are anchored to symbol
> names and were written against `main` at the time of writing; absolute line
> numbers drift, so trust the symbol name, not the number.

## Background

graith's most valuable multi-agent pattern is the **continuous reviewer**: an
implementer agent works in a worktree, and a second agent watches the same
files and feeds review comments back over messaging. Today that reviewer is set
up by hand — the orchestrator (or the human) spawns a background session that
shares the implementer's worktree and is told, in its prompt, to "keep
reviewing as changes appear":

```bash
gr new reviewer --share-worktree implementer --background \
  --prompt "continuously review changes as they appear, send feedback via messages"
```

That works, but the *reaction* is left entirely to the reviewer agent's own
initiative. Nothing in graith actually observes the files changing. The reviewer
polls, re-reads, and guesses when there's something new — burning tokens on
"anything changed yet?" turns and reacting late. There is no event that says
"the implementer just touched `handler.go`, go look."

Meanwhile graith already has the two ingredients this needs:

1. **Daemon-owned watch loops.** `internal/daemon/prwatch.go` is a fully worked
   example of the daemon watching external state (a PR's CI and review
   comments), diffing it against a per-session cursor, rate-limiting, and
   delivering a structured message into the owning session's inbox — which
   auto-resumes a stopped agent. It is launched from `RunPRWatchLoop` alongside
   the other background loops in `daemon.go` (`RunGitPullLoop`, `RunPurgeLoop`,
   `RunDetectionLoop`). A file-watcher is the same shape with a different event
   source. (Note: prwatch's `gate()` "debounce" is a minimum *send cooldown*
   between notifications, not a quiet-window debounce — this design borrows its
   rate-limit/gate model, not its debounce semantics.)

2. **File-system watching.** `internal/config/watcher.go` already wraps
   `github.com/fsnotify/fsnotify` (a direct dependency) to watch `config.toml`
   for changes with a **quiet-window** debounce timer (`time.AfterFunc`). It is
   the precedent for "coalesce a burst of writes, then fire once things settle"
   — the debounce model this design uses. Note it watches the *containing
   directory* (not the file) precisely to catch atomic-save replacement (rename
   over the path), then filters to the path of interest; a worktree watcher must
   handle the same rename/atomic-save patterns.

This design is the **file-event source** for the unified triggers framework
introduced in [#592](https://github.com/d0ugal/graith/issues/592) (the
*schedule* source). Both issues agree on one shape:

> **A trigger is `(schedule | file-event) → action`, and the daemon fires it
> directly** — no attached orchestrator required.

The action vocabulary is shared: whatever #592 can do on a timer, this issue can
do on a file change, and vice versa. This doc owns the file-event source and the
parts of the shared framework needed to make it concrete; #592 owns the schedule
source. Where the two overlap (the action vocabulary, the `gr trigger` CLI, the
config table) this doc proposes the shared surface and flags it as jointly
owned.

> **Coordination risk (flagged, not resolved).** As of writing there is **no
> #592 design doc in the repo** — the shared framework (the `Trigger` struct with
> `Source = file | schedule`, the `gr trigger` CLI, the `TriggerAction` union) is
> being defined *unilaterally* here. That is deliberate — a file source needs a
> framework to hang off — but if #592 later lands with a divergent shape, this
> contract must be renegotiated. The framework surface below should be treated as
> a proposal subject to reconciliation with #592, not a settled interface.

## Problem

1. **Reactions are agent-driven, not event-driven.** The continuous-reviewer
   pattern relies on the reviewer agent noticing changes on its own. There is no
   real signal, so the reviewer either polls wastefully or reacts late.

2. **Setup is imperative and ephemeral.** "When the implementer touches files,
   ensure a reviewer reacts" lives in an agent's prompt and context window. It
   isn't declared anywhere, isn't reproducible, and dies with the session.

3. **No standard place for file→action wiring.** An orchestrator that wants
   "run the tests whenever `*.go` changes" or "ping the reviewer when the diff
   grows" has to build a bespoke poll loop each time.

4. **Feedback loops are a footgun.** The obvious naive implementation — "watch
   the worktree, on any write spawn/notify a reviewer" — re-fires on the
   reviewer's *own* writes, on formatter output, on `go build` artefacts, and on
   git's index churn. Without a designed answer this pattern oscillates or
   spawns duplicates.

## Goals

- Let a trigger fire an **action** when files change in a session's worktree,
  fired by the daemon so it survives terminal close and needs no attached
  orchestrator.
- Reuse the **shared action vocabulary** with #592 — a file event and a schedule
  drive the same set of actions.
- Make the continuous-reviewer pattern **declarative and automatic**: "ensure a
  reviewer reacts to the implementer's changes" becomes config, not a prompt.
- **Debounce and coalesce** so a burst of writes (a multi-file edit, a
  formatter pass) produces one action, not one per write.
- **Break feedback loops by construction** — the reviewer's own writes, tool
  output, and build artefacts must not re-trigger.
- **Avoid duplicate reactors** — "ensure a reviewer exists" must be idempotent.
- Fit graith's existing invariants: soft-delete hiding, auth, the sandbox, and
  shared-worktree ownership.

### Non-Goals

- **Cross-machine watching.** Triggers watch worktrees on the daemon's own host,
  like every other daemon loop.
- **Watching arbitrary paths outside a worktree.** v1 watches session
  worktrees only. Watching a repo that has no session is out of scope.
- **A general rules engine.** No boolean combinators, no "fire only if CI is
  also green". A trigger is one source → one action. Compose by declaring
  multiple triggers.
- **Replacing `gr wait`.** `gr wait --contains` already blocks a client on a
  session's *output*; this watches the *filesystem* and drives daemon actions.
- **Sub-second latency.** Debounce windows are seconds. This is a quality gate,
  not a hot-reload dev server.
- **The schedule source (#592).** Owned there; referenced here only where the
  shared framework requires it.

## Proposals

### Proposal 0: Do Nothing

Keep the manual continuous-reviewer recipe. Orchestrators wire reviewers by
hand and reviewers self-poll.

**Pros:**
- Zero implementation cost.
- No new failure modes (feedback loops, duplicate spawns).

**Cons:**
- The reviewer keeps reacting late and burning tokens on "anything changed?"
  turns.
- The pattern stays trapped in prompts and context windows — not reproducible.
- #592 lands a schedule source with a shared action vocabulary and no
  event-driven sibling, leaving the framework lopsided.

Rejected: the whole point of the framework is that the daemon reacts to events
so agents (and humans) don't have to poll.

### Proposal 1: Full recommendation (chosen)

A `[[trigger]]` is a declarative `(source) → (action)` rule. This doc defines
the **`file` source**; #592 defines the **`schedule` source**. Both resolve to
the same **action** set. The daemon owns a watcher per active file-trigger,
built on `fsnotify`, modelled structurally on `prwatch.go`: coalesce a burst
into one logical event, apply guardrails (debounce, feedback-loop suppression,
rate-limit), then run the action.

The rest of this doc specifies the file source, the shared action vocabulary,
the config and CLI surface, and — most importantly — the feedback-loop and
duplicate-spawn answers, because those are what make or break the pattern.

## The five open questions, answered

The issue poses five open questions. Each gets a concrete recommendation below;
the detailed design then follows from these answers.

### Q1 — Watch scope: whole worktree, globs, or git-tracked only?

**Recommendation: watch the whole worktree recursively, but filter events
through (a) a built-in ignore set and (b) optional user globs; default the
match set to git-tracked-ish files by honouring `.gitignore`.**

The three options aren't mutually exclusive — they're layers:

- **Whole worktree** is the only thing `fsnotify` can actually watch (it watches
  directories; recursion is our job). So the *watch* is the worktree.
- **git-tracked only** is the right *default filter*. Watching raw writes but
  discarding anything matched by `.gitignore` kills the biggest noise sources
  for free: `node_modules/`, build output, `.git/` internals, `__pycache__`,
  editor swap files. Crucially, graith worktrees carry the repo's `.gitignore`,
  so this needs no extra config. We do **not** shell out to `git status` per
  event (too slow, and racy mid-write); we load the ignore rules once per
  watcher and match paths in-process, refreshing when `.gitignore` itself
  changes.
  - **This is a new dependency decision.** graith has no gitignore matcher today
    (`go.mod` has `fsnotify`, `pelletier/go-toml`, etc., but no `go-git`). We
    should **vendor a small matcher** (gitignore is a compact grammar) rather
    than pull in `go-git` for one function. The matcher must handle nested
    `.gitignore` files, negation (`!pattern`), `.git/info/exclude`, and the
    global excludes file — or the design must explicitly scope v1 to top-level
    `.gitignore` + built-in ignores and defer the rest.
  - **"git-tracked-ish", not tracked-only.** Honouring `.gitignore` means
    *untracked-but-not-ignored* files pass (correct — a brand-new source file
    should trigger) and a tracked-but-ignored file is dropped (an edge case).
    The design does not attempt exact tracked-set parity; it filters by ignore
    rules, which is close enough and cheap.
  - **Prune the watch set, not just the fire.** `.gitignore` must gate which
    directories we *recurse into and `watcher.Add`*, not only which events fire
    an action. On a repo with a large ignored subtree (`node_modules/`,
    `target/`), adding a watch per directory would burn thousands of inotify
    watches against `fs.inotify.max_user_watches` (often 8k). We must not
    descend into ignored directories at all. See §The daemon watch loop for
    watch-limit degradation behaviour.
- **Configurable globs** are the override for the minority who want tighter or
  looser scope: `paths = ["**/*.go", "**/*.ts"]` to only fire on source, or
  `ignore = ["docs/**"]` to add to the ignore set. Globs are matched against
  the worktree-relative path.

Precedence: an event fires the action iff the path **is not** `.gitignore`d (or
`respect_gitignore = false`), **is not** in the built-in ignore set, **is not**
matched by user `ignore`, and — if `paths` is set — **is** matched by `paths`.

The built-in ignore set (always applied, even with globs) covers things that
are never interesting and are prime feedback-loop sources: `.git/` (in a linked
worktree this is a `.git` *file* pointing at the real gitdir, not a directory —
handle both), VCS lock files, and the graith scratch/tmp dirs.

### Q2 — Event source: raw filesystem writes, or git stage/commit events?

**Recommendation: raw filesystem writes (via `fsnotify`), coalesced by a
debounce window. Do not use git commit/stage as the source in v1.**

Rationale:

- **The value is reacting to work-in-progress**, not to commits. The continuous
  reviewer wants to see the change as the implementer makes it, not wait for a
  commit that may never come (agents often leave work uncommitted until asked).
  Git-commit-driven triggers are strictly less useful for the motivating case.
- **Raw writes are available now.** `fsnotify` is already a dependency and
  already wrapped (`config/watcher.go`). Git-event watching would mean polling
  `git` or watching `.git/logs/HEAD` / index files — slower, racier, and it's
  exactly the `.git/` churn we otherwise ignore.
- **Debounce solves the coalescing concern the issue raises.** The reason to
  reach for commits is "don't fire on every intermediate write" — but a debounce
  window (fire once the worktree has been *quiet* for N seconds) coalesces a
  burst of writes into one event without waiting for a commit. This mirrors
  `config/watcher.go`'s existing debounce timer and `prwatch.go`'s
  `DebounceDuration` cooldown.

A **`git-commit` source is noted as future work** (§Future work) for triggers
that genuinely want commit granularity (e.g. "run the full test matrix on each
commit") — it slots into the same framework as a third source kind.

### Q3 — Default action: message an existing reviewer, or spawn one? How to avoid duplicates?

**Recommendation: the action is explicit in the trigger, but the flagship
`ensure-reviewer` action is *message-if-exists, spawn-if-not*, made idempotent
by tagging spawned reactors with the trigger's identity.**

There are two distinct actions and users pick per trigger:

- `message` / `publish` — deliver to an existing session or topic. Cheap,
  never spawns. Right when a reviewer is already running (or subscribed).
- `spawn` — create a new session (optionally sharing the source's worktree).
  Expensive; must be guarded against duplicates.

The convenience action that captures the issue's intent — "ensure a reviewer
reacts" — is **`ensure-reviewer`** (sugar over `spawn`): on fire, look for a
reactor already owned by *this trigger*; if one exists (running **or stopped**),
`message` it; otherwise `spawn` one and record ownership.

**Reuse stopped reactors — don't duplicate them.** A tagged reactor that is
merely *stopped* (not deleted) must be **messaged**, not re-spawned:
`notifyFromDaemon` publishes to the inbox and calls `notifyInbox` →
`resumeForInbox`, which **auto-resumes a `StatusStopped` session**
(`notify.go`). So messaging a stopped reactor revives the *existing* one — which
is exactly what we want. Spawning on "stopped" would strand a dead session and
create a duplicate on every reviewer exit, and it directly contradicts the
`message` action's own documented auto-resume behaviour. The rule is therefore:

- tagged reactor exists and is **running or stopped** (not soft-deleted) →
  `message` it (auto-resume handles the stopped case);
- tagged reactor is **soft-deleted or absent** → `spawn` a fresh one.

**Duplicate avoidance** is by **ownership tagging**, not by name-guessing. When
a trigger spawns a reactor it stamps the new session with the trigger's ID
(a new `TriggerID` / `TriggerReactor` marker on `SessionState`, alongside the
existing `ScenarioID` fields). This is robust against renames in a way that
"does a session named `reviewer` exist?" is not, and it reuses the pattern
scenarios already use to associate sessions with their parent (`ScenarioID`,
`ScenarioRole`).

**A marker lookup alone is not race-safe**, though. Two fires of the same
trigger (or a rapid fire during a slow `sm.Create`) can both observe "no
reactor" and both spawn. The daemon must **reserve** the reactor before
creating it: under `sm.mu`, atomically set the trigger's `ReactorID` to a
placeholder (or serialise per-trigger fires) so the second fire sees a reactor
in-flight and messages/waits instead of spawning, rolling back the reservation
if `sm.Create` fails. This mirrors how scenario start pre-reserves
`StatusCreating` placeholder records under lock *before* concurrent creation
(`scenario.go`), rather than tagging only after the fact.

### Q4 — Feedback loops: how to stop the reactor's own writes from re-triggering?

This is the crux. Four layers, defence in depth:

**The honest position up front:** `fsnotify` cannot attribute a write to a
process, so there is no *general* way to know "did the implementer or the
reactor write this file?" from the event stream alone. This design therefore
splits reactors into two classes and closes the loop **by construction** for the
one that matters, rather than pretending an approximation is sound for the one
that doesn't:

1. **Reactors that cannot write the watched tree (the flagship case) — loop
   closed by construction.** The continuous *reviewer* delivers feedback over
   **messaging**, not by editing files, and — decisively — a `spawn`/
   `ensure-reviewer` reactor with `share_worktree` gets its **own separate,
   writable scratch worktree**; the watched (source) worktree is mounted
   **read-only** in the reactor's sandbox `ReadDirs` (`daemon.go` requires the
   sandbox precisely so the shared worktree can be mounted read-only, and gives
   the reactor a distinct scratch `WorktreeDir`). So a spawned agent reactor
   **physically cannot write the files it watches**, regardless of what it does.
   For the motivating pattern the loop cannot exist. This is the strongest layer
   and it needs no approximation.

2. **Reactors that *do* write the watched tree (`run` actions only).** A `run`
   action such as `gofmt`/an auto-fixer executes daemon-side against the
   *source* worktree (it is not sandboxed into a scratch dir — that is the whole
   reason it can write, and the reason this layer exists). Here the defence is
   **generation-based discard, not deferral**:
   - Each trigger fire that launches a `run` command bumps a **generation
     counter** and records the launch. The watcher **discards every filesystem
     event observed while that `run` is in flight *and* until the next quiet
     window after it exits** — those events are attributed to the command and
     dropped, not coalesced into the next fire. Only writes that begin strictly
     *after* the command has exited and the settling window has passed count as
     a genuinely new event.
   - This is the critical correction over a naive "defer then fire on quiet":
     deferring would re-fire the action on the command's *own* output (a real
     `gofmt` loop). Discarding by generation breaks it.
   - **Tradeoff, stated honestly:** because we discard by time-window and not by
     true attribution, a *concurrent* implementer/human edit made while the
     `run` command is executing is discarded along with the command's output.
     For a fast formatter this window is sub-second and acceptable; for slow
     commands it is a real gap. This is why write-capable `run` actions are
     **phased after** the read-only actions (see Rollout) and why the honest
     fallback — recommended for v1 — is to require `run` actions to be
     non-mutating (report results over `delivery`, don't edit the tree), which
     collapses this case back into layer 1.

3. **Debounce (quiet-window coalescing).** The watcher fires only after the
   worktree has been *quiet* for the debounce window (the `config/watcher.go`
   model). A multi-file edit settles into one quiet window; the action fires
   once. This coalesces bursts; it does **not** distinguish causes (that is
   layer 2's job).

4. **Rate-limit backstop.** A rolling per-trigger rate-limit (mirroring
   `prwatch.go`'s `gate()` "at most N per 30 minutes") is the last line of
   defence: even if 1–3 all fail for some pathological input, the trigger cannot
   thrash. It **bounds** damage; it does not by itself *prevent* a loop, so it is
   a backstop, not the primary defence. When the limit trips, the daemon records
   it in the trigger's status (visible in `gr trigger status`, not just logs) so
   the user can see the trigger is hot.

**Cross-trigger interference.** Layers 2–4 are *per-trigger*. Two triggers on
one worktree — say A = `ensure-reviewer` (read-only) and B = `run gofmt` (writes
the source tree) — mean B's writes are visible to A's watcher, and A's
generation-discard only covers *A's own* `run` reactors, so A would fire on B's
output (bounded by A's rate-limit). The mitigation: **suppress writes attributed
to *any* trigger's in-flight `run` on the same worktree**, not just the firing
trigger's own — the generation/discard state is keyed by worktree, not by
trigger. The doc's "no rules engine" non-goal still holds; this is bookkeeping,
not conditional logic.

**Net:** the flagship read-only case (layer 1) has no loop at all. The
write-capable case is *bounded* (never a runaway) and, with the recommended
"`run` is non-mutating in v1" contract, also collapses to no-loop. The design is
deliberately honest that generic source attribution is not achievable from
`fsnotify` alone.

### Q5 — Opt-in granularity: per-session flag, or scenario-level config?

**Recommendation: all three levels, because they serve different users, unified
by one `[[trigger]]` schema.**

- **Global `config.toml`** (`[[trigger]]` tables) — for standing rules a user
  always wants ("in any graith session on repo X, run tests when `*.go`
  changes"). This mirrors how `[pr_watch]` and `[[mcp_servers]]` are configured.
- **Scenario-level** — a scenario can declare triggers in its TOML so a fleet
  ships with its coordination wired in (the implementer/reviewer pair becomes a
  scenario with a `file → ensure-reviewer` trigger). This is the natural home for
  the motivating example and fits the scenario model, where sessions already
  know about each other.
- **Per-session, imperative** — `gr trigger add --session implementer --on
  'file:**/*.go' --do 'message reviewer'` for ad-hoc wiring, and to
  create/enable/disable/remove triggers at runtime without editing files. This
  is the equivalent of `gr scenario add`.

All three produce the same in-daemon trigger object; the level is just where the
declaration lives and its lifetime (config = persistent, scenario = scenario
lifetime, `gr trigger` = daemon state until removed).

## Detailed design

### Declaration vs. runtime state

Triggers have **two sources of truth**, and conflating them is a trap. The
design keeps them separate:

- **Declaration** — where a trigger is *defined*. Config-declared triggers live
  in `config.toml` (authoritative; re-read on config reload). Scenario-declared
  triggers live in the scenario's TOML (lifetime = the scenario).
  `gr trigger`-declared triggers have no external declaration — they *are*
  runtime state.
- **Runtime state** — the daemon's live view in `state.json`: which triggers are
  active, their **stable IDs**, current `ReactorID`, enable/disable overrides,
  and suppression/rate-limit bookkeeping.

The reconciler (below) maps declarations → runtime triggers. Rules that must be
explicit to avoid drift:

- **Stable IDs.** A config/scenario trigger's runtime ID must be **deterministic
  from its declaration** (e.g. `hash(origin + name)`), so reactor adoption
  survives a config reload or daemon restart — otherwise a reload would orphan
  the tagged reactor. `gr trigger` triggers get a persisted random ID.
- **Removal.** Deleting a `[[trigger]]` from `config.toml` (or renaming it)
  removes/replaces the runtime trigger on reload and tears down its watcher.
  Stopping/deleting a scenario removes its triggers (cleanup hook in scenario
  teardown). `gr trigger remove` removes a CLI trigger.
- **Enable/disable.** `gr trigger disable` on a config/scenario trigger is a
  runtime override recorded in `state.json`; it does not edit the declaration,
  and a later reload must not silently re-enable it.
- **State migration.** `state.json` gains a `Triggers` map; loading old state
  with no such key yields an empty map (forward-compatible, like other added
  fields).

### The trigger object

```go
type Trigger struct {
    ID      string        // stable ID (deterministic for config/scenario; random for CLI)
    Name    string        // human label, optional
    Enabled bool          // runtime enable/disable override

    Source  TriggerSource // file | schedule (#592)
    Action  TriggerAction // shared vocabulary

    // File-source fields:
    SessionID        string   // whose worktree to watch (the source session)
    Paths            []string // optional include globs (worktree-relative)
    Ignore           []string // extra ignore globs
    RespectGitignore *bool    // pointer so "unset" is distinct from explicit false; default true
    Debounce         string   // duration string; default 3s

    // Bookkeeping (persisted):
    Origin          string    // "config" | "scenario:<id>" | "cli"
    CreatorID       string    // session that installed a CLI trigger (auth principal); "" for config
    AuthorizedTargets []string // immutable spawn/message targets this trigger may act on
    ReactorID       string    // session this trigger currently owns (ensure-reviewer)
    LastFired       time.Time // exported so it serialises
    Suppressed      string    // last suppression/rate-limit reason, for `gr trigger status`
    FireCount       int       // for status/rate accounting
}
```

`RespectGitignore` is a `*bool` so an omitted value (nil → default true) is
distinguishable from an explicit `false`; the loader populates the default once.

Sessions gain a small marker so a spawned reactor can be found idempotently
(mirroring the `ScenarioID` fields on `SessionState`):

```go
TriggerID      string `json:"trigger_id,omitempty"`      // trigger that spawned this session
TriggerReactor bool   `json:"trigger_reactor,omitempty"` // this is a trigger-owned reactor
```

### The daemon watch loop

A new `internal/daemon/filewatch.go`, launched from the same block as the other
loops in `daemon.go`:

```go
go sm.RunFileWatchLoop(ctx)
```

`RunFileWatchLoop` reconciles the set of *active file triggers* (enabled,
source kind `file`, whose session is running and not soft-deleted) against a set
of live `fsnotify` watchers:

- **Reconcile, don't poll.** On startup, on config reload, on session
  create/stop/delete, and on `gr trigger` mutations, the loop diffs desired vs.
  actual watchers and adds/removes `fsnotify` watches. Config reload is **new
  wiring**, not a free hook: `config.NewWatcher(configFile, sm.applyConfig)`
  exists (`daemon.go`), but `applyConfig` must be extended to reconcile
  triggers. Each watcher watches its session's worktree recursively (walk the
  tree, `watcher.Add` each **non-ignored** dir — ignored subtrees are pruned per
  Q1, never added — and add new dirs as `Create` events arrive). Recursion has
  known races this design must handle:
  - **Create-before-watch race.** Files can be created inside a new directory
    before its `Create` event is processed and the dir is watched. On adding a
    watch for a newly-created directory, **re-scan it** for entries that already
    exist and treat them as events (scan-on-registration), rather than silently
    losing them.
  - **Moved-in trees.** A directory moved into the worktree needs recursive
    registration of its whole subtree, not just its top level.
  - **Atomic saves.** Editors/formatters write to a temp file and rename over
    the target (the reason `config/watcher.go` watches the containing dir). The
    matcher keys on the final path, so a rename-into-place counts as a write to
    that path.
  - **Watch-limit / overflow degradation.** `watcher.Add` can fail (Linux
    `fs.inotify.max_user_watches` exhausted) and fsnotify can drop events on
    overflow. On failure the trigger enters a **degraded** status (recorded,
    surfaced in `gr trigger status` and `gr doctor`) and — decision to make
    explicit — **fails safe by disabling the trigger with a clear reason**
    rather than silently watching a partial tree. Overflow triggers a full
    re-scan + debounced fire so a dropped burst isn't missed.
- **Per-trigger goroutine + debounce timer.** Each active trigger has a debounce
  timer exactly like `config/watcher.go`: an accepted event (post-filtering)
  resets the timer; when it fires, the worktree has been quiet for `Debounce`,
  and the action runs once with the coalesced set of changed paths.
- **Off the request path.** Like `prwatch.go`, watcher bookkeeping has its own
  mutex, and the action (which may call `sm.Create`, message, or exec) runs
  without `sm.mu` held so `gr list` never blocks. Session-state snapshots are
  taken under `RLock`, released, then acted on.
- **Guardrails before firing:** feedback-loop suppression (Q4.2), then the
  rolling rate-limit gate (Q4.4). A suppressed/limited fire is logged, not
  silently dropped.

Watchers are torn down when the source session stops, is soft-deleted, or the
trigger is disabled — and pruned for vanished sessions the way
`prunePRWatchState` prunes cursors, so the maps don't grow unbounded on a
long-lived daemon.

### Shared action vocabulary (jointly owned with #592)

An action is a tagged union. v1 scope:

| Action | Params | Behaviour |
|--------|--------|-----------|
| `message` | `target` (session), `template` | Deliver a message to a session's inbox via `notifyFromDaemon` — which auto-resumes a stopped agent. |
| `publish` | `topic`, `template` | Publish to a pub/sub topic (`msgstore.Publish`); subscribers react. |
| `spawn` | `name`, `agent`, `prompt`, `share_worktree` (bool) | `sm.Create` a new session, tagged `TriggerID`/`TriggerReactor`. |
| `ensure-reviewer` | `agent`, `prompt` | Idempotent sugar over `spawn`: message the owned reactor if it exists (running/stopped), else spawn one sharing the source worktree read-only. |
| `run` | `command`, `cwd`, `timeout`, `delivery` | Run a command against the worktree; output delivered per `delivery`. See spec below. |
| `start-scenario` | `scenario` | Start a scenario by name (orchestrator-authorized only). Shared with #592. |

**`run` action spec.** Because `run` is the one action that can write the
watched tree (§Feedback loops layer 2), its execution model must be pinned
down, not left implicit:

- **argv, not shell.** `command` is an argv array (no implicit shell) unless a
  `shell = true` is set, to avoid quoting/injection surprises.
- **Runs daemon-side against the source worktree** with `cwd` defaulting to the
  worktree (contained to it). It is **not** sandboxed into a scratch dir — this
  is deliberate (it's how a formatter can write) and is exactly why it can loop,
  hence the v1 recommendation that `run` be non-mutating.
- **Timeout + cancellation** (default e.g. 5m); killed on daemon shutdown.
- **Concurrency:** a per-trigger `run` is serialised — a fire while the previous
  `run` is still executing is dropped or queued (not run in parallel), which is
  also what keeps generation-based discard well-defined.
- **Output bounds + delivery:** stdout/stderr are size-capped and delivered via
  `delivery` = `inbox` (a `notifyFromDaemon` message to the source session) or
  `store:<key>` (a store doc, e.g. `builds/{session_name}.log`). The exit code
  is included.

The message/publish/run templates expand the coalesced event: `{session_name}`,
`{changed_files}`, `{change_count}`, `{worktree_path}` — reusing the existing
template-expansion convention (`{session_id}` etc. from MCP-server config).
**Note:** that expander populates a fixed set of names and **hard-errors on
unknown `{name}` tokens** (per CLAUDE.md); the new `{changed_files}` /
`{change_count}` tokens must be **registered** in the expander or they'll be
rejected. Confirm the trigger templates go through the same (extended) expansion
code, not a fork.

`spawn`, `ensure-reviewer`, and `start-scenario` overlap with #592's "start a
scenario / spawn a session" actions; that shared surface is the contract between
the two issues. This doc proposes it; #592 consumes the same set from its
schedule source.

Actions that spawn or message run with **daemon authority at fire time**, so the
authority must be constrained at *install* time and re-checked at *fire* time —
a bare `Origin` string is not enough to reconstruct a principal after a daemon
restart. The design therefore persists an explicit auth context on the trigger:

- **`CreatorID`** — the session that installed a `cli`-origin trigger. This is
  the durable principal. Config-origin triggers have no session creator; they
  carry the authority of the config file (host-level), like `[pr_watch]`.
- **`AuthorizedTargets`** — the set of sessions/topics/scenarios the trigger may
  act on, **frozen at install time** from what the creator was allowed to target
  then (graith auth is relational — a session may target itself and its
  descendants; the orchestrator may target anything, per `auth.go`). Freezing
  the target set means a trigger can't gain reach later by ancestry changes.
- **Install-time check:** `gr trigger add` validates that the creator may target
  what the trigger will act on (reusing the existing authorization rules).
- **Fire-time check:** before spawning/messaging, the daemon re-validates the
  action target against `AuthorizedTargets`; a target no longer valid (e.g. the
  session was deleted) is skipped and logged.
- **Creator gone:** if `CreatorID` is deleted, a `cli`-origin trigger with
  spawn/message actions is **disabled** (fail-safe), not silently promoted to
  unrestricted daemon authority.
- Only the orchestrator can install triggers whose action is `start-scenario`
  (matching `scenario_start`'s `SystemKind == orchestrator` check).

### Config schema

Global, in `config.toml`:

```toml
[[trigger]]
name = "review-go"
source = "file"
session = "implementer"        # or match by repo/role; see below
paths = ["**/*.go"]
ignore = ["**/*_test.go"]
debounce = "3s"
respect_gitignore = true       # default

action = "ensure-reviewer"
agent = "claude"
prompt = "Review the changes since your last look; send feedback via gr msg."
```

```toml
[[trigger]]
name = "test-on-change"
source = "file"
session = "implementer"
paths = ["**/*.go"]
action = "run"
command = "go test ./..."
delivery = "inbox"             # or "store:builds/{session_name}.log"
```

Because a standing config trigger can't name a session that doesn't exist yet,
config triggers may target by **repo** and/or **scenario role** instead of a
literal session name (e.g. `role = "implementer"`), and bind to matching
sessions as they're created. A literal `session = "..."` binds to that one
session. (Scenario-embedded triggers can use role names directly, since the
scenario defines them.)

### CLI surface (jointly owned with #592)

```bash
gr trigger add --name review-go \
  --session implementer --on 'file:**/*.go' \
  --do 'ensure-reviewer --agent claude'
gr trigger list
gr trigger disable review-go
gr trigger enable review-go
gr trigger remove review-go
gr trigger status                 # last-fired, suppression/rate-limit state
```

`gr trigger` is the runtime front-end over daemon trigger state (a new
`trigger_*` control-message family in `protocol/messages.go`, dispatched in
`handler.go`). #592 adds `--on 'cron:...'` / `--on 'every:...'` to the same
command.

### Interaction with existing invariants

- **Soft-delete.** A trigger whose source session is soft-deleted stops firing
  (the reconciler drops the watcher, exactly as `prwatch.go` skips
  `IsSoftDeleted()` sessions). Its owned reactor is *not* auto-torn-down —
  restoring the source should be able to re-adopt it — but restore re-enables the
  trigger.
- **Sandbox.** `ensure-reviewer`/`spawn` with `share_worktree` require the
  sandbox (existing `--share-worktree` invariant in `daemon.go`), so the shared
  worktree is mounted read-only — which is also feedback-loop layer Q4.1.
- **Shared worktree.** The watcher watches the *source* (implementer) worktree.
  The reactor shares it read-only. There is exactly one **write-capable
  worktree** (the source), though "one writer" is too strong — the source agent,
  the human's editor, git hooks, and a daemon-side `run` action can all write it.
  The point is that the *reactor* can't, so the reactor never enters the write
  stream being coalesced.
- **Auth.** Covered above: trigger actions inherit the origin's authority.

## Alternatives considered

- **Git-commit / index watching as the source.** Rejected for v1 (Q2): less
  useful for work-in-progress review, racier, and it's the `.git/` churn we
  otherwise ignore. Kept as future work behind the same framework.
- **Client-side watching (a `gr watch` that runs in the attached client).**
  Rejected: dies when the terminal closes, and the whole framework's premise
  (shared with #592) is that the *daemon* fires triggers so they survive detach.
- **Reactor-attributed suppression via PID/process accounting.** `fsnotify`
  can't attribute a write to a process, and threading real process attribution
  through the sandbox is heavy. Read-only reviewers (Q4 layer 1) plus
  generation-based discard for `run` (Q4 layer 2) cover the real cases without
  it — and the design is explicit that generic attribution isn't achievable from
  `fsnotify` alone.
- **Polling `git status` per event to decide tracked-ness.** Rejected: too slow
  per event and racy mid-write; we match `.gitignore` in-process instead (Q1).

## Testing

Following the coverage expectations, the pure logic is extracted and unit-tested;
the `fsnotify` glue is thin:

- **Path filtering** (gitignore + built-in ignore + user globs + precedence) —
  table-driven, no filesystem needed. Fixtures use Scots words (`bothy/`,
  `glen/*.go`, `haar.tmp`).
- **Debounce/coalescing** — feed a synthetic event stream through the
  coalescer with a fake clock, assert one fire per quiet window.
- **Feedback-loop suppression** — assert a `run` action's own writes are
  **discarded** (generation counter), not re-fired; assert a *concurrent*
  legitimate write during a `run` and a *late* event after the command exits are
  handled per the documented policy (not silently mis-attributed); assert
  cross-trigger `run` writes are suppressed for a sibling read-only trigger; and
  the rate-limit gate trips and records a reason.
- **Duplicate avoidance / reuse** — `ensure-reviewer` messages when a tagged
  reactor is running, **messages (auto-resumes)** when it is stopped, spawns only
  when the reactor is soft-deleted/absent; two concurrent fires reserve one
  reactor (no double-`sm.Create`).
- **Reconciliation** — desired-vs-actual watcher diffing on session
  create/stop/delete and config reload; pruning of vanished sessions; stable IDs
  survive a reload so reactor adoption holds; a disabled config trigger is not
  re-enabled by reload.
- **Watch scale/degradation** — ignored subtrees are not `watcher.Add`ed;
  create-before-watch entries are picked up by scan-on-registration; a
  `watcher.Add` failure degrades/disables the trigger with a recorded reason.
- **Auth** — a `cli`-origin trigger can't spawn what its `CreatorID` couldn't at
  install time; a deleted creator disables the trigger; only the orchestrator
  installs `start-scenario` triggers.
- **Integration** (`internal/integration/`) — spawn a real daemon, create a
  session, write a file into its worktree, assert the configured action fires
  (message lands in the target inbox).

Every behaviour ships with its test; the fsnotify-driven loop keeps its
untestable surface minimal by delegating all decisions to tested pure functions
(the `prwatch.go` split of `RunPRWatchLoop` vs. `diffAndBuild`/`gate` is the
model).

## Rollout / phasing

1. **Framework + `file` source + `message`/`publish` actions.** The smallest
   useful slice: watch a worktree, debounce, deliver to an existing reviewer or
   topic. No spawning, so no duplicate/feedback risk beyond debounce.
2. **`ensure-reviewer` / `spawn`** (with reactor reuse + atomic reservation +
   auth principal). Adds ownership tagging and the idempotent spawn/message.
   This lights up the flagship "make the continuous reviewer automatic" case —
   and because reactors are read-only on the watched tree, it carries **no
   feedback-loop risk beyond debounce**.
3. **`run` action, non-mutating contract first.** Ship `run` with output
   `delivery` (tests/lint results to inbox/store) but **required non-mutating**
   (report, don't edit). Only then, if there's demand, add mutating `run`
   (formatters/auto-fixers) with the full generation-discard machinery and its
   honest concurrency caveat.
4. **Config + scenario-embedded triggers** (persistent and fleet-shipped rules),
   once `gr trigger` (runtime) has proven the model.

#592 lands the `schedule` source against the same framework in parallel; step 1
is the shared foundation.

## Future work

- **`git-commit` source** — commit-granularity triggers for "run the full
  matrix per commit", slotting in as a third source kind.
- **Compound conditions** — "fire only if CI is also green" by composing with
  `pr_watch` state. Deliberately out of scope to avoid a rules engine.
- **Watching non-session paths** — standing triggers on a repo with no session.
- **Change summarisation in the message** — include a diff stat or the actual
  hunk in the delivered message so the reviewer needs fewer read turns.

## Consensus

This draft was revised after an independent 2-judge review (Claude Opus 4.8 and
Codex/o3), each verifying the design's code citations against the worktree. Both
converged on the same load-bearing gaps, now addressed in-doc:

- **Feedback-loop soundness for write actions (both, critical).** `fsnotify`
  can't attribute a write to a process, so the original "quiescence" wording
  described a loop for `run` actions. Revised: read-only reactors close the loop
  by construction (verified: spawned reactors get a separate writable scratch
  worktree, watched tree is read-only); write-capable `run` actions use
  generation-based *discard* with an honest concurrency caveat, and v1 requires
  `run` to be non-mutating.
- **`ensure-reviewer` duplicating stopped reactors (both).** Revised to
  message-and-auto-resume a stopped reactor (matching `notifyFromDaemon`'s real
  behaviour) and to reserve the reactor under lock (scenario `StatusCreating`
  pattern) so concurrent fires can't double-spawn.
- **Recursive watch scale (both).** Revised to prune ignored subtrees from the
  watch set, handle create-before-watch / moved-in / atomic-save races, and
  degrade explicitly on inotify-limit failure.
- **Auth principal (Codex) and declaration-vs-state persistence (Codex).** Added
  a durable `CreatorID` + frozen `AuthorizedTargets` with install/fire-time
  checks, and separated authoritative declarations from runtime state with
  stable IDs and explicit removal/enable rules.
- **#592 coordination risk (Claude).** Called out that the shared framework is
  defined unilaterally here, pending reconciliation.

Both judges independently confirmed the code grounding was accurate and praised
the read-only-reviewer by-construction fix, the ownership-tag dedup, and the
prwatch-modelled testability split.

## Open questions (for review)

- **Config-trigger binding by role/repo vs. literal session.** Is
  `role = "implementer"` (bind-on-create) worth the matching complexity in v1,
  or should config triggers be scenario-only and `gr trigger` handle ad-hoc
  session targeting? (Leaning: scenario + `gr trigger` for v1, repo/role binding
  as a fast-follow.)
- **Default debounce.** 3s is a guess. Too short chases multi-file edits; too
  long makes review feel laggy. Should it scale with `change_count`?
- **Reactor lifetime.** When the source session ends, should its
  `ensure-reviewer` reactor be stopped, soft-deleted, or left running? (Leaning:
  left running so it can deliver a final review, then it stops on its own.)
