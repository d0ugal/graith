---
title: "Design Doc: File-Watcher Triggers"
authors: Dougal Matthews
created: 2026-07-11
status: Draft
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
   comments), diffing it against a per-session cursor, debouncing, rate-limiting,
   and delivering a structured message into the owning session's inbox — which
   auto-resumes a stopped agent. It is launched from `RunPRWatchLoop` alongside
   the other background loops in `daemon.go` (`RunGitPullLoop`, `RunPurgeLoop`,
   `RunDetectionLoop`). A file-watcher is the same shape with a different event
   source.

2. **File-system watching.** `internal/config/watcher.go` already wraps
   `github.com/fsnotify/fsnotify` (a direct dependency) to watch `config.toml`
   for changes with a debounce timer. The mechanics of "watch a path, coalesce
   a burst of writes, fire a callback" are already in the tree.

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
  watcher and match paths in-process (e.g. `go-git`'s gitignore matcher or a
  vendored equivalent), refreshing when `.gitignore` itself changes.
- **Configurable globs** are the override for the minority who want tighter or
  looser scope: `paths = ["**/*.go", "**/*.ts"]` to only fire on source, or
  `ignore = ["docs/**"]` to add to the ignore set. Globs are matched against
  the worktree-relative path.

Precedence: an event fires the action iff the path **is not** `.gitignore`d (or
`respect_gitignore = false`), **is not** in the built-in ignore set, **is not**
matched by user `ignore`, and — if `paths` is set — **is** matched by `paths`.

The built-in ignore set (always applied, even with globs) covers things that
are never interesting and are prime feedback-loop sources: `.git/`, VCS lock
files, and the graith scratch/tmp dirs.

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
live reactor already owned by *this trigger*; if one exists and is running,
`message` it; otherwise `spawn` one and record ownership.

**Duplicate avoidance** is by **ownership tagging**, not by name-guessing. When
a trigger spawns a reactor it stamps the new session with the trigger's ID
(a new `TriggerID` / `TriggerReactor` marker on `SessionState`, alongside the
existing `ScenarioID` fields). On the next fire the daemon looks up "is there a
non-deleted, running session tagged with this trigger's ID?" — if yes, message
it; if no (it stopped, was deleted, or never existed), spawn. This is robust
against renames and race conditions in a way that "does a session named
`reviewer` exist?" is not. It reuses the exact pattern scenarios already use to
associate sessions with their parent (`ScenarioID`, `ScenarioRole` set right
after `sm.Create`).

### Q4 — Feedback loops: how to stop the reactor's own writes from re-triggering?

This is the crux. Four layers, defence in depth:

1. **The reactor writes elsewhere.** The continuous *reviewer* is
   read-only on the code — it delivers feedback over **messaging**, not by
   editing files. A reviewer that only reads and messages produces **zero**
   worktree writes, so it cannot re-trigger. This is already how the pattern is
   documented, and `--share-worktree` mounts the shared worktree **read-only**
   under the sandbox (`daemon.go` requires the sandbox precisely so the shared
   worktree can be mounted read-only). So for the flagship case the loop is
   broken *by construction*: the reactor physically cannot write the files it
   watches.

2. **Source-attributed suppression.** For reactors that *do* write (e.g. a
   `run` action that runs `gofmt`, or an auto-fixer), the daemon knows which
   session each write-capable reactor belongs to. A trigger watches its
   **source** session's worktree; writes made by a session the trigger *owns*
   (its tagged reactors) are suppressed. Because `fsnotify` can't attribute a
   write to a process, we approximate this at the trigger level: while an owned
   reactor spawned by a `run`/`spawn` action is still running, the watcher
   enters a **quiescing** state and does not re-fire until it has both (a) seen
   the reactor finish and (b) observed a fresh debounce-quiet window. This is
   the "don't chase your own tail" guard.

3. **Debounce + quiescence.** The watcher fires only after the worktree has been
   *quiet* for the debounce window. A formatter pass that writes ten files
   settles into one quiet window; the action fires once. Combined with (2), a
   reactor's burst of writes is coalesced and attributed away rather than
   fanning out into ten more fires.

4. **Rate-limit backstop.** A rolling per-trigger rate-limit (mirroring
   `prwatch.go`'s "at most N per 30 minutes" gate) is the last line of defence:
   even if 1–3 all fail for some pathological input, the trigger cannot thrash.
   When the limit trips, the daemon logs it (never silently drops) so the user
   can see the trigger is hot.

The combination of "reviewers don't write" (1) + "owned writes are suppressed"
(2) + "coalesce then only fire on quiet" (3) + "hard rate cap" (4) means the
common case has no loop at all, and the uncommon case is bounded.

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

### The trigger object

A trigger, held in daemon state (`state.json`, so it survives daemon restart
like sessions and scenarios do):

```go
type Trigger struct {
    ID      string        // stable ID, e.g. "tr-abc123"
    Name    string        // human label, optional
    Enabled bool

    Source  TriggerSource // file | schedule (#592)
    Action  TriggerAction // shared vocabulary

    // File-source fields:
    SessionID       string   // whose worktree to watch (the source session)
    Paths           []string // optional include globs (worktree-relative)
    Ignore          []string // extra ignore globs
    RespectGitignore bool    // default true
    Debounce        string   // duration string; default 3s

    // Bookkeeping (not user-set):
    Origin      string // "config" | "scenario:<id>" | "cli"
    ReactorID   string // session this trigger currently owns (ensure-reviewer)
    lastFired   time.Time
}
```

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

- **Reconcile, don't poll.** On startup, on config reload (there's already a
  config watcher), on session create/stop/delete, and on `gr trigger` mutations,
  the loop diffs desired vs. actual watchers and adds/removes `fsnotify`
  watches. Each watcher watches its session's worktree recursively (walk the
  tree, `watcher.Add` each dir, add new dirs as `Create` events for them
  arrive — the standard recursive-fsnotify idiom).
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
| `ensure-reviewer` | `agent`, `prompt` | Idempotent sugar over `spawn`: message the owned reactor if live, else spawn one sharing the source worktree read-only. |
| `run` | `command`, `cwd` | Run a command in (or against) the worktree. Output delivered per `delivery` (inbox / store doc). |

The message/publish templates expand the coalesced event: `{session_name}`,
`{changed_files}`, `{change_count}`, `{worktree_path}` — reusing the existing
template-expansion convention (`{session_id}` etc. from MCP-server config).

`spawn` and `ensure-reviewer` overlap with #592's "start a scenario / spawn a
session" actions; that shared surface is the contract between the two issues.
This doc proposes it; #592 consumes the same set from its schedule source.

Actions that spawn or message are subject to the same **auth model** as the
equivalent human/agent operation. A daemon-fired action runs with daemon
authority, but a trigger's *origin* constrains it: a `cli`-origin trigger
created by a session inherits that session's authorization (it can't spawn a
reactor it couldn't have created itself), and only the orchestrator can install
triggers that spawn *scenarios* (matching `scenario_start`'s `SystemKind ==
orchestrator` check).

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
  The reactor shares it read-only. There is exactly one writer, so there is
  exactly one write stream to coalesce.
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
  through the sandbox is heavy. The quiescence approximation (Q4.2) plus
  read-only reviewers (Q4.1) covers the real cases without it.
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
- **Feedback-loop suppression** — assert an owned reactor's writes don't
  re-fire while quiescing, and the rate-limit gate trips and logs.
- **Duplicate avoidance** — `ensure-reviewer` messages when a tagged reactor is
  live, spawns when none is, spawns again after the reactor stops.
- **Reconciliation** — desired-vs-actual watcher diffing on session
  create/stop/delete and config reload; pruning of vanished sessions.
- **Auth** — a `cli`-origin trigger can't spawn what its creator couldn't;
  only the orchestrator installs scenario-spawning triggers.
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
2. **`ensure-reviewer` / `spawn`.** Adds ownership tagging and the idempotent
   spawn. This lights up the flagship "make the continuous reviewer automatic"
   case.
3. **`run` action + delivery to store/inbox.** Tests/lint on change.
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
