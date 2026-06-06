# graith

A terminal multiplexer for AI coding agent sessions. Manage multiple agents (Claude, Codex, OpenCode, Agy) running in isolated git worktrees, each in its own session that survives terminal closures.

**gr** + **wraith** = **graith** — your invisible army of coding agents, haunting your codebase.

## Why

When you're running multiple AI coding agents on different tasks, you need:

- **Isolation** — each agent works in its own git worktree, on its own branch
- **Persistence** — sessions survive terminal closures; the daemon keeps everything alive
- **Switching** — jump between agents with a tmux-style prefix key
- **Visibility** — see all sessions at a glance, grouped by repo

graith is purpose-built for this. It owns the PTY, manages worktrees, and gets out of your way.

## Install

```bash
go install github.com/dougalmatthews/graith/cmd/graith@latest
```

The binary is called `gr`.

## Quick Start

```bash
# Create a new session (auto-starts daemon, creates worktree)
gr new fix-auth-bug

# Create with a specific agent
gr new refactor-api --agent codex

# List all sessions
gr list

# Attach to a session (or show picker if no name given)
gr attach fix-auth-bug
gr    # bare gr opens the session picker

# Inside a session:
#   ctrl+b w    → session picker overlay
#   ctrl+b d    → detach
#   ctrl+b s    → open shell in the worktree
#   ctrl+b ctrl+b → send literal ctrl+b

# Rename / delete
gr rename fix-auth-bug auth-rewrite
gr delete auth-rewrite
```

## Architecture

```
┌──────────┐     Unix Socket      ┌──────────┐     PTY      ┌─────────┐
│ gr (CLI) │ ◄──── frames ──────► │ graithd  │ ◄──────────► │ claude  │
│  client  │   control + data     │  daemon   │              │ codex   │
└──────────┘                      └──────────┘              │ opencode│
                                       │                     └─────────┘
                                  state.json
                                  (persisted)
```

- **Daemon** (`graithd`) — owns PTYs, manages state, multiplexes connections
- **Client** (`gr`) — stateless, connects over Unix socket, auto-starts daemon
- **Protocol** — 5-byte framed multiplexing: `[channel:1][length:4][payload:N]`
  - Channel 0x00: JSON control messages
  - Channel 0x01: raw PTY data

## Configuration

Config lives at `~/.config/graith/config.toml` (or `$XDG_CONFIG_HOME/graith/config.toml`).

```toml
default_agent = "claude"
github_username = "d0ugal"
branch_prefix = "{username}/graith"
fetch_on_create = true

[keybindings]
prefix = "ctrl+b"
detach = "d"
session_list = "w"
shell = "s"

[agents.claude]
command = "claude"
args = ["--session-id", "{agent_session_id}"]
resume_args = ["--resume", "{agent_session_id}"]

[agents.codex]
command = "codex"
args = []
resume_args = ["resume", "--last"]
```

All fields are optional — sensible defaults are provided.

## Commands

| Command | Description |
|---------|-------------|
| `gr` | Attach (shows session picker if multiple) |
| `gr new <name>` | Create a new agent session |
| `gr list` | List all sessions |
| `gr attach [name]` | Attach to a session |
| `gr delete <name>` | Delete a session and its worktree |
| `gr rename <old> <new>` | Rename a session |
| `gr info` | Show info for the current session (when inside a worktree) |
| `gr doctor` | Health checks and diagnostics |
| `gr daemon start/stop` | Manage the daemon directly |

## Git Worktree Lifecycle

When you create a session:

1. Fetches latest from origin (configurable)
2. Creates a branch: `{username}/graith/{name}-{id}` from the default branch
3. Creates a worktree at `~/.local/share/graith/worktrees/{repo-hash}/{id}/`
4. Starts the agent in that worktree

When you delete a session:

1. Kills the agent process
2. Removes the worktree
3. Deletes the branch

## Development

```bash
# Build
go build -o gr ./cmd/graith

# Test
go test ./...

# Run
./gr doctor
```
