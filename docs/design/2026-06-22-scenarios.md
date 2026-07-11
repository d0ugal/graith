# Scenarios: Declarative Multi-Session Orchestration

Define and launch coordinated groups of agent sessions from a single
definition. An orchestrator session reads a scenario file and spawns child
sessions across different repos, each with a defined role and task, all aware
of each other.

## Background

Graith already supports the building blocks for multi-agent coordination:
parent-child session trees, hierarchical messaging (`gr msg send --children`),
input injection (`gr type`), shared document store (`gr store`), and an
orchestrator session concept. Today an orchestrator agent can manually create
sessions and coordinate them, but there's no structured way to define a
repeatable multi-session topology.

Users working across multiple repos (e.g. a backend service, a frontend SDK,
and a replay library) want to spin up a fleet of agents with defined roles,
have each agent know who the others are and what they're responsible for, and
have the orchestrator manage the lifecycle.

## Problem

1. **No reproducibility.** An orchestrator that manually runs `gr new` and
   `gr type` to set up a fleet is fragile — the setup lives in the AI's
   context window and is lost when the session ends.

2. **No mutual awareness.** Child sessions don't know about their siblings.
   An agent working on the frontend SDK doesn't know there's a sibling
   working on the replay viewer unless the orchestrator manually tells it.

3. **No structured coordination.** The orchestrator has to improvise
   coordination patterns each time. There's no standard way to define "these
   three agents work together on this goal."

4. **No lifecycle management.** There's no concept of "this group of sessions
   is one logical unit" — stopping or cleaning up requires knowing which
   sessions belong together.

## Goals

- Define a scenario as a declarative file (TOML) that specifies sessions,
  their repos, agents, roles, and tasks
- Launch all sessions in a scenario from the orchestrator with a single command
- Each session receives context about the scenario: its role, its siblings,
  and the overall goal
- Scenario lifecycle: start, status, stop, delete as a unit
- Only the system orchestrator session can start scenarios (enforced in the
  daemon by checking `SystemKind == SystemKindOrchestrator`)

### Non-Goals

- Cross-machine orchestration — scenarios run on a single machine
- Dependency ordering between sessions (e.g. "start A before B") — all
  sessions start concurrently
- Auto-retry or health monitoring of scenario sessions — the orchestrator
  agent handles this via existing primitives
- GUI or web UI for scenario management
- Nested scenarios (a scenario child starting its own scenario)

## Proposals

### Proposal 0: Do Nothing

The orchestrator agent uses existing primitives (`gr new`, `gr msg`, `gr type`)
to manually create and coordinate sessions. Skills teach the orchestrator
patterns for doing this.

**Pros:**
- Zero implementation cost
- Maximum flexibility — the AI adapts to each situation

**Cons:**
- Not reproducible — setup lives in AI context, lost on restart
- Fragile — depends on AI correctly executing multi-step setup each time
- No mutual awareness — siblings don't know about each other without manual
  injection
- No unit lifecycle — can't stop/status a "scenario" as a group

### Proposal 1: Skill-Only Approach

A skill defines a structured pattern for the orchestrator to follow. The skill
reads a scenario definition from `gr store` and guides the orchestrator through
creating sessions, injecting context, and coordinating.

**Pros:**
- Quick to implement (no Go changes)
- Can iterate on the format without recompiling
- Leverages existing primitives directly

**Cons:**
- Still depends on AI execution — the skill is guidance, not enforcement
- Scenario definition format isn't validated by graith itself
- No `gr scenario status` or `gr scenario stop` — still manual
- Mutual awareness injection is best-effort (AI might forget or get it wrong)

### Proposal 2: `gr scenario` CLI Commands

Add a `gr scenario` command family that reads a TOML scenario file and manages
the lifecycle as a first-class concept.

#### Scenario file format

```toml
version = 1

[scenario]
name = "tracing-pipeline"
goal = "Implement the new tracing pipeline across the backend, frontend SDK, and replay viewer"

[[sessions]]
name = "backend"
repo = "~/Code/my-backend"
agent = "claude"
model = "claude-opus-4-8"
role = "Backend engineer implementing the tracing ingest pipeline"
task = "Add OpenTelemetry trace ingestion to the backend service"

[[sessions]]
name = "sdk"
repo = "~/Code/my-frontend-sdk"
agent = "claude"
role = "Frontend SDK engineer adding trace export support"
task = "Add trace export to the frontend SDK that sends to the backend ingest endpoint"

[[sessions]]
name = "replay"
repo = "~/Code/my-replay-viewer"
agent = "claude"
base = "main"
agent_hooks = true
role = "Replay engineer adding trace overlay to session replay"
task = "Add a trace timeline overlay to the replay viewer that correlates with frontend traces"
```

Supported `[[sessions]]` fields: `name` (required), `repo` (required),
`agent`, `model`, `base`, `role`, `task`, `agent_hooks`, `shared`. All other
`CreateMsg` fields (`in_place`, `mirror`, `no_repo`,
`allow_concurrent`, `skip_model_validation`) are explicitly unsupported in v1
— the parser rejects unknown fields.

Validation rules:
- `version` must be `1` (forward compatibility)
- `scenario.name` is required, must match `[a-z0-9][a-z0-9-]*`
- Session names must be unique within the file
- Session names must match graith's session name grammar
- `repo` paths are expanded (`~` → home) by the CLI before sending to daemon
- `repo` paths must exist and be git repos (preflight check)
- `repo` paths must pass `allowed_repo_paths` config checks
- `agent` must be a configured agent name (or empty for default)
- Repos under `singleton = true` config reject a second concurrent session

#### CLI commands

```bash
# Start a scenario from a file (only works from orchestrator session)
gr scenario start ./tracing-pipeline.toml

# Start from stdin (for AI-generated scenarios)
gr scenario start -

# List running scenarios
gr scenario list

# Check status of all sessions in a scenario
gr scenario status tracing-pipeline

# Stop all sessions in a scenario
gr scenario stop tracing-pipeline

# Delete all sessions and clean up worktrees/branches
gr scenario delete tracing-pipeline
```

All commands support `--json` for agent-mode consumption. `start` returns the
full scenario record including generated `ScenarioID` and all session IDs.

#### How it works

**Daemon-orchestrated start.** The CLI parses and validates the TOML for
user-friendly errors, then sends a single `ScenarioStartMsg` to the daemon
containing the full parsed scenario definition. The daemon owns the entire
creation lifecycle:

1. **Preflight validation.** The daemon validates all repos exist, agents are
   configured, models are valid, names don't collide with existing sessions,
   and no running scenario with the same name exists. If any check fails, the
   entire start is rejected before creating anything.

2. **Reserve phase.** The daemon generates a `ScenarioID` and reserves all
   session records in `StatusCreating` state. Each gets a generated session
   ID. At this point, the full roster is known.

3. **Manifest construction.** The daemon builds a per-session manifest
   containing the complete roster (all sibling IDs, names, roles, repos) and
   the scenario goal.

4. **Start phase.** For each session, the daemon:
   - Sets up the git worktree and branch
   - Injects scenario environment variables:
     - `GRAITH_SCENARIO` — scenario ID
     - `GRAITH_SCENARIO_NAME` — scenario display name
     - `GRAITH_SCENARIO_ROLE` — this session's role
     - `GRAITH_SCENARIO_GOAL` — the overall goal
   - Sets `task` as the agent's initial prompt via the existing `Prompt`
     mechanism on `Create` (agent-agnostic delivery)
   - Publishes the manifest to the session's inbox
   - Starts the PTY process

   Sessions are created with `AgentHooks: true` so the existing `check-inbox`
   hook (`internal/cli/check_inbox.go`) automatically surfaces the manifest
   to agents on startup — no new env-var-signal mechanism needed.

5. **Rollback on failure.** If any session fails to start (worktree error,
   sandbox failure, PTY spawn failure), the daemon stops and deletes all
   already-created sessions, removes the scenario record, and returns a
   detailed error listing which sessions failed and why. This is
   all-or-nothing — no partial scenarios.

6. **Persist.** The daemon persists the `ScenarioState` record and updates
   all session records with `ScenarioID`. The orchestrator receives the full
   scenario record including all session IDs.

#### Scenario manifest (published to each session's inbox)

```json
{
  "version": 1,
  "scenario_id": "sc-a1b2c3d4",
  "scenario_name": "tracing-pipeline",
  "goal": "Implement the new tracing pipeline across the backend, frontend SDK, and replay viewer",
  "you": {
    "name": "sdk",
    "session_id": "x9y0z1w2",
    "role": "Frontend SDK engineer adding trace export support",
    "task": "Add trace export to the frontend SDK that sends to the backend ingest endpoint"
  },
  "siblings": [
    {
      "name": "backend",
      "session_id": "a1b2c3d4",
      "role": "Backend engineer implementing the tracing ingest pipeline",
      "repo": "my-backend"
    },
    {
      "name": "replay",
      "session_id": "e5f6g7h8",
      "role": "Replay engineer adding trace overlay to session replay",
      "repo": "my-replay-viewer"
    }
  ],
  "orchestrator": {
    "session_id": "i9j0k1l2",
    "name": "orchestrator"
  }
}
```

Agents address siblings by `session_id` in machine-generated messages (stable
identity) or by `name` for human-readable commands. The manifest is also
persisted to `gr store --shared` at `scenarios/<scenario_id>/manifest.json`
so it survives inbox acknowledgement and can be re-read by any participant
or a restarted orchestrator.

#### Lifecycle management

**Stop.** `gr scenario stop <name>` collects all sessions where `ScenarioID`
matches the target scenario, sends `StopMsg` to each individually. This is
**not** `StopWithChildren` on the orchestrator — it filters by scenario tag,
so it only stops scenario members and not unrelated orchestrator children.
Starred sessions are stopped (scenario membership overrides the star skip).
Sessions already stopped are silently skipped.

**Delete.** `gr scenario delete <name>` stops all running sessions in the
scenario, then deletes each (removing worktrees, branches). Removes the
`ScenarioState` record from daemon state. This is the full cleanup path.

**Status.** `gr scenario status <name>` returns per-session status
(running/stopped/errored), agent status, dirty/unpushed state, and an
aggregated scenario status:
- `running` — all sessions running
- `partial` — some running, some stopped/errored
- `stopped` — all sessions stopped
- `errored` — all sessions errored or a mix with errors

**List.** `gr scenario list` returns all `ScenarioState` records with
aggregated status.

#### Scenario state model

A new `ScenarioState` record in daemon state, keyed by generated `ScenarioID`:

```go
type ScenarioState struct {
    ID              string            `json:"id"`
    Name            string            `json:"name"`
    OrchestratorID  string            `json:"orchestrator_id"`
    Goal            string            `json:"goal"`
    SessionIDs      []string          `json:"session_ids"`
    Sessions        []ScenarioSession `json:"sessions"`
    Status          string            `json:"status"`
    CreatedAt       time.Time         `json:"created_at"`
    SourceFileHash  string            `json:"source_file_hash,omitempty"`
}

type ScenarioSession struct {
    Name     string `json:"name"`
    Role     string `json:"role"`
    Task     string `json:"task"`
    TaskDone bool   `json:"task_done,omitempty"`
    Repo     string `json:"repo"`
    Agent    string `json:"agent"`
    Model    string `json:"model,omitempty"`
    Shared   bool   `json:"shared,omitempty"`
}
```

`SessionState` gets these new fields:

```go
ScenarioID   string `json:"scenario_id,omitempty"`
ScenarioRole string `json:"scenario_role,omitempty"`
ScenarioGoal string `json:"scenario_goal,omitempty"`
```

Persisting `ScenarioRole` and `ScenarioGoal` on the session ensures the
scenario environment variables are re-injected on resume/restart — all three
start paths (create, resume, restart) read these fields and set the
corresponding `GRAITH_SCENARIO_*` env vars.

#### Authorization

The daemon handler validates the caller before processing `ScenarioStartMsg`:

1. Look up the caller's session by the authenticated session ID (from the
   token auth on the envelope — each session gets a unique `GRAITH_TOKEN`).
2. Bind `CallerSessionID` from the authenticated token so agents cannot
   spoof the orchestrator identity.
3. Verify `SessionState.SystemKind == SystemKindOrchestrator`.
4. Reject with a clear error if the caller is not the system orchestrator.

`gr scenario stop`, `delete`, and `status` are available to any session or
the human CLI — these are read/control operations, not creation. The
orchestrator-only gate applies only to `start`.

#### Files changed

| File | Change |
|------|--------|
| `protocol/messages.go` | Add `ScenarioStartMsg`, `ScenarioStopMsg`, `ScenarioDeleteMsg`, `ScenarioListMsg`, `ScenarioStatusMsg` |
| `protocol/messages.go` | Add `ScenarioID` to `SessionInfo` |
| `daemon/state.go` | Add `ScenarioState`, `ScenarioSession` types; add `ScenarioID`, `ScenarioRole`, `ScenarioGoal` to `SessionState`; state migration v9→v10 |
| `daemon/handler.go` | Handle scenario message types with orchestrator auth check |
| `daemon/scenario.go` | `StartScenario()`, `StopScenario()`, `DeleteScenario()`, `ResumeScenario()`, `AddToScenario()`, `TaskDone()` with two-phase creation, rollback, shared session support, and concurrency-safe manifest publishing |
| `cli/scenario.go` | New file: `gr scenario start/stop/delete/status/list` commands with TOML parsing and validation |
| `client/overlay.go` | Group sessions by scenario in the picker |

**Pros:**
- Reproducible — same file, same topology every time
- Agent-agnostic — works with Claude, Codex, or any agent (task delivered as
  initial prompt, manifest via inbox + check-inbox hook)
- First-class lifecycle — stop/status/delete as a unit
- Guaranteed mutual awareness — manifest constructed before PTY starts,
  delivered via inbox with hook-based surfacing
- All-or-nothing creation — no partial scenarios
- Scenario files are versionable and shareable

**Cons:**
- More implementation work (~800-1200 lines of Go including tests)
- New protocol messages, state types, and migration
- TOML format locked at v1 — changes require version bump
- Less flexible than ad-hoc orchestration for one-off tasks

### Proposal 3: Hybrid (Recommended)

Combine Proposal 2 (CLI) with a companion skill for the orchestrator.

The CLI handles the mechanical parts: parsing the scenario file, creating
sessions, injecting context, and lifecycle management. The skill teaches the
orchestrator how to use `gr scenario` commands and how to coordinate the
agents once they're running.

This means:
- The scenario file defines **what** to create (declarative, reproducible)
- The skill defines **how** to coordinate (flexible, AI-driven)
- The daemon enforces **who** can do what (orchestrator-only start via
  `SystemKind` check)

The skill also teaches the orchestrator to:
- Monitor child status via `gr scenario status`
- React to messages from children
- Use `gr store` for shared artifacts
- Handle failures (restart crashed children, redistribute work)
- On restart, run `gr scenario list` to rediscover active scenarios

The skill should call `gr scenario start -` (stdin) for dynamically generated
scenarios rather than reimplementing `Create` calls — the CLI is the single
entry point for scenario creation.

**Pros:**
- All the benefits of Proposal 2
- Orchestrator gets intelligent coordination on top of mechanical setup
- Skill can evolve independently of the CLI
- Skill can handle ad-hoc scenarios too (for one-off work without a file)

**Cons:**
- Slightly more work than Proposal 2 alone (skill authoring)
- Risk of the skill and CLI drifting if not maintained together — mitigate
  by documenting the scenario file schema and manifest format in `AGENTS.md`

## Consensus

Proposal 3 (Hybrid) is recommended. The CLI provides the foundation —
reproducible, agent-agnostic scenario management with daemon-enforced
guarantees. The skill adds intelligence on top. Ship the CLI first, add the
skill as a fast follow.

## Other Notes

### References

- Existing orchestrator implementation: `internal/daemon/orchestrator.go`
- Parent-child session tree: `SessionState.ParentID` in `internal/daemon/state.go`
- Hierarchical messaging: `gr msg send --children` in `internal/cli/msg.go`
- Check-inbox hook: `internal/cli/check_inbox.go` — surfaces unread inbox
  messages to agents with hooks enabled
- Template variables: `internal/config/template.go`
- Token auth: `daemon/auth.go` — per-session bearer token authentication,
  prevents agents from impersonating other sessions in scenario commands

### Security Considerations

- Scenario TOML can reference arbitrary repo paths — the daemon validates
  against `allowed_repo_paths` config during preflight.
- `task` and `role` strings become agent prompts and env vars — no shell
  injection risk since env vars are set programmatically, not via shell
  expansion.
- Manifest exposes repo names (basenames) but not absolute paths — siblings
  cannot access each other's worktrees (sandbox isolates them).
- Token auth ensures `sender_id` is verified by the daemon — agents cannot
  impersonate siblings or the orchestrator when `GRAITH_TOKEN` is present.

### Sandbox and Orchestrator File Access

The system orchestrator runs sandboxed in a scratch directory with no repo
context. It cannot read arbitrary disk paths like
`~/Code/my-project/scenarios/tracing.toml`. Supported input paths for
`gr scenario start`:

1. **Stdin** (`gr scenario start -`) — the orchestrator generates the TOML
   dynamically or receives it via `gr type` from a human. This is the primary
   path for sandboxed orchestrators.
2. **Shared store** — the user stores a scenario file with
   `gr store put --shared scenarios/my-scenario.toml --file ./my-scenario.toml`
   and the orchestrator reads it with `gr store get --shared`. The CLI can
   accept a `store:` prefix: `gr scenario start store:scenarios/my-scenario.toml`.
3. **Absolute path** — only works if the path is in the orchestrator's sandbox
   read dirs. Not the default path.

### Implementation Notes

#### Phase 1: Core CLI

1. Add `ScenarioState`, `ScenarioSession` types to daemon state
2. Add `ScenarioID`, `ScenarioRole`, `ScenarioGoal` to `SessionState`,
   migration v9→v10 (no-op — new optional fields default to zero values)
3. Add `ScenarioStartMsg` and handler with two-phase creation + rollback
4. Re-inject `GRAITH_SCENARIO_*` env vars in resume/restart paths when
   `ScenarioID` is set
5. Add `gr scenario start <file>` with TOML parsing, validation, and
   `--json` output
6. Add `gr scenario start -` for stdin
7. Add `gr scenario stop <name>` — stop by `ScenarioID` filter
8. Add `gr scenario delete <name>` — stop + delete + remove scenario record
9. Add `gr scenario list` — list scenario records
10. Add `gr scenario status <name>` — per-session and aggregated status
11. Persist manifest to `gr store --shared scenarios/<id>/manifest.json`

#### Phase 2: Overlay Integration

12. ~~Add `ScenarioID` to `SessionInfo` in protocol~~ (done)
13. ~~Group sessions by scenario in the session picker~~ (done — new
    "Scenarios" view mode)
14. ~~Show aggregated scenario status in overlay~~ (done — scenario group
    headers show running/partial/stopped/errored status)

#### Phase 3: Orchestrator Skill

15. ~~Write skill that teaches the orchestrator `gr scenario` commands~~ (done
    — `~/.claude/skills/scenario-orchestrator/SKILL.md`)
16. ~~Include coordination patterns~~ (done — fan-out/collect, progressive
    refinement, staged pipeline, emergency stop)
17. ~~Teach restart recovery~~ (done — `gr scenario list` + `gr scenario
    resume` pattern documented in skill)
18. ~~Document scenario file schema and manifest format in `AGENTS.md`~~ (done
    — shared sessions, authorization, and manifest format documented)

#### Open Questions

- Should scenarios support `includes` (multi-repo sessions)? The existing
  repo config `Includes` field could be exposed in the scenario file.
  **Resolution:** sessions inherit includes from repo config. Explicit TOML
  `includes` field deferred to a future iteration.

- Should `gr scenario resume` exist? **Resolution:** Yes, implemented.
  `gr scenario resume <name>` resumes all stopped/errored sessions and
  re-publishes updated manifests to all members.

- Can multiple scenarios run concurrently? The `ScenarioState` model supports
  this, but session name uniqueness across scenarios needs enforcement —
  either require globally unique session names (simplest) or prefix scenario
  session names with the scenario name (e.g. `tracing-pipeline-sdk`).
  **Recommendation:** require globally unique session names in v1, same as
  the rest of graith. Reject `scenario start` if any declared session name
  collides with an existing session.

- What happens when a child is restarted (new session ID)? **Resolution:**
  Manifests are automatically re-published to all siblings on resume and when
  new sessions are added. The persistent manifest in `gr store` is also
  updated.

- Should the "orchestrator" terminology be disambiguated? The codebase uses
  "orchestrator" for the system singleton (`SystemKindOrchestrator`), and this
  design uses it for "the session running a scenario." They are currently the
  same thing (only the system orchestrator can start scenarios), but if this
  restriction is relaxed later, "scenario coordinator" would be clearer.
  **Recommendation:** keep "orchestrator" for now since they are the same
  session. Revisit if the restriction is relaxed.

### Real-World Feedback (Post-Implementation)

The following gaps were identified during real-world orchestration of a
multi-repo rrweb orphan mutation fix scenario and have been addressed.

#### Lifecycle operations (implemented)

1. **`gr scenario resume <name>`** — Resumes all stopped/errored sessions in a
   scenario with one command. Env vars are re-injected on resume. Updated
   manifests are re-published to all sessions after resume.

2. **Task completion tracking** — `gr scenario task-done <name>` marks the
   calling session's task as complete in `ScenarioState`. The `task_done` field
   is visible in `gr scenario status` output and the manifest.

#### Dynamic membership (implemented)

3. **Adding sessions to a running scenario** —
   `gr scenario add <name> --name <session> --repo <path> --role "..."` creates
   a new session, tags it into the existing scenario, and re-publishes updated
   manifests to all siblings (including the new member).

4. **Manifest re-publish on resume and membership changes** — Whenever a
   session resumes (individually or via `gr scenario resume`) or a new session
   is added, the daemon re-publishes updated manifests to all scenario members'
   inboxes and persists them to the shared store. This eliminates staleness.

#### Scenario file conventions (implemented)

5. **Canonical location:** `~/.config/graith/scenarios/` (next to
   `config.toml`). Files in this directory can be started by name:
   `gr scenario start tracing-pipeline` resolves to
   `~/.config/graith/scenarios/tracing-pipeline.toml`. Direct paths and stdin
   (`-`) still work.

6. **`gr scenario list`** shows both running scenarios and available scenario
   files from the scenarios directory with their goal descriptions.

#### Parallel session creation (implemented)

7. **Concurrent creation** — Sessions are now created concurrently using
   goroutines instead of a sequential loop. All placeholders are removed first,
   then all `Create` calls run in parallel, and results are collected. If any
   fail, all successfully created sessions are rolled back.

#### Shared sessions (implemented)

8. **`shared = true` in scenario TOML** — A session with `shared = true`
   reuses an existing running session by name instead of creating a new one.
   This enables scenarios that reference sessions already running (e.g. the
   orchestrator itself, or a long-running service session).

   Semantics:
   - Name uniqueness is not enforced for shared sessions — they must match an
     existing running session
   - If the named session doesn't exist or isn't running, the scenario start
     fails
   - Shared sessions are tagged into the scenario (receive manifests, appear
     in `gr scenario status`) but are never stopped or deleted by scenario
     lifecycle operations (`gr scenario stop`, `gr scenario delete`)
   - The `shared` flag is tracked in `ScenarioSession.Shared` in state,
     protocol messages, and CLI output

   Example use case — an orchestrator scenario file that includes itself:

   ```toml
   [[sessions]]
   name = "orchestrator"
   repo = "~/Code/my-project"
   shared = true
   role = "Coordinator"
   ```

#### Concurrency safety fixes (implemented)

9. **Data race in `republishManifests`** — The function now snapshots `repos`
   and `orchestratorID` under `RLock`, then releases the lock before doing
   I/O. The scenario pointer is never used after the lock is released.

10. **Auth binding in `scenario_start` handler** — The handler now binds
    `CallerSessionID` from the authenticated session token, preventing
    agents from spoofing the orchestrator identity.

11. **Stale pointer in `AddToScenario`** — Snapshots `scenarioID`,
    `orchestratorID`, and `goal` as local strings under lock, then
    re-fetches the scenario after session creation to handle concurrent
    deletion gracefully.

12. **Rollback placeholder IDs** — `StartScenario` now updates
    `scenario.SessionIDs` with real session IDs as each session is created,
    so rollback on partial failure deletes actual sessions (not stale
    placeholder IDs).

13. **Redundant republish on empty resume** — `ResumeScenario` only calls
    `republishManifests` if at least one session was actually resumed.

#### Authorization hardening (implemented — review round 2)

14. **Auth checks on scenario mutation endpoints** — `scenario_stop`,
    `scenario_delete`, `scenario_resume`, and `scenario_add` now require the
    caller to be the scenario's orchestrator or a descendant. Unauthenticated
    (human CLI) callers are always permitted. New `checkScenarioOp` helper in
    `auth.go`.

15. **`scenario_start` requires authentication** — Unauthenticated connections
    can no longer send `scenario_start`. Previously, an unauthenticated client
    could supply any `CallerSessionID` and bypass the orchestrator check.

16. **Shared sessions fail closed** — If `shared = true` is set but no running
    session with that name exists, `StartScenario` now returns an error instead
    of creating a new session that would be orphaned by stop/delete.

17. **Snapshot all state under lock** — `StopScenario`, `DeleteScenario`, and
    `ResumeScenario` now snapshot `SessionState.Status` under `RLock` instead
    of reading it from the pointer after unlock. `StartScenario` snapshots
    `SystemKind` for the orchestrator check.

18. **Handle concurrent scenario deletion during create** — If `DeleteScenario`
    runs while `StartScenario` is in its concurrent create phase, the function
    now detects the nil scenario, rolls back all started sessions, and returns
    an error instead of crashing with a nil pointer dereference.

#### Overlay integration (implemented)

19. **Scenario view mode in session picker** — A new "Scenarios" tab in the
    overlay (ctrl+b w, then press right to cycle views) groups sessions by
    scenario ID with aggregated status (running/partial/stopped/errored).
    Sessions not in a scenario appear under "(no scenario)".

20. **`ScenarioName` on `SessionInfo`** — Added `ScenarioName` to
    `SessionState`, `SessionInfo`, and the protocol so the overlay can display
    human-readable scenario names in group headers.

#### Remaining items for future work

- `includes` support in scenario TOML — Sessions currently inherit includes
  from repo config. Explicit `includes = [...]` in the TOML could be added.
- `star = true` in TOML — Protect sessions from accidental deletion at
  creation. (`shared = true` already protects sessions from scenario
  stop/delete, but `star` would protect from manual deletion too.)
- Placeholder ID stability — `Create` still generates its own session ID, so
  the reserved placeholder ID changes during creation. A future change could
  have `Create` accept a pre-generated ID to close this race window.
