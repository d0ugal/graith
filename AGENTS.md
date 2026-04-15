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

There is no `Makefile` on main — the CI workflow on the `setup-ci` branch uses
`make lint-only` with a Docker-based golangci-lint. Locally, just use `go build`
and `go test`.

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
  cli/                   Cobra command definitions (one file per command)
  client/                Client-side: connection, passthrough, overlay, shell
  config/                TOML config loading, defaults, XDG paths
  daemon/                Daemon: session manager, handler, state, server, messaging
  detector/              Agent type detection from running processes
  integration/           Integration tests (spawn real daemon)
  output/                Structured output helpers
  protocol/              Wire protocol: framing, control messages, encoding
  pty/                   PTY session management, scrollback buffer
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
| Client | `client/overlay.go` | Session picker UI (bubbletea), preview rendering |
| Client | `client/client.go` | Connection, handshake, scrollback preview (vt10x) |
| CLI | `cli/attach.go` | Attach loop: passthrough ↔ overlay ↔ reconnect |
| CLI | `cli/new.go` | Session creation with worktree setup |
| CLI | `cli/msg.go` | `gr msg pub/sub/ack/topics` — inter-agent messaging |
| PTY | `pty/session.go` | PTY lifecycle, resize, I/O multiplexing |
| PTY | `pty/scrollback.go` | Append-only scrollback file with tail reads |

## Architecture patterns

**Protocol**: Channel 0x00 = JSON control messages, Channel 0x01 = raw PTY data.
Control messages use an envelope `{"type":"...", "payload":{...}}`. Add new
message types by adding a struct in `protocol/messages.go` and handling the
type string in `daemon/handler.go`.

**Session lifecycle**: Create → worktree + branch → start agent process → attach.
Delete → kill process → remove worktree → delete branch. Resume → restart
process in existing worktree.

**Passthrough**: When attached, the client enters raw terminal mode and forwards
stdin/stdout bytes directly to/from the daemon. A prefix key (default ctrl+b)
intercepts the next keystroke for commands (d=detach, w=overlay, s=shell, etc).

**State persistence**: `state.json` in the data dir. Loaded on daemon start,
saved on mutations. Sessions survive daemon restarts.

## Environment variables

The daemon sets these in every agent process:

- `GRAITH_SESSION_ID` — unique session ID
- `GRAITH_SESSION_NAME` — human-readable session name

These are used by `gr msg pub/sub` to identify the sender automatically.

## Configuration

TOML at `~/.config/graith/config.toml` (or `$XDG_CONFIG_HOME/graith/config.toml`).
All fields optional — `config.Default()` provides sensible defaults. See
`internal/config/config.go` for the full struct.

Template variables in agent args: `{agent_session_id}`, `{username}`, `{name}`, `{id}`.

## Testing

- Unit tests live next to the code (`*_test.go`)
- Integration tests are in `internal/integration/` — they spawn a real daemon
- Tests must pass with `-race` flag
- Use `t.TempDir()` for test fixtures, not hardcoded paths

## Conventions

- Commit messages: conventional commits (`feat:`, `fix:`, `chore:`, etc.)
- No `Makefile` needed for basic development — `go build` and `go test` suffice
- All packages are under `internal/` — no public API
- Errors: return `fmt.Errorf(...)`, don't use `log.Fatal` in library code
- The daemon logs to `~/.local/state/graith/daemon.log` (slog, JSON format)

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

### Inter-agent messaging

Sessions can communicate via the pub/sub messaging system:

```bash
# From one session, publish findings
gr msg pub --topic code-review "Found a race condition in handler.go:245"

# From another session, read messages
gr msg sub --topic code-review --all

# Wait for the next message (blocks until one arrives)
gr msg sub --topic code-review --wait

# Follow a stream continuously
gr msg sub --topic code-review --follow
```

The `--subscriber` flag tracks read position per consumer. Use `--ack` to
mark messages as read.

### Typing into sessions remotely

```bash
# Send input to a running session (appends newline by default)
gr type fix-overlay "/help"
gr type fix-overlay "please review the changes" 

# Send without trailing newline
gr type fix-overlay --no-newline "y"
```

### Monitoring sessions

```bash
# List all sessions with status
gr list

# Stream logs from a session
gr logs fix-overlay --follow

# Check health
gr doctor
```

### Daemon management

The daemon auto-starts on first command. To manage it explicitly:

```bash
gr daemon start
gr daemon stop
gr daemon restart         # preserves sessions
gr daemon upgrade         # hot-upgrade: rebuild binary, then restart
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
