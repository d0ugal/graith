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
| Protocol | `protocol/manifest.go` | Cross-language conformance: reflects every wire struct into a language-neutral manifest (fixture) the Swift client asserts against (#1129) |
| Daemon | `daemon/handler.go` | Main message dispatch loop (all control message types) |
| Daemon | `daemon/daemon.go` | SessionManager: create, delete, resume, worktree lifecycle |
| Daemon | `daemon/launch.go` | Launch throttle (semaphore bounding concurrent agent spawns) + startup watchdog (restarts sessions stuck in `unknown`/no-output past `[launch] startup_timeout`) |
| Daemon | `daemon/state.go` | Persistent state (JSON file) |
| Daemon | `daemon/prwatch.go`, `daemon/ghpr.go` | PR/CI poll loop, `gh` reader, comment author-trust gate, notification cursor/diff |
| Daemon | `daemon/prrefwatch.go` | git-refs fsnotify watch that kicks an immediate PR poll on push/commit/checkout (near-instant detection; poll is fallback) |
| Daemon | `daemon/msgstore.go` | Inter-agent pub/sub messaging (SQLite-backed) |
| Client | `client/passthrough.go` | Raw PTY passthrough with prefix key handling |
| Client | `client/overlay.go` | Session picker UI (bubbletea), view modes (All/Needs Attention/Active), preview rendering |
| Client | `client/client.go` | Connection, handshake, scrollback preview (vt10x) |
| CLI | `cli/attach.go` | Attach loop: passthrough ↔ overlay ↔ reconnect |
| CLI | `cli/new.go` | Session creation with worktree setup |
| CLI | `cli/msg.go` | `gr msg pub/sub/send/ack/topics` + `gr msg jail list/show/release` — inter-agent messaging & PR-comment quarantine |
| Jail | `daemon/jail.go`, `daemon/jailstore.go` | PR-comment quarantine: jail dropped untrusted comments, list/show, release (human/orchestrator), auto-release on trust-config reload |
| Agent | `agent/agent.go` | Auto-detect agent environments, enable JSON output |
| PTY | `pty/session.go` | PTY lifecycle, resize, I/O multiplexing |
| PTY | `pty/scrollback.go` | Append-only scrollback file with tail reads |
| Auth | `daemon/auth.go` | Per-session + human token auth: fail-closed local default, authorization rules, identity forcing, descendant checks |
| Sandbox | `sandbox/sandbox.go`, `sandbox/safehouse.go`, `sandbox/nono.go` | Pluggable backends: Wrap dispatch, per-backend command/profile construction, availability |
| Sandbox | `sandbox/why.go`, `cli/sandbox.go` | `gr sandbox explain` — predictive allow/deny via a policy oracle (`nono why`) against graith's generated profile (nono only; safehouse errors → points at `watch`) |
| Sandbox | `sandbox/denials.go`, `cli/sandbox.go` | `gr sandbox watch [session]` — retrospective: live-tail (default) or `--recent` window of macOS Seatbelt denials from the unified log; `--proc`/`[session]` scoping (macOS-only; covers both backends on macOS) |
| Store | `store/store.go` | Flat-file git-backed document store with key validation, git commits |
| Scenario | `daemon/scenario.go` | Scenario lifecycle: start, stop, resume, delete, add, task-done, status, list |
| Scenario | `cli/scenario.go` | `gr scenario start/stop/resume/delete/add/task-done/status/list` commands |
| Scenario | `scenariofile/scenariofile.go` | Shared scenario-TOML loader (used by CLI + daemon trigger action) |
| Trigger | `config/trigger.go` | `[[trigger]]` config types + validation |
| Trigger | `daemon/trigger.go` | Schedule source (`RunTriggerLoop`), run-state machine, action dispatch, delivery, `gr trigger` status/control API |
| Trigger | `daemon/trigger_actions.go` | Action executors: command (sandboxed), session/ensure-reviewer, scenario, message |
| Trigger | `daemon/filewatch.go` | Watch source (`RunFileWatchLoop`): binding reconcile, recursive fsnotify + gitignore pruning, debounce |
| Trigger | `cli/trigger.go` | `gr trigger list/status/run/pause/resume` commands |
| Capabilities | `capabilities/capabilities.go`, `capabilities/capabilities.json` | CLI/iOS/macOS capability matrix: hand-maintained JSON manifest (source of truth) + loader/validator/renderer; drift tests keep `website/content/docs/capabilities.md` (doc ↔ manifest) and the GUI fixture (manifest ↔ shared Swift affordance registry, #1149) in sync |

## Architecture patterns

**Protocol**: Channel 0x00 = JSON control messages, Channel 0x01 = raw PTY data.
Control messages use an envelope `{"type":"...", "payload":{...}}`. Add new
message types by adding a struct in `protocol/messages.go` and handling the
type string in `daemon/handler.go`.

**Protocol conformance (Go ↔ Swift, #1129)**: the wire protocol is defined
twice — Go `protocol/messages.go` and the hand-written Swift
`gui/shared/Sources/GraithProtocol/Messages.swift`. `protocol/manifest.go`
reflects every wire struct into a language-neutral manifest committed as a
fixture at
`gui/shared/Tests/GraithProtocolTests/Fixtures/protocol_manifest.json`. **When
you add or change a wire struct in `messages.go`, register it in
`registeredTypes`, classify it in `swiftAnnotations`
(`required`/`planned`/`na`), and regenerate the fixture:**
`go test ./internal/protocol -run TestManifestUpToDate -update`.
`TestManifestRegistryComplete` fails closed if a struct is unregistered,
unclassified, or double-registered (same discipline as `remoteMessagePolicy`);
`TestManifestUpToDate` runs on every PR (Go CI has no paths filter) and fails if
the fixture is stale. On the Swift side, `ManifestConformanceTests` decodes the
same fixture and fails if any `required` type isn't modelled in `Messages.swift`
(or if a modelled type can't decode a synthesized instance). Swift deliberately
models a *subset* of each Go type, so conformance means: every required type is
present and decodable, Swift's required-field set is a subset of Go's (checked
via a full-fields and a required-fields-only synthesis pass), array element and
nested-object shapes decode, and the probe's Swift type matches the manifest's
`swift_type` — not that Swift mirrors every Go field. Because the fixture lives
under `gui/`, regenerating it touches a `gui/` file, so the paths-filtered gui/
Swift CI runs and goes red when `Messages.swift` is behind (that job is not a
required status check, so it surfaces the failure rather than hard-blocking the
merge).

**Session lifecycle**: Create → worktree + branch → start agent process → attach.
Resume → restart process in existing worktree.

**Headless sessions** (experimental): a session's transport is a persisted
`DriverKind` (`"pty"` | `"headless"`), resolved once at creation. `headless = true`
/ `gr new --headless` runs the agent via Claude Code's stream-json mode
(`claude -p --output-format stream-json`) instead of an interactive PTY, for
fire-and-forget sessions no human will attach to (tribunal judges, trigger
briefings) — graith parses the typed event stream for status and captures the
run's cost/token usage from the result envelope (surfacing to `gr list` is a
planned follow-up). v1 is Claude-only and one-shot: it requires a prompt, is
incompatible with the sandbox, implies `--background`, and cannot be resumed once
it exits. Gated behind `[headless] experimental` (inert unless on); `[headless]
default` and per-agent `[agents.<name>] headless_capable` are the other inputs.
The headless engine lives in `internal/headless` and satisfies
`daemon.SessionDriver`. `gr attach` on a headless session is refused with a
pointer to `gr logs -f` (read-only). Planned follow-ups (issue #1075):
convert-to-interactive on attach (`claude --resume` in a PTY), control-protocol
interrupt/approvals wiring, cost/usage enrichment, and `[trigger.action]
headless`. See `docs/design/2026-07-13-headless-stream-json-design.md`.

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

**PR & CI awareness**: The daemon's `pr_watch` loop (`internal/daemon/prwatch.go`,
`ghpr.go`) resolves each session's GitHub PR via `gh`, polls CI checks and
comments, and delivers structured notifications to the owning session's inbox
(auto-resuming a stopped agent). Detection is made near-instant by a git-refs
file watch (`internal/daemon/prrefwatch.go`): each running session's worktree
git dirs (`HEAD` + `refs/` + reflogs in the per-worktree gitdir and the common
dir, never `objects/`) are watched with fsnotify, and a push/commit/checkout
sends the session ID to the poll loop's `kick` channel, which polls that session
immediately (bypassing the tick/batch-cap/negative-cache, bounded by a
per-session `prKickCooldown`). The timer poll stays the always-on fallback; the
watch is a pure accelerator, fail-open, gated by the same `[pr_watch] enabled`.
On first discovery of a PR the loop backfills currently-broken **mechanical**
state (failing CI, merge conflict) so a rediscovered PR doesn't strand a stopped
agent, but deliberately does NOT replay pre-discovery comments/reviews (priming
baselines them to avoid dumping a backlog). See
`docs/design/2026-07-14-pr-ref-watch-design.md`. Because comment bodies are free text from
arbitrary GitHub users, comment notifications are gated by an **author-trust
check**: a comment notifies only if its author's login is in an explicit
allowlist (`comment_author_allowlist` — the way to trust bots/Apps, whose
`author_association` is unreliable) **or** its `author_association` is in a
trusted set (`trusted_author_associations`, default
`OWNER`/`MEMBER`/`COLLABORATOR`). Untrusted comments are **jailed** — the
content is quarantined in the msgstore's `jailed_comments` table
(`daemon/jail.go`, `daemon/jailstore.go`) instead of discarded — the comment
cursor still advances, and the author is surfaced once, as metadata only, to
the orchestrator (`notify_untrusted_authors`, default on). The human or
orchestrator inspects and releases via `gr msg jail list/show/release`
(release gated to `roleHuman`/`roleOrchestrator` by `checkJailRelease`; a
plain agent is denied). The raw comment **body** is likewise only served to
that release-authorized set (`mayReadJailBody`): `list` is metadata-only and
`show` withholds the body from agents/guests, so the quarantined content can't
be fed to an agent through a graith channel. Adding an author to
`comment_author_allowlist` (or
widening `trusted_author_associations`) and reloading auto-releases their
jailed comments; jailed rows respect `[messages] max_age` retention. See
`docs/design/2026-07-11-pr-comment-author-trust-design.md` and
`docs/design/2026-07-13-pr-comment-jail-design.md` (issue #1082).

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
cannot impersonate other sessions when this token is present. It is **rotated on
every resume/restart**, so a leaked token is only valid for one agent lifetime.

**Local auth is fail-closed** (`daemon/auth.go`, design doc
`docs/design/2026-07-11-auth-identity-hardening.md`). On startup the daemon
writes a **human token** to `human.token` in the data dir (mode 0600, alongside
`state.json`, excluded from every agent sandbox) and reuses it across restarts.
A local connection is treated as the human **only** if it presents a valid
session token or that human token — a caller with no valid credential is
rejected, not granted human access. The CLI handles this transparently: a
session's `GRAITH_TOKEN` takes precedence, and outside a session `gr` reads
`human.token` automatically. This means a sandboxed agent that strips
`GRAITH_TOKEN` can no longer masquerade as the human (it cannot read
`human.token`). The boundary is the sandbox: an *unsandboxed* agent can read
either credential, so `gr doctor` warns when the sandbox is off.

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

`gr mcp list/restart/logs` inspect and control these daemon-managed processes
(`internal/daemon/mcpmanager.go` — `List`/`Restart`/`LogFiles`; handler cases
`mcp_list`/`mcp_restart`/`mcp_logs`; CLI in `internal/cli/mcp.go`). `list` and
`logs` are read-only; `restart` (stops a server's processes so proxies reconnect
with fresh ones) is gated by `authorizeTriggerOp`. Every new handler case needs
a matching `remoteMessagePolicy` row in `daemon/authmatrix.go` or
`TestRemoteMatrixCompleteness` fails.

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

## Triggers

Daemon-fired automation: a **trigger** is `(source) → (action)`. The daemon fires
triggers itself (no attached orchestrator needed), so they survive terminal
close. Two source kinds share one action vocabulary, one executor, one run-state
machine, and one `gr trigger` CLI. See
`docs/design/2026-07-11-triggers-design.md`.

**Sources** (exactly one per `[[trigger]]`):

- `[trigger.schedule]` — time-driven (#592): `cron = "0 9 * * *"` (5-field +
  `@hourly`/`@daily`/`@weekly`/`@monthly`, `timezone` optional) **or**
  `every = "15m"` (Go duration, supports `7d`). Uses `robfig/cron/v3` (parser +
  `Next()` only; the firing loop is ours). Runs in `RunTriggerLoop`
  (`internal/daemon/trigger.go`).
- `[trigger.watch]` — file-event-driven (#593): a **policy selector** by `repo`
  or `role` (never a live session name) that binds to matching running sessions
  and watches their worktrees via `fsnotify`. Honors `.gitignore` (always;
  ignored subtrees are pruned from the watch set), plus optional `paths`/`ignore`
  globs, coalesced by a `debounce` quiet-window (default **30s**). Runs in
  `RunFileWatchLoop` (`internal/daemon/filewatch.go`).

**Action vocabulary** (`[trigger.action] type = ...`):

| Type | What it does |
|------|--------------|
| `command` | Run a command (schedule: in `repo`; watch: in the bound worktree), capture output, deliver it. Sandboxed by default; `sandbox = false` runs unconfined, `[trigger.action.sandbox_config]` grants extra access (mirrors MCP-server `sandbox`/`sandbox_config`). Watch commands are read-only in v1 (`mutating` rejected). |
| `session` | Spawn a session parented to the orchestrator. Watch + `ensure = true` is the idempotent "ensure-reviewer": message the owned reactor if it exists (running/stopped — messaging auto-resumes), else spawn one sharing the bound worktree read-only. `auto_cleanup` (`true`/`"always"`, `"on_success"`, or absent/`false`) soft-deletes the spawned session when it stops — respecting the `[delete]` retention window — so finished briefing sessions don't pile up; incompatible with `ensure = true`. The exit watcher (`autoCleanupStopped`) marks the delete atomically under the lock, and only for a still-stopped, non-shutdown stop with retention > 0 (never turns cleanup into a hard purge; preserves shutdown-interrupted sessions across restart). Cleanup only fires once a session stops, so `auto_cleanup = "always"` also gives the spawned session a **1m `idle_timeout`** default (interactive agents don't self-exit — the daemon must idle-stop them first); `idle_timeout` (a Go duration) is a per-session override stored on `SessionState.IdleTimeoutSecs` and honored by `checkIdleSession` over the agent default. `"on_success"` is not auto-idled (an idle-stop is a non-zero exit it wouldn't clean up). |
| `scenario` | Start a named scenario from `~/.config/graith/scenarios/` (orchestrator-owned). |
| `message` | Route a fixed `body` to an inbox/topic (via `[trigger.action.deliver]`). |

**Delivery** (`[trigger.action.deliver]`): `inbox` (a session name,
`"orchestrator"`, or a template like `{session_name}`; auto-resumes the
orchestrator or any target with `wake = true`, never a soft-deleted session),
`topic` (pub/sub), `store` (a doc key; `shared:` prefix for the shared store).
Templated with a trigger-specific expander (`{name}`, `{date}`, `{datetime}`,
`{fire_time}`, and for watch `{session_name}`, `{worktree_path}`,
`{changed_files}`, `{change_count}`).

**Policy** (`[trigger.policy]`): `catch_up` (default `false` — never backfill a
burst of missed fires), `overlap` (default `skip` — skip if the previous run is
in flight; `allow` permits concurrent; `queue` is v2), `rate_limit` (default
`5/30m`). A daemon-wide `[triggers] max_concurrent` (default 4) bounds aggregate
fan-out.

**Run-state** is persisted per definition in `state.json`
(`TriggerRuntimeState`): the at-most-once `LastScheduledFireAt` (committed
durably *before* dispatch), a restart-stable interval anchor
(`ActivatedAt`/`NextScheduledFireAt`), a `Fingerprint` that resets the cursor
when a same-named definition changes, and a bounded run history. Per-binding
watch state (reactor, in-flight, debounce, degraded) is in-memory, rebuilt from
live sessions. Spawned reactors are tagged `TriggerID`/`TriggerReactor` on
`SessionState` for idempotent reuse.

**CLI** — definitions live in `config.toml` (v1; runtime authoring is v2). `gr
trigger` observes and controls:

```bash
gr trigger list                 # all triggers: source, action, next fire / watch scope, state
gr trigger status <name>        # detail: next fire, last run/result/error, bindings
gr trigger run <name>           # fire a schedule trigger once now (respects overlap)
gr trigger pause <name>         # pause (persists across restart)
gr trigger resume <name>
```

**Authorization**: `list`/`status` are read-only; `run`/`pause`/`resume` require
the caller to be the orchestrator or a descendant (`authorizeTriggerOp`).
Fired `session`/`scenario` actions are parented to the orchestrator; `message`
uses the `graith:system` sender; `command` runs under a dedicated sandbox
profile, fail-closed unless `sandbox = false`.

**Config example:**

```toml
# Daily PR report at 09:00, delivered to the orchestrator inbox and the store.
[[trigger]]
name = "daily-pr-report"
[trigger.schedule]
cron     = "0 9 * * *"
timezone = "Europe/London"
[trigger.action]
type   = "session"
prompt = "Summarise open PRs and post to the orchestrator inbox."
repo   = "~/Code/graith"
agent  = "claude"
[trigger.action.deliver]
inbox = "orchestrator"
store = "reports/pr/{date}.md"

# Run tests when Go source changes in any session on this repo.
[[trigger]]
name = "test-on-change"
[trigger.watch]
repo  = "~/Code/graith"
paths = ["**/*.go"]
[trigger.action]
type    = "command"
command = "go test ./..."
[trigger.action.deliver]
inbox = "{session_name}"
```

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

### Leave files better than you found them

When you touch a file, look for opportunities to improve it — don't just add to
the pile. Prefer **small, single-purpose functions**: if you're extending a long
function, consider splitting it; if you spot duplicated logic (here or in a
sibling file), extract a shared helper rather than copy-pasting. Rename an
unclear variable, delete dead code, tighten a comment that's now wrong. Keep
these cleanups small and in scope — a focused refactor alongside the change is
welcome, but don't let it balloon into an unrelated rewrite that muddies the
diff. The goal is that every file is a little cleaner each time it's edited, not
that any single PR fixes everything.

## Design docs

Non-trivial features get a design doc in `docs/design/` **before** they're
built — it's the argument for a decision, kept afterwards as the record of why.

- **Start from the template.** Copy [`docs/design/TEMPLATE.md`](docs/design/TEMPLATE.md)
  to `docs/design/YYYY-MM-DD-<slug>.md` (date = the day you start it) and fill
  it in. The template encodes the house style: the frontmatter block
  (`title`/`authors`/`created`/`status`/`reviewers`/`informed`, optional
  `issue`) and the section order — `Background → Problem → Goals` (with
  `Non-Goals`) `→ Proposals → Consensus → Other Notes`.
- **Lay out the options, not just the winner.** `## Proposals` is numbered,
  starts with `Proposal 0: Do Nothing`, and marks the advocated one
  `(Recommended)`. The rejected paths are the most valuable part later.
- **Advance `status`** as the doc moves: `Draft → Accepted → Implemented`
  (or `Implemented (v1)` when later phases are deferred). Add a `## Consensus`
  section only after a review has actually happened.

## Documentation

The published docs site lives in `website/` (Hugo) and is served at
<https://d0ugal.github.io/graith/> via `.github/workflows/docs.yml`. The pages
are Markdown under `website/content/docs/`. Larger topics are Hugo section
directories with an `_index.md` landing page plus focused sub-pages ordered by
`weight` (e.g. `configuration/`, `commands/`, `sandbox/`, `patterns/`); smaller
topics stay single files (e.g. `auth.md`, `triggers.md`). Cross-page links use
the `{{< relref >}}` shortcode so a broken reference fails the Hugo build.

**When a change alters user-facing behaviour — a command, flag, config key,
env var, auth/security model, or lifecycle — update the matching page under
`website/content/docs/` in the same PR.** A design doc in `docs/design/` records
*why* a thing was built; it does not replace the user-facing site docs, and the
two drift apart if you only update one. Updating `AGENTS.md` (agent-facing
reference) is likewise not a substitute for the site docs. If you're unsure a
change is user-visible, check whether an existing page would now be wrong — if
so, fix it.

### Capability matrix

`internal/capabilities/capabilities.json` is a **hand-maintained** manifest —
the source of truth for which capabilities each frontend (CLI, iOS, macOS)
supports. **When a frontend gains or loses a capability, update its state in the
JSON**, then regenerate the docs page and commit both together:

```bash
go test ./internal/capabilities -run TestDocMatchesManifest -update
```

`-update` rewrites only the marker-delimited region of
`website/content/docs/capabilities.md` from the manifest (via the same
`ReplaceRegion` code the check uses, so regen and check can't diverge).
`TestDocMatchesManifest` runs under `go test ./...` in CI and fails if the doc
and manifest disagree — so a JSON edit without a regen is caught.

**Manifest ↔ code (GUIs, #1149).** Beyond doc ↔ manifest, the two GUI columns
(iOS + macOS) are guarded against code drift the way the protocol fixture is
(#1144). The Go side projects the manifest to a language-neutral fixture,
`gui/shared/Tests/GraithSessionKitTests/Fixtures/capability_manifest.json`
(id + iOS/macOS state per capability), regenerated by:

```bash
go test ./internal/capabilities -run TestGUIFixtureUpToDate -update
```

`TestGUIFixtureUpToDate` runs on every Go PR (no paths filter), so a manifest
edit without a regen goes red; and because the fixture lives under `gui/`, the
regenerated file trips the paths-filtered gui/ Swift CI. On the Swift side,
`CapabilityConformanceTests` (in `GraithSessionKitTests`) decodes that fixture
and cross-checks it against **`sharedAffordances()`** — a *compile-anchored*
registry of the capabilities the shared `GraithSessionKit` layer actually wires
(`FleetModel` / `HostConnection` / `TerminalAttachViewModel` / the
`GraithHostClient` boundary). Each entry references the real symbol behind it,
so renaming or deleting the wiring stops the test file compiling. The check
fails when:

- a capability is wired in `sharedAffordances()` but the manifest marks it
  anything but `supported` on a GUI (the #1143 incident — code shipped, manifest
  still said `planned` — as a red test);
- a capability is `supported` on a GUI with no backing affordance (and not in
  the explicit `viewOnlyCapabilities` allowlist);
- iOS and macOS states diverge without a declared `knownDivergences` exception
  (view-level drift — the shared layer is parity-by-construction, #1147).

So when a GUI gains or loses a capability: wire it in `sharedAffordances()` (or
declare a reviewed `viewOnlyCapabilities` / `knownDivergences` exception),
update `capabilities.json`, and regenerate **both** the docs page and the GUI
fixture. What the checks still *can't* derive is the `supported`/`planned`/`n/a`
judgment for the CLI column and for un-wired GUI gaps — and especially the
`planned` (intended gap) vs `n/a` (deliberately not applicable) distinction.
Those stay human judgments you keep current by hand.

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

### Proactive notifications

`gr notify` sends a desktop/push notification to the **human** (via the
`[notifications]` backend), for things worth interrupting them over — a finished
briefing, a CI failure, a review needed. Unlike an inbox message it proactively
grabs attention rather than waiting to be read.

```bash
gr notify "Morning briefing ready" --priority low
gr notify "CI failing on main after 3 retries" --priority high
```

Only the **orchestrator** session and the human may notify (plain agent sessions
are rejected — don't try to route around it). Priority is `low`/`normal`/`high`;
`high` plays a sound and bypasses quiet hours + the rate limit. Low/normal are
rate-limited (default 12/hour) and suppressed during configured quiet hours, and
identical notifications within 30s are coalesced — so use `notify` sparingly for
genuinely attention-worthy events, not routine progress (that's what `gr status`
is for). Triggers can fire one on completion via `notify_on_complete` /
`notify_message` / `notify_priority` in `[trigger.action]`.

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

# Check health (and, with --autofix, clean up orphans)
gr doctor
gr doctor --autofix
```

## Crash-safety (issue #614)

- **Atomic state writes** — `state.json` and the document store (`gr store
  put/append`) are written via `internal/atomicfile` (temp → fsync → rename →
  dir fsync), so a crash mid-write can't corrupt them.
- **Pre-migration state backup** (issue #1065) — before a newer daemon migrates
  an older `state.json` forward in place, `LoadState` copies the pre-migration
  file to `state.json.v<oldVersion>.bak` (via `atomicfile`, alongside
  `state.json`). Only the most recent pre-migration backup is kept — an earlier
  one is pruned once the new one is durable. This makes a binary downgrade
  recoverable (restore the backup, start the older binary) and gives a rescue
  point if a forward migration corrupts state. The backup is best-effort: a
  write failure is logged, not fatal. `gr doctor` lists available backups;
  `daemon.ListStateBackups` / `daemon.StateBackupPath` are the helpers.
- **Delete tombstones** — before a session teardown starts, the daemon writes a
  durable tombstone; on startup any leftover tombstone means a delete was
  interrupted, and the daemon finishes it (reaps the orphan process, removes the
  worktree, drops the session). If the tombstone can't be written the delete
  fails closed rather than tearing down with no recovery marker.
- **Orphan GC** — worktree/scratch directories with no matching session are
  detected and removed by the daemon (`internal/daemon/gc.go`), surfaced through
  `gr doctor` (listed) and `gr doctor --autofix` (removed). Worktrees with
  uncommitted — or undeterminable — git state are never removed. The GC logic
  lives on the daemon (authoritative live-session set under lock); `gr doctor`
  calls it over the control protocol rather than scanning client-side.

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
