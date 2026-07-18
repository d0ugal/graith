---
weight: 1300
title: "Scenarios"
description: "End-to-end workflow scenarios."
icon: "playlist_add_check"
toc: true
draft: false
---

Scenarios are declarative multi-session orchestration. A TOML file defines a group of related sessions — each with its own repo, agent, role, startup prompt, and optional tracked task — and `gr scenario start` creates them atomically as a coordinated fleet.

Scenarios are operated through the `gr scenario` commands and orchestrator
sessions. The native iOS and macOS apps do not provide scenario grouping,
status, or lifecycle controls; they show scenario-created sessions as ordinary
sessions, which can still be opened and attached normally.

## When to use scenarios

| Approach | Best for |
|----------|----------|
| `gr new` (imperative) | Ad-hoc sessions, one-offs, quick experiments |
| Orchestrator + `gr new` | Dynamic decisions, branching logic, adaptive workflows |
| **Scenarios** | Reproducible multi-repo fleets, known session topologies, team playbooks |

Scenarios complement the orchestrator — the orchestrator can start scenarios declaratively, then coordinate the sessions dynamically after they're running.

## TOML file format

```toml
version = 1

[scenario]
name = "tracing-pipeline"
goal = "Build end-to-end distributed tracing across backend and frontend"

[scenario.policy]
completion = "quorum"
quorum = 2
on_exhausted = "fail"

[[sessions]]
name = "backend"
repo = "~/Code/my-backend"
agent = "claude"
model = "claude-opus-4-8"
role = "Backend engineer"
task = "Add tracing ingest endpoint and propagation middleware"

[[sessions.results]]
name = "implementation-notes"
format = "markdown"
store = "{session_name}/implementation.md"
required = true

[sessions.policy]
required = true
timeout = "30m"
retries = 2

[[sessions]]
name = "frontend"
repo = "~/Code/my-frontend"
agent = "cursor"
model = "gemini-3.1-pro"
role = "Frontend developer"
task = "Add trace export UI and correlation ID headers"
agent_hooks = false

[[sessions.results]]
name = "changed-components"
format = "json"
store = "{session_name}/components.json"
required = true

[sessions.policy]
required = false
timeout = "45m"
retries = 1

[[sessions]]
name = "synthesis"
repo = "~/Code/my-backend"
role = "Integrator"
task = "Combine the backend and frontend work"
depends_on = ["backend", "frontend"]
```

### Top-level fields

| Field | Required | Description |
|-------|----------|-------------|
| `version` | yes | Must be `1` |

### `[scenario]` section

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Scenario name (lowercase alphanumeric + hyphens, max 128 chars) |
| `goal` | no | Overall goal — injected as `GRAITH_SCENARIO_GOAL` env var |

### `[scenario.lifecycle]` section

Lifecycle cleanup is disabled by default. To clean up after final actions:

```toml
[scenario.lifecycle]
cleanup = "on_success" # off (default) | on_success | always
delay = "30m"          # optional; default 0
```

`on_success` schedules cleanup only after every completion action succeeds;
`always` waits until every action is terminal but also cleans up after failures.
The delay begins only after that gate is satisfied. Cleanup stops and
soft-deletes owned members, preserving their state and worktrees for
`gr restore` during the configured retention window. It never unstars sessions,
never touches shared members or unrelated trigger-spawned sessions, and never
turns retention `0` into a purge.

### `[scenario.policy]` section

The optional policy block turns on daemon-managed runtime completion and
failure handling. Omitting this block and every member policy preserves the
legacy indefinite/manual behaviour.

| Field | Default | Description |
|-------|---------|-------------|
| `completion` | `"all"` | `"all"` requires every required member; `"quorum"` also requires `quorum` successful members |
| `quorum` | — | Positive success threshold for quorum mode; cannot exceed the member count or be lower than the required-member count |
| `on_exhausted` | `"wait"` | `"fail"` terminally fails when a required member exhausts or exhaustion makes quorum unreachable; `"wait"` leaves it for manual recovery |

### `[[sessions]]` entries

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `name` | yes | — | Session name (must be unique across all sessions) |
| `repo` | yes, except shared/mirrored | — | Repository path (`~` is expanded); derived for `shared` members when omitted and always derived for mirrored members |
| `mirror` | no | — | Name of another member in this scenario whose exact worktree is mounted read-only |
| `agent` | no | config default | Agent type (`claude`, `codex`, `cursor`, etc.) |
| `model` | no | agent default | Model override (fills `{model}` in agent args) |
| `base` | no | repo default | Base branch for the worktree |
| `role` | no | — | Human-readable role description |
| `prompt` | no | `task` | Startup instructions sent to a newly created agent; does not seed a todo or form a completion contract (maximum 64 KiB) |
| `task` | no | — | Tracked work title: seeds an assigned todo and participates in completion; also supplies the startup prompt when `prompt` is omitted (maximum 500 bytes, or a lower configured todo-title limit) |
| `depends_on` | no | — | Member names whose seeded tasks must all finish before this seeded task is claimable |
| `agent_hooks` | no | `true` | Enable agent hooks (check-inbox, etc.) |
| `shared` | no | `false` | Reuse an existing running session by name |
| `includes` | no | — | Extra worktrees to attach, in addition to any inherited from the repo's `[[repos]]` config (`~` expanded; deduplicated against repo-config includes) |
| `star` | no | `false` | Create the session already starred, protecting it from an accidental manual `gr delete` |

### `[[sessions.results]]` entries

Each member may declare named artifacts that it publishes into the shared
document store. A declaration is nested under its owning `[[sessions]]` entry:

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `name` | yes | — | Stable member-local name: 1–64 lowercase letters, numbers, or hyphens, starting with a letter |
| `format` | yes | — | `text`, `markdown`, or `json` |
| `store` | yes | — | Relative destination template beneath the scenario's result directory |
| `required` | no | `false` | Whether this result must be available before the member can count as complete |

`store` templates may use `{scenario_id}`, `{scenario_name}`, `{session_id}`,
`{session_name}`, and `{result_name}`. The daemon resolves them beneath
`scenarios/<scenario-id>/results/` in the shared store and rejects invalid keys
or collisions at scenario start. Exact resolved keys appear in every member's
scenario manifest and in `gr scenario status --json`.

Result bodies must be non-empty. JSON results must contain syntactically valid
JSON; JSON Schema validation is not performed. Publication travels through the
bounded control protocol and reserves framing overhead below its 4 MiB ceiling,
so accepted artifacts remain readable through the store API.

### `[sessions.policy]` section

Put this block after the `[[sessions]]` entry it belongs to.

| Field | Default | Description |
|-------|---------|-------------|
| `required` | `true` | Required members always gate completion; optional successes still count toward quorum |
| `timeout` | — | Immutable wall-clock timeout for each attempt (Go duration, minimum `1s`) |
| `retries` | `0` | Additional automatic attempts after timeout (`0`–`10`; requires `timeout`) |

Shared members may be required or optional, but cannot have timeout or retry
actions because the scenario does not own their process.

Unknown fields are rejected — typos produce a parse error rather than being silently ignored.

Use `prompt` when the instructions are richer than the tracked work title, or
when a required declared result is the member's only completion contract. A
runtime-policy member still needs a non-empty `task` or a required result;
`prompt` alone is only launch input and never makes a member tracked or
complete. If both fields are present, the agent launches with `prompt` while
only `task` becomes an assigned todo. Task-only files retain their existing
launch and completion behaviour.

`depends_on` references names in the same file. Both the dependent and every
referenced member must have a non-empty `task`; unknown names, duplicates,
self-dependencies, and cycles are rejected before sessions start. The daemon
resolves names to the members' seeded assigned todo IDs. `gr scenario add`
accepts repeatable `--depends-on <existing-member>` flags for the same behavior.

**Shared sessions:** Set `shared = true` to reference an existing running
session instead of creating a new one. The named session must already be
running. Shared sessions participate in the scenario (receive manifests, appear
in status) but are never stopped or deleted by scenario lifecycle operations.
Because a shared agent is already running, a shared entry cannot declare a
startup `prompt`; it may still declare tracked `task` work.

**Mirrored sessions:** Set `mirror` to another `[[sessions]]` member's `name` to
create a normal scenario-owned worker over that member's exact worktree. The
worker sees committed and uncommitted files, but the sandbox denies writes to
the source worktree and provides the usual writable `GRAITH_TMPDIR` scratch
space. Several members may mirror one source without creating Git worktrees or
branches.

`mirror` is a scenario-local member reference, never a session ID or filesystem
path. A mirrored member must not also set `shared`, `repo`, `base`, or
`includes`: the repository, base, worktree, and included worktrees are derived
from its target. The target may itself be mirrored, but references must be
acyclic. Missing targets, duplicate/ambiguous names, cycles, sources without a
worktree, and unavailable sandbox enforcement fail preflight before any member
starts. Agent, model, role, prompt, task, hooks, and `star` still configure the mirrored
worker itself.

This generic multi-reader scenario attaches two independent readers to an
existing source session named `subject`:

```toml
version = 1

[scenario]
name = "read-the-subject"
goal = "Inspect the same work in progress from independent perspectives"

[[sessions]]
name = "subject"
shared = true

[[sessions]]
name = "audit-reader"
mirror = "subject"
agent = "claude"
role = "Security and correctness auditor"
task = "Audit the subject worktree without modifying it."

[[sessions]]
name = "test-reader"
mirror = "subject"
agent = "codex"
role = "Test analyst"
task = "Identify missing tests in the subject worktree without modifying it."
```

**Extra worktrees:** `includes` attaches additional repo worktrees to the
session (the same mechanism as the repo-level `includes` config), so an agent
can see and edit sibling repos. Paths are merged with — and deduplicated
against — any includes configured on the repo's `[[repos]]` entry.

`includes` and `star` only apply to sessions the scenario creates. A
`shared = true` session reuses an already-running session as-is, so those two
fields are ignored for it.

**Starred sessions:** `star = true` creates the session already starred. A
starred session is protected from an accidental manual `gr delete` (and bulk
sweeps). Note `shared = true` only shields a session from scenario
stop/delete, not from a manual `gr delete` — use `star` for that.

### `[[trigger]]` blocks (scenario-embedded triggers)

A scenario can ship its own automation: add `[[trigger]]` blocks to the scenario
TOML and they activate with the scenario. This is how a scenario wires in a
continuous reviewer — a watch trigger that spawns (or reuses) a reviewer session
whenever an implementer's files change. See [Triggers]({{< relref "triggers.md" >}})
for the full trigger vocabulary; scenario-embedded triggers use the same
`[trigger.schedule]`/`[trigger.watch]` sources, the scenario-only
`[trigger.completion]` source, and `[trigger.action]` verbs, with
these extra restrictions:

- **Watch triggers select by `role` only** — never `repo` — and the role must be
  one a `[[sessions]]` entry in the same scenario declares. The trigger binds
  only to sessions **within its own scenario**, so two running instances of the
  same scenario file never cross-fire.
- **No external references.** A scenario trigger cannot start another scenario
  (`type = "scenario"`), and a `command` action must use a `[trigger.watch]` or
  `[trigger.completion]` source (a schedule `command` would name a repo outside
  the scenario).
- **No `[trigger.gcx]` source.** A gcx cursor, authentication context, and
  on-call gate are daemon-global and can outlive a scenario, so gcx triggers
  belong in the main `config.toml`.
- **Completion context stays inside the scenario.** A completion `command` or
  `session` names a non-shared member with `completion.session`; literal inbox
  delivery resolves against this scenario's members, even if another session
  elsewhere has the same name.

```toml
version = 1

[scenario]
name = "review-pipeline"
goal = "Implement the feature with a continuous reviewer"

[[sessions]]
name = "impl"
repo = "~/Code/graith"
role = "implementer"
task = "Implement the feature."

# Whenever the implementer changes Go source, ensure a reviewer session exists
# (reusing it if it does) and ask it to review the latest changes.
[[trigger]]
name = "review-go"
[trigger.watch]
role  = "implementer"      # a role this scenario defines
paths = ["**/*.go"]
[trigger.action]
type   = "session"
ensure = true
agent  = "claude"
prompt = "Review the changes since your last look; send feedback via gr msg."
```

**Lifecycle.** Embedded triggers activate only **after** the scenario's
two-phase start succeeds — if the start rolls back, the triggers are discarded
with it, so there are never orphaned watchers. They are stored on the scenario
(namespaced `scenario:<id>:<name>`) and survive a daemon restart. `gr scenario
stop` tears down their watchers; `gr scenario resume` and `gr scenario add`
rebind them to the scenario's running sessions; `gr scenario delete` removes them
entirely. They appear in `gr trigger list` alongside config-origin triggers.

Completion actions are an exception to the running-member activation rule: they
remain addressable while their scenario record exists, including after members
stop. `gr scenario status` shows the current epoch and each action as `pending`,
`running`, `succeeded`, or `failed`, plus scheduled/running/completed cleanup.
An interrupted non-session action becomes a diagnosable failure after restart
instead of being replayed; retry it explicitly with its namespaced name:

```bash
gr trigger run scenario:<scenario-id>:<trigger-name>
```

Retrying during a delayed cleanup window returns cleanup to `pending`. A retry
is refused once cleanup is already running or has succeeded, because member
teardown may already be underway.

Reopening an assigned todo creates a not-complete transition and cancels pending
work and cleanup for that epoch. A later recompletion creates a new epoch.
Manual `gr scenario stop` likewise cancels pending/running completion work
before stopping members; manual delete cancels it before explicit teardown.

### Completion examples

Archive a deterministic report, require the store write, and retain the working
sessions for an hour so a human can inspect the result:

```toml
[scenario.lifecycle]
cleanup = "on_success"
delay = "1h"

[[trigger]]
name = "archive-report"
[trigger.completion]
session = "reporter"
[trigger.action]
type = "command"
command = "./scripts/render-report"
[trigger.action.deliver]
store = "shared:reports/{scenario_name}/epoch-{completion_epoch}.md"
inbox = "orchestrator"
required = true
```

For agent-led synthesis, use the ordinary session action. The completion action
remains `running` until the synthesizer exits, so cleanup cannot race its result:

```toml
[[trigger]]
name = "synthesise"
[trigger.completion]
session = "implementer"
[trigger.action]
type = "session"
agent = "claude"
prompt = "Synthesize the completed work into a release report, deliver it, then exit."
[trigger.action.deliver]
store = "shared:reports/{scenario_name}-final.md"
topic = "scenario-reports"
required = true
```

Note that a session/reactor a trigger *spawns* (e.g. an `ensure = true`
reviewer) is parented to the **orchestrator**, not owned by the scenario — like
any [session action]({{< relref "triggers.md" >}}) reactor. `gr scenario delete`
removes the scenario and its own sessions but does **not** stop such a reactor;
manage it with `gr stop`/`gr delete`, or give the trigger an
`idle_timeout`/`auto_cleanup` so it reaps itself.

## CLI commands

### `gr scenario start <file>`

Start a scenario from a TOML file. Pass `-` to read from stdin.

```bash
# From a file
gr scenario start tracing.toml

# From stdin (useful for piping from gr store or templates)
cat tracing.toml | gr scenario start -
```

Only the orchestrator session can start scenarios. Running this from a regular session produces an error.

### `gr scenario status <name>`

Show the status of each session in a scenario.

```bash
gr scenario status tracing-pipeline
```

Output includes session names, IDs, status, agent, role, each member's
`done/total` task progress, and compact `name=status` result summaries. JSON
status includes each result's format, required flag, resolved destination,
size, publication time, error, and one of these states:

- `pending` — no successful publication has occurred
- `available` — validated content is stored at the declared destination
- `invalid` — the latest body was empty, oversized, or malformed for its format
- `failed` — validation passed but the store operation failed

Task progress is derived from assigned
[todo items]({{< relref "todo.md#in-scenarios" >}}). Status then shows the
current completion epoch, completion-action states, and lifecycle-cleanup
deadline or failure (see below).

### `gr scenario result put <name> [body]`

Publish the authenticated member's own declared result. The body may be an
argument, a file, or standard input:

```bash
gr scenario result put implementation-notes --file ./implementation.md
jq '{components: $ARGS.positional}' --args api worker | \
  gr scenario result put changed-components
```

The daemon derives the member from `GRAITH_TOKEN`; the request cannot select a
different member or destination. Use `--scenario <name>` only when a shared
session belongs to multiple scenarios; ordinary members default to
`GRAITH_SCENARIO_NAME`. A peer, local/remote human, misnamed result, or wrong
scenario receives an explicit error. Direct `gr store put` remains available,
but writing the same key does not mark a declared result successful.

When a runtime policy is configured, the status table also shows
required/optional membership, attempt budget, immutable deadline, and any
exhaustion reason. The scenario header shows successful and required counts,
plus quorum when configured.

### `gr scenario list`

List all scenarios with their aggregate status.

```bash
gr scenario list
```

Aggregate status is `complete` when every member with tracked todos or required
results has satisfied both and no member is errored. Otherwise it reflects
session lifecycle: `running` (all running), `stopped` (all stopped), `errored`
(any errored), or `partial` (mixed).

At least one member must have tracked todo work or a required result before a
completion edge can occur. Members with neither do not block members that do
have tracked work from completing the scenario.

Policy scenarios additionally report `retrying`, `exhausted`, terminal
`complete`, or terminal `failed`, and list renders quorum progress.

### `gr scenario stop <name>`

Stop all running sessions in a scenario.

```bash
gr scenario stop tracing-pipeline
```

Stopping a policy scenario suspends automatic actions, but wall-clock deadlines
continue to elapse. `gr scenario resume` unsuspends it and immediately reconciles
overdue members; stopping does not grant a fresh timeout window.

### `gr scenario add <name>`

Add a member from the orchestrator. Runtime policy flags are `--optional`,
`--timeout <duration>`, and `--retries <0-10>`. Adding to a terminal policy
scenario or a paused policy scenario is rejected; resume it first.
Use `--prompt` for startup instructions and `--task` for the independently
tracked todo. With no `--prompt`, `--task` remains the startup prompt for
compatibility.

Adding any runtime-policy flag to a legacy scenario opts the whole scenario into
policy completion semantics. Existing members become required with no timeout;
the new member uses the supplied policy. Before opting in, the daemon verifies
that every existing task member still has its original durable seeded todo; the
add is rejected without changing the scenario if that contract cannot be
proved. `scenario add` always creates a new scenario-owned session and does not
accept shared members.

### `gr scenario delete <name>`

Delete a scenario and all its sessions, including worktrees and branches.

```bash
gr scenario delete tracing-pipeline
```

## How it works

1. The CLI parses the TOML file (with strict field validation) and sends a `scenario_start` control message to the daemon
2. The daemon validates all inputs, including prompt bodies, todo-title limits, result contracts, dependencies, scenario/session names, repo paths, and agent configs, before any member starts
3. **Reserve phase:** placeholders are created atomically under the state lock
4. **Start phase:** independent members are created concurrently using the normal `Create` flow; mirror dependency waves follow after their sources are running, using the same read-only sandbox primitive as `gr new --mirror`
5. **Manifest phase:** after all sessions start, the daemon publishes a manifest to each session's inbox and persists it to the shared store
6. If any session fails to create, already-started sessions are rolled back (stopped and deleted)

## Environment variables

Every session in a scenario gets these additional environment variables at creation time and on resume:

| Variable | Description |
|----------|-------------|
| `GRAITH_SCENARIO` | Scenario ID |
| `GRAITH_SCENARIO_NAME` | Scenario display name |
| `GRAITH_SCENARIO_ROLE` | This session's role |
| `GRAITH_SCENARIO_GOAL` | The overall scenario goal |

## Manifest

Each session receives a JSON manifest in its inbox describing the full scenario topology. The manifest is also persisted to the shared store at `scenarios/<id>/manifest-<name>.json`.

```json
{
  "version": 1,
  "scenario_id": "sc-abc123",
  "scenario_name": "tracing-pipeline",
  "goal": "Build end-to-end distributed tracing",
  "you": {
    "name": "backend",
    "session_id": "def456",
    "role": "Backend engineer",
    "prompt": "Implement the backend plan and publish the declared notes.",
    "task": "Add tracing ingest endpoint",
    "results": [
      {
        "name": "implementation-notes",
        "format": "markdown",
        "destination": "scenarios/sc-abc123/results/backend/implementation.md",
        "required": true
      }
    ]
  },
  "siblings": [
    {
      "name": "frontend",
      "session_id": "ghi789",
      "role": "Frontend developer",
      "repo": "my-frontend"
    }
  ],
  "orchestrator": {
    "session_id": "orch-001",
    "name": "orchestrator"
  }
}
```

The manifest gives each agent awareness of:

- **`you`** — its own identity, role, effective startup prompt, tracked task, and declared result destinations
- **`siblings`** — the other sessions in the scenario, with their roles, repos, and result destinations
- **`orchestrator`** — the parent session that started the scenario

For mirrored members, both `you` and sibling entries also include `mirror` with
the referenced member name, so the declared read-only relationship survives
manifest persistence and republishing.

## Coordination

Sessions in a scenario coordinate through the standard graith messaging primitives:

```bash
# Message a sibling by name
gr msg send frontend "API contract ready, see openapi.yaml"

# Message the orchestrator
gr msg send --parent "backend work complete, ready for review"

# Read your inbox (where the manifest was delivered)
gr msg inbox --all --ack
```

## Task tracking

Per-member progress is tracked through the [todo list]({{< relref "todo.md" >}}),
not a per-session boolean. At start, each member with a `task` is seeded **one
assigned todo item** in the scenario's shared scope; a member breaks its task down
by adding sub-items. A member with `depends_on` starts with that seeded item
blocked until every named member's seeded item is done. A member is *complete*
once every item assigned to it is `done` **and every required declared result is
`available`**. A member with required results but no todos is complete once
those results are available. Optional results never block completion. The
scenario as a whole is complete once every member with tracked todos or
required results is complete. `gr scenario status` renders per-member
`done/total`, **WAITING ON** names, and result state from those durable records;
JSON uses `blocked_by` for dependencies. Completion actions and lifecycle
cleanup use this same gate, so neither starts while a required result is
pending, invalid, or failed.

A `prompt` is never seeded into the todo list. This makes prompt-only members
useful for result contracts: the agent receives full instructions, and publishing
its required result is sufficient for completion without a redundant todo.

The original member-to-seed identity is durable. Reassigning a seeded item
changes current responsibility and progress accounting, but later
`gr scenario add --depends-on <member>` commands still resolve the named
member's original seed.

Without a runtime policy, every tracked member must complete. With a policy,
required and quorum rules decide which successful members complete the
scenario.

So instead of flipping a single flag, a member signals it has finished by marking
its task item done:

```bash
gr todo done <its-task-item>       # from the member session
gr todo list --scenario tracing-pipeline   # see the shared backlog
```

See [Todo list — in scenarios]({{< relref "todo.md#in-scenarios" >}}) for the full
model.

## Fan-out / fan-in results

A generic fan-out scenario can give several workers the same result name while
using member-specific destinations, then let a later synthesizer consume them:

```toml
[[sessions]]
name = "research-api"
repo = "~/Code/graith"
prompt = "Research the API surface in detail and publish your findings."
[[sessions.results]]
name = "findings"
format = "markdown"
store = "{session_name}/findings.md"
required = true

[[sessions]]
name = "research-data"
repo = "~/Code/graith"
prompt = "Collect structured compatibility facts and publish them."
[[sessions.results]]
name = "facts"
format = "json"
store = "{session_name}/facts.json"
required = true

[[sessions]]
name = "synthesizer"
repo = "~/Code/graith"
prompt = "Wait for required sibling results, read their manifest destinations, and synthesize the recommendation."
[[sessions.results]]
name = "recommendation"
format = "markdown"
store = "{session_name}/recommendation.md"
required = true
```

Each worker publishes with `gr scenario result put`. The synthesizer (or
orchestrator) waits until status reports the inputs `available`, then reads the
exact destinations from its inbox manifest or JSON status:

```bash
gr scenario status research-swarm --json
gr store get --shared scenarios/sc-abc123/results/research-api/findings.md
gr store get --shared scenarios/sc-abc123/results/research-data/facts.json
```

Those keys include the stable scenario ID and declared member destinations, so
two runs of the same scenario name cannot overwrite or consume each other's
results.

## Runtime policy semantics

An initial attempt starts only after atomic scenario startup has committed. An
added member starts when its add commits. A retry attempt starts when the daemon
durably claims its one retry path, before it waits for a launch slot; the frozen
deadline therefore includes launch queue time. Daemon downtime, process stops,
and user activity all consume wall-clock time. Output, messages, hooks, todo
claims, and other activity never extend a deadline.

Success is contract-based: a member must have at least one assigned todo or
required declared result; every assigned todo must be `done`, and every required
result must be `available`. Exiting zero, crashing, or entering `stopped` is not
success. Completion already observed when a deadline is sampled wins; after a
timeout claim is durable, later completion belongs to the newly claimed attempt.
Completed and outstanding todo and result state survive retry.
Policy start/add validates that each member has at least one completion contract
and commits todo contracts before policy activation.

A retry uses the ordinary restart/resume path. It keeps the graith session ID,
agent conversation, worktree, branch, and todo assignee identity; stopping the
old process reopens any in-progress todo ownership so work is not stranded. It
also uses the normal launch concurrency control. Attempts and deadlines are
stored in daemon state, and a durable launch generation prevents a daemon
restart from repeating a retry that already launched successfully.
Retryable members use a PTY even when the soft global headless default is on,
because the one-shot headless driver cannot resume the same conversation.

A second durable dispatch marker is written immediately before process work.
After daemon restart an undispatched claim continues; a dispatched attempt with
neither an advanced launch generation nor a durable outcome is exhausted as
interrupted, rather than risking a duplicate restart of the same attempt.

Daemon cancellation reaches retries waiting on scenario serialization or a
launch slot and is checked again immediately before spawning. A daemon restart
that finds a scenario reserve record whose atomic startup never reached policy
activation marks that scenario as a visible terminal startup failure; it does
not retry a partial fleet. Completed policy todo contracts are exempt from todo
retention until the daemon has observed and durably recorded the policy outcome.

Quorum completion is terminal but non-destructive: graith records the outcome
without stopping or deleting remaining workers. Required members must all
succeed even if enough optional members have reached the numerical quorum.
Optional exhaustion does not fail the scenario by itself. With
`on_exhausted = "fail"`, it does fail once the successful and still-eligible
members together can no longer reach the configured quorum.

## In the GUI

The macOS and iOS apps surface running scenarios through the shared session
layer:

- **Scenarios view** — a toolbar button (badged with the running-scenario count)
  opens a list of every scenario on the connected daemons, showing each one's
  goal, status, and member sessions with their role, task, and `done/total`
  progress.
- **Sidebar grouping** — a **SCENARIOS** section at the top of the sidebar groups
  each scenario's member sessions together, so a fleet reads as a unit rather
  than scattered across repo groups. Tapping a member selects it.
- **Lifecycle actions** — the human-authorized **stop**, **resume**, and
  **delete** actions are available from the scenarios view and the sidebar
  context menu.

`start` and `add` stay CLI-only: the daemon scopes them to the scenario's
orchestrator *session*, which the GUI (a human client) is not.

## Constraints

- **Orchestrator only:** Only the orchestrator session (`system_kind: orchestrator`) can start scenarios
- **Unique names:** Scenario names must be unique across all scenarios. Session names must not collide with any existing session
- **Atomic creation:** All sessions are created or none are — partial failures trigger rollback
- **Add-only topology:** `gr scenario add` can append a session, but sessions and
  result declarations cannot be edited or removed in place. Delete and recreate
  the scenario for those changes
- **Bounded policy:** Retries are finite (`0`–`10`); provider/model replacement and failover are not part of runtime policy
- **Live additions:** The orchestrator can add sessions, but cannot add to a terminal policy scenario; member removal still requires delete/recreate
