---
title: "Design Doc: Cron / Automated Actions (Scheduled Triggers)"
authors: Dougal Matthews
created: 2026-07-11
status: Draft
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/592
---

# Cron / Automated Actions (Scheduled Triggers)

> **Note on code references.** `file:line` citations are anchored to symbol
> names as of writing; if a line has drifted, search for the named function.

## Background

graith coordination is entirely *driven* today — nothing happens unless a human
or an agent is actively pushing work through. There is no way to say "every
morning at 09:00, produce a report of open PRs" or "every hour, sweep for review
requests and spin up review worktrees". These time-driven jobs are exactly the
kind of housekeeping and reporting that should run unattended.

The daemon already runs several unattended background loops, and they are the
template this design follows:

- `RunPRWatchLoop` — polls each session's PR/CI state on a timer and publishes
  notifications into the owning session's inbox
  (`internal/daemon/prwatch.go:82`).
- `RunGitPullLoop` — periodic `git pull` on eligible worktrees
  (`internal/daemon/gitpull.go:20`).
- `RunPurgeLoop` — a startup sweep plus a coarse ticker that hard-deletes
  expired soft-deleted sessions (`internal/daemon/daemon.go:3034`).
- `RunDetectionLoop`, `RunMessageCleanupLoop` — status detection and message
  GC.

All five are launched as detached goroutines from daemon startup and cancelled
via a shared `context.Context`:

```go
go sm.RunDetectionLoop(ctx)
go sm.RunMessageCleanupLoop(ctx)
go sm.RunGitPullLoop(ctx)
go sm.RunPRWatchLoop(ctx)
go sm.RunPurgeLoop(ctx)
```
(`internal/daemon/daemon.go:5790`)

The daemon also already knows how to *author an action itself* and deliver it:
`notifyFromDaemon(sessionID, body)` publishes a message into a session's inbox
as the synthetic `graith:system` sender and auto-resumes the session if it was
stopped (`internal/daemon/notify.go:73`). Session creation, scenario start, and
messaging are all callable in-process from the `SessionManager`. In other words,
every capability a scheduler needs to *do something* already exists — what's
missing is the thing that fires on a clock.

This issue (#592) is the **time-driven** half of a unified triggers framework.
Its sibling, #593, is the **file-event-driven** half (a file-watch that fires
when a worktree changes). Both issues agree on the shared shape: a trigger is
`(schedule | file-event) → action`, and **the daemon fires triggers directly**
so they survive terminal close and need no attached orchestrator. This doc
defines the trigger/action model in full and implements the *schedule* source;
#593 will add the *watch* source and reuse the action vocabulary and executor
defined here.

## Problem

1. **No time-driven automation.** Recurring work — daily PR reports, hourly
   review sweeps, nightly housekeeping — has no home. It lives in a human's head
   or a cron entry that shells out to `gr`, outside graith's lifecycle,
   authorization, and observability.

2. **External cron can't see graith.** A system `crontab` running `gr new ...`
   has no access to the orchestrator identity, the session tree, the sandbox
   policy, or the messaging fabric. It can't deliver into an inbox, can't be
   listed by `gr`, and can't be paused when graith is idle.

3. **No shared action vocabulary.** #592 and #593 both want to "run an action".
   Without a shared model they'd each grow their own ad-hoc dispatch, doubling
   the surface and drifting apart.

4. **No missed-run / overlap semantics.** A naive `time.Ticker` fires N times to
   "catch up" after the daemon was down, and happily starts a second run while
   the first is still going. Both are footguns for actions that spawn sessions
   or run commands.

## Goals

- A **schedule → action** trigger that the daemon fires on its own, surviving
  terminal close and daemon restart.
- Support **both** cron expressions and simple intervals for the schedule.
- A **shared action vocabulary** with #593 covering the issue's candidate
  actions: run a command, start a scenario, spawn a session, send a message,
  produce a report.
- **Delivery** of action output into the inbox, a store doc, or a topic —
  reusing `notifyFromDaemon` so a stopped orchestrator is auto-resumed.
- Explicit, safe defaults for **missed runs** (don't backfill a burst) and
  **overlap** (skip if the previous run is still in flight).
- Observability and manual control via a `gr trigger` CLI (list, status, run
  now, pause/resume).

### Non-Goals

- **File-watch triggers** — that is #593. This doc defines the shared model and
  action executor so #593 slots in, but implements only the schedule source.
- **Cross-machine scheduling** — triggers run on the single local daemon, like
  scenarios.
- **Distributed/HA scheduling** — one daemon, one clock. No leader election.
- **Sub-second precision** — a coarse tick (seconds) is enough for cron/interval
  work; this is not a real-time job runner.
- **Arbitrary DAGs / job dependencies** — a trigger fires one action. Chaining
  is expressed by the action itself (e.g. a spawned session that starts a
  scenario), not by the scheduler.
- **CLI-authored triggers in v1** (`gr trigger add` writing new triggers) — v1
  defines triggers in `config.toml`; the CLI observes and controls them. See
  Open Questions.

## The unified trigger model (shared with #593)

A trigger has three parts: a **source** (what makes it fire), an **action**
(what runs), and a **policy** (missed-run / overlap behaviour). #592 provides
the `schedule` source; #593 will provide the `watch` source. Everything below
the source line is identical for both.

```
        source                 action                 delivery
   ┌──────────────┐      ┌──────────────────┐    ┌────────────────┐
   │  schedule    │      │  command         │    │  inbox         │
   │  (#592, cron │─────▶│  session         │───▶│  topic         │
   │   or every)  │      │  scenario        │    │  store doc     │
   │              │      │  message         │    │  (daemon log)  │
   │  watch       │      └──────────────────┘    └────────────────┘
   │  (#593)      │
   └──────────────┘         + policy: catch_up, overlap
```

The Go model lives in `internal/config` (definition) and a new
`internal/daemon/trigger.go` (runtime):

```go
// config.TriggerConfig — one [[trigger]] block.
type TriggerConfig struct {
    Name     string          `toml:"name"`
    Enabled  *bool           `toml:"enabled"`  // pointer: nil ⇒ default true (see note)
    Schedule *ScheduleConfig `toml:"schedule"` // #592 source (this doc)
    Watch    *WatchConfig    `toml:"watch"`    // #593 source (nil in v1)
    Action   ActionConfig    `toml:"action"`
    Policy   TriggerPolicy   `toml:"policy"`
}

type ScheduleConfig struct {
    Cron     string `toml:"cron"`     // e.g. "0 9 * * *", or a descriptor "@daily"
    Every    string `toml:"every"`    // Go duration, e.g. "15m", "1h30m"
    Timezone string `toml:"timezone"` // IANA name; default = daemon local time
}

type ActionConfig struct {
    Type     string         `toml:"type"` // command | session | scenario | message
    // command:
    Command  string         `toml:"command"`
    Repo     string         `toml:"repo"`
    // session:
    Prompt   string         `toml:"prompt"`
    Agent    string         `toml:"agent"`
    Model    string         `toml:"model"`
    // scenario:
    Scenario string         `toml:"scenario"`
    // message:
    Body     string         `toml:"body"`
    // command trust escape hatch (see §3 command trust boundary):
    Trusted  bool           `toml:"trusted"`
    Deliver  DeliverConfig  `toml:"deliver"`
}

type DeliverConfig struct {
    Inbox string `toml:"inbox"` // session name, or "orchestrator"
    Topic string `toml:"topic"` // pub/sub topic
    Store string `toml:"store"` // store key, templated (see Delivery)
}

type TriggerPolicy struct {
    CatchUp bool   `toml:"catch_up"` // default false: never backfill missed fires
    Overlap string `toml:"overlap"`  // skip (default) | allow | queue
}
```

Exactly one of `Schedule` / `Watch` must be set per trigger (validation
rejects both or neither). Both are **pointers** so that an omitted source and an
empty `[trigger.schedule]` block are distinguishable after TOML decode — a value
`ScheduleConfig` would make "no schedule" and "empty schedule" indistinguishable.
In v1 `Watch` is always nil.

`Enabled` is a `*bool` for the same reason: a plain `bool` decodes an absent key
as `false`, which would make every trigger default to *disabled*. With a pointer,
`nil` means "unset ⇒ default enabled"; an explicit `enabled = false` disables.
`Enabled` is a static config switch (the trigger is inert, never scheduled);
`Paused` (state, below) is a runtime toggle via `gr trigger pause`. **Precedence:
`enabled = false` in config always wins** — a paused-then-config-disabled trigger
stays off, and `gr trigger resume` on a config-disabled trigger is rejected with
a clear error. Changing a trigger's definition (see fingerprint, below) resets
its `Paused` flag, run count, and cursor.

## Proposals

### Proposal 0: Do Nothing

Users wire recurring `gr` invocations through the system `crontab` or a
`launchd`/`systemd` timer.

**Pros:** zero implementation cost; leverages a battle-tested scheduler.

**Cons:** the external scheduler has no graith identity — it can't act as the
orchestrator, can't deliver into an inbox, can't auto-resume a stopped session,
isn't visible to `gr list`/overlay, and isn't paused when graith is idle. It
also duplicates the concept the issue explicitly wants to unify with #593. The
sandbox can't cover it, and there's no missed-run/overlap policy — the user
reimplements all of that in shell. This is the status quo the issue rejects.

### Proposal 1: Daemon-fired scheduled triggers, defined in `config.toml`, controlled by `gr trigger` (Recommended)

Triggers are declared as `[[trigger]]` blocks in `config.toml` (the same file
that already configures `[pr_watch]`, `[git_pull]`, `[delete]`, and
`[orchestrator]`). A new daemon loop, `RunTriggerLoop(ctx)`, evaluates each
schedule against the wall clock, fires due actions off-lock, and records
last/next-run bookkeeping. A `gr trigger` CLI family surfaces status and offers
manual control (`run`, `pause`, `resume`). This mirrors graith's existing
config-driven background loops while adding the ergonomics people expect from a
scheduler.

This is the recommended proposal; the rest of the doc specifies it and answers
each open question from the issue.

### Proposal 2: Daemon state + full CRUD CLI (`gr trigger add/rm/edit`)

Triggers live only in daemon state (`state.json`), authored exclusively through
`gr trigger add`. No config file involvement.

**Pros:** fully dynamic; an agent can create a trigger at runtime; no config
reload needed.

**Cons:** triggers become invisible mutable state rather than reviewable,
version-controllable declarations. It diverges from `[pr_watch]`/`[git_pull]`,
which are config-only. It also hands agents a durable self-scheduling primitive
(an agent could schedule itself to respawn) with no reviewable artifact — a
meaningful authorization and "surprise cost" concern. Deferred: v1 uses config
as the source of truth; a future `gr trigger add` (Proposal 2's ergonomics) can
be layered on once the model has proven out. See Open Questions.

## How it works

### 1. Schedule syntax: **both cron and intervals**

The issue asks: cron, intervals, or both? **Both**, selected by which field is
set on `[trigger.schedule]`:

- `every = "15m"` — parsed by the existing `config.ParseDurationWithDays`
  helper (`internal/config/config.go:386`), which is already used for
  `[delete] retention` and supports `"7d"`-style day suffixes on top of Go's
  `time.ParseDuration`. **Anchored to the persisted `LastScheduledFireAt`**
  (State model, below), falling back to first-seen time when there is no prior
  fire. This keeps intervals restart-safe: `nextFire = LastScheduledFireAt + N`,
  computed once on load, so a daemon restarted more often than the interval
  period doesn't reset the phase and starve a long-interval trigger. `every`
  must be `> 0` — `ParseDurationWithDays("0")` parses successfully, so a
  zero/negative interval is rejected at validation. Best for "roughly every N"
  housekeeping.
- `cron = "0 9 * * *"` — a standard 5-field cron expression (minute hour dom
  month dow), plus the common descriptors `@hourly`, `@daily`, `@weekly`,
  `@monthly`. Best for wall-clock-anchored jobs ("09:00 every day").
- `timezone = "Europe/London"` — optional IANA zone for cron expressions;
  defaults to the daemon's local time. DST is handled by the cron library's
  zone-aware `Next()`.

Exactly one of `cron` / `every` is required. Validation rejects both-set,
neither-set, and unparseable values at config load, with a clear error naming
the offending trigger — the same fail-closed posture as sandbox config.

**Time edge cases.** Cron `Next()` is computed off the zone-aware library, which
handles DST gaps (spring-forward: a 02:30 daily fire on the skipped hour runs at
the next valid instant) and folds (fall-back: fires once, not twice). Standard
cron DOM/DOW semantics apply (when both day-of-month and day-of-week are
restricted, the union fires — robfig's documented behaviour). A wall-clock jump
(NTP step, laptop sleep/wake) is tolerated because each tick recomputes
`now >= nextFire` against the current clock rather than counting elapsed ticks;
a large backward jump could re-arm a just-fired cron entry, which the
at-most-once fire guard (State model) suppresses. Changing the daemon's local
timezone takes effect on the next config reload / restart when schedules are
re-parsed.

**Library choice.** graith vendors no cron parser today (confirmed:
`go.mod` has `fsnotify` — already present for #593 — but nothing cron-shaped).
Recommend vendoring **`github.com/robfig/cron/v3`**: it is the de-facto standard
Go cron library, parses the 5-field syntax and `@`-descriptors, is timezone- and
DST-aware via `cron.ParseOption`/`WithLocation`, and exposes a pure
`Schedule.Next(time.Time) time.Time` we can call from our own loop **without**
adopting its runner goroutine. We use only its parser + `Next()`; the firing
loop, policy, and dispatch are ours (so the missed-run/overlap semantics below
are under our control, not the library's). Intervals use `time.ParseDuration`
directly — no library needed.

### 2. Where defined: **`config.toml`, controlled by `gr trigger`**

Definition source of truth is `config.toml`:

```toml
[[trigger]]
name = "daily-pr-report"

[trigger.schedule]
cron     = "0 9 * * *"
timezone = "Europe/London"

[trigger.action]
type   = "session"
prompt = "Summarise all open PRs across repos graith, dem, and rrweb. For each: title, author, CI state, review state, age. Post the summary to the orchestrator inbox and write it to the store."
repo   = "~/Code/graith"
agent  = "claude"

[trigger.action.deliver]
inbox = "orchestrator"
store = "reports/pr/{date}.md"

[trigger.policy]
catch_up = false
overlap  = "skip"
```

This matches how every other daemon background feature is configured
(`[pr_watch]` in `config.PRWatchConfig` at `internal/config/config.go:271`,
`[git_pull]`, `[delete] retention`). Config is reviewable, diffable, and
restart-safe. A new `Triggers []TriggerConfig` field is added to the top-level
`Config` struct (`internal/config/config.go:24`) with `toml:"trigger"`, and
`Default()` returns an empty slice (feature off unless configured).

The daemon loads triggers on startup and on the existing config-reload path
(the same path that already re-reads `sm.Config()` each tick in the other
loops — see `RunPRWatchLoop`'s `cfg := sm.Config()` at `prwatch.go:95`). A
reloaded trigger set is diffed by `name`: unchanged triggers keep their
next-fire cursor, added triggers are scheduled, removed triggers are dropped.

**`gr trigger` CLI** (control + observability, not authoring, in v1):

```bash
gr trigger list              # all triggers: name, schedule, next fire, last run, state
gr trigger status <name>     # detail: last run result, in-flight, next fire, recent history
gr trigger run <name>        # fire now, out-of-band (respects overlap policy)
gr trigger pause <name>      # stop firing (persists across restart); keeps definition
gr trigger resume <name>     # re-enable
```

All support `--json` for agent-mode. The CLI is a new `internal/cli/trigger.go`
registered on the root command exactly like `scenarioCmd`
(`internal/cli/scenario.go:176`, `rootCmd.AddCommand(scenarioCmd)` at
`scenario.go:647`), with subcommands added via `triggerCmd.AddCommand(...)`.

`gr trigger run` is an **out-of-band manual fire**: it records a `RunRecord`
with `Cause = "manual"`, bumps `RunCount`, and respects the `overlap` policy,
but does **not** touch `LastScheduledFireAt` or the recurring `nextFire` — a
manual run must not shift the schedule (a manual 15:00 run of a 09:00-daily
trigger still fires at 09:00 tomorrow).

### 3. Action vocabulary (v1 scope)

The issue lists five candidates: command, scenario, session, message, report.
Recommended v1 `action.type` values — **command, session, scenario, message** —
with **report modelled as a composition** rather than a distinct type:

| Type | What it does | In-process call |
|------|--------------|-----------------|
| `command` | Run a shell command in `repo`'s context, capture stdout/stderr, deliver the output. | new `runCommandAction` under a dedicated command-action sandbox profile (see below) — **not** the bare `sh -c` of `sendNotification` (`notify.go:255`), which is unsandboxed and not a security model |
| `session` | Spawn a session with `prompt`/`agent`/`model` in `repo`, parented to the orchestrator so it's addressable and lifecycle-managed. | `sm.Create(...)` (`daemon.go:502`) — see call-shape note below |
| `scenario` | Start a named scenario from `~/.config/graith/scenarios/`. | a shared scenario loader/start service — **not** `sm.StartScenario` directly; see note below |
| `message` | Publish a fixed `body` to an inbox or topic. | `notifyFromDaemon` (inbox) / `messages.Publish` (topic) |

**`session` call shape.** `Create` is a 16-positional-argument function
(`daemon.go:502`) with `... prompt, model, parentID string, ... rows, cols
uint16, envExtra ...map[string]string)` — there is no `Background` flag
(sessions are inherently PTY-backed and detached from any client; "background"
is a CLI/client attachment concern, not a `Create` parameter). A daemon-fired
session also has **no attached client to source `rows, cols` from** (scenarios
pass `clientRows, clientCols` from the connection). Trigger-spawned sessions use
**default headless dimensions of 80×24**; the agent's first real resize (if ever
attached) supersedes them. `parentID` is set to the resolved orchestrator
session ID (below).

**`scenario` dispatch is not a straight `sm.StartScenario` call.** Two real
obstacles, both confirmed against the code:
- **All scenario-TOML parsing lives in the CLI** (`internal/cli/scenario.go`,
  `toml.NewDecoder`); the daemon has no "load scenario by name from disk"
  path. This work must be extracted into a shared, daemon-reachable loader
  package so both the CLI and the trigger executor can build a
  `protocol.ScenarioStartMsg`.
- **`StartScenario` requires an authenticated orchestrator caller** — it sets
  `CallerSessionID = auth.sessionID` at the handler (`handler.go:1436`) and
  rejects any caller whose `SystemKind != SystemKindOrchestrator`
  (`scenario.go` reserve phase). A daemon-fired action has no caller. The
  executor must resolve the **currently live** orchestrator session ID and pass
  it as `CallerSessionID` (or a small internal trusted entry point that records
  orchestrator ownership without a token). If no orchestrator session exists
  (disabled, or `creating`/`stopped` at fire time), the `scenario` action is
  skipped with a logged error — config-time `orchestrator.enabled` validation is
  necessary but not sufficient, because the session may be absent at fire time.

A `scenario` action referencing a name with **no file** in
`~/.config/graith/scenarios/` is a **runtime** error (logged, recorded in
`LastError`), not a config-load rejection — scenario files can be added/removed
independently of `config.toml`, so pre-validation would be brittle.

**"Report" is not a fourth verb** — a report is a `session` (or `command`)
action whose output is routed by `[trigger.action.deliver]`. The issue's own
example ("spawn a background session 'summarise open PRs ... and post to the
orchestrator inbox'") is precisely a `session` action + inbox delivery. Folding
report into delivery keeps the vocabulary orthogonal: *what runs* (command /
session / scenario / message) is independent of *where output goes* (inbox /
topic / store). This is the same vocabulary #593 will dispatch on, unchanged.

**Ownership & authorization of fired actions.** Scheduled actions run with
**daemon authority** — there is no authenticated caller, the daemon fires them
itself (the issue's "daemon fires triggers directly" decision). Concretely:

- `message` actions are authored as the `graith:system` sender
  (`systemSenderID`, `notify.go:56`), exactly like PR/CI notices — non-replyable
  and clearly automated.
- `session` and `scenario` actions are **parented to the orchestrator session**
  (`SystemKindOrchestrator`). The spawned session's `ParentID` is the
  orchestrator, so it appears in the session tree, is addressable by
  `gr msg send --children` from the orchestrator, and is torn down with normal
  lifecycle rules. If the orchestrator is disabled in config, `session`/
  `scenario` actions are rejected at config-validation time with a clear error
  (they need an owner).
- `command` actions have the weakest natural confinement and need an **explicit
  contract** (see below) — they are the one place a trigger runs arbitrary code
  with the daemon's own identity.

This keeps scheduled *session/scenario/message* work inside the same trust
boundary as everything else: fired sessions are owned (parented to the
orchestrator), not orphaned, and inherit the normal per-session sandbox.
`command` actions are the exception that the next section pins down.

#### `command` action trust boundary (explicit, fail-closed)

The daemon executes `command` actions with **its own environment and
privileges**, and — unlike a session — a command has no agent identity to merge
per-agent sandbox config from and no session/worktree to key a nono profile on.
The design must therefore be explicit rather than hand-wave "same as agents":

- **A dedicated command-action sandbox profile.** Reuse the `internal/sandbox`
  backend (`Wrap`) but with a purpose-built scope: read+write on the action's
  `repo` root, a minimal env allowlist (`PATH`/`HOME`/`GRAITH_*` only, no
  inherited secrets), network blocked unless the trigger opts in, and a
  process-group kill on cancel. This is a new profile shape (repo-rooted,
  session-less), not the existing session-worktree profile.
- **Fail closed on no enforcement.** Session `Create` may return
  `sandboxed=false` when `[sandbox].enabled=false` (`daemon.go` `resolveSandbox`).
  A `command` trigger must **not** silently fall through to unsandboxed
  execution: either the sandbox is enabled and the backend can enforce (else the
  trigger is rejected at validation), or the operator sets an explicit
  `action.trusted = true` acknowledging the command runs as unconfined
  daemon-user code. There is no implicit unconfined path.
- **Bounds.** A per-command `timeout` (default e.g. 5m) with context
  cancellation, an output cap (truncate to a `prCommentMaxBody`-style limit,
  `prwatch.go:34`), and process-group termination on daemon shutdown.
- **Validation.** A `command` action with no `repo`, or a `repo` outside
  `allowed_repo_paths` (`cfg.RepoPathAllowed`), is rejected at config load.

`notify.go:255` (`exec.Command("sh", "-c", command)` with inherited env) is
**not** the model — it is an unsandboxed local notification hook and is cited
only for the mechanical exec pattern, not as a confinement design.

### 4. Delivery

`[trigger.action.deliver]` routes output. Any combination may be set; each is
best-effort and independent (a store-write failure doesn't suppress the inbox
message):

- `inbox = "orchestrator"` (or any session name) — deliver via
  `notifyFromDaemon(sessionID, body)` (`notify.go:73`), which publishes to the
  session's inbox and, via `notifyInbox`/`resumeForInbox` (`notify.go:125`),
  **auto-resumes a stopped target**. `"orchestrator"` resolves to the current
  `SystemKindOrchestrator` session. **Auto-resume is broader than delivery** —
  any configured inbox name will *wake* an ordinary stopped agent on the clock,
  which may be surprising. So: soft-deleted targets are rejected (never woken),
  and auto-resume defaults **on only for `inbox = "orchestrator"`**; for any
  other named session, delivery publishes to the inbox but does not resume
  unless the trigger sets `deliver.wake = true`. A report waking the
  orchestrator is intended; a timer silently restarting a paused-for-a-reason
  agent is not.
- `topic = "pr-reports"` — publish to a pub/sub topic via `messages.Publish`.
  No PTY notification (topics are broadcast; subscribers pull), matching
  existing topic semantics.
- `store = "reports/pr/{date}.md"` — write a durable store doc via
  `store.Put(storePath, key, body)` (`internal/store/store.go:202`), or
  `store.Append` for `.jsonl` logs (`store.go:237`), after an idempotent
  `store.Init(storePath)` (`store.go:26`) — the same sequence the scenario
  manifest write uses (`scenario.go`). The key is templated (see below) and must
  pass `ValidateKey` (`store.go:90`, no traversal/glob). Store scoping: a key
  targets the **repo-scoped** store (`StorePath`, `store.go:142`) only when the
  action has a `repo` (i.e. `command`/`session` actions); `scenario` and
  `message` actions have no single repo, so they **require** a `shared:` prefix
  targeting the shared store (`SharedStorePath`, `store.go:149`) and a plain key
  is rejected at validation. The repo path is canonicalized before hashing so
  different spellings resolve to the same namespace.

For a `session` action, "delivery" is subtly different: the daemon can't capture
a long-running agent's final answer synchronously. Two sub-cases:

- If `deliver` is set, the daemon **injects the delivery instruction into the
  prompt** — e.g. appends "When done, post your summary to the orchestrator
  inbox with `gr msg send orchestrator` and write it to
  `gr store put reports/pr/2026-07-11.md`" (the daemon expands `{date}` into the
  concrete key before injecting, so the agent isn't asked to run shell
  substitutions). The agent performs delivery using its own token-authenticated
  `gr` access. This is **best-effort and unverified**: a non-compliant or failing
  agent silently produces no delivery, and the daemon can't confirm it happened.
  `gr trigger status` for a `session` action therefore reports "spawned OK", not
  "report delivered" — call this out so the observability contract isn't
  overstated (see per-action `last run result` below).
- If `deliver` is unset, the spawned session just runs; its work product is
  whatever it commits/pushes/messages on its own.

**Per-action delivery validity.** Not every `deliver` field applies to every
action, and validation enforces it: `command` delivers captured output
(daemon-enforced, all three sinks valid); `session` delivery is the prompt
injection above (best-effort); `message` has no separate delivery (the `body`
*is* the payload — a `deliver` block on a `message` action is rejected); and a
`scenario` action produces no single output to route, so a `deliver` block on it
is rejected (its sessions deliver their own work). This makes the combinations
explicit rather than implying every pairing works.

For `command` actions the daemon **does** capture stdout/stderr directly and
delivers the captured text (truncated to a sane cap, like `prCommentMaxBody` at
`prwatch.go:34`), since a command is bounded and synchronous.

**Template variables** available in `deliver.store`/`deliver.topic`,
`message.body`, and the injected `session` delivery instruction: `{name}`
(trigger name), `{date}` (`2026-07-11`), `{datetime}` (RFC3339), `{fire_time}`
(the scheduled fire instant). These need a **new, trigger-specific variable set
and expander** — the existing `config.TemplateVars`/`Expand`
(`internal/config/template.go`) is a *fixed struct* (`Username`, `SessionID`,
`SessionName`, `WorktreePath`, `Model`, …) whose `Expand` **errors on any
unknown variable name**, so `{name}`/`{date}`/`{fire_time}` cannot pass through
it. Follow its style (same `{token}` syntax, same unknown-token-is-an-error
discipline) but with a distinct variable map. Values are computed at fire time
from the daemon clock (and the resolved timezone for `{date}`/`{datetime}`).

### 5. Missed-run and concurrency policy

`[trigger.policy]` with safe defaults:

These semantics only hold up if a *fire* is a durable, identifiable event
rather than an in-memory tick. So the design rests on a small **run-state
machine** with two persisted facts per trigger (State model):

- `LastScheduledFireAt` — the scheduled instant of the last fire the daemon
  **committed to** (not when the action finished). It is written **atomically
  before dispatch**, inside the same `triggerState.mu`-held critical section
  that advances the cursor, and persisted before the action goroutine launches.
  This gives an **at-most-once** guarantee per scheduled instant: if the daemon
  crashes after committing but before/ during the action, on restart it sees the
  fire already recorded and does **not** replay it. (At-least-once is not offered
  — actions are not assumed idempotent, so a crash mid-action loses that run
  rather than risking a duplicate.)
- `Fingerprint` — a hash of the trigger's `schedule` + `action` + `policy`. A
  config edit that keeps the same `name` but changes any of these produces a new
  fingerprint, which **resets** the cursor, `LastScheduledFireAt`, `Paused`, and
  counters. This closes the name-only-diff hole where an old cursor or stale
  `LastScheduledFireAt` would be applied to a materially different trigger.

**Missed runs — `catch_up` (default `false`).** When the daemon is down across
one or more scheduled fire times:

- `catch_up = false` (default): **do not backfill**. On startup/reload, compute
  the next fire fresh (cron: `Next(now)`; interval: `LastScheduledFireAt + N`,
  advanced past `now`) and fire only on future ticks. A daemon down for three
  days does **not** fire three daily reports on boot — a stale report burst is
  noise, not signal.
- `catch_up = true`: if the most recent scheduled fire (`Next` from
  `LastScheduledFireAt`) is now in the past, fire **once** immediately on
  startup, then resume normal scheduling. Never replay a backlog — at most one
  catch-up fire per trigger per startup. This mirrors `RunPurgeLoop`'s "one sweep
  shortly after startup to catch windows that elapsed while the daemon was down"
  (`daemon.go:3031`).

**Overlap — `overlap` (default `"skip"`).** When a fire is due but the previous
run of the *same trigger* is still in flight:

- `overlap = "skip"` (default): skip this fire, log it, advance to the next.
- `overlap = "allow"`: fire regardless (concurrent runs permitted).
- `overlap = "queue"` (**deferred to v2**): a single-slot coalesced defer. This
  is the fiddliest mode to get right under `-race` (it needs the same
  completion-tracking wiring as below), and `skip`/`allow` cover the motivating
  cases, so v1 ships only those two. `queue` in config is rejected in v1 with a
  "not yet supported" error rather than silently treated as `skip`.

**What "in flight" means — and its restart-safety.** For a `command` action,
in-flight is a per-trigger flag under `triggerState.mu`, set when the exec
goroutine starts and cleared via `defer` — the `prWatch.mu`-independent-of-`sm.mu`
discipline (`prwatch.go:53`). Commands are bounded and don't survive a daemon
restart, so an in-memory flag is sufficient.

`session` and `scenario` actions are different: the spawned work **outlives the
daemon**, so an in-memory in-flight flag is *not* restart-safe — after a restart
the trigger's link to its spawned session is gone and the next tick could launch
a duplicate despite `overlap = "skip"`. Two options, and v1 takes the simpler
one explicitly:

- **v1 (chosen): executor-call overlap.** A `session`/`scenario` action is
  considered "complete once creation succeeds" — the in-flight window is just
  the `Create`/scenario-start call, not the lifetime of the spawned work. So
  `overlap` for these actions guards against two *starts* racing on the same
  tick, not against a long-running spawned session. This is honest and
  race-free without persistence. The doc states this plainly so the guarantee
  isn't oversold.
- **v2 (noted, not built): lifetime overlap.** To make `overlap = "skip"` mean
  "don't spawn a second while the first spawned session is still running", the
  design would persist an **active-run record** (`RunID`, spawned `SessionID`/
  `ScenarioID`, `startedAt`) and, on startup, **reconcile** it against
  `State.Sessions`/`State.Scenarios` (still running ⇒ still in flight; gone ⇒
  clear). Completion would be learned from the existing session-stop path. This
  is the correct long-term model but is deferred; v1's executor-call semantics
  are the documented behaviour.

**Global concurrency cap.** Per-trigger `skip` does not bound *aggregate* load —
many distinct triggers can come due on the same tick and all spawn at once. A
daemon-wide cap (config `[trigger] max_concurrent`, default e.g. 4) bounds
simultaneously-running action goroutines; fires that would exceed it are treated
as `skip` for that tick (logged), never queued unboundedly.

### 6. The firing loop

`RunTriggerLoop(ctx)` in a new `internal/daemon/trigger.go`, launched from
daemon startup alongside the others (`daemon.go:5790`):

```go
go sm.RunTriggerLoop(ctx)
```

Structure, modeled directly on `RunPRWatchLoop` (`prwatch.go:82`) and
`RunPurgeLoop` (`daemon.go:3034`):

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

func (sm *SessionManager) runTriggerTick(ctx context.Context, now time.Time) {
    cfg := sm.Config()
    if len(cfg.Triggers) == 0 {
        return
    }
    // dueTriggers ATOMICALLY (under triggerState.mu) selects due triggers,
    // advances each cursor + records LastScheduledFireAt, and marks in-flight —
    // all before returning. This is essential: with a 1s tick, if the cursor
    // weren't advanced synchronously here, the same trigger would re-match
    // now >= nextFire on every tick until the async goroutine got around to it,
    // and double-fire. The goroutine only runs the (slow) action + delivery.
    for _, due := range sm.dueTriggers(now) {
        go sm.fireTrigger(ctx, due) // off-lock; runs action, delivers, clears in-flight
    }
}
```

The cursor advance and `LastScheduledFireAt` write happen **inside**
`dueTriggers` under `triggerState.mu`, not in the async `fireTrigger` — the
worked example's "advance to tomorrow" is this synchronous step. `fireTrigger`
takes the loop's `ctx` so a `command` action can be cancelled on daemon
shutdown (session/scenario creation intentionally detaches once the normal
session lifecycle takes ownership, matching how scenario start already outlives
the request ctx).

**Locking discipline** (identical philosophy to prwatch): the trigger loop has
its own mutex `triggerState.mu`, independent of `sm.mu`. Schedule cursors,
in-flight flags, and pause state live under `triggerState.mu`. Action dispatch
(`sm.Create`, the shared scenario-start service, sandboxed command exec,
`store.Put`) runs in a detached goroutine holding neither lock, so a slow action
never blocks `gr list` or the tick. State snapshots (session lookups for delivery targets) follow the
`RLock → copy → unlock → work` pattern used throughout (`prwatch.go:150`).

**Why a 1s tick with 1-minute cron granularity?** Cron's finest unit is a
minute; a 1s tick guarantees we never miss a minute boundary and keeps fire
latency under a second without a per-trigger timer forest. The tick itself does
almost nothing when nothing is due (a map scan comparing `now` to each cached
`nextFire`). This matches `prWatchTick = 15s` (`prwatch.go:31`) scaled down for
minute-accuracy.

## State model

A new persisted collection, mirroring how `ScenarioState` was added
(`internal/daemon/state.go`, migration bump):

```go
// Persisted in state.json — only what must survive restart.
type TriggerRuntimeState struct {
    Name                string     `json:"name"`
    Fingerprint         string     `json:"fingerprint"`            // hash(schedule+action+policy); mismatch ⇒ reset
    LastScheduledFireAt *time.Time `json:"last_scheduled_fire_at,omitempty"` // written before dispatch (at-most-once)
    LastError           string     `json:"last_error,omitempty"`
    Paused              bool       `json:"paused,omitempty"`       // gr trigger pause
    RunCount            int        `json:"run_count,omitempty"`
    History             []RunRecord `json:"history,omitempty"`     // bounded ring (last N runs)
}

// RunRecord is one entry in the bounded history shown by `gr trigger status`.
type RunRecord struct {
    ScheduledAt time.Time `json:"scheduled_at"`
    Cause       string    `json:"cause"`  // "schedule" | "catch_up" | "manual"
    Result      string    `json:"result"` // per action type — see below
}
```

`LastScheduledFireAt` (not "last run finished") is the missed-run / at-most-once
anchor, and drives interval `nextFire`. `History` is a **bounded ring** (last
~20 runs) so `gr trigger status`'s "recent history" promise is backed by real
data, not just `LastError` + `RunCount`. `Result` is defined **per action type**
so the CLI contract is unambiguous: `command` → exit code + truncated output;
`session` → "spawned `<id>`" (**not** "delivered" — session delivery is
best-effort, §4); `scenario` → "started N sessions"; `message` → "published".

Keyed by trigger `name` in a `map[string]*TriggerRuntimeState` on `State`
(`internal/daemon/state.go:210`, alongside `Sessions`/`Scenarios`), initialized
in `NewState`/`LoadState` like the other collections. The
**definition** (schedule, action, policy) is *not* persisted here — it lives in
`config.toml` and is the source of truth. Only mutable runtime facts persist.
On load, runtime rows for triggers no longer in config are pruned (like
`prunePRWatchState`, `prwatch.go:322`); rows for new triggers are created lazily.

Everything else — parsed `cron.Schedule`, cached `nextFire`, in-flight flag — is
in-memory in a `triggerState` struct (like `prWatchState`, `prwatch.go:53`),
rebuilt on every daemon start and config reload. This keeps `state.json` small
and avoids persisting derived data.

A state migration bumps `CurrentStateVersion` from 14 to 15
(`internal/daemon/state.go:19`) and appends a `migrateV14ToV15` entry to the
`migrations` map (`state.go:315`); it's a no-op migration since the field
defaults to an empty map, exactly like the existing `migrateV13ToV14` chain
link.

## Protocol & handler

New control messages in `internal/protocol/messages.go`, following the
`ScenarioListMsg` / `ScenarioStatusMsg` shapes (`messages.go:625`):

```go
type TriggerListMsg struct{}
type TriggerStatusMsg struct{ Name string `json:"name"` }
type TriggerRunMsg    struct{ Name string `json:"name"` }
type TriggerPauseMsg  struct{ Name string `json:"name"`; Pause bool `json:"pause"` }
```

Handler cases in `internal/daemon/handler.go` next to the scenario cases
(`handler.go:1428`), each decoded with the existing
`decodePayload[T](msg, sendControl, ...)` helper and replying via
`sendControl(...)`.

**Authorization.** `list`/`status` are read-only and available to any session
or the human CLI (like `scenario_status`/`scenario_list`). `run`/`pause`/
`resume` are mutating. Note the owner model differs from scenarios:
`authorizeScenarioOp` authorizes relative to a *specific scenario's*
`OrchestratorID` (`checkScenarioOp`, `auth.go:204`), but a config-defined
trigger has **no per-trigger owner** — the daemon owns it. So the trigger rule
is: the caller must be the **system orchestrator session or a descendant of it**
(`isDescendantOf` against the current `SystemKindOrchestrator` ID), a new
`authorizeTriggerOp` helper rather than a reuse of the scenario check.
Unauthenticated (human CLI) callers are always permitted — same posture as
scenario ops.

This is deliberately a **broad, durable privilege**: any descendant of the
system orchestrator can `run`/`pause`/`resume` *any* trigger, and `pause`
persists across restarts. That means an agent (not just a human) can durably
disable a config-defined trigger — including one with a security or hygiene
purpose. This is accepted for v1 because (a) creation still requires editing
`config.toml`, so agents can't *author* self-scheduling triggers, and (b) the
same trust model already governs scenario stop/delete. It is called out here so
the "config is the only durable authoring surface" claim isn't read as "agents
can't durably affect triggers at all" — they can pause them. A future
`allow_agent_control = false` per-trigger flag could lock a sensitive trigger to
human-only control.

Because the daemon can be reached remotely (Tailscale, `[remote]`), the new
message types must also be added to the remote allow-matrix
(`remoteAllowed` in `internal/daemon/authmatrix.go:121`) — `trigger_list`/
`trigger_status` as read-only-allowed, and `trigger_run`/`trigger_pause` gated
to the appropriate role. Omitting them fails closed (remote requests rejected),
which is the safe default but would silently break remote CLI use.

## Worked example: daily PR report

Given the `[[trigger]]` config above, at 09:00 Europe/London the daemon:

1. `dueTriggers` (under `triggerState.mu`) sees `daily-pr-report` is due
   (`now >= nextFire`), not paused, not in flight (overlap=skip).
2. **Atomically, in that same critical section**, it advances `nextFire` to
   tomorrow 09:00 via `schedule.Next(now)`, writes `LastScheduledFireAt = 09:00`
   (persisted before the goroutine launches — the at-most-once commit point),
   marks the trigger in-flight, and returns it; then `runTriggerTick` spawns
   `go sm.fireTrigger(ctx, due)`.
3. `fireTrigger` dispatches the `session` action: `sm.Create(...)` (16-arg call)
   with the resolved orchestrator as `parentID`, headless `rows/cols = 80/24`,
   and a `Prompt` that is the configured `prompt` **plus** an injected delivery
   instruction with `{date}` already expanded ("post to the orchestrator inbox
   and write to `reports/pr/2026-07-11.md`").
4. The spawned session runs, summarises the PRs, and delivers using its own
   `gr msg send orchestrator ...` and `gr store put reports/pr/2026-07-11.md ...`.
   The orchestrator's inbox message triggers `notifyInbox`, auto-resuming it if
   idle.
5. Under v1 executor-call overlap, the in-flight flag clears when `Create`
   returns (not when the session finishes); a `RunRecord{Cause:"schedule",
   Result:"spawned <id>"}` is appended and `RunCount` bumped.

If the daemon was down at 09:00 and `catch_up=false`, nothing fires late — the
next 09:00 handles it. With `catch_up=true`, one report fires on the next
startup; because `LastScheduledFireAt` was persisted at commit time, a crash
between step 2 and step 3 does **not** re-fire the same 09:00 on restart.

## Interaction with #593 (file-watch triggers)

#593 adds a `watch` source: `[trigger.watch]` with a path/glob, debounce, and a
session-worktree scope. It reuses **verbatim**:

- the `ActionConfig` vocabulary (command / session / scenario / message),
- the `DeliverConfig` routing,
- the `TriggerPolicy` overlap semantics (file-watch especially needs
  `overlap = "skip"` + debounce to avoid re-trigger storms — the issue's
  "feedback loops" concern),
- the `fireTrigger` dispatcher and ownership/authorization rules.

The only new machinery in #593 is the event *source*: an `fsnotify` watcher
(already a dependency — `go.mod`) feeding a debounced channel that calls the same
`fireTrigger`. Because both sources funnel through one executor, the action
surface, delivery, sandbox rules, and CLI (`gr trigger list` shows both kinds)
stay unified — which is the whole point of the shared framework the two issues
agreed on.

## Consensus

Reviewed by a 3-judge tribunal (independent models) against the codebase. All
three confirmed the grounding is real — ~25 `file:line` citations spot-checked
and accurate to within the doc's own drift disclaimer — that all five open
questions are answered with justified defaults, and that the #593 factoring is
correctly scoped (shared executor/policy, watcher deferred). The following gaps
were raised and have been **incorporated into the design above**:

- **Run-state machine (biggest).** A fire is now a durable, identifiable event:
  `LastScheduledFireAt` is committed atomically *before* dispatch (at-most-once
  crash guarantee), intervals anchor to it (restart-safe), and a `Fingerprint`
  resets a trigger whose definition changed under the same name. (§5, State
  model.)
- **`session`/`scenario` overlap restart-safety.** v1 uses honest
  executor-call overlap ("complete once creation succeeds"); the durable
  active-run + startup-reconciliation model is documented as v2. (§5.)
- **`command` trust boundary.** Now an explicit, fail-closed contract
  (dedicated repo-rooted sandbox profile, env allowlist, timeout, output cap,
  process-group kill; no implicit unconfined path). `notify.go:255` demoted to
  "exec pattern only, not a security model". (§3.)
- **`scenario` dispatch reality.** Requires extracting a daemon-reachable
  scenario-file loader and resolving a live orchestrator caller ID — not a bare
  `sm.StartScenario` call. (§3.)
- **`Create` call shape.** Corrected to the real 16-arg signature with no
  `Background` flag; headless sessions get default 80×24 dimensions. (§3.)
- **Template variables.** A new trigger-specific expander (the existing
  `config.Expand` rejects unknown vars); the `$(date)` example replaced with a
  daemon-expanded `{date}`. (§4.)
- **Smaller items folded in:** `*bool`/`*ScheduleConfig` for clean
  presence/exclusivity, `enabled`-vs-`Paused` precedence, per-action delivery
  validity + best-effort labelling, repo-less store scoping (`shared:`),
  auto-resume `wake` gating, global `max_concurrent` cap, bounded run `History`,
  manual-`run` semantics, cron/DST/interval-zero edge cases, and a precise
  `authorizeTriggerOp` owner model (with the `pause` durable-privilege caveat
  called out).

**Deferred to v2 (explicitly out of scope, noted in-line):** `overlap="queue"`,
lifetime-based session/scenario overlap with durable reconciliation,
CLI-authored triggers (`gr trigger add`), configurable `tick_interval`, and a
per-trigger `allow_agent_control=false` lock. Delivery failures in v1 are logged,
not retried.

Net disposition: **approve with the above revisions incorporated** — the
config-first, daemon-fired, unified-with-#593 architecture is sound; the
revisions harden the runtime semantics and the command trust boundary before
implementation.

## Other Notes

### Answers to the issue's open questions (summary)

| Open question | Recommendation |
|---------------|----------------|
| Schedule syntax | **Both.** `every = "15m"` (Go duration) or `cron = "0 9 * * *"` (5-field + `@`-descriptors, timezone-aware). Exactly one required. |
| Where defined | **`config.toml` `[[trigger]]`** as source of truth (reviewable, restart-safe, matches `[pr_watch]`/`[git_pull]`); `gr trigger` CLI for list/status/run/pause. Defer CLI authoring to v2. |
| Action vocabulary (v1) | **command, session, scenario, message.** "Report" = a session/command action + `deliver` routing, not a separate type. |
| Delivery | **`deliver` block**: `inbox` (via `notifyFromDaemon`, auto-resumes), `topic` (pub/sub), `store` (durable doc, templated key). Default report target = orchestrator inbox. |
| Missed-run policy | **`catch_up=false` default** — never backfill a burst; at most one catch-up fire on startup when `catch_up=true` (mirrors `RunPurgeLoop`). |
| Concurrency policy | **`overlap="skip"` default** — skip if previous run in flight; `allow` as the v1 alternative (`queue` deferred to v2). A daemon-wide `max_concurrent` bounds aggregate fan-out. |

### References

- `internal/daemon/prwatch.go:82` — `RunPRWatchLoop`, the model for a
  config-gated, off-request-path daemon loop with an independent mutex, off-lock
  work, per-item scheduling (`schedulePoll`), and state pruning.
- `internal/daemon/daemon.go:3034` — `RunPurgeLoop`, the model for
  startup-sweep-then-ticker and missed-window handling.
- `internal/daemon/daemon.go:5790` — where background loops are launched.
- `internal/daemon/notify.go:73` — `notifyFromDaemon`, the inbox-delivery +
  auto-resume primitive; `systemSenderID` (`notify.go:56`) for daemon-authored
  messages.
- `internal/daemon/scenario.go` / `internal/cli/scenario.go` — the closest
  prior art for a config/CLI/daemon-state feature with authorization
  (`StartScenario`, `scenarioCmd`, `authorizeScenarioOp`).
- `internal/daemon/handler.go:1428` — scenario control-message dispatch;
  `decodePayload[T]` pattern for new message types.
- `internal/protocol/messages.go:598` — `ScenarioStartMsg` et al., the shape to
  follow for `Trigger*Msg`.
- `internal/config/config.go:24` — top-level `Config`; `:270` `PRWatchConfig`
  as a rich per-feature config-section example.
- `internal/store/store.go:202` — `Put` / `:237` `Append` for report delivery.
- `internal/config/template.go` — template-variable expansion for
  `deliver`/`body`.
- `go.mod` — `fsnotify` already vendored (for #593); `robfig/cron/v3` to be
  added for cron parsing.
- Issue #593 — the file-watch sibling that reuses this action vocabulary.

### Implementation Notes

| File | Change |
|------|--------|
| `go.mod` / `go.sum` | Add `github.com/robfig/cron/v3` (parser + `Next()` only). |
| `internal/config/config.go` | Add `Triggers []TriggerConfig` (+ `[trigger] max_concurrent`) to `Config`; add `TriggerConfig` (`*bool Enabled`, `*ScheduleConfig`), `ScheduleConfig`, `WatchConfig` (nil in v1), `ActionConfig`, `DeliverConfig`, `TriggerPolicy`; validation (exactly-one-source, one-action-type, cron/every parse, `every>0`, `queue`-rejected-in-v1, repo allow-list, orchestrator-required-for-session/scenario, per-action delivery validity, `shared:`-required-for-repo-less store). `Default()` returns empty. |
| `internal/config/trigger_template.go` | New trigger-specific template var set + expander (`{name}`/`{date}`/`{datetime}`/`{fire_time}`) — NOT `config.Expand` (rejects unknown vars). |
| `internal/daemon/scenario_loader.go` | Extract scenario-TOML loading out of `internal/cli/scenario.go` into a shared, daemon-reachable package so the trigger executor can build a `ScenarioStartMsg` and resolve a live orchestrator caller ID. |
| `internal/daemon/trigger.go` | New: `RunTriggerLoop`, `runTriggerTick(ctx,…)`, `dueTriggers` (atomic cursor advance + `LastScheduledFireAt` commit + in-flight mark under `triggerState.mu`), `fireTrigger(ctx,…)`, per-type executors (`fireCommand`/`fireSession`/`fireScenario`/`fireMessage`), delivery routing (+ `wake` gating), `triggerState` struct + mutex, `initTriggerSchedules` (fingerprint diff, catch_up), `max_concurrent` semaphore, bounded `History` ring, prune. |
| `internal/sandbox` | Command-action profile: repo-rooted, session-less, minimal env allowlist, network-off-by-default, process-group kill; fail-closed unless enforced or `action.trusted`. |
| `internal/daemon/daemon.go` | `go sm.RunTriggerLoop(ctx)` in startup (near :5790). |
| `internal/daemon/state.go` | Add `TriggerRuntime map[string]*TriggerRuntimeState` (with `Fingerprint`, `LastScheduledFireAt`, `History`) to `State`; migrate `CurrentStateVersion` 14→15 (no-op, documented comment). Prune + fingerprint-reset on load. |
| `internal/daemon/handler.go` | Handle `trigger_list`/`trigger_status`/`trigger_run`/`trigger_pause` with read-only vs orchestrator-or-descendant auth. |
| `internal/daemon/auth.go` | `authorizeTriggerOp` (reuse `checkScenarioOp` logic, `auth.go:204`). |
| `internal/daemon/authmatrix.go` | Add `trigger_*` message types to `remoteAllowed` (`:121`) so remote CLI works and fails closed otherwise. |
| `internal/protocol/messages.go` | `TriggerListMsg`, `TriggerStatusMsg`, `TriggerRunMsg`, `TriggerPauseMsg`; a `TriggerInfo` result type. |
| `internal/cli/trigger.go` | New `triggerCmd` + `list/status/run/pause/resume` subcommands; `--json`; registered on `rootCmd`. |
| `internal/config/default_config.toml` | Document `[[trigger]]` with a commented daily-PR-report example. |
| `docs/site/` | New `triggers.md` page; cross-link from patterns/orchestrator docs. |
| `AGENTS.md` | Document the trigger model, action vocabulary, and `gr trigger` commands. |

### Testing

**Unit tests** (Scots fixtures — trigger names like `dreich-sweep`,
`braw-report`):
- Schedule parsing: valid cron, `@`-descriptors, intervals, timezones; reject
  both-set / neither-set / unparseable.
- `Next()` computation across a DST boundary (zone-aware).
- `every > 0` rejected for zero/negative; `queue` overlap rejected in v1.
- Missed-run: `catch_up=false` never backfills; `catch_up=true` fires exactly
  once on startup when the last fire was missed, zero times when not.
- **At-most-once:** `LastScheduledFireAt` written before dispatch means a
  simulated crash between commit and action does not re-fire on reload.
- **Interval anchoring:** `nextFire = LastScheduledFireAt + N` survives restart;
  a restart mid-interval does not reset the phase.
- **Fingerprint reset:** a same-name trigger with a changed schedule/action/
  policy resets cursor + `LastScheduledFireAt` + `Paused` + counters.
- Overlap: `skip` suppresses a fire while in-flight; `allow` permits concurrent;
  executor-call in-flight window for `session`/`scenario` (clears when `Create`
  returns).
- `max_concurrent`: fires exceeding the daemon-wide cap are skipped (logged).
- Action validation: `session`/`scenario` rejected when orchestrator disabled;
  `command` rejected without an allowed `repo`; `deliver` rejected on
  `message`/`scenario`; repo-less store key without `shared:` rejected.
- Template expander: `{date}`/`{name}`/`{fire_time}` expand; an unknown `{var}`
  errors (parity with `config.Expand` discipline).
- Delivery routing: inbox/topic/store each invoked; store-write failure doesn't
  suppress inbox; `wake` off ⇒ non-orchestrator stopped target not resumed;
  soft-deleted target never woken.
- `enabled=false` overrides persisted `Paused`; `resume` on a config-disabled
  trigger rejected.
- Manual `run`: records `Cause="manual"`, does not shift `LastScheduledFireAt`/
  `nextFire`.
- State prune: runtime rows for removed triggers dropped on load.
- Config reload diff: unchanged (same-fingerprint) triggers keep `nextFire`;
  added scheduled; removed dropped.

**Integration tests** (`internal/integration/` — spawn a real daemon):
- A trigger with `every = "1s"` and a `message` action delivers into a target
  session's inbox within a couple ticks.
- A `session` action spawns a background session parented to the orchestrator
  and it appears in `gr list`.
- `gr trigger list`/`status`/`run`/`pause`/`resume` round-trip over the control
  protocol; `pause` survives a `gr daemon restart`.
- Missed-run across a simulated daemon restart with `catch_up` on/off.

**All tests must pass with `-race`.** The independent-mutex + off-lock-dispatch
design (mirroring prwatch) is the thing `-race` is there to police.
