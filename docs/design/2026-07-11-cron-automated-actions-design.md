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
    Enabled  bool            `toml:"enabled"`  // default true; see UnmarshalTOML note
    Schedule ScheduleConfig  `toml:"schedule"` // #592 source (this doc)
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
rejects both or neither). In v1 `Watch` is always nil.

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
  `time.ParseDuration`. Anchored to the trigger's first-seen time (daemon start
  or config reload), then every `N` thereafter. Best for "roughly every hour"
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
(`[pr_watch]` in `config.PRWatchConfig` at `internal/config/config.go:270`,
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

### 3. Action vocabulary (v1 scope)

The issue lists five candidates: command, scenario, session, message, report.
Recommended v1 `action.type` values — **command, session, scenario, message** —
with **report modelled as a composition** rather than a distinct type:

| Type | What it does | In-process call |
|------|--------------|-----------------|
| `command` | Run a shell command (via `mvdan.cc/sh` or the configured shell) in `repo`'s context, capture stdout/stderr, deliver the output. | new `runCommandAction` (model on `sendNotification`'s `exec.Command`, `notify.go:253`), wrapped in the sandbox like an agent process |
| `session` | Spawn a background session with `prompt`/`agent`/`model` in `repo`. Owned by the orchestrator (parent = orchestrator session) so it's addressable and lifecycle-managed. | `sm.Create(...)` (`daemon.go` `Create()`), with `Prompt` set and `Background: true` |
| `scenario` | Start a named scenario from `~/.config/graith/scenarios/`. | `sm.StartScenario(...)` (`internal/daemon/scenario.go`) |
| `message` | Publish a fixed `body` to an inbox or topic. | `notifyFromDaemon` (inbox) / `messages.Publish` (topic) |

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
- `command` actions run inside the sandbox using the same backend and profile
  construction as agent processes (`internal/sandbox`), scoped to the action's
  `repo`. A command action with no `repo`, or a `repo` outside
  `allowed_repo_paths`, is rejected at validation.

This keeps scheduled work inside the same trust boundary as everything else:
the sandbox is the enforcement layer, and fired sessions are owned, not
orphaned.

### 4. Delivery

`[trigger.action.deliver]` routes output. Any combination may be set; each is
best-effort and independent (a store-write failure doesn't suppress the inbox
message):

- `inbox = "orchestrator"` (or any session name) — deliver via
  `notifyFromDaemon(sessionID, body)` (`notify.go:73`), which publishes to the
  session's inbox **and auto-resumes it if stopped** (`resumeForInbox`,
  `notify.go:125`). This is the recommended default for reports: the
  orchestrator wakes up, reads the report, and can act on it. `"orchestrator"`
  resolves to the current `SystemKindOrchestrator` session.
- `topic = "pr-reports"` — publish to a pub/sub topic via `messages.Publish`.
  No PTY notification (topics are broadcast; subscribers pull), matching
  existing topic semantics.
- `store = "reports/pr/{date}.md"` — write a durable store doc via
  `store.Put(storePath, key, body)` (`internal/store/store.go:202`), or
  `store.Append` for `.jsonl` logs (`store.go:237`), after an idempotent
  `store.Init(storePath)` (`store.go:26`) — the same sequence the scenario
  manifest write uses (`scenario.go`). The key is templated (see below) and must
  pass `ValidateKey` (`store.go:90`, no traversal/glob). Defaults to the
  repo-scoped store (`StorePath`, `store.go:142`); a `shared:` prefix targets the
  shared store (`SharedStorePath`, `store.go:149`).

For a `session` action, "delivery" is subtly different: the daemon can't capture
a long-running agent's final answer synchronously. Two sub-cases:

- If `deliver` is set, the daemon **injects the delivery instruction into the
  prompt** — e.g. appends "When done, post your summary to the orchestrator
  inbox with `gr msg send orchestrator` and write it to
  `gr store put reports/pr/$(date +%F).md`." The agent performs delivery using
  its own (token-authenticated) `gr` access. This is exactly the issue's daily-
  PR-report example and keeps the daemon out of the business of scraping agent
  output.
- If `deliver` is unset, the spawned session just runs; its work product is
  whatever it commits/pushes/messages on its own.

For `command` actions the daemon **does** capture stdout/stderr directly and
delivers the captured text (truncated to a sane cap, like `prCommentMaxBody` at
`prwatch.go:34`), since a command is bounded and synchronous.

**Template variables** available in `deliver.store`/`deliver.topic` and in
`message.body`: `{name}` (trigger name), `{date}` (`2026-07-11`), `{datetime}`
(RFC3339), `{fire_time}` (the scheduled fire instant). These reuse the existing
template-expansion helper style (`internal/config/template.go`). `{date}` etc.
are computed at fire time from the daemon clock.

### 5. Missed-run and concurrency policy

`[trigger.policy]` with safe defaults:

**Missed runs — `catch_up` (default `false`).** When the daemon is down across
one or more scheduled fire times (or a trigger's next-fire is in the past on
startup because state was persisted before a crash):

- `catch_up = false` (default): **do not backfill**. On startup/reload, compute
  `Next(now)` fresh and fire only on future ticks. A daemon down for three days
  does **not** fire three daily reports on boot. This is almost always what you
  want — a stale report burst is noise, not signal.
- `catch_up = true`: if the *most recent* scheduled fire was missed, fire
  **once** immediately on startup, then resume normal scheduling. Never fire N
  times to replay a backlog — at most one catch-up fire per trigger per startup.
  This mirrors `RunPurgeLoop`'s "one sweep shortly after startup to catch
  windows that elapsed while the daemon was down" (`daemon.go:3031`), which is
  the established graith pattern for missed-window handling.

Missed-run detection needs `LastRunAt` to survive restarts, so it is **persisted
in daemon state** (see State model). Everything else (next-fire cursor,
in-flight flag) is in-memory and recomputed on load.

**Overlap — `overlap` (default `"skip"`).** When a fire is due but the previous
run of the *same trigger* is still in flight:

- `overlap = "skip"` (default): skip this fire, log it, advance to the next.
  Right for reports and sweeps — if the 09:00 report is somehow still running at
  10:00, don't stack a second.
- `overlap = "allow"`: fire regardless (concurrent runs permitted). For cheap,
  independent `message` actions.
- `overlap = "queue"`: run at most one pending fire after the current finishes
  (coalesced — not an unbounded queue). At most one deferred run.

In-flight tracking is a per-trigger flag under the trigger loop's own mutex,
set when an action goroutine starts and cleared (via `defer`) when it returns —
the same discipline `RunPRWatchLoop` uses with `prWatch.mu` independent of
`sm.mu` (`prwatch.go:53`). For `session` actions, "in flight" means "the spawned
session is still running"; the daemon already tracks session lifecycle, so the
flag clears when that session stops.

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
            sm.runTriggerTick(now)
        }
    }
}

func (sm *SessionManager) runTriggerTick(now time.Time) {
    cfg := sm.Config()
    if len(cfg.Triggers) == 0 {
        return
    }
    for _, due := range sm.dueTriggers(now) { // snapshot under trigger.mu, off sm.mu
        go sm.fireTrigger(due) // off-lock; sets in-flight, respects overlap, delivers
    }
}
```

**Locking discipline** (identical philosophy to prwatch): the trigger loop has
its own mutex `triggerState.mu`, independent of `sm.mu`. Schedule cursors,
in-flight flags, and pause state live under `triggerState.mu`. Action dispatch
(`sm.Create`, `sm.StartScenario`, command exec, `store.Put`) runs in a detached
goroutine holding neither lock, so a slow action never blocks `gr list` or the
tick. State snapshots (session lookups for delivery targets) follow the
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
    Name      string     `json:"name"`
    LastRunAt *time.Time `json:"last_run_at,omitempty"` // missed-run detection
    LastError string     `json:"last_error,omitempty"`
    Paused    bool       `json:"paused,omitempty"`      // gr trigger pause
    RunCount  int        `json:"run_count,omitempty"`
}
```

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
`resume` are mutating: require the caller to be the **orchestrator or a
descendant**, reusing the `auth.authorizeScenarioOp`-style check
(`handler.go:1458`, `daemon/auth.go`). Unauthenticated (human CLI) callers are
always permitted — same posture as scenario ops. Config-defined triggers
themselves are only editable by editing `config.toml` (a human-trust action), so
agents can't author self-scheduling triggers in v1.

Because the daemon can be reached remotely (Tailscale, `[remote]`), the new
message types must also be added to the remote allow-matrix
(`remoteAllowed` in `internal/daemon/authmatrix.go:121`) — `trigger_list`/
`trigger_status` as read-only-allowed, and `trigger_run`/`trigger_pause` gated
to the appropriate role. Omitting them fails closed (remote requests rejected),
which is the safe default but would silently break remote CLI use.

## Worked example: daily PR report

Given the `[[trigger]]` config above, at 09:00 Europe/London the daemon:

1. `runTriggerTick` sees `daily-pr-report` is due (`now >= nextFire`), not
   paused, not in flight (overlap=skip).
2. Sets the in-flight flag, spawns `go sm.fireTrigger(...)`, advances `nextFire`
   to tomorrow 09:00 via `schedule.Next(now)`.
3. `fireTrigger` dispatches the `session` action: `sm.Create` with `repo`,
   `agent`, `Background: true`, `ParentID = orchestrator`, and a `Prompt` that
   is the configured `prompt` **plus** an injected delivery instruction derived
   from `deliver` ("post to the orchestrator inbox and write to
   `reports/pr/2026-07-11.md`").
4. The spawned session runs, summarises the PRs, and delivers using its own
   `gr msg send orchestrator ...` and `gr store put reports/pr/2026-07-11.md ...`.
   The orchestrator's inbox message triggers `notifyInbox`, auto-resuming it if
   idle.
5. When the session stops, the in-flight flag clears; `LastRunAt` is persisted.

If the daemon was down at 09:00 and `catch_up=false`, nothing fires late — the
next 09:00 handles it. With `catch_up=true`, one report fires on the next
startup.

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

TBD — to be filled after review and discussion.

## Other Notes

### Answers to the issue's open questions (summary)

| Open question | Recommendation |
|---------------|----------------|
| Schedule syntax | **Both.** `every = "15m"` (Go duration) or `cron = "0 9 * * *"` (5-field + `@`-descriptors, timezone-aware). Exactly one required. |
| Where defined | **`config.toml` `[[trigger]]`** as source of truth (reviewable, restart-safe, matches `[pr_watch]`/`[git_pull]`); `gr trigger` CLI for list/status/run/pause. Defer CLI authoring to v2. |
| Action vocabulary (v1) | **command, session, scenario, message.** "Report" = a session/command action + `deliver` routing, not a separate type. |
| Delivery | **`deliver` block**: `inbox` (via `notifyFromDaemon`, auto-resumes), `topic` (pub/sub), `store` (durable doc, templated key). Default report target = orchestrator inbox. |
| Missed-run policy | **`catch_up=false` default** — never backfill a burst; at most one catch-up fire on startup when `catch_up=true` (mirrors `RunPurgeLoop`). |
| Concurrency policy | **`overlap="skip"` default** — skip if previous run in flight; `allow` / `queue` (coalesced, max one deferred) as alternatives. |

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
| `internal/config/config.go` | Add `Triggers []TriggerConfig` to `Config`; add `TriggerConfig`, `ScheduleConfig`, `WatchConfig` (nil in v1), `ActionConfig`, `DeliverConfig`, `TriggerPolicy`; validation (exactly-one-source, one-action-type, cron/every parse, repo allow-list, orchestrator-required-for-session/scenario). `Default()` returns empty. |
| `internal/daemon/trigger.go` | New: `RunTriggerLoop`, `runTriggerTick`, `dueTriggers`, `fireTrigger`, per-type executors (`fireCommand`/`fireSession`/`fireScenario`/`fireMessage`), delivery routing, `triggerState` struct + mutex, `initTriggerSchedules`, catch_up + overlap handling, prune. |
| `internal/daemon/daemon.go` | `go sm.RunTriggerLoop(ctx)` in startup (near :5790). |
| `internal/daemon/state.go` | Add `TriggerRuntime map[string]*TriggerRuntimeState` to `State`; migration bump (no-op). Prune on load. |
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
- Missed-run: `catch_up=false` never backfills; `catch_up=true` fires exactly
  once on startup when the last fire was missed, zero times when not.
- Overlap: `skip` suppresses a fire while in-flight; `allow` permits concurrent;
  `queue` runs exactly one deferred fire and no more.
- Action validation: `session`/`scenario` rejected when orchestrator disabled;
  `command` rejected without an allowed `repo`.
- Delivery routing: inbox/topic/store each invoked with templated keys;
  `{date}`/`{name}` expansion; store-write failure doesn't suppress inbox.
- State prune: runtime rows for removed triggers dropped on load.
- Config reload diff: unchanged triggers keep `nextFire`; added scheduled;
  removed dropped.

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
