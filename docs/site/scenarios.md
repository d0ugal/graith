# Scenarios

Scenarios are declarative multi-session orchestration. A TOML file defines a group of related sessions — each with its own repo, agent, role, and task — and `gr scenario start` creates them atomically as a coordinated fleet.

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

[[sessions]]
name = "backend"
repo = "~/Code/my-backend"
agent = "claude"
model = "claude-opus-4-8"
role = "Backend engineer"
task = "Add tracing ingest endpoint and propagation middleware"

[[sessions]]
name = "frontend"
repo = "~/Code/my-frontend"
agent = "cursor"
model = "gemini-3.1-pro"
role = "Frontend developer"
task = "Add trace export UI and correlation ID headers"
agent_hooks = false
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

### `[[sessions]]` entries

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `name` | yes | — | Session name (must be unique across all sessions) |
| `repo` | yes | — | Repository path (`~` is expanded) |
| `agent` | no | config default | Agent type (`claude`, `codex`, `cursor`, etc.) |
| `model` | no | agent default | Model override (fills `{model}` in agent args) |
| `base` | no | repo default | Base branch for the worktree |
| `role` | no | — | Human-readable role description |
| `task` | no | — | Task/prompt sent to the agent on start |
| `agent_hooks` | no | `true` | Enable agent hooks (check-inbox, etc.) |
| `shared` | no | `false` | Reuse an existing running session by name |

Unknown fields are rejected — typos produce a parse error rather than being silently ignored.

**Shared sessions:** Set `shared = true` to reference an existing running
session instead of creating a new one. The named session must already be
running. Shared sessions participate in the scenario (receive manifests, appear
in status) but are never stopped or deleted by scenario lifecycle operations.

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

Output includes session names, IDs, status, agent, and role.

### `gr scenario list`

List all scenarios with their aggregate status.

```bash
gr scenario list
```

Aggregate status is derived from session states: `running` (all running), `stopped` (all stopped), `errored` (any errored), or `partial` (mixed).

### `gr scenario stop <name>`

Stop all running sessions in a scenario.

```bash
gr scenario stop tracing-pipeline
```

### `gr scenario delete <name>`

Delete a scenario and all its sessions, including worktrees and branches.

```bash
gr scenario delete tracing-pipeline
```

## How it works

1. The CLI parses the TOML file (with strict field validation) and sends a `scenario_start` control message to the daemon
2. The daemon validates all inputs: scenario name uniqueness, session name uniqueness, repo paths, agent configs
3. **Reserve phase:** placeholders are created atomically under the state lock
4. **Start phase:** each session is created using the normal `Create` flow, with scenario environment variables injected
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
    "task": "Add tracing ingest endpoint"
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

- **`you`** — its own identity, role, and task
- **`siblings`** — the other sessions in the scenario, with their roles and repos
- **`orchestrator`** — the parent session that started the scenario

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

## Constraints

- **Orchestrator only:** Only the orchestrator session (`system_kind: orchestrator`) can start scenarios
- **Unique names:** Scenario names must be unique across all scenarios. Session names must not collide with any existing session
- **Atomic creation:** All sessions are created or none are — partial failures trigger rollback
- **No live updates:** You cannot add or remove sessions from a running scenario. Delete and recreate instead
