# AGENTS.md — graith

Instructions for AI coding agents working on this codebase.

## What is graith?

A terminal multiplexer for AI coding agent sessions. It manages multiple agents
(Claude, Codex, OpenCode, Agy) running in isolated git worktrees, each in its
own PTY session that survives terminal closures. The binary is called `gr`.

Architecture: a long-lived daemon (`graithd`) owns PTYs and state; a stateless
CLI client (`gr`) connects over a Unix socket using a framed binary protocol.

## Build and test

```bash
go build ./cmd/graith          # build (binary is at ./graith)
go test ./...                  # unit tests
go test -race ./...            # race detector (CI runs this)
```

There is a `Makefile` with `build`, `test`, `lint`, `lint-only`, and `fmt` targets.
`make build` produces `./gr`. You can also use `go build` and `go test` directly.

The build path is `./cmd/graith`, not `./cmd/gr`.

## Lint

CI runs `golangci-lint` via Docker. To check locally:

```bash
gofmt -l ./...                 # formatting check
go vet ./...                   # static analysis
```

Always run `gofmt -w` on files you modify. CI will fail on gofmt violations.

## Project layout

```
cmd/graith/              Entry point (main.go)
internal/
  agent/                 Agent environment detection (auto-JSON)
  cli/                   Cobra command definitions (one file per command)
  client/                Client-side: connection, passthrough, overlay, shell
  config/                TOML config loading, defaults, XDG paths
  daemon/                Daemon: session manager, handler, state, server, messaging
  detector/              Agent type detection from running processes
  integration/           Integration tests (spawn real daemon)
  output/                Structured output helpers
  protocol/              Wire protocol: framing, control messages, encoding
  pty/                   PTY session management, scrollback buffer
  sandbox/               Pluggable OS sandbox backends (safehouse, nono)
  store/                 Flat-file git-backed document store
  version/               Build-time version injection
```

Key files by area:

| Area | Files | What they do |
|------|-------|-------------|
| Protocol | `protocol/frame.go`, `protocol/messages.go` | 5-byte framed multiplexing, JSON control envelope |
| Daemon | `daemon/handler.go` | Main message dispatch loop (all control message types) |
| Daemon | `daemon/daemon.go` | SessionManager: create, delete, resume, worktree lifecycle |
| Daemon | `daemon/state.go` | Persistent state (JSON file) |
| Daemon | `daemon/msgstore.go` | Inter-agent pub/sub messaging (SQLite-backed) |
| Client | `client/passthrough.go` | Raw PTY passthrough with prefix key handling |
| Client | `client/overlay.go` | Session picker UI (bubbletea), view modes (All/Needs Attention/Active), preview rendering |
| Client | `client/client.go` | Connection, handshake, scrollback preview (vt10x) |
| CLI | `cli/attach.go` | Attach loop: passthrough ↔ overlay ↔ reconnect |
| CLI | `cli/new.go` | Session creation with worktree setup |
| CLI | `cli/msg.go` | `gr msg pub/sub/send/ack/topics` — inter-agent messaging |
| Agent | `agent/agent.go` | Auto-detect agent environments, enable JSON output |
| PTY | `pty/session.go` | PTY lifecycle, resize, I/O multiplexing |
| PTY | `pty/scrollback.go` | Append-only scrollback file with tail reads |
| Auth | `daemon/auth.go` | Per-session token auth: authorization rules, identity forcing, descendant checks |
| Sandbox | `sandbox/sandbox.go`, `sandbox/safehouse.go`, `sandbox/nono.go` | Pluggable backends: Wrap dispatch, per-backend command/profile construction, availability |
| Sandbox | `sandbox/why.go`, `cli/sandbox.go` | `gr sandbox why` — explain an allow/deny decision via nono's `nono why` oracle against graith's generated profile (nono backend only) |
| Store | `store/store.go` | Flat-file git-backed document store with key validation, git commits |
| Scenario | `daemon/scenario.go` | Scenario lifecycle: start, stop, resume, delete, add, task-done, status, list |
| Scenario | `cli/scenario.go` | `gr scenario start/stop/resume/delete/add/task-done/status/list` commands |

## Architecture patterns

**Protocol**: Channel 0x00 = JSON control messages, Channel 0x01 = raw PTY data.
Control messages use an envelope `{"type":"...", "payload":{...}}`. Add new
message types by adding a struct in `protocol/messages.go` and handling the
type string in `daemon/handler.go`.

**Session lifecycle**: Create → worktree + branch → start agent process → attach.
Resume → restart process in existing worktree.

**Delete is soft by default** (`SessionManager.SoftDelete`): `gr delete` stops the
agent and marks the session deleted (a `DeletedAt`/`ExpiresAt` marker on
`SessionState`), hiding it from `gr list` and the overlay but **keeping the
worktree, branch, and state** for a retention window (default 24h, `[delete]
retention`). `gr restore` clears the marker (back to `stopped`); `gr list
--deleted` and the overlay's *Deleted* view show trashed sessions with their
expiry. A background purge loop (`RunPurgeLoop`) hard-deletes sessions past their
frozen `ExpiresAt`. `gr purge` is the only destructive verb: an immediate hard
delete (`SessionManager.Delete` → kill process → remove worktree → delete branch),
bypassing the window, and owns the unsaved-work confirmation. The daemon routes on
the `DeleteMsg.Purge` flag: `Purge` → hard; `!Purge && retention>0` → soft;
`!Purge && retention==0` → rejected (delete never destroys). Scenario teardown and
other internal callers invoke `Delete` directly and are always hard. Because a
soft-deleted session is a hidden `stopped` session that still holds its token,
ID-addressable operations (resume, restart, rename, star, update, fork, migrate,
set-summary) carry an `IsSoftDeleted()` guard so hiding can't be bypassed by raw
ID; `gr delete --force`/`-y` are inert deprecated aliases.

**Passthrough**: When attached, the client enters raw terminal mode and forwards
stdin/stdout bytes directly to/from the daemon. A prefix key (default ctrl+b)
intercepts the next keystroke for commands (d=detach, w=overlay, s=shell, etc).

**State persistence**: `state.json` in the data dir. Loaded on daemon start,
saved on mutations. Sessions survive daemon restarts.

**Sandbox**: When enabled via config, agent processes are wrapped in an OS
sandbox by a pluggable backend selected with `[sandbox] backend`
(`safehouse` = macOS `sandbox-exec`; `nono` = Landlock+seccomp on Linux /
Seatbelt on macOS). `backend` is **required** when the sandbox is enabled —
there is no default; an unset backend fails closed. The sandbox is config-only —
no CLI flags — so agents can't escape by spawning unsandboxed children
(restrictions are kernel-inherited). The daemon resolves the merged sandbox
config (global + per-agent), expands `~`/globs to absolute paths, and either
builds a `safehouse wrap` command or generates a per-session nono JSON profile
(`nono run --profile`). The nono profile maps write_dirs + worktree to
`filesystem.allow` (read+write, not the write-only `filesystem.write`), read_dirs
to `filesystem.read`, and the file-level grants read_files/write_files to
`filesystem.read_file` / `filesystem.allow_file` (for single files that can't be
a directory grant without over-sharing, e.g. an agent's `~/.claude.json` login
file), the env allowlist to `environment.allow_vars` (incl. PATH/HOME/GRAITH_*),
grants read on the agent binary dir, and rejects read-only read_dirs/read_files
grants located under `/tmp`/`$TMPDIR` with a clear config error (fail-closed):
those prefixes are writable by default under nono and it cannot make a subpath
read-only — Landlock has no deny-under-an-allowed-parent and macOS deny removes
read too (issue #789). An optional `[sandbox.network]` block
(`block` / `allow_domains`) maps to the profile's `network.block` /
`network.allow_domain`, and `[sandbox] signal_mode` maps to
`security.signal_mode`. `process-control` gates under safehouse but is a no-op
under nono unless `signal_mode = "isolated"` is set. If the selected backend
can't enforce (missing binary, kernel too old, nono below the version pin, or a
network policy requested on a kernel below Landlock ABI v4 / on safehouse),
session creation fails closed. See
`docs/design/2026-07-02-nono-sandbox-design.md`.

**Scenarios**: Declarative multi-session orchestration. A TOML file defines
sessions (name, repo, agent, role, task), and `gr scenario start` creates
them concurrently with two-phase rollback. Each session gets a manifest (via
inbox message + shared store) describing itself, its siblings, and the
orchestrator. The daemon tracks scenarios in `ScenarioState` alongside
sessions. Only the orchestrator session (`SystemKind: orchestrator`) can
start scenarios. Scenarios support dynamic membership (`gr scenario add`),
task completion tracking (`gr scenario task-done`), and bulk resume
(`gr scenario resume`). Manifests are re-published whenever membership
changes or sessions resume. Scenario TOML files live in
`~/.config/graith/scenarios/` and can be started by name.

## Environment variables

The daemon sets these in every agent process:

- `GRAITH_SESSION_ID` — unique session ID
- `GRAITH_SESSION_NAME` — human-readable session name
- `GRAITH_AGENT_TYPE` — agent type (e.g. `claude`, `codex`)
- `GRAITH_WORKTREE_PATH` — absolute path to the session worktree
- `GRAITH_REPO_PATH` — absolute path to the source repository (canonical)
- `GRAITH_TMPDIR` — temporary directory for the repo (persists across sessions)
- `GRAITH_TOKEN` — bearer token for session authentication (used automatically by `gr`)
- `TMPDIR` — set to `GRAITH_TMPDIR` so `mktemp` etc. land in the tmp dir

These are used by `gr msg pub/sub` to identify the sender automatically.
`GRAITH_TOKEN` is used by the CLI to authenticate with the daemon — agents
cannot impersonate other sessions when this token is present.

For scenario sessions, these additional env vars are set at creation and on
resume/restart:

- `GRAITH_SCENARIO` — scenario ID
- `GRAITH_SCENARIO_NAME` — scenario display name
- `GRAITH_SCENARIO_ROLE` — this session's role in the scenario
- `GRAITH_SCENARIO_GOAL` — the overall scenario goal

### Agent-mode detection

When `gr` detects it's running inside an AI agent (via `GRAITH_SESSION_ID`,
`CLAUDECODE`, `CURSOR_AGENT`, `GITHUB_COPILOT`, `AMAZON_Q`, or `OPENCODE`),
it auto-enables `--json` output. Override with `GR_AGENT_MODE=0` to disable
or `GR_AGENT_MODE=1` to force. The `--agent-mode` flag also forces it on.

### Hierarchical session control

- `gr stop --children` / `gr delete --children` — operate on all descendant
  sessions. When run without a positional arg, auto-resolves from
  `GRAITH_SESSION_ID` and excludes the calling session.
- `gr msg send --children "body"` — send to all descendant sessions' inboxes.
- `gr msg send --parent "body"` — send to the parent session's inbox.

These flags make it easy for agents to manage their child sessions and
communicate up/down the session tree without knowing session names.

## Configuration

TOML at `~/.config/graith/config.toml` (or `$XDG_CONFIG_HOME/graith/config.toml`).
All fields optional — `config.Default()` provides sensible defaults. See
`internal/config/config.go` for the full struct.

Template variables in agent args: `{agent_session_id}`, `{session_id}`, `{session_name}`,
`{username}`, `{worktree_path}`, `{model}`, `{fork_source_agent_session_id}`.

### MCP servers

MCP servers are configured under `[[mcp_servers]]`. The daemon spawns one
process per session+server (keyed by `<session_id>-<server>`), so each session
gets its own server process. The `command`, `args`, and `env` values support
per-session template expansion — `{session_id}`, `{session_name}`, and
`{worktree_path}` — so a server can be given session-scoped resources. (Only
these three vars are populated; other template names like `{username}` expand
to empty.) When there's no session, `{session_id}` falls back to the proxy ID
(`-<server>`) so it isn't empty; note this fallback is per-server, not unique
per connection, and `{session_name}`/`{worktree_path}` still expand to empty in
that case. Real agent sessions always have a session ID, so this only affects
session-less proxies (e.g. the auto-injected `graith` server, which templates
nothing). Literal `{name}` tokens are reserved as template syntax — an unknown
name is a hard error.

This matters for stateful servers like `chrome-devtools-mcp`, which otherwise
default to a single shared Chrome profile and debug port — every session would
control the same browser. Give each session its own profile:

```toml
[[mcp_servers]]
name = "chrome-devtools"
command = "npx"
args = [
  "chrome-devtools-mcp@latest",
  "--isolated",
  "--user-data-dir=/tmp/graith-chrome-{session_id}",
]
sandbox = false
```

`--isolated` launches a fresh browser with an ephemeral debug port per process,
and the templated `--user-data-dir` keeps each session's profile separate.

## Testing

- Unit tests live next to the code (`*_test.go`)
- Integration tests are in `internal/integration/` — they spawn a real daemon
- Tests must pass with `-race` flag
- Use `t.TempDir()` for test fixtures, not hardcoded paths

### Coverage expectations

Test coverage is a hard requirement, not a nice-to-have.

- **Keep coverage high and never regress it.** The target is **≥ 80%** of Go
  statements overall. The self-hosted Coverage workflow comments the Go coverage
  delta on every PR — a negative delta is a red flag and needs justification in
  the PR description.
- **New code ships with tests.** Any PR that adds behaviour adds the tests that
  exercise it. Don't defer test coverage to "a follow-up".
- **Test real behaviour, not lines.** Cover edge cases and error paths — invalid
  input, missing files, auth failures, context cancellation, rollback — not just
  the happy path. Tests written only to touch lines are worse than no tests: they
  give false confidence and calcify implementation details.
- **Some code is genuinely hard to unit-test** (raw PTY passthrough, the
  interactive attach loop, unix-socket servers). Extract the pure logic
  (state machines, formatters, validators) into functions you *can* test, and
  cover those. Prefer testing a bubbletea `Model`'s `Update`/`View` over driving
  a real terminal.
- Name test files with the plain `<file>_test.go` convention — don't encode the
  reason you wrote them (e.g. `foo_coverage_test.go`) into the filename.

### Regression tests

**Every bug fix must come with a regression test.** The test should fail against
the old (buggy) code and pass with the fix — write it first, watch it fail, then
fix. This locks the bug closed, documents the intended behaviour, and stops the
same regression from silently returning later. A bug-fix PR without a regression
test should be sent back.

### Scots words in test fixtures

The project name "graith" is an old Scots word meaning equipment or gear. As a
nod to this heritage, test fixture strings (session names, topic names, message
bodies, scenario names, repo names, etc.) should use old Scots words instead of
generic placeholders like "test-session", "foo", "my-topic", etc.

Map words thematically where it fits:

| Word | Meaning | Use for |
|------|---------|---------|
| `braw` | fine, handsome | session names (success cases) |
| `canny` | careful, wise | session names |
| `dreich` | dreary, wet | error/edge cases |
| `bide` | stay, wait | resume/persist tests |
| `blether` | chat, gossip | messaging topics |
| `fash` | worry, trouble | error cases |
| `ken` | know | info/query tests |
| `thrawn` | stubborn, twisted | failure/rejection cases |
| `croft` | small farm | repo names |
| `bothy` | small shelter | worktree/workspace names |
| `loch` | lake | store tests |
| `glen` | valley | path tests |
| `ben` | mountain peak | hierarchy (parent sessions) |
| `kirk` | church | structured/formal tests |
| `wynd` | narrow lane | path tests |
| `haar` | sea fog | unclear/edge cases |
| `scunner` | annoyance | error/rejection cases |
| `bairn` | child | child session tests |
| `auld` | old | rename/legacy tests |
| `bonnie` | beautiful | success/happy-path cases |
| `whin` | gorse bush | misc fixtures |
| `skelf` | splinter | small/detail tests |
| `hame` | home | home/root path tests |
| `speir` | to ask/inquire | query tests |
| `strath` | wide valley | scenario names |
| `clachan` | small village | multi-session groups |
| `neep` | turnip | simple/trivial fixtures |
| `brae` | hillside | hierarchy tests |
| `brig` | bridge | connection/protocol tests |

This is just for human-readable fixture strings — not for Go variable names,
struct field names, or test function names.

## Conventions

- Commit messages: conventional commits (`feat:`, `fix:`, `chore:`, etc.)
- `make build` produces `./gr`; `go build` and `go test` also work directly
- All packages are under `internal/` — no public API
- Errors: return `fmt.Errorf(...)`, don't use `log.Fatal` in library code
- The daemon logs to `~/.local/share/graith/daemon.log` (slog, JSON format)

## Using graith to work on graith

graith can manage its own development sessions. This is the intended workflow
for working on graith itself:

### Creating sessions for parallel work

```bash
# Work on a feature in an isolated worktree
gr new fix-overlay --repo ~/Code/graith

# Work on CI in parallel
gr new setup-ci --repo ~/Code/graith

# Work with a different agent
gr new refactor-protocol --agent codex --repo ~/Code/graith
```

Each session gets its own git worktree and branch. Agents can work in parallel
without stepping on each other's files.

### Switching between sessions

Press `ctrl+b w` to open the session picker, or use `ctrl+b n`/`ctrl+b p` to
cycle through sessions. `ctrl+b d` detaches without stopping the agent.
`ctrl+b c` opens a create-session form, or press `n` in the session picker.

### Inter-agent messaging

Sessions can communicate via direct messages or the pub/sub system:

```bash
# Send a message directly to a session's inbox (preferred for 1:1 comms)
gr msg send fix-overlay "Found a race condition in handler.go:245"

# Publish to a topic (for broadcast to multiple sessions)
gr msg pub --topic code-review "Found a race condition in handler.go:245"

# From another session, read messages
gr msg sub --topic code-review --all

# Wait for the next message (blocks until one arrives)
gr msg sub --topic code-review --wait

# Follow a stream continuously
gr msg sub --topic code-review --follow
```

Use `gr msg send <session> <body>` to message a specific session — this is
the right choice when you want to provide context to one agent. Use
`gr msg pub --topic` for broadcasting to any session that subscribes.

Use `--ack` to mark messages as read.

### Shared document store

Sessions can persist documents that survive worktree deletion. Store operations
go directly to flat files on disk (not through the daemon), so files are
browsable in any IDE and git history is available via `git log` in the store
directory.

```bash
# Store a document (key should include a file extension)
gr store put design/api.md --file ./api-design.md
gr store put design/api.md "# API Design\n\nEndpoints: ..."
echo '{"score": 85}' | gr store put tribunal/2026-06-15.json

# Retrieve a document (always outputs raw body)
gr store get design/api.md

# List documents (optional prefix filter)
gr store list
gr store list design/
gr store ls tribunal/

# Append a line to a document (creates if missing, adds newline)
gr store append logs/builds.jsonl '{"status":"pass","ts":"2026-06-16"}'
echo '{"run":2}' | gr store append logs/builds.jsonl

# Remove a document
gr store rm design/api.md

# Explicit repo scoping (when not in a session)
gr store list --repo ~/Code/graith

# Shared store (not scoped to any repo)
gr store put --shared prompts/review.md "Review this code..."
gr store get --shared prompts/review.md
gr store ls --shared
```

Documents are scoped per-repo by default — sessions for `~/Code/graith` share
one namespace, sessions for `~/Code/other` share another. Use `--shared` to
access a global store that is not scoped to any repo. Keys are slash-separated
paths and should include a file extension (e.g. `.md`, `.json`) to indicate
content type. The repo path is canonicalized, so different path spellings for
the same repo resolve to the same namespace.

Use `gr store` for artifacts you want to keep but don't want to commit:
design docs, research notes, build outputs, shared context between agents.
Use `gr store append` with `.jsonl` keys for log-style data (e.g. tribunal
results, build history) where each entry is one JSON line. Use `--shared` for
artifacts that span repos (e.g. prompt templates, cross-project config).

### Typing into sessions remotely

```bash
# Send input to a running session (appends newline by default)
gr type fix-overlay "/help"
gr type fix-overlay "please review the changes" 

# Send without trailing newline
gr type fix-overlay --no-newline "y"
```

### Status summaries

**You should call `gr status` to keep the session picker informed of what
you're doing.** This is visible to the user and other agents in the overlay
(ctrl+b w). Update it at key milestones — when you start a new phase of work,
when you're waiting on something, and when you're done.

```bash
# Set your current status (auto-detects session from GRAITH_SESSION_ID)
gr status "Exploring code"
gr status "Running tests"
gr status "Waiting for CI"
gr status "Reviewing PR"
gr status "Done"

# Override the TTL for long-running waits
gr status --ttl 30m "Waiting for CI"

# Clear when no longer relevant
gr status --clear
```

The status auto-expires when the agent is actively producing output but hasn't
updated it (default 5 minutes). When idle, it fades but persists — so "Done"
on a stopped session stays visible.

### Monitoring sessions

```bash
# List all sessions with status
gr list

# Stream logs from a session
gr logs fix-overlay --follow

# Block until a session matches a condition (event-driven, no polling)
gr wait fix-overlay --contains "tests passed" --timeout 5m
gr wait fix-overlay --status stopped
gr wait fix-overlay --idle --timeout 10m

# Check health
gr doctor

# Garbage-collect orphaned worktrees/scratch dirs (dry run by default)
gr gc
gr gc --force
```

`gr gc` reaps worktree and scratch directories under the data dir that have no
matching session — typically left by a daemon crash mid-delete. It is a dry run
by default; `--force` actually removes them. Worktrees with uncommitted changes
are never removed. The daemon also runs this cleanup automatically on startup
(alongside tombstone-based resume of interrupted deletes), so `gr gc` is mainly
for reclaiming disk on demand. State writes (`state.json`, the document store)
use a crash-safe temp→fsync→rename pattern via `internal/atomicfile`.

`gr wait` exits 0 as soon as the condition is met and non-zero on timeout, so
orchestrators can gate on a session's output or state instead of polling
`gr logs -f`. Exactly one of `--contains` (regexp over output), `--status`
(lifecycle status, e.g. `running`/`stopped`), or `--idle` (agent at rest) must
be given.

### Scenarios (multi-session orchestration)

Scenarios let you define a fleet of related sessions in a TOML file and launch
them as a coordinated group. Each session knows about the others and can
communicate with them via messaging.

**TOML file format:**

```toml
version = 1

[scenario]
name = "tracing-pipeline"
goal = "Build end-to-end distributed tracing"

[[sessions]]
name = "backend"
repo = "~/Code/my-backend"
agent = "claude"
model = "claude-opus-4-8"
role = "Backend engineer"
task = "Add tracing ingest endpoint"

[[sessions]]
name = "frontend"
repo = "~/Code/my-frontend"
agent = "cursor"
model = "gemini-3.1-pro"
role = "Frontend developer"
task = "Add trace export UI"
agent_hooks = false
```

**Fields per session:**

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `name` | yes | — | Session name (lowercase alphanumeric + hyphens) |
| `repo` | yes | — | Repository path (`~` expanded) |
| `agent` | no | config default | Agent type (`claude`, `codex`, `cursor`, etc.) |
| `model` | no | agent default | Model override (fills `{model}` in agent args) |
| `base` | no | repo default | Base branch for the worktree |
| `role` | no | — | Human-readable role description |
| `task` | no | — | Task/prompt sent to the agent on start |
| `agent_hooks` | no | `true` | Enable agent hooks (check-inbox, etc.) |
| `shared` | no | `false` | Reuse an existing running session by name |

Unknown fields are rejected — typos in field names produce a parse error.

**Shared sessions:** A session with `shared = true` reuses an existing running
session instead of creating a new one. The named session must already exist and
be running — otherwise the scenario start fails. Shared sessions receive
manifests and appear in `gr scenario status` but are never stopped or deleted
by scenario lifecycle operations. This is useful for including the orchestrator
itself or long-running service sessions in a scenario.

**Scenario file location:** Place scenario TOML files in
`~/.config/graith/scenarios/` (next to `config.toml`). Files in this
directory can be started by name: `gr scenario start tracing-pipeline`
resolves to `~/.config/graith/scenarios/tracing-pipeline.toml`.

**CLI commands:**

```bash
# Start a scenario by name or file path
gr scenario start tracing-pipeline
gr scenario start ./scenario.toml
cat scenario.toml | gr scenario start -

# Resume all stopped sessions in a scenario
gr scenario resume tracing-pipeline

# Add a session to a running scenario
gr scenario add tracing-pipeline --name review --repo ~/Code/backend --role "Reviewer"

# Mark this session's task as complete
gr scenario task-done tracing-pipeline

# Check status
gr scenario status tracing-pipeline
gr scenario list

# Stop all sessions in a scenario
gr scenario stop tracing-pipeline

# Delete scenario and all its sessions/worktrees
gr scenario delete tracing-pipeline
```

**How it works internally:**

1. The CLI parses the TOML and sends a `scenario_start` control message
2. The daemon validates inputs, reserves placeholders, then creates all
   sessions concurrently with scenario env vars injected
3. After all sessions start, the daemon publishes a manifest to each
   session's inbox and persists it to the shared store at
   `scenarios/<id>/manifest-<name>.json`
4. If any session fails to create, already-started sessions are rolled back
5. Manifests are re-published when sessions resume or new sessions are added

**Manifest:** Each session receives a JSON manifest describing the scenario:

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

Sessions can use `gr msg send <sibling-name> "message"` to coordinate with
siblings, and `gr msg send --parent "message"` to report back to the
orchestrator.

**Authorization:** `scenario_start` requires authentication and verifies the
caller is the system orchestrator. `scenario_stop`, `scenario_delete`,
`scenario_resume`, and `scenario_add` require the caller to be the scenario's
orchestrator or a descendant. `scenario_status` and `scenario_list` are
read-only and available to any session or the human CLI. Unauthenticated
(human CLI) callers can manage scenarios without restriction.

**Constraints:** Only the orchestrator session (system kind `orchestrator`)
can start scenarios. Scenario names must be globally unique. Session names
within a scenario must not collide with existing sessions (except shared
sessions, which reuse existing sessions by name).

### Daemon management

The daemon auto-starts on first command. To manage it explicitly:

```bash
gr daemon start
gr daemon stop
gr daemon restart         # preserves sessions (picks up new binary)
```

After rebuilding `gr` (e.g., `go build -o $(which gr) ./cmd/graith`), run
`gr daemon restart` to pick up the new daemon binary. Note: the client binary
in your current shell is a separate process and won't update until you restart
your shell or rebuild.

### Practical tips for self-development

1. **Always use `--repo`** when creating sessions, since the worktree for the
   session you're currently in is not the main repo.

2. **Test overlay changes** by building, restarting the daemon, then pressing
   `ctrl+b w` in an attached session.

3. **Test passthrough changes** by rebuilding and re-attaching — the client
   binary is what handles key interception.

4. **Test daemon changes** by rebuilding and running `gr daemon restart` —
   sessions are preserved across restarts.

5. **Test protocol changes** require rebuilding both client and daemon, since
   both sides must agree on the wire format.

6. **Integration tests** spawn their own daemon, so they test the full
   client→daemon→PTY pipeline. Run them when changing protocol or handler code.
