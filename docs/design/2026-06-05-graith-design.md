# graith — Agent Session Manager

**Date**: 2026-06-05
**Status**: Draft

## Overview

graith is a terminal multiplexer purpose-built for managing multiple AI coding agent sessions. It provides a daemon/client architecture where each agent session runs in an isolated git worktree, with a tmux-inspired interface for switching between sessions.

Supported agents: Claude, Codex, OpenCode, Agy (and any arbitrary command).

## Architecture

### High-Level

```
┌─────────────────────────────────────────────────┐
│                   graithd (daemon)               │
│                                                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐      │
│  │ Session A │  │ Session B │  │ Session C │      │
│  │  PTY+proc │  │  PTY+proc │  │  PTY+proc │     │
│  │  scrollbuf│  │  scrollbuf│  │  scrollbuf│     │
│  └──────────┘  └──────────┘  └──────────┘      │
│                                                  │
│  ┌────────────────┐  ┌─────────────────┐        │
│  │  Session Store  │  │  State File     │        │
│  │  (in-memory)    │  │  (persisted)    │        │
│  └────────────────┘  └─────────────────┘        │
│                                                  │
│  ┌────────────────────────────────────┐         │
│  │  Socket Listener (Unix socket)     │         │
│  │  per-client: control ch + data ch  │         │
│  └────────────────────────────────────┘         │
└─────────────────────────────────────────────────┘
        ▲                          ▲
        │ control (framed JSON)    │ data (framed PTY bytes)
        │                          │
┌───────┴──────────────────────────┴──────────┐
│              graith client (TUI)             │
│                                              │
│  ┌──────────────┐  ┌─────────────────┐      │
│  │ Passthrough   │  │ Overlay (session │      │
│  │ mode          │  │ list, search)   │      │
│  └──────────────┘  └─────────────────┘      │
│                                              │
│  ┌──────────────────────────────────┐       │
│  │ Input handler (prefix key detect) │       │
│  └──────────────────────────────────┘       │
└──────────────────────────────────────────────┘
```

### Design Decisions

- **graith owns PTY management directly** — no tmux/zellij dependency. The scope is narrower than a general-purpose multiplexer (one process per session, no splits/panes), making this tractable.
- **Daemon/client split** — the daemon (`graithd`) is a long-lived process that owns PTYs and state. Clients are short-lived, stateless, and connect over a Unix socket.
- **Sessions survive terminal closure** — the daemon keeps agent processes alive. Clients attach and detach freely.
- **Dual-mode client rendering** — passthrough mode for agent interaction (zero overhead, raw byte forwarding), bubbletea for the overlay UI. Avoids building a terminal emulator widget.
- **Implementation: Go with bubbletea** — best TUI framework ecosystem, fast prototyping, single binary distribution.

## Data Model

### Session

```
Session {
    id:                string      // 8-character hex hash (e.g. "a3f2b1c9"), immutable
    name:              string      // user-facing display name, mutable via rename
    repo_path:         string      // absolute path to the original git repo
    repo_name:         string      // derived from repo_path for display grouping
    worktree_path:     string      // ~/.local/share/graith/worktrees/<repo-name>/<repo-hash>/<id>/
    branch:            string      // e.g. "d0ugal/graith/fix-auth-bug-a3f2b1c9"
    agent:             string      // agent key from config (e.g. "claude")
    agent_session_id:  string?     // the agent's own session/conversation ID
    status:            enum        // running | stopped | errored
    exit_code:         int?        // set when agent process exits
    pid:               int?        // agent process PID when running
    created_at:        timestamp
    last_attached_at:  timestamp?  // updated on each attach, used for MRU ordering
    attached_client:   string?     // client ID, null if no client attached
}
```

### Session Names

Session names must be globally unique. The random suffix on creation ensures this. The CLI resolves user input to session IDs: exact name match first, then prefix match, then substring. If ambiguous, show a picker. The protocol always uses `session_id`, never names — name resolution happens in the client.

### State Persistence

- In-memory map of `id -> Session` in the daemon
- Persisted to `~/.local/share/graith/state.json` on every mutation via atomic write (write to temp file, then rename)
- PID file at `$XDG_RUNTIME_DIR/graith/graith.pid` prevents duplicate daemons
- On daemon startup, reconcile state vs reality: check PIDs, verify worktree paths, update statuses
- After daemon crash, PTYs are lost — sessions are marked "stopped", user can resume
- If state file is corrupted (invalid JSON), daemon starts with empty state and logs a warning

### Scrollback

- Per-session log at `~/.local/share/graith/logs/<session-id>.log`
- Configurable size cap (default 100MB), oldest content truncated when exceeded
- Raw PTY output bytes (includes escape sequences)

## Daemon Internals

### Lifecycle

1. Auto-started on first `gr` command if not already running
2. Creates Unix socket at `$XDG_RUNTIME_DIR/graith/graith.sock` with `0700` permissions
3. Loads and reconciles state from disk
4. Stays alive indefinitely (even with zero sessions)
5. Manually stopped with `gr daemon stop`:
   - Warns if any sessions have unpushed work
   - Sends SIGTERM to all running agent processes
   - Waits up to 5 seconds for graceful shutdown, then SIGKILL
   - Disconnects all attached clients with a `detached` reason of `"daemon_shutdown"`
   - Removes socket and PID file
   - Does NOT remove worktrees or branches (they persist for recovery)

### Session Operations

All mutating operations are serialized through a single goroutine (channel-based, no locks):

- **Create**: validate git repo → fetch origin → discover base branch → create branch → create worktree → allocate PTY → spawn agent → write state
- **Delete**: confirm with client → kill agent (if alive) → remove worktree → remove branch → remove scrollback log → write state
- **Rename**: update display name only (stable ID and worktree path unchanged) → write state
- **Resume**: for stopped sessions, re-launch agent in the same worktree. Uses `resume_args` from config if defined (these may or may not use `{agent_session_id}` — e.g. codex uses `resume --last` which doesn't need it). Falls back to normal `command` + `args` if no `resume_args` configured.

Create rolls back on any step failure — no partial state is ever persisted.

### PTY Management

Per session, the daemon runs a goroutine that:
1. Reads from the PTY master fd
2. Writes to the scrollback log file
3. If a client is attached, forwards bytes over the data channel
4. If no client is attached, bytes are only written to the log

When a client attaches, the daemon replays the tail of the scrollback to fill the terminal, then switches to live streaming.

### Logging

All protocol messages (create, delete, rename, attach, detach, resize) are logged to `~/.local/share/graith/daemon.log` with timestamps. PTY data is not logged here.

## Client Architecture

### Connection Flow

1. Connect to daemon socket
2. Handshake: `{ version, terminal_size, client_id, cwd }` (cwd used for repo-aware session list ordering)
3. Daemon checks version compatibility, rejects on mismatch
4. If session specified → attach; otherwise → request session list, enter overlay mode

### Rendering Modes

**Passthrough mode** (default when attached):
- Terminal in raw mode
- Input: stdin → prefix key scan → forward to daemon or enter overlay
- Output: daemon data channel → stdout (direct byte forwarding)
- Mouse scroll: if the agent has enabled mouse reporting (e.g. TUI apps), wheel events pass through to the agent. Otherwise, graith intercepts them for scrollback navigation.
- Zero rendering overhead

**Overlay mode** (triggered by prefix key):
- Uses terminal alternate screen (like vim/less/fzf)
- bubbletea renders the session list panel
- On dismiss: exit alternate screen, original content reappears, resume data forwarding

### Prefix Key Handling

```
read byte from stdin
├─ Is it the prefix key?
│   ├─ Read next byte (with ~500ms timeout):
│   │   ├─ prefix key again → send one prefix key to agent (escape hatch)
│   │   ├─ known action key → execute action (see keybindings)
│   │   │   (includes: s → open shell in worktree)
│   │   ├─ timeout → open overlay (prefix alone = session list, same as ctrl+b w)
│   │   └─ unknown key → discard
└─ Not prefix key → forward to daemon
```

Bracketed paste detection: when paste mode is active (ESC[200~ ... ESC[201~), prefix key detection is disabled and all bytes pass through raw.

### Shell in Worktree

`ctrl+b s` opens the user's `$SHELL` in the current session's worktree directory. This allows running arbitrary commands (git operations, tests, file inspection) in the exact location the agent is working, without having to find the worktree path manually.

Implementation: the client suspends passthrough mode, restores the terminal to cooked mode, spawns `$SHELL` as a child process with `cwd` set to the session's worktree path, and waits for it to exit. On exit, the client re-enters raw mode and resumes passthrough. The daemon connection stays alive throughout — the agent keeps running while the user is in the shell. The shell process is purely client-side; the daemon is not involved.

### Multi-Client

- Each client independently views one session
- If two clients attach to the same session, the newest client wins
- The old client receives a "takeover" detach message and either drops to the session list (if in full TUI mode) or disconnects cleanly (if attached via `gr attach`)
- Prevents lock-out scenarios (no need to hunt for orphaned terminals)

## Protocol

### Frame Format

```
[1 byte: channel] [4 bytes: length (big-endian uint32)] [N bytes: payload]
```

- Channel `0x00` = control (JSON messages)
- Channel `0x01` = data (raw PTY bytes)

5 bytes overhead per frame. Single socket connection per client — control and data are logical channels multiplexed over one connection via the channel byte in the frame header.

### Control Messages

Client → Daemon:
- `handshake` — version, client_id, terminal_size, cwd
- `list` — request session list
- `create` — name, agent, repo_path; base branch auto-discovered
- `attach` — session_id
- `detach`
- `delete` — session_id
- `rename` — session_id, new_name
- `resume` — session_id
- `resize` — cols, rows
- `scrollback` — session_id, lines
- `search` — session_id, query, direction
- `confirm_response` — confirm_id, confirmed (bool)

Daemon → Client:
- `handshake_ok` / `handshake_err`
- `session_list` — array of session objects
- `created` / `attached` / `detached` / `deleted` / `renamed` / `resumed`
- `scrollback_data` — base64 encoded
- `search_result` — matches with offsets
- `error` — message
- `confirm` — interactive confirmation prompt (e.g. "has unpushed changes")
- `session_update` — status change notifications

### Data Messages

- Client → Daemon: raw keyboard input (when attached)
- Daemon → Client: raw PTY output (when attached)

### Backpressure

Per-client buffer of 1MB. If full, daemon disconnects the slow client with `detached` reason `"slow_client"`. Session continues unaffected.

## Git Worktree Lifecycle

### Creation (`gr new <name>`)

1. Validate: must be inside a git repo
2. `git fetch origin`
3. Discover base branch:
   - `origin/main` exists → use it
   - else `origin/master` exists → use it
   - else check remote HEAD
   - else fail with error asking user to specify `--base`
4. Generate session ID (short random hash)
5. Create branch: `{username}/graith/{name}-{id}` (e.g. `d0ugal/graith/fix-auth-bug-a3f2b1c9`)
6. Create worktree at `~/.local/share/graith/worktrees/<repo-name>/<repo-hash>/<id>/`
7. Launch agent process in the worktree

### GitHub Username Discovery

Priority order:
1. `github_username` in graith config (explicit, always wins)
2. `gh api user` (if gh CLI installed and authenticated)
3. `git config github.user`
4. Parse from git remote URL
5. Error with helpful message

### Deletion (`gr delete <name>`)

Confirmation ladder:
1. Agent running → "Agent is still running. Kill it and delete?"
2. Uncommitted changes → "Session has uncommitted changes. Delete anyway?"
3. Unpushed commits → "Session has N unpushed commits. Delete anyway?"

On confirmation: kill process, remove worktree, remove branch, remove scrollback log, remove state entry.

### Rename (`gr rename <old> <new>`)

Changes display name only. Stable ID, worktree path, and branch are unchanged.

## Session List UI

Overlay panel triggered by prefix key. Sessions grouped by repo:

```
 graith
   fix-auth-bug-a3f2b1c9      ● running
   add-tests-b1c9e4d2         ● running

 grafana
   dashboard-perf-x8d2f3a7    ○ stopped

 alerting-service
   fix-flaky-test-k4e17b2a    ● running
```

Context-aware: if invoked from within a repo, that repo's group is highlighted/expanded by default.

Keybindings within the overlay:
- `Enter` — attach to selected session
- `c` — create new session
- `x` — delete selected session
- `r` — rename selected session
- `/` — filter/search sessions
- `Escape` — dismiss, return to current session
- `j/k` or arrows — navigate

## Scrollback

- **Disk-persisted** with configurable cap (default 100MB per session)
- **Mouse scroll** works naturally: scroll up enters scrollback, scroll to bottom resumes live
- **Search**: `ctrl+b /` opens incremental search with match highlighting, regex support. Search operates on ANSI-stripped text for matching, but displays results with original formatting preserved.
- **Keyboard navigation**: `ctrl+b [` enters scroll mode (arrows, page up/down, Home/End)
- **External access**: log files at `~/.local/share/graith/logs/<id>.log` can be grepped directly

## Configuration

File: `~/.config/graith/config.toml`

```toml
default_agent = "claude"
github_username = "d0ugal"                    # auto-discovered if not set
branch_prefix = "{username}/graith"           # template
scrollback_limit = "100MB"
fetch_on_create = true

[keybindings]
prefix = "ctrl+b"
new_session = "c"
delete_session = "x"
detach = "d"
session_list = "w"
next_session = "n"
prev_session = "p"
resume_session = "R"
rename_session = ","
search = "/"
scroll_mode = "["
shell = "s"

[agents.claude]
command = "claude"
args = ["--session-id", "{agent_session_id}"]
resume_args = ["--resume", "{agent_session_id}"]

[agents.codex]
command = "codex"
args = []
resume_args = ["resume", "--last"]

[agents.opencode]
command = "opencode"
args = []
resume_args = ["--session", "{agent_session_id}"]

[agents.agy]
command = "agy"
args = []
resume_args = ["--conversation", "{agent_session_id}"]
```

### Template Variables

Available in `args`, `resume_args`, `branch_prefix`:
- `{username}` — resolved GitHub username
- `{agent_session_id}` — agent's own session ID (generated for claude, manual/discovered for others)
- `{session_name}` — graith session display name
- `{session_id}` — graith session stable ID
- `{worktree_path}` — absolute worktree path

### Agent Environment Variables

```toml
[agents.claude-sandboxed]
command = "safehouse"
args = ["claude", "--session-id", "{agent_session_id}"]
resume_args = ["safehouse", "claude", "--resume", "{agent_session_id}"]
env = { SAFEHOUSE_ALLOW_READ = "/opt/homebrew" }
```

### Config Resolution

1. Built-in defaults (hardcoded)
2. `~/.config/graith/config.toml` (user config)
3. CLI flags (highest priority)

## CLI Commands

The binary is named `graith`. Users can alias it as `gr` in their shell config (e.g. `alias gr=graith`). A future enhancement could install both names via symlink.

```
graith (aliased as gr)

USAGE:
    gr                                  Attach to daemon, show session list
    gr new <name> [flags]               Create a new session
    gr list [--json] [--repo <path>]    List all sessions
    gr attach [name]                    Attach to a session (fuzzy match)
    gr delete <name>                    Delete a session
    gr rename <old> <new>              Rename a session
    gr info [--json]                    Show current session info (from within worktree)
    gr doctor [--autofix]               Health checks and diagnostics
    gr daemon start|stop                Daemon management

NEW FLAGS:
    --agent <name>                      Override default agent
    --base <branch>                     Override auto-discovered base branch
    --background                        Create without attaching

GLOBAL FLAGS:
    --config <path>                     Override config file path
```

### Behavior Details

- `gr` (no args): auto-start daemon, show session list. Shows all repos but highlights/defaults to current repo if inside one.
- `gr new`: must be inside a git repo. Auto-discovers base branch, fetches, creates worktree, launches agent, attaches.
- `gr attach` (no name): most recently used session.
- `gr attach fix-auth`: fuzzy match. If ambiguous, show picker.
- `gr delete`: confirmation ladder based on state (running, uncommitted, unpushed).
- `gr info`: detects current session from working directory. `--json` for LLM consumption.
- `gr doctor`: checks for orphaned worktrees, stale state, dead PIDs, socket health. `--autofix` cleans up.
- `gr daemon stop`: warns if sessions have unpushed work. Does not remove worktrees.

## Agent Session ID Discovery

**Claude**: fully supported. graith generates a UUID and passes `--session-id` at launch. Resume uses `--resume` with the same ID.

**Codex, OpenCode, Agy**: v1 limitation. Automatic session ID discovery is not implemented. Users can manually specify `resume_args` in config. Future enhancement: parse agent state directories to discover session IDs automatically.

## Known Risks

1. **bubbletea + raw PTY mode switching** — the dual-mode client (passthrough vs overlay) is the riskiest technical bet. Must be validated with an early prototype that runs real agent CLIs (claude, codex) and tests: resize, mouse events, bracketed paste, alternate screen, escape sequences, image drag-drop, and rich paste.

2. **Daemon crash kills all sessions** — acceptable for v1, documented clearly. State file enables recovery of metadata on restart. Agent resume support mitigates the impact.

3. **Git worktree edge cases** — submodules, sparse checkouts, bare repos, non-origin remotes, repos with both main and master. The base branch discovery logic handles common cases; `--base` flag is the escape hatch.

4. **Bracketed paste with prefix key** — handled by disabling prefix detection during paste mode.

5. **Scrollback search through escape sequences** — searching raw PTY output that contains ANSI codes. May need ANSI stripping for search matching while preserving codes for display.

## Future Ideas

- **`gr logs <name>`** — dump/tail session log without attaching
- **Desktop notifications** — OS-level notifications when agent exits or errors (configurable)
- **Multi-session delete** — `gr delete --repo graith` or multiple names
- **Batch creation** — `gr batch` to create multiple sessions from JSON input
- **Inter-agent communication** — shared context directories and pub/sub messaging between agents
- **Session recycling** — reuse old worktrees instead of creating fresh ones
- **Hooks** — run commands after session creation (install deps, copy config)
- **Workspace discovery** — configure workspace dirs so `gr new` works from anywhere
- **Initial prompt** — `gr new fix-bug --prompt "Fix the auth timeout"` to pass a task to the agent
- **Agent state tracking** — parse agent output for richer status indicators (like Hive)
- **Per-repo config** — `.graith.toml` in repo root to override agent and branch settings
