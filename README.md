# graith

A terminal multiplexer for AI coding agent sessions. Manage multiple agents (Claude, Codex, OpenCode, Agy) running in isolated git worktrees, each in its own session that survives terminal closures.

**graith** (Scots) — *noun:* equipment, tools, gear for a specific trade. *verb:* to make ready, prepare, equip. Your agents, graithed and ready to work.

## Why

When you're running multiple AI coding agents on different tasks, you need:

- **Isolation** — each agent works in its own git worktree, on its own branch
- **Persistence** — sessions survive terminal closures; the daemon keeps everything alive
- **Switching** — jump between agents with a tmux-style prefix key
- **Visibility** — see all sessions at a glance, with views for what needs attention
- **Coordination** — agents can message each other and you can drive them remotely

graith is purpose-built for this. It owns the PTY, manages worktrees, and gets out of your way.

## Install

The binary is called `gr`.

### Homebrew

```bash
brew install d0ugal/tap/graith
```

### From a release

Download a prebuilt binary for your platform from the [releases page](https://github.com/d0ugal/graith/releases), extract it, and put `gr` on your `$PATH`.

### go install

```bash
go install github.com/d0ugal/graith/cmd/graith@latest
```

> `go install` names the binary after the package directory, so this produces a binary called `graith`. Rename it to `gr` (or symlink it) to match the rest of these docs:
> ```bash
> mv "$(go env GOPATH)/bin/graith" "$(go env GOPATH)/bin/gr"
> ```

### From source

```bash
git clone https://github.com/d0ugal/graith
cd graith
make build      # produces ./gr
```

## Quick Start

```bash
# Create a new session (auto-starts daemon, creates worktree)
gr new fix-auth-bug

# Create with a specific agent
gr new refactor-api --agent codex

# Create with an initial prompt
gr new fix-tests --prompt "the auth tests are flaky, find out why"

# Create in the background without attaching
gr new long-task --background

# List all sessions
gr list

# Attach to a session (or show picker if no name given)
gr attach fix-auth-bug
gr    # bare gr opens the session picker

# Inside a session (prefix is ctrl+b):
#   ctrl+b w    → session picker overlay
#   ctrl+b d    → detach
#   ctrl+b s    → open shell in the worktree
#   ctrl+b n/p  → next / previous session
#   ctrl+b l    → last (most recently attached) session
#   ctrl+b c    → create a new session
#   ctrl+b f    → fork the current session
#   ctrl+b r    → restart a stopped session
#   ctrl+b ctrl+b → send a literal ctrl+b

# Rename / delete
gr rename fix-auth-bug auth-rewrite
gr delete auth-rewrite
```

## Commands

| Command | Description |
|---------|-------------|
| `gr` | Attach (shows session picker if multiple) |
| `gr new <name>` | Create a new agent session |
| `gr list` (`ls`) | List all sessions |
| `gr attach [name]` (`a`) | Attach to a session |
| `gr stop <name>` | Stop a running session (keeps the worktree); `--children` stops descendants |
| `gr restart <name>` | Restart a stopped session |
| `gr delete <name>` (`rm`) | Delete a session and its worktree; `--children` deletes descendants |
| `gr rename <old> <new>` | Rename a session |
| `gr fork <source> <name>` | Fork a session (new worktree + agent conversation history) |
| `gr info` | Show info for the current session (when inside a worktree) |
| `gr logs <name>` (`l`) | Show a session's output without attaching |
| `gr type <name> <text>` (`t`) | Type text into a session's stdin |
| `gr status [session] <text>` | Set a status summary visible in the session picker |
| `gr msg ...` (`m`) | Inter-agent messaging — see below |
| `gr dashboard` | Live-updating dashboard of all sessions |
| `gr approvals` | List sessions waiting for approval |
| `gr doctor` (`doc`) | Health checks and diagnostics |
| `gr daemon ...` (`d`) | Manage the daemon — see below |
| `gr config ...` | Manage configuration (`show`, `diff`, `reset`) |
| `gr mcp` | Run graith as an MCP tool server (stdio) |
| `gr completion <shell>` | Generate a shell completion script |
| `gr version` | Print version information |

Global flags: `--config <path>` to point at a non-default config file, `--json` for machine-readable output, and `--agent-mode` to force agent-friendly behavior (auto-enables `--json`). Agent mode is also auto-detected when running inside a graith session or other AI agent environment.

### `gr new`

```bash
gr new <name> [flags]
```

| Flag | Description |
|------|-------------|
| `--agent <name>` | Agent to run (defaults to `default_agent` from config) |
| `--base <branch>` | Base branch to fork the worktree from (defaults to the repo default branch) |
| `-C, --repo <path>` | Path to the git repo (defaults to the current directory) |
| `--no-repo` | Create a session with no git repo or worktree |
| `--in-place` | Run agent directly in the repo without creating a worktree |
| `--allow-concurrent` | Allow multiple in-place sessions on the same repo |
| `--share-worktree <session>` | Share another session's worktree (read-only) |
| `--background` | Create the session without attaching to it |
| `-p, --prompt <text>` | Send an initial prompt to the agent on startup |
| `--prompt-file <path>` | Read the initial prompt from a file |
| `-m, --model <name>` | Model for the agent to use (expands `{model}` in agent args) |

### `gr daemon`

The daemon auto-starts on the first command. Manage it explicitly with:

| Command | Description |
|---------|-------------|
| `gr daemon start` | Start the daemon |
| `gr daemon stop` | Stop the daemon |
| `gr daemon restart` | Restart, preserving live sessions via exec (`--force` for a clean stop/start that kills sessions) |
| `gr daemon reload` | Reload config without restarting |

After rebuilding `gr`, run `gr daemon restart` to pick up the new daemon binary.

## Inter-agent messaging

Sessions — and you — can communicate over a SQLite-backed pub/sub system. Each agent process gets `GRAITH_SESSION_ID`/`GRAITH_SESSION_NAME` set, so `gr msg` automatically knows who is sending.

| Command | Description |
|---------|-------------|
| `gr msg pub -t <topic> <body>` | Publish a message to a stream |
| `gr msg send <session> <body>` | Send to a session's inbox; `--children`/`--parent` for tree comms |
| `gr msg sub -t <topic>` | Read messages from a stream |
| `gr msg ack -t <topic>` | Acknowledge all messages in a stream |
| `gr msg topics` | List streams with total/unread counts |

```bash
# Publish findings to a topic
gr msg pub --topic code-review "Found a race condition in handler.go:245"

# Read unread messages from a topic
gr msg sub --topic code-review

# Show all messages (not just unread)
gr msg sub --topic code-review --all

# Block until the next message arrives
gr msg sub --topic code-review --wait

# Follow a stream continuously, acking as you go
gr msg sub --topic code-review --follow --ack

# Message another session directly (types a notification into it unless --quiet)
gr msg send fix-auth-bug "the tests are green now, rebase on main"
```

`pub`/`send` accept `--file` to read the body from a file, and `--thread`/`--reply-to` for threaded conversations. `sub` accepts `--thread` to filter to one thread.

```bash
# From inside a session, message all direct child sessions
gr msg send --children "rebase on main and re-run tests"

# From a child session, message the parent
gr msg send --parent "tests are green, ready for review"
```

## Status summaries

Agents can report what they're doing with `gr status`. The summary appears in the session picker overlay (ctrl+b w) and in `gr list`.

```bash
# Set status (auto-detects session when inside one)
gr status "Exploring code"
gr status "Waiting for CI"
gr status "Done"

# Set with a custom TTL for long-running waits
gr status --ttl 30m "Waiting for CI"

# Clear explicitly
gr status --clear

# Set from outside the session
gr status my-session "Reviewing PR"
```

Statuses auto-expire when the agent is actively producing output but hasn't updated the status (default 5 minutes). When idle, the status fades but remains visible — so "Done" on a stopped session stays put.

The session picker also auto-derives a summary from hook reports (e.g. "Using Bash", "Using Edit") when no explicit status is set.

Configure the default TTL in `config.toml`:

```toml
[status]
ttl = "5m"    # default
```

## Driving sessions remotely

```bash
# Type text into a running session (appends a newline by default)
gr type fix-auth-bug "/help"
gr type fix-auth-bug --no-newline "y"

# Watch a session's output without attaching
gr logs fix-auth-bug --follow
gr logs fix-auth-bug --lines 500

# See which sessions are blocked waiting for you to approve something
gr approvals

# A live TUI dashboard of every session (attach/stop/delete/resume inline)
gr dashboard
```

## MCP server

`gr mcp` runs graith as a [Model Context Protocol](https://modelcontextprotocol.io) server over stdio, exposing session management as tools: `list_sessions`, `session_status`, `create_session`, `publish_message`, `read_messages`, and `subscribe`. This lets an agent manage other graith sessions as part of its own tool set.

## Shell completion

```bash
# bash
source <(gr completion bash)

# zsh
gr completion zsh > "${fpath[1]}/_gr"

# fish
gr completion fish | source
```

`powershell` is also supported.

## Architecture

```
┌──────────┐     Unix Socket      ┌──────────┐     PTY      ┌─────────┐
│ gr (CLI) │ ◄──── frames ──────► │ graithd  │ ◄──────────► │ claude  │
│  client  │   control + data     │  daemon  │              │ codex   │
└──────────┘                      └──────────┘              │ opencode│
                                       │                    └─────────┘
                                  state.json
                                  (persisted)
```

- **Daemon** (`graithd`) — owns PTYs, manages state, multiplexes connections
- **Client** (`gr`) — stateless, connects over a Unix socket, auto-starts the daemon
- **Protocol** — 5-byte framed multiplexing: `[channel:1][length:4][payload:N]`
  - Channel `0x00`: JSON control messages, envelope `{"type":"...","payload":{...}}`
  - Channel `0x01`: raw PTY data

## Sandbox (macOS)

graith can wrap agent processes with [safehouse](https://github.com/nicholasgasior/safehouse), a macOS kernel-level sandbox. This lets you run agents with their "skip permissions" flags (e.g. `--dangerously-skip-permissions` for Claude, `--dangerously-bypass-approvals-and-sandbox` for Codex) while confining them to a deny-by-default sandbox that restricts file access, network, and system calls.

Sandboxing is **config-only** — there are no CLI flags to enable or disable it. This prevents a sandboxed agent from spawning a child agent that escapes the sandbox.

### Setup

1. Install safehouse: `brew install nicholasgasior/tools/safehouse`
2. Verify: `gr doctor` (checks for safehouse on `$PATH`)
3. Add to your config:

```toml
allowed_repo_paths = ["~/Code"]         # restrict which repos the daemon will create sessions in

[sandbox]
enabled  = true                         # wrap all agents with safehouse
features = ["ssh", "process-control"]   # safehouse feature gates to enable
read_dirs  = ["~/Code"]                 # additional read-only paths
write_dirs = []                         # additional read-write paths

[agents.claude]
command     = "claude"
args        = ["--dangerously-skip-permissions", "--session-id", "{agent_session_id}"]
resume_args = ["--dangerously-skip-permissions", "--resume", "{agent_session_id}"]

[agents.codex]
command     = "codex"
args        = ["--dangerously-bypass-approvals-and-sandbox"]
resume_args = ["resume", "--last", "--dangerously-bypass-approvals-and-sandbox"]
```

### How it works

When `sandbox.enabled = true`, the daemon wraps the agent command with `safehouse wrap`. The agent process runs inside a macOS `sandbox-exec` policy that:

- **Denies all file access by default**, then allows the session worktree (read-write), plus any paths in `read_dirs`/`write_dirs`
- **Strips the environment** (`/usr/bin/env -i`) and re-adds only what the agent needs
- **Gates capabilities** behind `features` — e.g. `ssh` grants `SSH_AUTH_SOCK` access, `process-control` allows signal sending

The sandbox **fails closed**: if `sandbox.enabled = true` but safehouse isn't installed, session creation is refused with an error. `gr doctor` checks this proactively.

### Per-agent overrides

Each agent can extend or disable the global sandbox config:

```toml
[sandbox]
enabled  = true
features = ["ssh"]

[agents.claude.sandbox]
features = ["clipboard"]                # merged with global: ["ssh", "clipboard"]
write_dirs = ["~/.claude"]             # agent-specific write access

[agents.codex.sandbox]
disabled = true                         # opt this agent out of sandboxing
```

Features and directories are merged (global + agent). Setting `disabled = true` on an agent overrides `enabled = true` on the global config.

### Path restrictions

`allowed_repo_paths` limits which directories the daemon will accept for `--repo` / `-C`. If set, any repo path outside these prefixes is rejected. Paths support `~` expansion and are resolved to absolute paths before comparison.

```toml
allowed_repo_paths = ["~/Code", "~/Work"]
```

When empty (the default), any repo path is accepted.

## Configuration

Config lives at `~/.config/graith/config.toml` (or `$XDG_CONFIG_HOME/graith/config.toml`). All fields are optional — sensible defaults are provided. The block below shows common options at their default values. Run `gr config show` for the full effective config.

```toml
default_agent   = "claude"              # agent used when --agent isn't given
github_username = ""                    # used by {username} in branch_prefix
branch_prefix   = "{username}/graith"   # template for new branch names
fetch_on_create = true                  # fetch origin before creating a worktree
# allowed_repo_paths = ["~/Code"]       # restrict which repos the daemon allows (empty = any)

[sandbox]
enabled    = false                      # wrap agents with safehouse (macOS only)
# command  = "safehouse"               # path to safehouse binary (default: "safehouse")
# features = ["ssh", "process-control"] # safehouse feature gates
# read_dirs  = []                       # additional read-only paths for sandboxed agents
# write_dirs = []                       # additional read-write paths for sandboxed agents

[status_bar]
enabled  = true                         # show a status bar while attached
position = "bottom"

[notifications]
enabled     = true                      # desktop notifications
on_approval = true                      # notify when a session needs approval
on_stopped  = false                     # notify when a session stops
command     = ""                        # custom notification command (optional)

[approvals]
mode    = "prompt"                      # "prompt" (ask the agent) or "notify" (just notify)
timeout = "10m"                         # how long to wait for an approval decision

[messages]
max_age        = ""                     # prune messages older than e.g. "7d" / "168h" (empty = keep)
max_per_stream = 0                      # cap messages per stream (0 = unlimited)

[keybindings]
prefix         = "ctrl+b"               # prefix key
new_session    = "c"                    # create a session
fork_session   = "f"                    # fork the current session
delete_session = "x"                    # delete a session
detach         = "d"                    # detach
session_list   = "w"                    # open the session picker overlay
next_session   = "n"                    # next session
prev_session   = "p"                    # previous session
last_session   = "l"                    # last (most recently attached) session
resume_session = "R"                    # resume a stopped session (config; passthrough uses 'r')
rename_session = ","                    # rename
search         = "/"                    # filter sessions
scroll_mode    = "["                    # enter scroll mode
shell          = "s"                    # open a shell in the worktree

# Each agent is configured under [agents.<name>]. The five below ship by default.
[agents.claude]
command     = "claude"
args        = ["--session-id", "{agent_session_id}"]
resume_args = ["--resume", "{agent_session_id}"]
fork_args   = ["--resume", "{fork_source_agent_session_id}", "--fork-session", "--session-id", "{agent_session_id}"]
# env         = { KEY = "value" }       # extra env for the agent process (optional)
# idle_timeout = "1h"                   # stop after idle (defaults to 1h if resume_args set)
# [agents.claude.sandbox]              # per-agent sandbox overrides (merged with global)
# features  = ["clipboard"]
# write_dirs = ["~/.claude"]

[agents.codex]
command     = "codex"
args        = []
resume_args = ["resume", "--last"]
fork_args   = ["fork", "{fork_source_agent_session_id}"]

[agents.cursor]
command     = "agent"
args        = []
resume_args = ["resume"]

[agents.opencode]
command     = "opencode"
args        = []
resume_args = ["--session", "{agent_session_id}"]

[agents.agy]
command     = "agy"
args        = []
resume_args = ["--conversation", "{agent_session_id}"]
```

### Template variables

These are substituted in agent `args`, `resume_args`, and `fork_args`. Only `{username}` is available in `branch_prefix`.

| Variable | Expands to |
|----------|-----------|
| `{agent_session_id}` | the agent session ID (used for `--session-id` / `--resume`) |
| `{session_id}` | the unique session ID |
| `{session_name}` | the session name |
| `{username}` | `github_username` (or the system username) |
| `{worktree_path}` | absolute path to the session worktree |
| `{model}` | the model passed via `--model` (empty if not set) |
| `{fork_source_agent_session_id}` | agent session ID of the forked source (empty if not a fork) |

## Keybindings

### While attached (passthrough)

Press the prefix (`ctrl+b`), then:

| Key | Action |
|-----|--------|
| `w` | Open the session picker overlay |
| `d` | Detach (leave the agent running) |
| `s` | Open a shell in the worktree |
| `c` | Create a new session |
| `f` | Fork the current session |
| `n` / `p` | Next / previous session |
| `l` | Last (most recently attached) session |
| `r` | Restart a stopped session |
| `a` | Open the approvals overlay |
| `,` | Rename the session |
| `x` | Delete the session |
| `ctrl+b` | Send a literal prefix byte to the agent |

### Session picker overlay

| Key | Action |
|-----|--------|
| `enter` | Attach to the highlighted session |
| `j` / `k` (or arrows) | Move the cursor |
| `h` / `l` (or left/right) | Cycle view: All → Needs Attention → Active |
| `n` / `p` | Next / previous session |
| `/` | Filter by name |
| `x` then `y` | Delete (with confirmation) |
| `q` / `esc` | Close the overlay |

**Views:**
- **All** — every session, grouped by repo (default)
- **Needs Attention** — sessions waiting for approval, errored, idle, or stopped with uncommitted/unpushed changes, sorted by time in current state (oldest first)
- **Active** — running sessions only, sorted newest first

### Dashboard (`gr dashboard`)

| Key | Action |
|-----|--------|
| `enter` / `a` | Attach to the highlighted session |
| `j` / `k` (or arrows) | Move the cursor |
| `s` | Stop the session (with confirmation) |
| `x` / `d` | Delete the session (with confirmation) |
| `r` | Resume a stopped session |
| `q` / `ctrl+c` | Quit |

## Git worktree lifecycle

When you create a session:

1. Fetches latest from origin (when `fetch_on_create` is true)
2. Creates a branch `<branch_prefix>/<session-name>-<session-id>` from the base branch
3. Creates a worktree at `~/.local/share/graith/worktrees/<repo-name>/<repo-hash>/<session-id>/`
4. Starts the agent in that worktree

When you stop a session, the agent process is killed but the worktree and branch are kept (resume restarts the agent in place). When you delete a session, the process is killed, the worktree is removed, and the branch is deleted.

## Environment variables

The daemon sets these in every agent process:

| Variable | Value |
|----------|-------|
| `GRAITH_SESSION_ID` | unique session ID |
| `GRAITH_SESSION_NAME` | human-readable session name |
| `GRAITH_AGENT_TYPE` | agent type (e.g. `claude`, `codex`) |
| `GRAITH_WORKTREE_PATH` | absolute path to the worktree |

`gr shell` additionally exports `GRAITH_WORKTREE`. `gr msg` reads `GRAITH_SESSION_ID`/`GRAITH_SESSION_NAME` to identify the sender automatically.

Set `GR_AGENT_MODE=1` to force agent mode (auto-JSON) or `GR_AGENT_MODE=0` to disable auto-detection.

## File locations

graith follows the XDG base directory spec:

| Path | Contents |
|------|----------|
| `~/.config/graith/config.toml` | configuration |
| `~/.local/share/graith/state.json` | persisted session state |
| `~/.local/share/graith/messages.sqlite` | inter-agent message store |
| `~/.local/share/graith/daemon.log` | daemon log (slog, JSON) |
| `~/.local/share/graith/worktrees/<repo>/<hash>/<id>/` | session worktrees |
| `$XDG_RUNTIME_DIR/graith/graith.sock` | Unix control socket |
| `$XDG_RUNTIME_DIR/graith/graith.pid` | daemon PID file |

## Development

```bash
# Build (binary is ./gr)
make build            # or: go build -o gr ./cmd/graith

# Test
go test ./...
go test -race ./...   # CI runs the race detector

# Lint (Docker-based golangci-lint)
make lint             # run with --fix
make lint-only        # check only
make fmt              # format

# Run
./gr doctor
```

All packages live under `internal/` — there is no public API. See [`AGENTS.md`](AGENTS.md) for a package-by-package map and guidance on using graith to develop graith.

## License

MIT — see [`LICENSE`](LICENSE).
