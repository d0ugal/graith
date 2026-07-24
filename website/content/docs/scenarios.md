---
weight: 1300
title: "Scenarios"
description: "End-to-end workflow scenarios."
icon: "playlist_add_check"
toc: true
draft: false
---

Scenarios are declarative multi-session orchestration. A TOML file defines a group of related sessions — each with its own repo, agent, role, startup prompt, and optional tracked task — and `gr scenario start` creates them atomically as a coordinated fleet.

You operate scenarios through the `gr scenario` commands and orchestrator sessions; see [In the GUI](#in-the-gui) for app support.

## When to use scenarios

| Approach | Best for |
|----------|----------|
| `gr new` (imperative) | Ad-hoc sessions, one-offs, quick experiments |
| Orchestrator + `gr new` | Dynamic decisions, branching logic, adaptive workflows |
| **Scenarios** | Reproducible multi-repo fleets, known session topologies, team playbooks |

Scenarios complement the orchestrator — it can start them declaratively, then coordinate the sessions dynamically once running.

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

### Instance name templates

Scenario names, member names, and scenario-local member references can use a small template vocabulary, so one saved file runs repeatedly or concurrently without external rewrites:

```toml
version = 1

[scenario]
name = "parallel-review-{initiator}-{date}-{short_id}"

[[sessions]]
name = "{initiator}"
shared = true

[[sessions]]
name = "{scenario}-reviewer"
mirror = "{initiator}"
```

The daemon allocates the scenario ID and snapshots one UTC render context, rendering the scenario name first, then every member name and these member references: `mirror`, each `depends_on` entry, completion-trigger `session`, and literal or mixed `action.deliver.inbox` targets.

| Token | Start-time value |
|-------|------------------|
| `{caller}` | Authenticated session that submitted the start |
| `{parent}` | Orchestrator session that owns the scenario and its created members |
| `{initiator}` | Original requesting session; the caller for a direct start |
| `{date}` | UTC date as `YYYYMMDD` |
| `{time}` | UTC time as `hhmmss` |
| `{datetime}` | UTC timestamp as `YYYYMMDDthhmmssz` |
| `{scenario_id}` | Full stable ID, such as `sc-a1b2c3d4` |
| `{short_id}` | Eight hexadecimal characters from the stable ID |
| `{scenario}` | Fully rendered scenario name; available only after rendering `[scenario].name` |

Expansion is one pass: introduced text isn't re-expanded. Unknown tokens and malformed braces are errors; graith doesn't sanitize, truncate, or rename rendered values. The final scenario name must still be lowercase alphanumeric with hyphens, member names must satisfy the normal session-name rules, and both keep their 128-character limits. Use `{short_id}` where concurrent instances need practical uniqueness; a scenario or owned-member collision is a clear preflight error.

Trigger fire-time variables stay separate: `inbox = "{scenario}-{session_name}"` fixes the prefix at start but leaves `{session_name}` for the fire. When vocabularies overlap the instance token wins — `{date}`, `{datetime}`, and `{scenario_id}` in an inbox target are fixed at start, not re-evaluated on fire. Result `store` templates keep the result vocabulary below; `{session_name}` receives the already-rendered member name.

`gr scenario start` returns the rendered scenario and member names — the selectors `status`, results, `stop`, `resume`, `delete`, and other lifecycle commands use; the authored template isn't an alias. `list` and `status --json` include the immutable identities, timestamp, and authored-to-rendered mappings; human `status` prints the authored name and caller/parent/initiator context. That metadata and the rendered graph persist across daemon restart and are never re-rendered.

### `[scenario.lifecycle]` section

Lifecycle cleanup is disabled by default. To clean up after final actions:

```toml
[scenario.lifecycle]
cleanup = "on_success" # off (default) | on_success | always
delay = "30m"          # optional; default 0
```

`on_success` schedules cleanup only after every completion action succeeds; `always` waits until every action is terminal but also cleans up after failures. The delay begins once that gate is satisfied. Cleanup stops and soft-deletes owned members, preserving state and worktrees for `gr restore` during the configured retention window. It never unstars sessions, touches shared members or unrelated trigger-spawned sessions, or turns retention `0` into a purge.

### `[scenario.policy]` section

The optional policy block turns on daemon-managed runtime completion and failure handling. Omit it and every member policy to keep the legacy indefinite/manual behaviour.

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
| `prompt` | no | `task` | Startup instructions sent to a newly created agent; does not seed a todo or form a completion contract (maximum 64 KiB; NUL is rejected) |
| `task` | no | — | Tracked work title: seeds an assigned todo and participates in completion; also supplies the startup prompt when `prompt` is omitted (maximum 500 raw bytes, or a lower configured todo-title limit; NUL is rejected) |
| `depends_on` | no | — | Member names whose seeded tasks must all finish before this seeded task is claimable |
| `agent_hooks` | no | `true` | Enable agent hooks (check-inbox, etc.) |
| `shared` | no | `false` | Reuse an existing running or stopped session by name; see the eligibility and ownership rules below |
| `includes` | no | — | Extra worktrees to attach, in addition to any inherited from the repo's `[[repos]]` config (`~` expanded; deduplicated against repo-config includes) |
| `star` | no | `false` | Create the session already starred, protecting it from an accidental manual `gr delete` |

### `[[sessions.results]]` entries

Each member can declare named artifacts it publishes into the shared document store. Nest a declaration under its owning `[[sessions]]` entry:

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `name` | yes | — | Stable member-local name: 1–64 lowercase letters, numbers, or hyphens, starting with a letter |
| `format` | yes | — | `text`, `markdown`, or `json` |
| `store` | yes | — | Relative destination template beneath the scenario's result directory |
| `required` | no | `false` | Whether this result must be available before the member can count as complete |

`store` templates can use `{scenario_id}`, `{scenario_name}`, `{session_id}`, `{session_name}`, and `{result_name}`. The daemon resolves them beneath `scenarios/<scenario-id>/results/` in the shared store and rejects invalid keys or collisions at start. Resolved keys appear in every member's manifest and in `gr scenario status --json`.

Result bodies must be non-empty; JSON results must be syntactically valid JSON (no JSON Schema validation). Publication uses the bounded control protocol under a 4 MiB ceiling (framing overhead reserved), so accepted artifacts stay readable through the store API.

### `[sessions.policy]` section

Place this block after the `[[sessions]]` entry it belongs to.

| Field | Default | Description |
|-------|---------|-------------|
| `required` | `true` | Required members always gate completion; optional successes still count toward quorum |
| `timeout` | — | Immutable wall-clock timeout for each attempt (Go duration, minimum `1s`) |
| `retries` | `0` | Additional automatic attempts after timeout (`0`–`10`; requires `timeout`) |

Shared members can be required or optional, but can't have timeout or retry actions because the scenario doesn't own their process.

Unknown fields are rejected — typos produce a parse error rather than being silently ignored.

Across one roster, the JSON-encoded effective prompts and tracked task fields can occupy at most 3 MiB, reserving 1 MiB of the 4 MiB control frame for member metadata and response fields. `gr scenario add` checks the new member with the existing roster before starting it. Whitespace-only prompt and task values are checked against their raw field limits, then stored as omitted.

Use `prompt` when the instructions are richer than the tracked work title, or when a required declared result is the member's only completion contract. A runtime-policy member still needs a non-empty `task` or a required result; `prompt` alone is only launch input and never makes a member tracked or complete. If both are present, the agent launches with `prompt` while only `task` becomes an assigned todo. Task-only files keep their existing behaviour.

`depends_on` references names in the same file. Both the dependent and every referenced member must have a non-empty `task`; unknown names, duplicates, self-dependencies, and cycles are rejected before sessions start. The daemon resolves names to the members' seeded assigned todo IDs. `gr scenario add` accepts repeatable `--depends-on <existing-member>` flags for the same behavior. Templated references render from the same snapshot before this validation.

**Shared sessions:** `shared = true` references an existing running or stopped session instead of creating one. A stopped session stays stopped; start and resume don't relaunch it. Shared sessions participate (receive manifests, appear in status) but are never stopped, resumed, deleted, or cleaned up by scenario lifecycle operations. Soft-deleted, errored, creating, and deleting sessions are unavailable; an ambiguous name (more than one matching running/stopped session) fails startup. A shared entry can't declare a startup `prompt` (its effective prompt is empty in status and its self manifest) but can still declare tracked `task` work. That task and any required results still participate in completion even when the shared source is stopped; omit them unless the external session or a human will satisfy those obligations.

**Mirrored sessions:** `mirror` set to another `[[sessions]]` member's `name` creates a normal scenario-owned worker over that member's exact worktree. The worker sees committed and uncommitted files, but the sandbox denies writes to the source worktree and provides the usual writable `GRAITH_TMPDIR` scratch space. Several members can mirror one source without creating Git worktrees or branches.

`mirror` is a scenario-local member reference, never a session ID or filesystem path. A mirrored member must not also set `shared`, `repo`, `base`, or `includes` — repository, base, worktree, and included worktrees derive from its target. The target can itself be mirrored, but references must be acyclic; every session in a source's backing chain must still exist, be running or stopped, and remain non-deleted. Missing targets, duplicate/ambiguous names, stale or cyclic backing chains, sources without a worktree, stopped sources whose saved worktree was already cleaned up or replaced by an unrelated checkout, sources with missing/invalid/unrelated inherited included worktrees, and unavailable sandbox enforcement all fail preflight before any member starts. Agent, model, role, prompt, task, hooks, and `star` still configure the mirrored worker itself.

This generic multi-reader scenario attaches two independent readers to an existing source session called `subject`:

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

**Extra worktrees:** `includes` attaches additional repo worktrees (same mechanism as the repo-level `includes` config), so an agent can see and edit sibling repos.

**Starred sessions:** `star = true` creates the session already starred, protecting it from an accidental manual `gr delete` and bulk sweeps. `shared = true` only shields a session from scenario stop/delete, not from a manual `gr delete` — use `star` for that.

`includes` and `star` apply only to sessions the scenario creates; a `shared = true` session reuses an existing one as-is, so both are ignored for it.

### `[[trigger]]` blocks (scenario-embedded triggers)

`[[trigger]]` blocks in the scenario TOML ship the scenario's own automation and activate with it — for example a continuous reviewer: a watch trigger that spawns (or reuses) a reviewer session whenever an implementer's files change. See [Triggers]({{< relref "triggers.md" >}}) for the full vocabulary; scenario-embedded triggers use the same `[trigger.schedule]`/`[trigger.watch]` sources, the scenario-only `[trigger.completion]` source, and `[trigger.action]` verbs, with these extra restrictions:

- **Watch triggers select by `role` only** — never `repo` — and the role must be one a `[[sessions]]` entry in the same scenario declares. The trigger binds only to sessions **within its own scenario**, so two running instances of the same file never cross-fire.
- **No external references.** A scenario trigger can't start another scenario (`type = "scenario"`), and a `command` action must use a `[trigger.watch]` or `[trigger.completion]` source (a schedule `command` would name a repo outside the scenario).
- **No `[trigger.gcx]` source.** A gcx cursor, authentication context, and on-call gate are daemon-global and can outlive a scenario, so gcx triggers belong in the main `config.toml`.
- **Completion context stays inside the scenario.** A completion `command` or `session` names a non-shared member with `completion.session`; literal inbox delivery resolves against this scenario's members, even if another session elsewhere shares the name. Instance-name tokens in both fields render at start.

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

**Lifecycle.** Embedded triggers activate only **after** the scenario's two-phase start succeeds — a rolled-back start discards them too, so there are never orphaned watchers. They're stored on the scenario (namespaced `scenario:<id>:<name>`) and survive a daemon restart. `gr scenario stop` tears down their watchers; `gr scenario resume` and `gr scenario add` rebind them to running sessions; `gr scenario delete` removes them entirely. They appear in `gr trigger list` alongside config-origin triggers.

Completion actions are an exception to the running-member activation rule: they stay addressable while their scenario record exists, including after members stop. `gr scenario status` shows the current epoch and each action as `pending`, `running`, `succeeded`, or `failed`, plus scheduled/running/completed cleanup. An interrupted non-session action becomes a diagnosable failure after restart instead of being replayed; retry it explicitly with its namespaced name:

```bash
gr trigger run scenario:<scenario-id>:<trigger-name>
```

Retrying during a delayed cleanup window returns cleanup to `pending`. A retry is refused once cleanup is running or has succeeded, because member teardown may already be underway.

Reopening an assigned todo creates a not-complete transition and cancels pending work and cleanup for that epoch; a later recompletion creates a new epoch. Manual `gr scenario stop` likewise cancels pending/running completion work before stopping members; a manual delete cancels it before explicit teardown.

### Completion examples

Archive a deterministic report, require the store write, and keep the working sessions for an hour so a human can inspect the result:

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

For agent-led synthesis, use the ordinary session action. It stays `running` until the synthesizer exits, so cleanup can't race its result:

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

A session/reactor a trigger *spawns* (e.g. an `ensure = true` reviewer) is parented to the **orchestrator**, not owned by the scenario — like any [session action]({{< relref "triggers.md" >}}) reactor. `gr scenario delete` removes the scenario and its own sessions but **not** such a reactor; manage it with `gr stop`/`gr delete`, or give the trigger an `idle_timeout`/`auto_cleanup` so it reaps itself.

## CLI commands

### `gr scenario start <file>`

```bash
# From a file
gr scenario start tracing.toml

# From stdin (useful for piping from gr store or templates)
cat tracing.toml | gr scenario start -
```

Only the orchestrator session can start scenarios. The success output always shows the rendered scenario and member names, including the unique suffix when the file uses `{short_id}`.

### `gr scenario status <name>`

```bash
gr scenario status tracing-pipeline
```

Human output uses a labeled block for each member, with session names, IDs,
status, agent, role, each member's `done/total` task progress, and `name=status`
result summaries. Lines adapt to the terminal width; long identifiers retain
their distinguishing suffix after a middle ellipsis, and result entries stay on
separate lines. Redirected output uses `lifecycle.default_cols` (80 by default)
as its deterministic width. JSON is unchanged and adds each result's format,
required flag, resolved destination, size, publication time, error, and one of
these states:

- `pending` — no successful publication has occurred
- `available` — validated content is stored at the declared destination
- `invalid` — the latest body was empty, oversized, or malformed for its format
- `failed` — validation passed but the store operation failed

Task progress comes from assigned [todo items]({{< relref "todo.md#in-scenarios" >}}). Status then shows the current completion epoch, completion-action states, and lifecycle-cleanup deadline or failure (see below).

### `gr scenario result put <name> [body]`

Publish the authenticated member's own declared result — body as an argument, a file, or stdin:

```bash
gr scenario result put implementation-notes --file ./implementation.md
jq '{components: $ARGS.positional}' --args api worker | \
  gr scenario result put changed-components
```

The daemon derives the member from `GRAITH_TOKEN`; the request can't select a different member or destination. Use `--scenario <name>` only when a shared session belongs to multiple scenarios; ordinary members default to `GRAITH_SCENARIO_NAME`. A peer, local/remote human, misnamed result, or wrong scenario gets an explicit error. Direct `gr store put` still works, but writing the same key doesn't mark a declared result successful.

When a runtime policy is configured, the status table also shows required/optional membership, attempt budget, immutable deadline, and any exhaustion reason. The scenario header shows successful and required counts, plus quorum when configured.

### `gr scenario list`

```bash
gr scenario list
```

List responses omit startup prompt bodies; use `gr scenario status <name> --json` for one scenario's detailed member prompts.

Aggregate status is `complete` when every member with tracked todos or required results has satisfied both and none is errored. Otherwise it reflects session lifecycle: `running` (all running), `stopped` (all stopped), `errored` (any errored), or `partial` (mixed).

At least one member must have tracked todo work or a required result before a completion edge can occur; members with neither don't block members that do. Policy scenarios additionally report `retrying`, `exhausted`, terminal `complete`, or terminal `failed`, and list renders quorum progress.

### `gr scenario stop <name>`

```bash
gr scenario stop tracing-pipeline
```

Stopping a policy scenario suspends automatic actions, but wall-clock deadlines keep elapsing. `gr scenario resume` unsuspends it and immediately reconciles overdue members; stopping doesn't grant a fresh timeout window.

### `gr scenario add <name>`

Add a member from the orchestrator. Runtime policy flags are `--optional`, `--timeout <duration>`, and `--retries <0-10>`. Adding to a terminal or paused policy scenario is rejected; resume it first. Use `--prompt` for startup instructions and `--task` for the independently tracked todo; with no `--prompt`, `--task` stays the startup prompt for compatibility. The same per-field and aggregate prompt/task limits used by scenario files are enforced against the existing roster before the daemon creates the session.

Adding any runtime-policy flag to a legacy scenario opts the whole scenario into policy completion semantics: existing members become required with no timeout; the new member uses the supplied policy. Before opting in, the daemon verifies every existing task member still has its original durable seeded todo, rejecting the add unchanged if that contract can't be proved. `scenario add` always creates a new scenario-owned session and doesn't accept shared members. It operates after the instance namespace is fixed, so `--name` and every `--depends-on` value must be literal rendered names; it doesn't evaluate instance-name templates.

### `gr scenario delete <name>`

Delete a scenario and all its sessions, including worktrees and branches.

```bash
gr scenario delete tracing-pipeline
```

## How it works

1. The CLI parses the TOML (strict field validation) and sends a `scenario_start` control message to the daemon
2. The daemon validates all inputs — prompt bodies, NUL bytes, aggregate frame headroom, todo-title limits, result contracts, dependencies, scenario/session names, repo paths, and agent configs — before any member starts
3. **Reserve phase:** placeholders are created atomically under the state lock
4. **Start phase:** independent members are created concurrently via the normal `Create` flow; mirror dependency waves follow once their sources run, using the same read-only sandbox primitive as `gr new --mirror`
5. **Manifest phase:** once all sessions start, the daemon publishes a manifest to each inbox and persists it to the shared store
6. If any session fails to create, already-started sessions are rolled back (stopped and deleted)

## Environment variables

Every session gets these extra variables at creation and on resume:

| Variable | Description |
|----------|-------------|
| `GRAITH_SCENARIO` | Scenario ID |
| `GRAITH_SCENARIO_NAME` | Scenario display name |
| `GRAITH_SCENARIO_ROLE` | This session's role |
| `GRAITH_SCENARIO_GOAL` | The overall scenario goal |

## Manifest

Each session receives a version 2 JSON manifest in its inbox describing the full rendered scenario topology, including the immutable render context and authored-to-rendered mappings. It's also persisted to the shared store at `scenarios/<id>/manifest-<rendered-name>.json`.

```json
{
  "version": 2,
  "scenario_id": "sc-abc12345",
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
        "destination": "scenarios/sc-abc12345/results/backend/implementation.md",
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
  },
  "render": {
    "authored_name": "tracing-pipeline",
    "scenario_id": "sc-abc12345",
    "short_id": "abc12345",
    "rendered_at": "2026-07-18T09:10:11Z",
    "caller": {"session_id": "orch-001", "name": "orchestrator"},
    "parent": {"session_id": "orch-001", "name": "orchestrator"},
    "initiator": {"session_id": "orch-001", "name": "orchestrator"},
    "members": [
      {"authored_name": "backend", "rendered_name": "backend"}
    ]
  }
}
```

The manifest gives each agent awareness of its own identity, role, effective startup prompt, tracked task, and result destinations (`you`); the other sessions with their roles, repos, and result destinations (`siblings`); the parent (`orchestrator`); and immutable start identities, timestamp, unique token, and authored-to-rendered mappings (`render`). For mirrored members, `you` and sibling entries also include `mirror` with the referenced member name, so the read-only relationship survives persistence and republishing.

## Coordination

Sessions coordinate through the standard graith messaging primitives:

```bash
# Message a sibling by name
gr msg send frontend "API contract ready, see openapi.yaml"

# Message the orchestrator
gr msg send --parent "backend work complete, ready for review"

# Read your inbox (where the manifest was delivered)
gr msg inbox --all --ack
```

## Task tracking

Per-member progress is tracked through the [todo list]({{< relref "todo.md" >}}), not a per-session boolean. At start, each member with a `task` is seeded **one assigned todo item** in the scenario's shared scope; the member breaks it down by adding sub-items. A member with `depends_on` starts with that seeded item blocked until every named member's seeded item is done. A member is *complete* once every item assigned to it is `done` **and every required declared result is `available`**; a member with required results but no todos is complete once those results are available. Optional results never block completion. The scenario is complete once every member with tracked todos or required results is complete. `gr scenario status` renders per-member `done/total`, **WAITING ON** names, and result state from those durable records; JSON uses `blocked_by` for dependencies. Completion actions and lifecycle cleanup use this same gate, so neither starts while a required result is pending, invalid, or failed.

A `prompt` is never seeded into the todo list, making prompt-only members useful for result contracts: the agent receives full instructions, and publishing its required result completes it without a redundant todo.

The original member-to-seed identity is durable. Reassigning a seeded item changes current responsibility and progress accounting, but later `gr scenario add --depends-on <member>` commands still resolve the named member's original seed.

Without a runtime policy, every tracked member must complete. With a policy, required and quorum rules decide which successful members complete the scenario.

The seeded item is assigned but initially ownerless. A member signals completion by finding its assigned item, claiming it, then marking it done (a `prompt` is never seeded, so a prompt-only member completes via its required result alone):

```bash
gr todo list --scenario "<scenario-name-from-manifest>" # find assignee=$GRAITH_SESSION_ID
gr todo claim <its-task-item>                            # establish ownership
gr todo done <its-task-item>                             # complete the claimed item
```

Assigned items are reserved: another member can't claim or complete this work. The orchestrator stays the override authority and the human keeps transition authority.

Scenario-created members can substitute `$GRAITH_SCENARIO_NAME`; a shared member keeps its existing environment, so it uses the scenario name from the delivered manifest instead. A dependency-blocked seed is claimed only after its upstream items finish; members without a `task` receive no seeded item.

See [Todo list — in scenarios]({{< relref "todo.md#in-scenarios" >}}) for the full model.

## Fan-out / fan-in results

A generic fan-out scenario gives several workers the same result name with member-specific destinations, then lets a later synthesizer consume them:

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

Each worker publishes with `gr scenario result put`. The synthesizer (or orchestrator) waits until status reports the inputs `available`, then reads the exact destinations from its inbox manifest or JSON status:

```bash
gr scenario status research-swarm --json
gr store get --shared scenarios/sc-abc12345/results/research-api/findings.md
gr store get --shared scenarios/sc-abc12345/results/research-data/facts.json
```

Those keys include the stable scenario ID and declared member destinations, so two runs of the same scenario name can't overwrite or consume each other's results.

Completion actions also receive a `{result_index}` trigger template variable. It
resolves to a shared-store JSON document for the current completion epoch,
containing metadata for every declared result (member, name, format, required
flag, status, destination, and available publication metadata) in deterministic
member-name/result-name order. The index is bounded and contains no result
bodies. Commands may read it with `gr store get --shared {result_index}`;
completion session prompts include that instruction. Each recompletion gets a
new epoch-specific key, so an old index is never exposed as current.

## Runtime policy semantics

An initial attempt starts only after atomic startup commits; an added member starts when its add commits. A retry starts when the daemon durably claims its one retry path, before waiting for a launch slot, so the frozen deadline includes launch queue time. Daemon downtime, process stops, and user activity all consume wall-clock time; output, messages, hooks, todo claims, and other activity never extend a deadline.

Success is contract-based: a member must have at least one assigned todo or required declared result; every assigned todo must be `done` and every required result `available`. Exiting zero, crashing, or entering `stopped` isn't success. Completion already observed when a deadline is sampled wins; after a timeout claim is durable, later completion belongs to the newly claimed attempt. Completed and outstanding todo and result state survive retry. Policy start/add validates each member has at least one completion contract and commits todo contracts before policy activation.

A retry uses the ordinary restart/resume path, keeping the graith session ID, agent conversation, worktree, branch, and todo assignee identity; stopping the old process reopens any in-progress todo ownership so work isn't stranded. It uses normal launch concurrency control. Attempts and deadlines are stored in daemon state, and a durable launch generation stops a restart from repeating a retry that already launched. Retryable members use a PTY even when the soft global headless default is on, because the one-shot headless driver can't resume the same conversation.

A second durable dispatch marker is written immediately before process work. After a restart an undispatched claim continues; a dispatched attempt with neither an advanced launch generation nor a durable outcome is exhausted as interrupted, rather than risking a duplicate restart.

Daemon cancellation reaches retries waiting on scenario serialization or a launch slot, and is checked again immediately before spawning. A restart that finds a scenario reserve record whose atomic startup never reached policy activation marks it a visible terminal startup failure rather than retrying a partial fleet. Completed policy todo contracts are exempt from todo retention until the daemon has durably recorded the policy outcome.

Quorum completion is terminal but non-destructive: graith records the outcome without stopping or deleting remaining workers. Required members must all succeed even if enough optional members reached the numerical quorum. Optional exhaustion doesn't fail the scenario by itself; with `on_exhausted = "fail"`, it fails once the successful and still-eligible members together can no longer reach the configured quorum.

## In the GUI

The macOS and iOS apps surface running scenarios through the shared session layer:

- **Scenarios view** — a toolbar button (badged with the running-scenario count) opens a list of every scenario on the connected daemons, showing each one's goal, status, and member sessions with role, task, and `done/total` progress.
- **Sidebar grouping** — a **SCENARIOS** section at the top of the sidebar groups each scenario's members together, so a fleet reads as a unit rather than scattered across repo groups. Tapping a member selects it.
- **Lifecycle actions** — the human-authorized **stop**, **resume**, and **delete** actions are available from the scenarios view and the sidebar context menu.

`start` and `add` stay CLI-only: the daemon scopes them to the scenario's orchestrator *session*, which the GUI (a human client) isn't.

## Constraints

- **Orchestrator only:** only the orchestrator session (`system_kind: orchestrator`) can start scenarios
- **Unique names:** scenario names must be unique across all scenarios; session names must not collide with any existing session
- **Atomic creation:** all sessions are created or none are — partial failures trigger rollback
- **Add-only topology:** `gr scenario add` appends a session, but sessions and result declarations can't be edited or removed in place — delete and recreate for those changes (or to add to a terminal policy scenario)
- **Bounded policy:** retries are finite (`0`–`10`); provider/model replacement and failover aren't part of runtime policy
