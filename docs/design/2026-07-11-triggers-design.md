---
title: "Design Doc: Unified Triggers (Scheduled + File-Watch Actions)"
authors: Dougal Matthews
created: 2026-07-11
status: Draft (merges #592 and #593; incorporates three review tribunals inc. a post-merge pass — see Consensus)
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/592, https://github.com/d0ugal/graith/issues/593
---

# Unified Triggers

> **Note on code references.** `file:line` citations are anchored to symbol
> names as of writing; if a line has drifted, search for the named function.

A **trigger** is `(source) → (action)`. This doc defines one framework covering
both source kinds the two open issues ask for:

- **[#592](https://github.com/d0ugal/graith/issues/592) — schedule source:**
  time-driven actions (cron / interval), e.g. "every morning at 09:00, produce a
  PR report".
- **[#593](https://github.com/d0ugal/graith/issues/593) — file-watch source:**
  event-driven actions, e.g. "when the implementer touches files, ensure a
  reviewer reacts".

Both share one action vocabulary, one delivery model, one executor, one state
machine, and one `gr trigger` CLI. **The daemon fires triggers directly**, so
they survive terminal close and need no attached orchestrator. This doc was
originally two separate design docs; they are merged here because everything
below the *source* line is identical for both, and keeping them apart produced
drift (a parallel action vocabulary, a config schema that named ephemeral
sessions). One framework, two sources.

## Background

graith coordination is entirely *driven* today — nothing happens unless a human
or an agent is actively pushing work through.

- **Nothing is time-driven.** There is no way to say "every morning at 09:00,
  produce a report of open PRs" or "every hour, sweep for review requests". These
  jobs live in a human's head or an external `crontab` that shells out to `gr`,
  outside graith's lifecycle, authorization, and observability.
- **Nothing is event-driven either.** graith's most valuable multi-agent
  pattern — the **continuous reviewer** — is set up by hand: an implementer works
  in a worktree and a second agent watches the same files and feeds review over
  messaging. Today that reviewer is spawned manually and left to notice changes
  on its own initiative:

  ```bash
  gr new reviewer --share-worktree implementer --background \
    --prompt "continuously review changes as they appear, send feedback via messages"
  ```

  Nothing in graith observes the files changing, so the reviewer polls and
  guesses — burning tokens on "anything changed yet?" turns and reacting late.

The daemon already runs several unattended background loops, and they are the
template this design follows:

- `RunPRWatchLoop` — polls each session's PR/CI state on a timer, diffs against a
  per-session cursor, debounces, rate-limits, and delivers notifications into the
  owning session's inbox (`internal/daemon/prwatch.go:82`).
- `RunGitPullLoop` — periodic `git pull` on eligible worktrees
  (`internal/daemon/gitpull.go:20`).
- `RunPurgeLoop` — a startup sweep plus a coarse ticker that hard-deletes expired
  soft-deleted sessions (`internal/daemon/daemon.go:3034`).
- `RunDetectionLoop`, `RunMessageCleanupLoop` — status detection and message GC.

All are launched as detached goroutines from daemon startup and cancelled via a
shared `context.Context` (`internal/daemon/daemon.go:5790`):

```go
go sm.RunDetectionLoop(ctx)
go sm.RunMessageCleanupLoop(ctx)
go sm.RunGitPullLoop(ctx)
go sm.RunPRWatchLoop(ctx)
go sm.RunPurgeLoop(ctx)
```

The daemon also already knows how to *author an action itself* and deliver it:
`notifyFromDaemon(sessionID, body)` publishes a message into a session's inbox as
the synthetic `graith:system` sender and **auto-resumes the session if it was
stopped** (`internal/daemon/notify.go:73`). Session creation, scenario start, and
messaging are all callable in-process from the `SessionManager`. Filesystem
watching is available too: `internal/config/watcher.go` wraps
`github.com/fsnotify/fsnotify` (a direct dependency) to watch `config.toml` with a
quiet-window debounce timer. In other words, every capability a trigger needs to
*do something* and to *observe a change* already exists — what's missing is the
thing that fires on a clock or a file event and routes to a shared executor.

## Problem

1. **No time-driven automation.** Recurring work — daily PR reports, hourly
   review sweeps, nightly housekeeping — has no home inside graith's lifecycle,
   authorization, and observability.

2. **No event-driven automation.** The continuous-reviewer pattern relies on the
   reviewer agent noticing changes on its own. There is no real signal, so the
   reviewer polls wastefully or reacts late, and the wiring lives in a prompt and
   dies with the session — not reproducible.

3. **External cron can't see graith.** A system `crontab` running `gr new ...`
   has no access to the orchestrator identity, the session tree, the sandbox
   policy, or the messaging fabric. It can't deliver into an inbox, can't be
   listed by `gr`, and can't be paused when graith is idle.

4. **Two sources want one vocabulary.** Time-driven and event-driven triggers
   both want to "run an action". Without a shared model they grow their own
   ad-hoc dispatch, doubling the surface and drifting apart — which is exactly
   what happened when these were two docs.

5. **Naive firing is a footgun.** A bare `time.Ticker` fires N times to "catch
   up" after downtime and happily starts a second run while the first is going. A
   bare file-watch re-fires on the reactor's own writes and on every intermediate
   save. Both need explicit missed-run / overlap / debounce / feedback-loop
   semantics.

## Goals

- A trigger the daemon fires on its own, surviving terminal close and daemon
  restart, from **either** a schedule **or** a file event.
- **Schedule source:** both cron expressions and simple intervals.
- **File-watch source:** react to worktree changes, debounced and coalesced,
  without feedback loops or duplicate reactors.
- A **shared action vocabulary** covering the issues' candidates: run a command,
  start a scenario, spawn a session, send a message, produce a report.
- **Delivery** of action output into the inbox, a store doc, or a topic — reusing
  `notifyFromDaemon` so a stopped orchestrator is auto-resumed.
- Explicit, safe defaults for **missed runs** (don't backfill a burst),
  **overlap** (skip if a previous run is in flight), and **debounce** (coalesce a
  burst of writes into one fire).
- Observability and manual control via a `gr trigger` CLI (list, status, run now,
  pause/resume).

### Non-Goals

- **Cross-machine scheduling / distributed HA** — triggers run on the single
  local daemon, one clock, no leader election.
- **Sub-second precision** — a coarse tick (seconds) is enough; this is not a
  real-time job runner, and file-watch debounce windows are seconds.
- **Arbitrary DAGs / job dependencies** — a trigger fires one action. Chaining is
  expressed by the action itself (a spawned session that starts a scenario), not
  by the framework. No boolean combinators, no "fire only if CI is also green".
- **CLI-authored triggers in v1** (`gr trigger add` writing new triggers) — v1
  defines triggers in `config.toml` / scenario TOML; the CLI observes and
  controls them. See Open Questions.
- **Watching arbitrary paths outside a worktree** — v1 watches session worktrees
  only.
- **Replacing `gr wait`** — that blocks a *client* on a session's *output*; this
  watches the *filesystem* (or clock) and drives *daemon* actions.

## The unified trigger model

A trigger has three parts: a **source** (what makes it fire), an **action** (what
runs), and a **policy** (missed-run / overlap behaviour). Everything except the
source is identical for both source kinds.

```
        source                 action                 delivery
   ┌──────────────┐      ┌──────────────────┐    ┌────────────────┐
   │  schedule    │      │  command         │    │  inbox         │
   │  (#592, cron │─────▶│  session         │───▶│  topic         │
   │   or every)  │      │  scenario        │    │  store doc     │
   │              │      │  message         │    │  (daemon log)  │
   │  watch       │      └──────────────────┘    └────────────────┘
   │  (#593, glob)│
   └──────────────┘         + policy: catch_up, overlap, debounce
```

The Go model lives in `internal/config` (definition) and a new
`internal/daemon/trigger.go` (runtime):

```go
// config.TriggerConfig — one [[trigger]] block.
type TriggerConfig struct {
    Name     string          `toml:"name"`
    Enabled  *bool           `toml:"enabled"`  // pointer: nil ⇒ default true (see note)
    Schedule *ScheduleConfig `toml:"schedule"` // #592 source
    Watch    *WatchConfig    `toml:"watch"`    // #593 source
    Action   ActionConfig    `toml:"action"`
    Policy   TriggerPolicy   `toml:"policy"`
}

type ScheduleConfig struct {
    Cron     string `toml:"cron"`     // e.g. "0 9 * * *", or a descriptor "@daily"
    Every    string `toml:"every"`    // Go duration, e.g. "15m", "1h30m"
    Timezone string `toml:"timezone"` // IANA name; default = daemon local time
}

type WatchConfig struct {
    // Target selection — a POLICY selector, never a live session name in config.
    Repo string `toml:"repo"` // bind to sessions on this repo (like [pr_watch])
    Role string `toml:"role"` // bind to sessions with this scenario role

    Paths    []string `toml:"paths"`    // optional include globs (worktree-relative)
    Ignore   []string `toml:"ignore"`   // extra ignore globs (added to built-ins + .gitignore)
    Debounce string   `toml:"debounce"` // quiet-window; default 3s
    // No respect_gitignore flag — .gitignore is always honoured (§Watch scope).
}

type ActionConfig struct {
    Type     string `toml:"type"` // command | session | scenario | message
    // command:
    Command  string `toml:"command"`
    Repo     string `toml:"repo"`     // REQUIRED for a schedule source; for a watch source
                                       // the execution root is the bound session's worktree
                                       // (repo is rejected) — see §command trust boundary
    Timeout  string `toml:"timeout"`  // command: max run time; default 5m
    Mutating bool   `toml:"mutating"` // command: may write its execution root; REJECTED in v1
                                       // (watch commands are read-only in v1) — see §Feedback loops
    Network  bool   `toml:"network"`  // command: allow network egress (default: blocked)
    Trusted  bool   `toml:"trusted"`  // command: run unconfined (no sandbox) — explicit opt-in
    // session:
    Prompt   string `toml:"prompt"`
    Agent    string `toml:"agent"`
    Model    string `toml:"model"`
    Ensure   bool   `toml:"ensure"`   // session: idempotent "ensure-reviewer" (watch); see §Duplicate avoidance
    // scenario:
    Scenario string `toml:"scenario"`
    // message:
    Body     string `toml:"body"`

    Deliver  DeliverConfig `toml:"deliver"`
}

type DeliverConfig struct {
    Inbox string `toml:"inbox"` // session name, "orchestrator", or a template like "{session_name}"
    Topic string `toml:"topic"` // pub/sub topic
    Store string `toml:"store"` // store key, templated (see Delivery)
    Wake  bool   `toml:"wake"`  // resume a non-orchestrator stopped inbox target
}

type TriggerPolicy struct {
    CatchUp   bool   `toml:"catch_up"`  // default false: never backfill missed fires
    Overlap   string `toml:"overlap"`   // skip (default) | allow | queue(v2)
    RateLimit string `toml:"rate_limit"`// max fires per window; default "5/30m" — the feedback backstop
}
```

The daemon-wide concurrency cap is a **separate top-level table**, not a key on
the `[[trigger]]` array (TOML forbids the same key being both an array-of-tables
and a table): `[triggers] max_concurrent = 4`.

**`message` has an explicit destination via `deliver`.** A `message` action's
`body` is routed by its `[trigger.action.deliver]` `inbox`/`topic` (exactly one
required) — `deliver` is **not** rejected for `message` (an earlier draft's
"deliver rejected on message" rule was a half-merge that left `message` with no
destination). `store` is not valid for `message` (there's no captured output
beyond the body, which `store` can hold — so `store` *is* allowed as an
additional sink; only the "no destination at all" case is rejected).

**Exactly one of `Schedule` / `Watch` must be set** per trigger (validation
rejects both or neither). Both are **pointers** so an omitted source and an empty
`[trigger.schedule]`/`[trigger.watch]` block are distinguishable after TOML
decode — a value struct would make "no source" and "empty source"
indistinguishable.

`Enabled` is a `*bool` for the same reason: a plain `bool` decodes an absent key
as `false`, which would default every trigger to *disabled*. `nil` means "unset ⇒
default enabled"; an explicit `enabled = false` disables. `Enabled` is a static
config switch (the trigger is inert, never scheduled); `Paused` (runtime state,
below) is a toggle via `gr trigger pause`. **Precedence: `enabled = false` always
wins** — a paused-then-config-disabled trigger stays off, and `gr trigger resume`
on a config-disabled trigger is rejected with a clear error. Changing a trigger's
definition (see fingerprint, State model) resets its `Paused` flag, run count,
and cursor.

## Proposals

### Proposal 0: Do Nothing

Users wire recurring `gr` invocations through the system `crontab`/`launchd`, and
keep spawning continuous reviewers by hand.

**Pros:** zero implementation cost; leverages a battle-tested scheduler.

**Cons:** the external scheduler has no graith identity — it can't act as the
orchestrator, can't deliver into an inbox, can't auto-resume a stopped session,
isn't visible to `gr list`, and isn't paused when graith is idle. The manual
reviewer keeps reacting late and burning tokens, and the wiring stays trapped in
prompts. Neither half is unified with the other. This is the status quo the
issues reject.

### Proposal 1: Daemon-fired triggers, defined in config, controlled by `gr trigger` (Recommended)

Triggers are declared as `[[trigger]]` blocks in `config.toml` (the same file
that already configures `[pr_watch]`, `[git_pull]`, `[delete]`, `[orchestrator]`)
and in scenario TOML. Two daemon-owned sources feed one executor: `RunTriggerLoop`
evaluates schedules against the wall clock, and `RunFileWatchLoop` feeds an
`fsnotify`-backed, debounced channel; both call the same `fireTrigger`. A `gr
trigger` CLI surfaces status and offers manual control. This mirrors graith's
existing config-driven background loops while adding scheduler ergonomics.

This is the recommended proposal; the rest of the doc specifies it.

### Proposal 2: Daemon state + full CRUD CLI (`gr trigger add/rm/edit`)

Triggers live only in daemon state, authored exclusively through `gr trigger
add`.

**Pros:** fully dynamic; an agent can create a trigger at runtime; no config
reload needed. It is also the *only* clean way to target a specific already-live
session by name (which the file-watch source wants — see §Opt-in granularity).

**Cons:** triggers become invisible mutable state rather than reviewable,
version-controllable declarations, and it hands agents a durable self-scheduling
primitive with no reviewable artifact — a meaningful authorization concern.
Deferred: v1 uses config as the source of truth; a future `gr trigger add` can be
layered on once the model has proven out. See Open Questions.

## How it works (shared framework)

Sections 1–7 are shared by both sources. The source-specific details follow in
**Source: schedule** and **Source: file-watch**.

### 1. Action vocabulary (v1 scope)

The issues list command, scenario, session, message, and report. v1 `action.type`
values — **command, session, scenario, message** — with **report modelled as a
composition** rather than a distinct type:

| Type | What it does | In-process call |
|------|--------------|-----------------|
| `command` | Run a command, capture stdout/stderr, deliver the output. Execution root: `repo` (schedule) or the bound session's worktree (watch). | new `runCommandAction` under a dedicated command-action sandbox profile (below) — **not** the bare `sh -c` of `sendNotification` (`notify.go:255`), which is unsandboxed and not a security model |
| `session` | Spawn a session with `prompt`/`agent`/`model` in `repo` (or the bound worktree for a watch `ensure`), parented to the orchestrator so it's addressable and lifecycle-managed. | `sm.Create(...)` — see call-shape note |
| `scenario` | Start a named scenario from `~/.config/graith/scenarios/`. | a shared scenario loader/start service — **not** `sm.StartScenario` directly; see note |
| `message` | Route a fixed `body` to an inbox and/or topic via `deliver`. | `notifyFromDaemon`/bare `Publish` (inbox, per `wake`) / `messages.Publish` (topic) |

**`session` call shape.** `Create` (`daemon.go:502`) is a function with **17
fixed positional parameters** (ending `... rows, cols uint16`) plus a variadic
`envExtra ...map[string]string` — there is no `Background` flag (sessions are
inherently PTY-backed and detached; "background" is a client-attachment concern),
and no client to source `rows, cols` from, so trigger-spawned sessions use
**default headless dimensions of 80×24** (a later real resize supersedes them),
with `parentID` = the resolved orchestrator session ID.

Because `Create` already generates and durably reserves its own session ID under
`sm.mu`, and its fixed positional signature has **no slot for a trigger tag**,
the `TriggerID`/`TriggerReactor` markers (State model) **cannot** be bolted on
after `Create` returns (that leaves a crash window where a reactor exists
un-tagged and a second fire would spawn a duplicate). This design therefore
requires a small **create-options refactor** — a `CreateOptions` struct (or a
dedicated `createTaggedSession` entry point) that installs the trigger tags in the
*same* durable placeholder `Create` already reserves. This is a prerequisite for
the atomic reactor reservation in §Duplicate avoidance, not an afterthought.

**`scenario` dispatch is not a straight `sm.StartScenario` call.** Two obstacles,
both confirmed against the code:
- **All scenario-TOML parsing lives in the CLI** (`internal/cli/scenario.go`);
  the daemon has no "load scenario by name from disk" path. This must be extracted
  into a shared, daemon-reachable loader so both the CLI and the trigger executor
  can build a `protocol.ScenarioStartMsg`.
- **`StartScenario` requires an authenticated orchestrator caller** — it sets
  `CallerSessionID = auth.sessionID` (`handler.go:1436`) and rejects any caller
  whose `SystemKind != SystemKindOrchestrator`. A daemon-fired action has no
  caller. The executor must resolve the **currently live** orchestrator session ID
  as `CallerSessionID` (or a small internal trusted entry point). If no
  orchestrator exists at fire time, the `scenario` action is skipped with a logged
  error.

A `scenario` action referencing a name with **no file** in
`~/.config/graith/scenarios/` is a **runtime** error (logged in `LastError`), not
a config-load rejection — scenario files change independently of `config.toml`.

**"Report" is not a fourth verb** — a report is a `session` (or `command`) action
whose output is routed by `[trigger.action.deliver]`. Folding report into delivery
keeps the vocabulary orthogonal: *what runs* is independent of *where output
goes*.

**The file-watch convenience: `ensure = true` on a `session` action.** This is the
"ensure a reviewer reacts" behaviour the #593 motivation asks for — idempotent
sugar over `session`, not a new verb. On fire, message the reactor this trigger
already owns if it exists (running **or** stopped — messaging auto-resumes a
stopped one), else spawn one sharing the source worktree read-only and tag it. The
dedup/reservation rules are in §Duplicate avoidance. It is only meaningful for a
`watch` source (a schedule has no "source session" to attach a reactor to).

**Ownership & authorization of fired actions.** Fired actions run with **daemon
authority** — there is no authenticated caller. Concretely:

- `message` actions are authored as the `graith:system` sender (`notify.go:56`) —
  non-replyable and clearly automated.
- `session` and `scenario` actions are **parented to the orchestrator session**
  (`SystemKindOrchestrator`), so they appear in the session tree, are addressable
  by `gr msg send --children`, and are torn down by normal lifecycle rules. If the
  orchestrator is disabled in config, `session`/`scenario` actions are rejected at
  config-validation time (they need an owner). A `watch`-source `session`
  reactor with `ensure = true` additionally shares the source worktree read-only.
- `command` actions have the weakest natural confinement and need an **explicit
  contract** (below).

This keeps scheduled and watched *session/scenario/message* work inside the same
trust boundary as everything else. There is deliberately **no** separate
per-trigger `CreatorID`/authorized-targets scheme — an earlier #593 draft invented
one before this shared model existed; the daemon-authority + orchestrator-parenting
model above subsumes it.

#### `command` action trust boundary (explicit, fail-closed)

The daemon executes `command` actions with **its own environment and
privileges**, and — unlike a session — a command has no agent identity to merge
sandbox config from and no session/worktree to key a nono profile on. So it is
explicit, not "same as agents":

- **Execution root differs by source.** A **schedule** `command` requires
  `action.repo` and runs rooted there. A **watch** `command` runs rooted in the
  **bound session's worktree** (the whole point — it must test *the change that
  fired it*, not the canonical checkout); `action.repo` is **rejected** for a
  watch command, and the sandbox profile is worktree-rooted, not repo-rooted. The
  validation and the sandbox scoping both key off the source kind.
- **v1 watch commands are enforceably read-only.** "Non-mutating" is not a
  detectable property, so it is *enforced*, not assumed: a v1 watch `command`
  mounts the bound worktree in the sandbox **`ReadDirs`** (read-only) and runs
  with a **separate writable scratch cwd** for any output/cache — the exact same
  read-only-source + writable-scratch shape the `ensure` reactor uses. A command
  that tries to write the worktree fails at the sandbox, and — decisively — it
  then **cannot enter the watched write stream at all**, collapsing feedback-loop
  layer 2 into layer 1 (§Feedback loops). A **mutating** watch command
  (`action.mutating = true`, writable worktree) is **rejected in v1** and deferred
  to v2 with the generation-discard machinery; only then is generation-discard the
  (best-effort, bounded) defence.
- **A dedicated command-action sandbox profile.** Reuse the `internal/sandbox`
  backend (`Wrap`) with a purpose-built scope: read+write on the writable scratch
  (schedule: `repo` root; watch: scratch cwd), the worktree in `ReadDirs` for a
  watch command, a minimal env allowlist (`PATH`/`HOME`/`GRAITH_*` only, no
  inherited secrets), and network **explicitly blocked by default** — note
  `WrapOpts.Network == nil` means *unrestricted* (`sandbox.go`), so the profile
  must construct an explicit blocking policy unless `action.network = true`.
- **Fail closed on no enforcement.** A `command` trigger must **not** silently
  fall through to unsandboxed execution: either the sandbox is enabled and the
  backend can enforce (else the trigger is rejected at validation), or the operator
  sets an explicit `action.trusted = true` acknowledging unconfined daemon-user
  code. There is no implicit unconfined path.
- **Bounds.** `action.timeout` (default **5m**) with context cancellation, an
  output cap (`prCommentMaxBody`-style, `prwatch.go:34`), process-group
  termination on daemon shutdown, and per-trigger (per-binding, for a watch
  source) serialisation of concurrent runs.
- **Validation.** A schedule `command` with no `repo`, or a `repo` outside a
  configured `allowed_repo_paths`, is rejected at config load. Note
  `RepoPathAllowed` returns `true` when `allowed_repo_paths` is **empty**
  (`config.go`), so the repo allow-list only constrains when the operator has set
  one; with it unset, a schedule `command` may target any repo (still subject to
  the sandbox/`trusted` gate). The "fail-closed" guarantee is the *sandbox*, not
  the repo allow-list.

`notify.go:255` (`exec.Command("sh", "-c", command)` with inherited env) is
**not** the model — it is cited only for the mechanical exec pattern.

### 2. Delivery

`[trigger.action.deliver]` routes output. Any combination may be set; each is
best-effort and independent (a store-write failure doesn't suppress the inbox
message):

- `inbox = "orchestrator"` (a named session, or a template like `{session_name}`
  that resolves to the watch trigger's bound session). **The executor selects the
  delivery primitive by target and `wake`, because `notifyFromDaemon` cannot gate
  resume on its own** — `notifyFromDaemon` → `notifyInbox` → `resumeForInbox`
  **unconditionally** resumes any `StatusStopped` target and has **no
  soft-delete guard** (`notify.go`). So:
  - `inbox = "orchestrator"`, or any target with `wake = true` → use
    `notifyFromDaemon` (publish **and** auto-resume). A report waking the
    orchestrator is intended.
  - any other named session with `wake = false` (default) → use a **bare
    `messages.Publish`** (deliver to the inbox, do **not** resume). A timer or a
    file change silently restarting a paused-for-a-reason agent is not intended.
  - in **all** cases the executor adds its own **soft-delete guard**
    (`IsSoftDeleted`) before delivering — a soft-deleted session is a hidden
    `stopped` session, and `notifyFromDaemon` would otherwise wake it. The helper
    does not provide this guard, so delivery must.
  - `"orchestrator"` resolves to the current `SystemKindOrchestrator` session.
- `topic = "pr-reports"` — publish to a pub/sub topic via `messages.Publish`. No
  PTY notification (subscribers pull).
- `store = "reports/pr/{date}.md"` — write a durable store doc via `store.Put`
  (`internal/store/store.go:202`), or `store.Append` for `.jsonl` logs
  (`store.go:237`), after an idempotent `store.Init` (`store.go:26`). The key is
  templated (below) and must pass `ValidateKey` (`store.go:90`). Store scoping: a
  key targets the **repo-scoped** store only when the action has a `repo`
  (`command`/`session`); `scenario`/`message` actions have no single repo and
  **require** a `shared:` prefix.

For a `session` action, "delivery" is subtly different — the daemon can't capture
a long-running agent's final answer synchronously:

- If `deliver` is set, the daemon **injects the delivery instruction into the
  prompt** (with `{date}` etc. already expanded), and the agent performs delivery
  using its own token-authenticated `gr` access. This is **best-effort and
  unverified**; `gr trigger status` reports "spawned OK", not "report delivered".
- If `deliver` is unset, the spawned session just runs; its work product is
  whatever it commits/pushes/messages.

**Per-action delivery validity** is enforced: `command` delivers captured output
(all sinks valid); `session` delivery is the best-effort prompt injection;
`message` **requires** a `deliver` with at least an `inbox` or `topic` (the `body`
is the payload, `deliver` is its destination — see the model note); a `scenario`
action produces no single output, so a `deliver` block on it is rejected.

**Template variables** in `deliver.inbox`/`deliver.topic`/`deliver.store`,
`message.body`, and the injected `session` delivery instruction: `{name}`
(trigger), `{date}`, `{datetime}` (RFC3339), `{fire_time}`, and — for a `watch`
source — `{session_name}`, `{worktree_path}`, `{changed_files}`, `{change_count}`.
`deliver.inbox` is templatable specifically so a repo/role-bound watch trigger can
route output back to its bound session with `inbox = "{session_name}"` — without
that, a policy trigger matching a runtime-discovered session has no way to name it.
These need a **new, trigger-specific expander**: the existing
`config.TemplateVars`/`Expand` (`internal/config/template.go`) is a *fixed struct*
whose `Expand` **errors on any unknown variable name**, so these tokens can't pass
through it. Follow its style (same `{token}` syntax, same unknown-token-is-error
discipline) with a distinct variable map, computed at fire time.

### 3. Missed-run, concurrency, and debounce policy

`[trigger.policy]` plus (for the watch source) `watch.debounce`, with safe
defaults. These semantics only hold up if a *fire* is a durable, identifiable
event rather than an in-memory tick, so the design rests on a small **run-state
machine** (State model) with two persisted facts per trigger:

- `LastScheduledFireAt` — the scheduled instant of the last fire the daemon
  **committed to** (not when the action finished), written **atomically before
  dispatch** inside the same critical section that advances the cursor. This gives
  an **at-most-once** guarantee per scheduled instant: a crash after committing
  but before/during the action does **not** replay it on restart. (At-least-once
  is not offered — actions aren't assumed idempotent.)
- `Fingerprint` — a hash of `source` + `action` + `policy`. A config edit that
  keeps the same `name` but changes any of these **resets** the cursor,
  `LastScheduledFireAt`, `Paused`, and counters — closing the name-only-diff hole.

**Missed runs — `catch_up` (default `false`, schedule source).** When the daemon
was down across scheduled fires:
- `catch_up = false`: **do not backfill**. Compute the next fire fresh and fire
  only on future ticks. A daemon down for three days does not fire three daily
  reports on boot.
- `catch_up = true`: if the most recent scheduled fire is now past, fire **once**
  immediately on startup, then resume. Never replay a backlog — mirrors
  `RunPurgeLoop`'s "one sweep shortly after startup" (`daemon.go:3031`).

**Overlap — `overlap` (default `"skip"`).** When a fire is due but the previous
run of the *same trigger* is still in flight:
- `overlap = "skip"` (default): skip, log, advance.
- `overlap = "allow"`: fire regardless.
- `overlap = "queue"` (**deferred to v2**): a single-slot coalesced defer, the
  fiddliest to get right under `-race`; `queue` in config is rejected in v1.

**What "in flight" means, and restart-safety.** For a `command` action, in-flight
is a per-trigger flag under `triggerState.mu`; commands don't survive a restart,
so an in-memory flag suffices. `session`/`scenario` actions outlive the daemon, so
v1 uses **executor-call overlap**: the action is "complete once creation
succeeds" — the in-flight window is just the `Create`/scenario-start call, not the
lifetime of the spawned work. This is honest and race-free without persistence; a
durable active-run + startup-reconciliation model (true lifetime overlap) is
documented as v2.

**Global concurrency cap.** Per-trigger `skip` doesn't bound *aggregate* load —
many triggers can come due on the same tick (or many watchers fire at once). A
daemon-wide cap (`[triggers] max_concurrent`, default e.g. 4 — a `[triggers]`
table, distinct from the `[[trigger]]` array) bounds simultaneously
running action goroutines; fires that would exceed it are treated as `skip`
(logged), never queued unboundedly.

**Debounce (watch source).** `watch.debounce` (default 3s) is the quiet-window
coalescer: the watcher fires only after the worktree has been quiet for the
window, so a multi-file edit or formatter pass produces one fire. This is the
`config/watcher.go` model, and combined with `overlap = "skip"` it is the first
line against re-trigger storms (the issue's "feedback loops" concern; the full
answer is §Feedback loops).

### 4. The firing loops

Both sources funnel into one executor. Two loops, launched from daemon startup
(`daemon.go:5790`):

```go
go sm.RunTriggerLoop(ctx)     // schedule source (#592)
go sm.RunFileWatchLoop(ctx)   // watch source (#593)
```

**Schedule loop**, modeled on `RunPRWatchLoop`/`RunPurgeLoop`:

```go
const triggerTick = 1 * time.Second // coarse; cron granularity is 1 minute

func (sm *SessionManager) RunTriggerLoop(ctx context.Context) {
    sm.initTriggerSchedules() // parse specs, compute first Next(), handle catch_up
    ticker := time.NewTicker(triggerTick)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case now := <-ticker.C:
            sm.runTriggerTick(ctx, now)
        }
    }
}
```

`dueTriggers` (under `triggerState.mu`) atomically selects due triggers, advances
each cursor + records `LastScheduledFireAt`, and marks in-flight — all before
returning — then `runTriggerTick` spawns `go sm.fireTrigger(ctx, due)`. The
synchronous cursor advance is essential: with a 1s tick, an un-advanced cursor
would re-match `now >= nextFire` every tick and double-fire. A 1s tick with
1-minute cron granularity guarantees we never miss a minute boundary; the tick
does almost nothing when nothing is due.

**Watch loop** reconciles the set of *active watch triggers* against live
`fsnotify` watchers and feeds a debounced channel into the same `fireTrigger`.
Its source-specific mechanics (recursion, races, watch-set pruning, degradation)
are in §Source: file-watch.

**Locking discipline** (identical philosophy to prwatch): the loops share a mutex
`triggerState.mu`, independent of `sm.mu`. Definition cursors, per-binding
in-flight flags, watcher bookkeeping, per-worktree generation counters, and pause
state live under `triggerState.mu` — including each binding's `ReactorID`. Action
dispatch (`sm.Create`, scenario-start, sandboxed command exec, `store.Put`) runs
in a detached goroutine holding neither lock, so a slow action never blocks
`gr list`. State snapshots follow `RLock → copy → unlock → work` (`prwatch.go:150`).

**Lock ordering (the one cross-lock path).** The `ensure` reactor reservation
(§Duplicate avoidance) must be atomic *with respect to* `sm.Create`'s own
under-`sm.mu` reservation, so it takes both locks. The fixed order is
**`triggerState.mu` → `sm.mu`** (never the reverse): under `triggerState.mu` the
executor checks/sets the binding's `ReactorID` placeholder, then acquires `sm.mu`
(via the create-options path) to durably reserve the session, then releases both.
Every other trigger path takes only `triggerState.mu`. Stating the order keeps the
`-race`-clean claim achievable rather than aspirational.

**At-most-once requires a durable save, not just a mutex.** "Write
`LastScheduledFireAt`/`NextScheduledFireAt` before dispatch" means a **successful
durable `saveState`** before the action goroutine launches — advancing the cursor
in memory and dispatching after a *failed* save would break the restart guarantee.
So: persist the cursor+fire record successfully → then dispatch; if the save fails,
do **not** dispatch, record `LastError`/degraded, and retry persistence without
treating the instant as committed.

### 5. State model — definition vs. binding

The load-bearing correction from review: **a watch policy trigger (selected by
`repo`/`role`) can bind to *many* live sessions at once** (two implementers on the
same repo; the same role in two scenarios). So runtime state splits into two
levels — a per-**definition** row and a per-**binding** row — rather than one row
per trigger name (which could only ever hold one `ReactorID`, one in-flight flag,
one debounce state, and would break the moment a policy matched two sessions).

```go
// Per-DEFINITION — one per [[trigger]] block. Schedule triggers use only this.
type TriggerRuntimeState struct {
    Name                string      `json:"name"`         // namespaced: "config:<name>" | "scenario:<id>:<name>"
    Fingerprint         string      `json:"fingerprint"`  // canonical hash(source+action+policy)
    Paused              bool        `json:"paused,omitempty"`
    ActivatedAt         *time.Time  `json:"activated_at,omitempty"`          // first reconcile — interval phase anchor
    LastScheduledFireAt *time.Time  `json:"last_scheduled_fire_at,omitempty"`// schedule: at-most-once anchor
    NextScheduledFireAt *time.Time  `json:"next_scheduled_fire_at,omitempty"`// schedule: restart-stable cursor
    LastError           string      `json:"last_error,omitempty"`
    RunCount            int         `json:"run_count,omitempty"`
    History             []RunRecord `json:"history,omitempty"`               // bounded ring (last ~20)
}

// Per-BINDING — one per (definition, bound source session). Watch triggers only.
// Keyed by (Name, SourceSessionID). NOT persisted: rebuilt from live sessions on
// start/reload (a binding is meaningless without its live session).
type TriggerBinding struct {
    Name            string    // owning definition (namespaced)
    SourceSessionID string    // the bound session (resolved from repo/role)
    ReactorID       string    // this binding's ensure-reviewer session (or reservation placeholder)
    InFlight        bool      // this binding's overlap guard
    Degraded        string    // this binding's watcher failure/limit reason
    // debounce timer, per-worktree generation counter, changed-path batch — in-memory
}

type RunRecord struct {
    ScheduledAt     time.Time `json:"scheduled_at"`
    SourceSessionID string    `json:"source_session_id,omitempty"` // which binding fired (watch)
    Cause           string    `json:"cause"`  // "schedule" | "catch_up" | "manual" | "file"
    Result          string    `json:"result"` // per action type
}
```

- **Interval anchoring is now restart-stable.** `ActivatedAt` (persisted at first
  reconcile) is the interval phase anchor and `NextScheduledFireAt` is the
  persisted cursor, so a daemon restarted more often than a long interval — even
  *before* the first fire — cannot keep choosing a new "first-seen" time and starve
  the trigger (a real gap in the earlier single-`LastScheduledFireAt` model).
- **Per-binding isolation.** Each binding has its own `ReactorID`, in-flight guard,
  debounce, generation counter, and degraded status — so `overlap = "skip"` on
  worktree A never suppresses worktree B, one reactor is never reused across
  unrelated sources, and one degraded watcher doesn't mark the whole policy down.
  The atomic reservation (§Duplicate avoidance) reserves a real placeholder session
  ID into *this binding's* `ReactorID`, keyed by `(Name, SourceSessionID)`.
- **Naming/namespacing.** Definition names are namespaced by origin
  (`config:<name>`, `scenario:<id>:<name>`) so two scenario instances of the same
  file, or a config and scenario trigger sharing a `name`, don't collide.
- `Result` is per action type: `command` → exit code + truncated output; `session`
  → "spawned `<id>`" (**not** "delivered"); `scenario` → "started N sessions";
  `message` → "published". `History` is a bounded ring (last ~20). The
  **definition** (source/action/policy) is *not* persisted — it lives in config /
  scenario TOML and is the source of truth; only mutable runtime facts persist.
  Definition rows for triggers no longer declared are pruned on load (like
  `prunePRWatchState`); binding rows are rebuilt from live sessions. Derived data
  (parsed `cron.Schedule`, `fsnotify` handles, timers) is in-memory in a
  `triggerState` struct. A state migration bumps `CurrentStateVersion` (no-op —
  the maps default empty).

Sessions also gain a marker so a watch trigger's spawned reactor can be found
idempotently (mirroring the `ScenarioID` fields on `SessionState`):

```go
TriggerID      string `json:"trigger_id,omitempty"`      // trigger that spawned this session
TriggerReactor bool   `json:"trigger_reactor,omitempty"` // this is a trigger-owned reactor
```

### 6. Protocol, handler, authorization

New control messages in `internal/protocol/messages.go`, following the
`ScenarioListMsg`/`ScenarioStatusMsg` shapes:

```go
type TriggerListMsg   struct{}
type TriggerStatusMsg struct{ Name string `json:"name"` }
type TriggerRunMsg     struct{ Name string `json:"name"` }
type TriggerPauseMsg   struct{ Name string `json:"name"`; Pause bool `json:"pause"` }
```

Handler cases in `internal/daemon/handler.go` next to the scenario cases, decoded
with `decodePayload[T]`.

**Authorization.** `list`/`status` are read-only, available to any session or the
human CLI (like `scenario_status`). `run`/`pause`/`resume` are mutating. A
config-defined trigger has **no per-trigger owner** — the daemon owns it — so the
rule (a new `authorizeTriggerOp`, not a reuse of the per-scenario check) is: the
caller must be the **system orchestrator session or a descendant** (`isDescendantOf`
against the current `SystemKindOrchestrator` ID). Unauthenticated human-CLI callers
are always permitted, same posture as scenario ops. This is a **broad, durable
privilege**: any descendant of the orchestrator can `pause` any trigger (persists
across restart), so an agent can durably disable a config-defined trigger. Accepted
for v1 because creation still requires editing config (agents can't *author*
self-scheduling triggers) and the same trust model governs scenario stop/delete; a
future `allow_agent_control = false` per-trigger flag could lock a sensitive one to
human-only control. The new message types are added to the remote allow-matrix
(`remoteAllowed`, `authmatrix.go:121`) — read-only allowed, mutating gated —
failing closed if omitted.

## Source: schedule (#592)

The time-driven source. Answers the #592 open questions.

### Schedule syntax: both cron and intervals

Selected by which field is set on `[trigger.schedule]`:

- `every = "15m"` — parsed by `config.ParseDurationWithDays`
  (`internal/config/config.go:386`; supports `"7d"` day suffixes). **Anchored to
  the persisted `LastScheduledFireAt`**, falling back to first-seen time: `nextFire
  = LastScheduledFireAt + N`, computed once on load, so a daemon restarted more
  often than the interval doesn't reset the phase and starve a long-interval
  trigger. `every` must be `> 0` (validated). Best for "roughly every N"
  housekeeping.
- `cron = "0 9 * * *"` — 5-field cron (minute hour dom month dow) plus `@hourly`,
  `@daily`, `@weekly`, `@monthly`. Best for wall-clock-anchored jobs.
- `timezone = "Europe/London"` — optional IANA zone for cron; defaults to the
  daemon's local time. DST handled by the library's zone-aware `Next()`.

Exactly one of `cron`/`every` is required; validation rejects both-set,
neither-set, and unparseable values at load, naming the offending trigger
(fail-closed, same posture as sandbox config). `timezone` is only meaningful with
`cron` — setting it alongside `every` is a load error (not silently ignored),
consistent with the fail-closed posture.

**Time edge cases.** Cron `Next()` handles DST gaps (spring-forward runs at the
next valid instant) and folds (fall-back fires once). Standard cron DOM/DOW union
semantics apply (robfig's documented behaviour). A wall-clock jump (NTP, sleep/wake)
is tolerated because each tick recomputes `now >= nextFire` against the current
clock; a large backward jump could re-arm a just-fired entry, which the
at-most-once guard suppresses.

**Library choice.** Vendor **`github.com/robfig/cron/v3`** — the de-facto Go cron
library. We use only its parser + pure `Schedule.Next(time.Time)`; the firing
loop, policy, and dispatch are ours (so missed-run/overlap semantics are under our
control, not the library's runner). Intervals use `time.ParseDuration` directly.

### Config example (schedule)

```toml
[[trigger]]
name = "daily-pr-report"

[trigger.schedule]
cron     = "0 9 * * *"
timezone = "Europe/London"

[trigger.action]
type   = "session"
prompt = "Summarise all open PRs across repos graith, service-a, and service-b. For each: title, author, CI state, review state, age."
repo   = "~/Code/graith"
agent  = "claude"

[trigger.action.deliver]
inbox = "orchestrator"
store = "reports/pr/{date}.md"

[trigger.policy]
catch_up = false
overlap  = "skip"
```

`gr trigger run` is an **out-of-band manual fire**: it records a `RunRecord` with
`Cause = "manual"`, bumps `RunCount`, and respects `overlap`, but does **not**
touch `LastScheduledFireAt` or `nextFire` — a manual 15:00 run of a 09:00-daily
trigger still fires at 09:00 tomorrow.

## Source: file-watch (#593)

The event-driven source. `RunFileWatchLoop` watches a session worktree, coalesces
a burst of writes into one event, applies feedback-loop guards, and calls the same
`fireTrigger`. Answers the #593 open questions.

### Watch scope: whole worktree, filtered; `.gitignore` always respected

The watch is the whole worktree (`fsnotify` watches directories; recursion is
ours), but events are filtered through layers:

- **git-tracked-ish default.** Discard anything matched by `.gitignore` — this
  kills the biggest noise sources for free (`node_modules/`, build output,
  `.git/`, caches) and needs no extra config, because worktrees carry the repo's
  `.gitignore`. We do **not** shell out to `git status` per event (slow, racy
  mid-write); we match ignore rules **in-process**. This is a **new dependency
  decision**: graith has no gitignore matcher today, so **vendor a small matcher**
  (gitignore is a compact grammar) rather than pull in `go-git` for one function;
  v1 may scope to top-level `.gitignore` + built-ins and defer nested/negation/
  global-excludes. It is "git-tracked-*ish*": untracked-but-not-ignored files pass
  (a new source file should trigger), tracked-but-ignored files drop.
- **Prune the watch set, not just the fire.** `.gitignore` gates which directories
  we recurse into and `watcher.Add`, not only which events fire — otherwise a
  large ignored subtree burns thousands of inotify watches against
  `fs.inotify.max_user_watches` (often 8k). We must not descend into ignored dirs.
- **Configurable globs** override: `paths = ["**/*.go"]` to fire only on source,
  `ignore = ["docs/**"]` to add to the ignore set. Matched against the
  worktree-relative path.

Precedence: an event fires iff the path is **not** `.gitignore`d, **not** in the
built-in ignore set, **not** matched by user `ignore`, and — if `paths` is set —
**is** matched by `paths`. The built-in ignore set (always on) covers `.git/` (a
`.git` *file* in a linked worktree — handle both), VCS lock files, and graith
scratch/tmp dirs.

**Why there is no `respect_gitignore` off-switch.** An earlier draft had one; it
is removed because there is no good use case and two strong reasons against: (1) it
re-opens inotify exhaustion (watching ignored trees is the blow-up pruning
prevents), and (2) it re-opens the feedback loop (build/formatter/coverage output
is gitignored *because it's derived*). The one semi-real case — "react to a
specific generated file that's gitignored", e.g. `dist/bundle.js` — is served by a
targeted, explicit include naming that one path, not a blanket switch that watches
*everything* ignored to catch *one* path; that targeted include is future work.
Built-in ignores stay always-on and non-overridable.

### Event source: raw filesystem writes, coalesced by debounce

**Raw `fsnotify` writes, not git stage/commit events.** The value is reacting to
work-in-progress — the continuous reviewer wants to see the change as it's made,
not wait for a commit that may never come (agents leave work uncommitted until
asked). Raw writes are available now (`fsnotify` already wrapped in
`config/watcher.go`); git-event watching would mean polling `git` or watching
`.git/logs/HEAD` — slower, racier, and it's the `.git/` churn we ignore. The
concern behind "commit events" — *don't fire on every intermediate write* — is
solved by the debounce quiet-window (§3), which coalesces a burst without waiting
for a commit. A `git-commit` source is noted as future work for triggers that
genuinely want commit granularity.

### The daemon watch loop details

`RunFileWatchLoop` reconciles the set of **watch bindings** — one per (active
watch trigger, matching live session) — against live `fsnotify` watchers.
Reconcile — don't poll — on startup, config reload (new wiring in `applyConfig`,
which today doesn't manage triggers), session create/stop/delete, scenario
membership changes, and `gr trigger` mutations. A `repo`/`role` policy that
matches N sessions produces N bindings, each with its own watcher and per-binding
state (§State model).

**Binding lifecycle vs. session state.** A binding is created when a matching
session starts and torn down when it stops or is soft-deleted (v1: **watch
bindings are active only while their source session is running** — unlike PR-watch,
which also polls stopped sessions, because a stopped agent isn't producing the
work-in-progress a reviewer reacts to). Pending debounce events are dropped on
stop; resuming the source rebuilds the binding and does a reconciliation scan
(below). The owned `ensure` reactor is **not** torn down with the binding (it may
still be delivering a final review); it is re-adopted by `TriggerID` when the
source resumes.

Recursion has races this must handle:

- **Create-before-watch race.** Files can be created inside a new directory before
  its `Create` event registers the watch. On adding a watch for a new dir,
  **re-scan it** for existing entries and treat them as events (scan-on-registration).
- **Moved-in trees** need recursive registration of the whole subtree.
- **Atomic saves.** Editors/formatters write-temp-then-rename (the reason
  `config/watcher.go` watches the containing dir); the matcher keys on the final
  path, so a rename-into-place counts as a write to that path.
- **Watch-limit / overflow degradation.** `watcher.Add` can fail (inotify limit)
  and fsnotify can drop events on overflow (the `fsnotify.Error` channel / a
  kernel-queue-overflow event, platform-specific). On `watcher.Add` failure the
  **binding** enters a **degraded** status (`TriggerBinding.Degraded`, surfaced in
  `gr trigger status` and `gr doctor`) and **fails safe by disabling that binding
  with a clear reason** rather than watching a partial tree. On overflow, because a
  bare re-scan has no prior snapshot to diff against, v1 **fires conservatively**
  (a single debounced fire with `{changed_files}` reported as "unknown — watcher
  overflow") rather than pretending to know the changed set; maintaining a bounded
  path/mtime snapshot to diff is future work.

### Feedback loops: how the reactor's own writes don't re-trigger

This is the crux. `fsnotify` **cannot attribute a write to a process**, so there
is no *general* way to know "implementer or reactor?" from the event stream. The
design closes the loop by construction for the case that matters and is honest that
the other case is only bounded:

**In v1, both watch action classes are structurally read-only on the watched
tree, so the loop cannot exist — full stop.** `fsnotify` can't attribute a write
to a process, so v1 does not *try* to tell reactor writes apart; it makes reactor
writes to the watched tree **impossible**:

1. **`session` / `ensure` reactors.** The reviewer delivers feedback over
   **messaging**, not edits, and — decisively — a `session` reactor with a shared
   worktree gets its **own separate, writable sandbox scratch directory** (a
   scratch `WorktreeDir`, *not* a second git checkout) while the watched (source)
   worktree is mounted **read-only** in its sandbox `ReadDirs` (`daemon.go`
   requires the sandbox precisely so the shared worktree can be mounted read-only).
   A spawned agent reactor **physically cannot write the files it watches**.

2. **`command` actions.** v1 watch commands are **enforced read-only** the same
   way (§command trust boundary): the bound worktree is in `ReadDirs`, output/cache
   goes to a separate writable scratch cwd. A command that tries to write the
   worktree fails at the sandbox — so it, too, cannot enter the watched write
   stream. `go test ./...` that only reads and writes to scratch never re-fires.

3. **Debounce (quiet-window coalescing)** — §3. Coalesces bursts; does not
   distinguish causes.

4. **Rate-limit backstop** — the `policy.rate_limit` rolling per-binding limit
   (default `5/30m`, mirroring `prwatch.go`'s `gate()`), enforced in the executor.
   It **bounds** damage; it does not by itself *prevent* a loop, so it's a
   backstop. A trip is recorded in `gr trigger status`.

**v2 — mutating commands (deferred, honest about being bounded not eliminated).**
If `action.mutating = true` is ever allowed (v2), a command *can* write the
watched tree, and the loop returns. The v2 defence is **generation-based discard**,
which is temporal suppression, not attribution, so it must be specified precisely
(and its guarantee weakened to "bounded, best-effort"):
- Each in-flight mutating command adds its generation to a **per-worktree active
  set** (a refcount/set, not a single bump — two commands can overlap on one
  worktree). Events observed while the set is non-empty, and until a quiet window
  after it empties, are discarded.
- **Late kernel/user-space delivery** (fsnotify events arriving after the process
  exits) can land after the set empties and the quiet window passes — this can
  re-fire; the window bounds but doesn't eliminate it.
- **Concurrent human edits** during the window are discarded too (a real gap).
- **Cross-binding:** the active-set is keyed by **worktree**, so a mutating command
  on worktree W suppresses re-fires for *every* binding watching W.
- On daemon restart mid-command, the in-memory set is lost; the binding does a
  reconciliation scan and fires conservatively.

**Net:** in v1 the loop cannot exist for either action class (both read-only by
construction). v2 mutating commands are bounded, not eliminated, and the doc says
so. Generic source attribution isn't achievable from `fsnotify` alone
(PID/process-accounting was considered and rejected — fsnotify can't attribute,
and threading real attribution through the sandbox is heavy).

### Duplicate avoidance (`ensure = true`)

The "ensure a reviewer reacts" convenience (§1) must be idempotent **per
binding** — each bound source session has its own reactor (§State model), so a
policy matching two implementers gets two reactors, never one shared across them.
Reuse rules (against *this binding's* `ReactorID`):

- reactor exists and is **running or stopped** (not soft-deleted) → `message` it
  (messaging **auto-resumes** a stopped one via `notifyFromDaemon`, so this revives
  the existing reactor — spawning instead would strand a dead session and duplicate
  on every reviewer exit);
- reactor is **soft-deleted or absent** → spawn a fresh one, tagged
  `TriggerID`/`TriggerReactor`.

Dedup is by **ownership tagging** (`TriggerID`/`TriggerReactor` on `SessionState`,
mirroring `ScenarioID`), robust against renames in a way name-guessing is not. A
marker lookup alone is **not race-safe** — two fires of the same binding could both
see "no reactor" and both spawn — so the daemon **reserves** the reactor before
creating it. Reservation reserves a **real placeholder session** (a `StatusCreating`
record with the trigger tags set in the *same* durable write, via the create-options
path — not a string sentinel a second fire couldn't safely message) into the
binding's `ReactorID`, taking `triggerState.mu` → `sm.mu` in that order (§Locking).
The second fire sees the reservation and messages/waits instead of spawning; a
`Create` failure rolls the reservation back. This mirrors how scenario start
pre-reserves `StatusCreating` placeholders under `sm.mu` before concurrent creation
(`scenario.go`).

### Opt-in granularity: a declaration never names a live session

The critical rule (an earlier #593 draft got this wrong): a session is ephemeral
and created on the fly, while `config.toml` is static and global, so a `[[trigger]]`
that says `session = "implementer"` is unusable — you'd hand-edit global config to
wire a watcher onto a session that already exists, and its name/ID isn't known at
config-write time. Therefore:

- **Global `config.toml` = a policy, scoped by `repo` and/or `role`, never a
  session.** It expresses a standing rule ("in *any* session on repo X, run tests
  when `*.go` changes") and **binds to sessions as they are created** — exactly how
  `[pr_watch]` applies to every eligible session. A config watch trigger with no
  live match simply watches nothing until a matching session exists.
- **Scenario TOML = the home for the flagship pair.** A scenario defines its own
  roles, so a scenario-level `watch → session (ensure)` trigger targets
  `role = "implementer"` unambiguously; its lifetime is the scenario's. This needs
  real lifecycle wiring, specified next — it is **not** just "extract a loader".
- **`gr trigger add` (runtime) = the only place a literal session is named**,
  because at that moment it exists. The daemon resolves the name to a session **ID**
  at add-time so a later rename doesn't break the trigger. This is exactly why
  runtime authoring matters more for the watch source than the schedule source —
  but it stays gated behind the same v2 deferral as all CLI authoring (Proposal 2),
  not introduced in v1.

#### Scenario-embedded trigger lifecycle (must be wired end-to-end)

Scenario triggers are a first-class path, not a config afterthought. Today
scenario TOML is parsed CLI-side with unknown fields rejected, and
`protocol.ScenarioStartMsg` carries only caller/name/goal/sessions; `StartScenario`
owns the two-phase reserve/rollback (`scenario.go`). Adding triggers means wiring
*all* of:

- **Schema:** a `[[trigger]]` array on the scenario-file model (same
  `TriggerConfig`), decoded by the shared scenario loader — with a scenario trigger
  restricted to `role` selectors (the roles the scenario defines) and forbidden
  from naming a repo/session outside the scenario.
- **Transport:** extend `ScenarioStartMsg` to carry the parsed triggers into the
  daemon (they can't be re-read from disk at fire time — the file may change).
- **Persistence / source of truth:** associate the triggers with `ScenarioState`
  (namespaced `scenario:<id>:<name>`), so they survive restart with the scenario
  even though the TOML isn't re-read.
- **Activation ordering + rollback:** triggers activate **only after** the
  scenario's two-phase session reservation succeeds; if start rolls back, the
  triggers are discarded with it (no orphaned watchers).
- **Teardown:** `scenario stop`/`delete` removes its triggers and their bindings
  (and, per the reactor-lifetime open question, decides their reactors' fate).
- **Membership changes:** `scenario resume`/`add` and shared-membership changes
  **rebind** — a role that gains a new session gets a new binding; a removed
  session's binding is torn down. This reuses the manifest-republish hook points
  scenarios already have.
- **Namespacing:** two running instances of the same scenario file must not collide
  on trigger name — hence the `scenario:<id>:<name>` key.

Either this is wired end-to-end, or scenario triggers (and the flagship `ensure`
example) drop to v2 and v1 ships watch triggers via `config.toml` policies only.
The design's recommendation is to wire it, because the flagship continuous-reviewer
pattern is the whole motivation for the watch source.

### Config examples (file-watch)

Global policy (config):

```toml
# "In any session on this repo, run the tests when Go source changes."
[[trigger]]
name = "test-on-change"

[trigger.watch]
repo     = "~/Code/graith"     # policy selector — binds to sessions on this repo
paths    = ["**/*.go"]
ignore   = ["**/*_test.go"]
debounce = "3s"

[trigger.action]
type    = "command"
command = "go test ./..."

[trigger.action.deliver]
inbox = "{session_name}"       # or store = "builds/{session_name}.log"
```

Scenario-level (flagship continuous reviewer):

```toml
[[trigger]]
name = "review-go"

[trigger.watch]
role  = "implementer"          # unambiguous: the scenario defines this role
paths = ["**/*.go"]

[trigger.action]
type   = "session"
ensure = true                  # message the owned reactor if it exists, else spawn read-only
agent  = "claude"
prompt = "Review the changes since your last look; send feedback via gr msg."
```

## The `gr trigger` CLI (both sources)

Control + observability, **not authoring**, in v1 (authoring is config/scenario
TOML; `gr trigger add/rm/edit` CRUD is deferred to v2 — see Proposal 2):

```bash
gr trigger list              # all triggers (schedule + watch): name, source, next fire / watch scope, last run, state
gr trigger status <name>     # detail: last run result, in-flight, degraded/suppressed, next fire, recent history
gr trigger run <name>        # fire now, out-of-band (respects overlap)
gr trigger pause <name>      # stop firing (persists across restart); keeps definition
gr trigger resume <name>     # re-enable
```

All support `--json`. A new `internal/cli/trigger.go` registered on the root
command like `scenarioCmd`.

## Worked examples

**Daily PR report (schedule).** At 09:00 Europe/London: `dueTriggers` (under
`triggerState.mu`) sees `daily-pr-report` due, not paused, not in flight;
atomically advances `nextFire` to tomorrow, writes `LastScheduledFireAt = 09:00`
(the at-most-once commit point), marks in-flight, returns it; `fireTrigger`
dispatches the `session` action via `sm.Create(...)` with the orchestrator as
`parentID`, headless 80×24, and a prompt with an injected delivery instruction
(`{date}` pre-expanded). The agent summarises and delivers with its own `gr msg`/
`gr store`; the inbox message auto-resumes the orchestrator. In-flight clears when
`Create` returns (executor-call overlap); a `RunRecord` is appended. If the daemon
was down at 09:00 with `catch_up = false`, nothing fires late; a crash between the
commit and the dispatch does not re-fire on restart.

**Continuous reviewer (watch).** A scenario declares the `review-go` trigger.
The implementer edits `handler.go`; after the 3s quiet window the watcher fires;
`ensure = true` finds no owned reactor, reserves one, and spawns a reviewer
sharing the implementer's worktree read-only, tagged `TriggerID`. The reviewer
reads the change and messages feedback. On the next edit, the watcher fires again;
`ensure = true` finds the (now stopped) reactor and **messages** it, auto-resuming
it — no duplicate. The reviewer's own activity produces no writes to the watched
tree (read-only mount), so it never re-triggers.

## Consensus

This design is the merge of two separately-drafted docs, each independently
reviewed against the codebase before merging:

- **#592 (schedule)** — reviewed by a 3-judge tribunal; grounding confirmed
  accurate, all five open questions answered. Findings incorporated: the durable
  **run-state machine** (at-most-once `LastScheduledFireAt` commit, restart-safe
  interval anchoring, definition fingerprinting), the fail-closed **`command` trust
  boundary**, the real `Create`/scenario-dispatch call shapes, a **trigger-specific
  template expander**, and the precise `authorizeTriggerOp` owner model.
- **#593 (watch)** — reviewed by a 2-judge source-verified tribunal, then hardened
  further during this merge in response to direct maintainer review. Findings
  incorporated: the **feedback-loop crux** (read-only reactors close the loop by
  construction; write-capable `command` actions use generation-based discard, and
  are non-mutating in v1); **`ensure` reactor reuse** (auto-resume a stopped
  reactor rather than duplicate) with **atomic reservation**; **watch-set pruning**
  + inotify-limit degradation; and the removal of two design mistakes surfaced in
  review — a `respect_gitignore` off-switch (a footgun that re-opens the two
  problems the design closes) and config triggers naming an ephemeral session
  (replaced by repo/role policy selectors; literal sessions are runtime-only).

The two docs were merged (this file) because everything below the source line is
shared; maintaining them separately had already produced drift (a parallel action
vocabulary and a session-naming config schema in the #593 draft) that the shared
model eliminates.

**Post-merge review (2-judge, source-verified).** The *merged* doc was then
reviewed for merge integrity. Both judges confirmed the grounding remained
accurate across ~20 citations, the shared framework was coherent, and the
maintainer decisions (no `respect_gitignore`, repo/role selectors, `ensure`
reuse) were reflected consistently — but converged on real gaps in the shared
runtime model, all now incorporated:

- **Per-binding runtime state (the big one):** a `repo`/`role` policy can bind to
  many live sessions, so runtime state splits into per-definition and per-binding
  (`(name, source session id)`) rows — one `ReactorID`, in-flight guard, debounce,
  generation counter, and degraded status *per binding* (§State model).
- **Source-dependent `command` execution root:** schedule commands require `repo`;
  watch commands run in the bound worktree and reject `repo` (§command trust
  boundary).
- **Enforceable read-only v1 watch commands:** "non-mutating" is enforced by the
  sandbox (worktree in `ReadDirs` + writable scratch cwd), not assumed, so the
  feedback-loop claim is actually true; `mutating` is a rejected-in-v1 flag
  (§Feedback loops).
- **`message` destination:** `message` routes via `deliver` (inbox/topic) — the
  earlier "deliver rejected on message" left it with nowhere to go.
- **`deliver.wake`/soft-delete:** delivery selects bare `Publish` vs
  `notifyFromDaemon` per `wake` and adds its own soft-delete guard, since
  `notifyFromDaemon` resumes unconditionally and has no such guard.
- **Scenario-embedded trigger lifecycle:** now wired end-to-end (schema, transport
  via `ScenarioStartMsg`, `ScenarioState` association, activation-after-two-phase,
  rollback, teardown, rebinding, namespacing) rather than assumed.
- **Smaller:** restart-stable interval anchor (`ActivatedAt`/`NextScheduledFireAt`),
  durable-save-before-dispatch for at-most-once, `triggerState.mu → sm.mu` lock
  order, create-options path for durable reactor tagging, `[triggers]
  max_concurrent` (not `[trigger]`), explicit network-blocking policy,
  conservative overflow firing, `timezone`-with-`every` rejection, and `Create`
  corrected to 17-fixed-args.

**Deferred to v2 (out of scope, noted in-line):** `overlap = "queue"`, lifetime-
based session/scenario overlap with durable reconciliation, CLI-authored triggers
(`gr trigger add`, incl. runtime `--session` targeting for the watch source),
mutating watch-driven `command` actions, a `git-commit` watch source, a targeted
"watch this one ignored path" include, configurable `tick_interval`, and a
per-trigger `allow_agent_control = false` lock. Delivery failures in v1 are logged,
not retried.

## Testing

Following the coverage expectations: pure logic is extracted and unit-tested; the
`fsnotify`/loop glue is thin, delegating decisions to tested functions (the
`RunPRWatchLoop` vs `diffAndBuild`/`gate` split is the model). Scots fixtures
(trigger names like `dreich-sweep`, `braw-report`; watch fixtures `bothy/`,
`glen/*.go`, `haar.tmp`).

**Shared / schedule:**
- Schedule parsing: valid cron, `@`-descriptors, intervals, timezones; reject
  both-set/neither-set/unparseable; `every > 0`; `queue` overlap rejected in v1.
- `Next()` across a DST boundary (zone-aware).
- Missed-run: `catch_up = false` never backfills; `catch_up = true` fires exactly
  once on startup when the last fire was missed, zero times otherwise.
- **At-most-once:** `LastScheduledFireAt` written before dispatch ⇒ a simulated
  crash between commit and action does not re-fire on reload.
- **Interval anchoring** survives restart; **fingerprint reset** on a changed
  same-name definition.
- Overlap: `skip` suppresses while in-flight; `allow` permits concurrent;
  executor-call window for `session`/`scenario`. `max_concurrent` cap skips excess.
- Action validation: `session`/`scenario` rejected when orchestrator disabled;
  `command` rejected without an allowed `repo`; `deliver` rejected on
  `message`/`scenario`; repo-less store key without `shared:` rejected.
- Template expander: `{date}`/`{name}`/`{fire_time}` expand; unknown `{var}` errors.
- Delivery routing: inbox/topic/store each invoked; store failure doesn't suppress
  inbox; `wake` off ⇒ non-orchestrator stopped target not resumed; soft-deleted
  never woken.
- `enabled = false` overrides persisted `Paused`; `resume` on config-disabled
  rejected. Manual `run` records `Cause = "manual"`, doesn't shift the schedule.

**Watch:**
- Path filtering (gitignore + built-in + user globs + precedence), table-driven.
- Watch-set pruning: ignored subtrees not `watcher.Add`ed; create-before-watch
  entries picked up by scan-on-registration; `watcher.Add` failure degrades/disables
  with a recorded reason.
- Debounce/coalescing with a fake clock: one fire per quiet window.
- Feedback-loop: a `command`'s own writes are **discarded** (generation counter),
  not re-fired; a concurrent legitimate write and a late post-exit event handled
  per policy; cross-trigger `command` writes suppressed for a sibling read-only
  trigger; rate-limit gate trips and records a reason.
- `ensure`: messages a running reactor, messages-and-auto-resumes a stopped one,
  spawns only when soft-deleted/absent; two concurrent fires reserve one reactor
  (no double-`Create`).
- Config policy binding: a `repo`/`role` watch trigger binds to a matching session
  on create and not before.

**Integration** (`internal/integration/`, real daemon):
- `every = "1s"` + `message` action delivers into a target inbox within a couple
  ticks; missed-run across a simulated restart with `catch_up` on/off.
- A `session` action spawns a background session parented to the orchestrator,
  appears in `gr list`.
- Write a file into a watched worktree ⇒ the configured action fires (message lands
  in the target inbox).
- `gr trigger list/status/run/pause/resume` round-trip; `pause` survives
  `gr daemon restart`.

**All tests must pass with `-race`** — the independent-mutex + off-lock-dispatch
design (mirroring prwatch) is what `-race` polices.

## Rollout / phasing

1. **Framework + schedule source + `message`/`command`(read-only)/`session`.** The
   shared executor, state machine, delivery, and `gr trigger` CLI, driven first by
   the clock. No feedback-loop risk (no watcher yet).
2. **Watch source + `message`/read-only-`command`.** Adds `RunFileWatchLoop`,
   watch-set pruning, debounce. Deliver to an existing reviewer / topic; run tests
   on change. Bounded only by debounce.
3. **`session` + `ensure`** (ownership tags, reuse, atomic reservation). Lights up
   the automatic continuous reviewer — read-only reactors, so no loop beyond
   debounce.
4. **`scenario` action; config + scenario-embedded watch policies** (repo/role
   binding), once the runtime model has proven out.
5. **v2:** mutating `command`, `overlap = "queue"`, lifetime overlap, CLI
   authoring (incl. runtime `--session` watch targeting), `git-commit` source.

## Open questions

- **CLI authoring vs config-only (v1).** v1 makes config/scenario TOML the source
  of truth and defers `gr trigger add`. The watch source arguably needs runtime
  `--session` targeting sooner than the schedule source does — is that enough to
  pull a minimal `gr trigger add --session` into v1, or does it wait for full CRUD?
- **Default watch debounce.** 3s is a guess: too short chases multi-file edits, too
  long makes review feel laggy. Should it scale with `change_count`?
- **Reactor lifetime.** When a watched source session ends, should its `ensure`
  reactor be stopped, soft-deleted, or left running to deliver a final review?
  (Leaning: left running.)
- **Per-trigger `allow_agent_control = false`.** Worth adding in v1 to lock a
  security/hygiene trigger to human-only pause/resume, or defer?

## Implementation notes

| File | Change |
|------|--------|
| `go.mod` / `go.sum` | Add `github.com/robfig/cron/v3` (parser + `Next()`); vendor a small gitignore matcher. `fsnotify` already present. |
| `internal/config/config.go` | Add `Triggers []TriggerConfig` + a `[triggers] max_concurrent` table; `TriggerConfig` (`*bool Enabled`, `*ScheduleConfig`, `*WatchConfig`), `ScheduleConfig`, `WatchConfig`, `ActionConfig` (incl. `timeout`/`mutating`/`network`/`trusted`), `DeliverConfig`, `TriggerPolicy` (incl. `rate_limit`); validation (exactly-one-source, one-action-type, cron/every parse, `every>0`, `queue`/`mutating`-rejected-in-v1, `timezone` rejected with `every`, repo allow-list, schedule-command-requires-repo / watch-command-rejects-repo, orchestrator-required-for-session/scenario, `message`-requires-inbox-or-topic, per-action delivery validity, `shared:`-required-for-repo-less store, watch requires exactly one of repo/role and never a session). `Default()` empty. |
| `internal/config/trigger_template.go` | New trigger-specific expander (`{name}`/`{date}`/`{datetime}`/`{fire_time}` + watch `{session_name}`/`{changed_files}`/`{change_count}`/`{worktree_path}`) — NOT `config.Expand`. |
| `internal/daemon/scenario_loader.go` | Extract scenario-TOML loading from `internal/cli/scenario.go` into a shared, daemon-reachable package. |
| `internal/daemon/trigger.go` | `RunTriggerLoop`, `runTriggerTick`, `dueTriggers` (atomic cursor advance + `LastScheduledFireAt` commit + in-flight under `triggerState.mu`), `fireTrigger`, per-type executors, delivery routing (+ `wake` gating), `triggerState` struct + mutex, `initTriggerSchedules` (fingerprint diff, catch_up), `max_concurrent` semaphore, bounded `History`, prune. |
| `internal/daemon/filewatch.go` | `RunFileWatchLoop`, watcher reconcile, recursive add with ignore-pruning + scan-on-registration, debounce coalescer, per-worktree generation-discard, degradation status; funnels into `fireTrigger`. |
| `internal/sandbox` | Command-action profile: repo-rooted, session-less, minimal env allowlist, network-off-by-default, process-group kill; fail-closed unless enforced or `action.trusted`. |
| `internal/daemon/daemon.go` | `go sm.RunTriggerLoop(ctx)` + `go sm.RunFileWatchLoop(ctx)` at startup (near :5790); extend `applyConfig` to reconcile triggers/watchers. |
| `internal/daemon/state.go` | Add `TriggerRuntime map[string]*TriggerRuntimeState` (Fingerprint, LastScheduledFireAt, Degraded, ReactorID, History); `TriggerID`/`TriggerReactor` on `SessionState`; migrate `CurrentStateVersion` (no-op); prune + fingerprint-reset on load. |
| `internal/daemon/handler.go` | Handle `trigger_list`/`trigger_status`/`trigger_run`/`trigger_pause` (read-only vs orchestrator-or-descendant auth). |
| `internal/daemon/auth.go` | `authorizeTriggerOp` (orchestrator-or-descendant). |
| `internal/daemon/authmatrix.go` | Add `trigger_*` to `remoteAllowed` (fail-closed otherwise). |
| `internal/protocol/messages.go` | `TriggerListMsg`, `TriggerStatusMsg`, `TriggerRunMsg`, `TriggerPauseMsg`; a `TriggerInfo` result type. |
| `internal/cli/trigger.go` | New `triggerCmd` + `list/status/run/pause/resume`; `--json`; registered on `rootCmd`. |
| `internal/config/default_config.toml` | Commented `[[trigger]]` examples (a daily report and a watch). |
| `docs/site/` | New `triggers.md`; cross-link patterns/orchestrator docs. |
| `AGENTS.md` | Document the trigger model, action vocabulary, both sources, and `gr trigger`. |

## References

- `internal/daemon/prwatch.go:82` — `RunPRWatchLoop`: the model for a config-gated,
  off-request-path loop with an independent mutex, off-lock work, per-item
  scheduling, state pruning, and a debounce/rate-limit `gate()`.
- `internal/daemon/daemon.go:3034` — `RunPurgeLoop`: startup-sweep-then-ticker and
  missed-window handling.
- `internal/daemon/daemon.go:5790` — where background loops are launched.
- `internal/config/watcher.go` — `fsnotify` quiet-window debounce; watches the
  containing dir for atomic-save renames.
- `internal/daemon/notify.go:73` — `notifyFromDaemon`: inbox delivery + auto-resume;
  `systemSenderID` (`:56`) for daemon-authored messages.
- `internal/daemon/scenario.go` / `internal/cli/scenario.go` — closest prior art for
  a config/CLI/daemon-state feature with authorization and pre-reserved
  `StatusCreating` placeholders.
- `internal/daemon/handler.go:1428` — scenario control-message dispatch;
  `decodePayload[T]`.
- `internal/protocol/messages.go` — `ScenarioStartMsg` et al., the shape for
  `Trigger*Msg`.
- `internal/config/config.go` — top-level `Config`; `PRWatchConfig` as a rich
  per-feature config-section example; `ParseDurationWithDays`.
- `internal/store/store.go:202` — `Put` / `:237` `Append` for delivery.
- `internal/daemon/daemon.go:502` — `sm.Create` 17-fixed-arg + variadic-env shape
  (needs a create-options path to install trigger tags durably); `:729` shared-worktree
  read-only + sandbox-required invariant.
- `go.mod` — `fsnotify` already vendored; `robfig/cron/v3` + gitignore matcher to be
  added.
