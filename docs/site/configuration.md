# Configuration

Configuration lives at `~/.config/graith/config.toml` (or `$XDG_CONFIG_HOME/graith/config.toml`). All fields are optional. Sensible defaults are provided.

Manage config with:

```bash
gr config show     # print effective (merged) config
gr config diff     # show changes from defaults
gr config reset    # write built-in defaults to config file
```

The daemon reloads config on `gr daemon reload` without restarting.

## Global settings

```toml
default_agent      = "claude"             # agent used when --agent is not given
github_username    = ""                   # expands {username} in branch_prefix
branch_prefix      = "{username}/graith"  # template for new branch names
fetch_on_create    = true                 # fetch origin before creating a worktree
data_dir           = ""                   # override data directory (default: XDG data home)
allowed_repo_paths = []                   # restrict which repo paths the daemon accepts
```

### `agent_prompt`

A multiline string injected into the agent's environment. For Claude, it is passed via `--append-system-prompt`. For Cursor, it is written to `.cursor/rules/graith.mdc`. Other agents (Codex, OpenCode, Agy) do not currently support prompt injection. Teaches agents how to use `gr status`, `gr msg`, `gr store`, and other graith primitives. Set `inject_prompt = false` on a per-agent basis to disable.

### `allowed_repo_paths`

When non-empty, the daemon rejects `--repo` / `-C` paths that are not under one of these prefixes. Paths support `~` expansion and are resolved to absolute paths before comparison. These paths also feed the repo autocomplete in the create-session form (`ctrl+b c` or `n` in the overlay) — each path is scanned one level deep for git repositories.

```toml
allowed_repo_paths = ["~/Code", "~/Work"]
```

## Status bar

```toml
[status_bar]
enabled  = true      # show a status bar while attached
position = "bottom"  # "bottom" or "top"
```

The status bar shows the session name, status, agent type, branch, git status, unread messages, and fleet summary. It updates in real time.

## Notifications

```toml
[notifications]
enabled     = true   # desktop notifications
on_approval = true   # notify when a session needs approval
on_stopped  = false  # notify when a session stops
command     = ""     # custom notification command (optional)
```

When `command` is set, graith executes it via `sh -c` instead of using the system notification API. The command receives context via environment variables: `GRAITH_SESSION_NAME`, `GRAITH_STATUS`, and `GRAITH_MESSAGE`.

## Approvals

```toml
[approvals]
mode     = "prompt"  # "prompt" or "localmost"
timeout  = "10m"     # how long to wait for a decision
auto_pop = false     # auto-show approval overlay when approval is needed
command  = ""        # custom command to run for approval decisions
```

The approval system integrates with agent hooks. When an agent requests approval (e.g. for a dangerous tool call), graith can prompt the user via the overlay (`prompt` mode) or delegate to a local command (`localmost` mode, which runs a command to make the decision with a 5-second timeout, falling back to user prompt).

## Messages

```toml
[messages]
max_age        = ""  # prune messages older than this, e.g. "7d", "168h" (empty = keep forever)
max_per_stream = 0   # cap messages per stream (0 = unlimited)
```

Duration strings support days: `7d`, `30d`, `1d12h`.

## Status

```toml
[status]
ttl = "5m"  # default TTL for status updates
```

When an agent sets a status via `gr status`, it auto-expires after this TTL if the agent produces new output without updating the status. Override per-update with `gr status --ttl <duration>`.

## Git pull

```toml
[git_pull]
enabled  = false  # periodically pull updates into worktrees
interval = "1h"   # how often to pull (minimum: 1 minute)
```

When enabled, the daemon periodically fetches and fast-forward merges repos registered with `git maintenance`. It skips repos that have active sessions to avoid disrupting running agents. This keeps default branches up to date for future session creation. It does not pull into active session worktrees.

## Keybindings

```toml
[keybindings]
prefix              = "ctrl+b"  # prefix key
new_session         = "c"       # create a session (configurable)
fork_session        = "f"       # fork the current session (configurable)
next_session        = "n"       # next session (configurable)
prev_session        = "p"       # previous session (configurable)
last_session        = "l"       # last (most recently attached) session (configurable)
orchestrator_session = "o"      # switch to orchestrator session (configurable)
detach              = "d"       # detach (reserved, currently hardcoded)
session_list        = "w"       # open the session picker overlay (reserved, currently hardcoded)
shell               = "s"       # open a shell in the worktree (reserved, currently hardcoded)
delete_session      = "x"       # reserved, not currently wired
resume_session      = "R"       # reserved, not currently wired
rename_session      = ","       # reserved, not currently wired
search              = "/"       # reserved, not currently wired
scroll_mode         = "["       # reserved, not currently wired
```

Only `prefix`, `new_session`, `fork_session`, `next_session`, `prev_session`, `last_session`, and `orchestrator_session` are currently read from config. Other keys are present in the config struct but hardcoded in passthrough or not yet wired.

The prefix key accepts values like `ctrl+b`, `ctrl+x`, or a single character. graith handles both raw control bytes and Kitty keyboard protocol sequences, so it works in terminals like Ghostty that use the extended protocol.

See [Keybindings](keybindings.md) for the complete keybinding reference.

## Orchestrator

```toml
[orchestrator]
enabled      = false    # enable the orchestrator session
agent        = "claude" # agent to run as orchestrator
model        = ""       # optional model override
idle_timeout = "30m"    # auto-stop if idle
prompt       = "..."    # orchestrator-specific system prompt
prompt_file  = ""       # or read from file
```

See [Orchestrator](orchestrator.md) for details.

## MCP servers

Define global MCP servers that are available to all agent sessions:

```toml
[[mcp_servers]]
name    = "my-tools"
command = "/usr/local/bin/my-mcp-server"
args    = ["--port", "8080"]
env     = { API_KEY = "..." }
disabled = false
sandbox  = true  # override sandbox for this server
```

MCP servers can be overridden or disabled per-agent (see agent config below).

## Agent definitions

Each agent is configured under `[agents.<name>]`. Five agents ship by default: `claude`, `codex`, `opencode`, `cursor`, and `agy`.

```toml
[agents.claude]
command        = "claude"
args           = ["--session-id", "{agent_session_id}"]
resume_args    = ["--resume", "{agent_session_id}"]
fork_args      = ["--resume", "{fork_source_agent_session_id}", "--fork-session", "--session-id", "{agent_session_id}"]
env            = {}             # extra environment variables
idle_timeout   = ""             # auto-stop after idle (default: 1h when resume_args is set, 0 otherwise)
inject_prompt  = true           # inject agent_prompt into the session
validate_model = ""             # command to validate --model values
```

### Template variables

These are substituted in `args`, `resume_args`, and `fork_args`:

| Variable | Expands to |
|----------|-----------|
| `{agent_session_id}` | UUID for the agent session (used for `--session-id` / `--resume`) |
| `{session_id}` | Internal graith session ID |
| `{session_name}` | Human-readable session name |
| `{username}` | `github_username`, or discovered GitHub username, or literal `"user"` |
| `{worktree_path}` | Absolute path to the session worktree |
| `{model}` | Model passed via `gr new --model` (empty if not set) |
| `{fork_source_agent_session_id}` | Agent session ID of the fork source (empty if not a fork) |

Only `{username}` is available in `branch_prefix`.

### Per-agent sandbox

```toml
[agents.claude.sandbox]
enabled    = true        # enable sandbox for this agent (merged with global)
disabled   = false       # force-disable even if global sandbox is enabled
read_dirs  = ["~/.claude"]
write_dirs = ["~/.claude"]
features   = ["clipboard"]
```

Features and directories are merged with the global sandbox config. Setting `disabled = true` overrides `enabled = true` on the global config.

### Per-agent MCP overrides

Override or disable global MCP servers for a specific agent:

```toml
[agents.claude.mcp_servers.my-tools]
disabled = true  # disable this server for Claude

[agents.codex.mcp_servers.extra-tools]
command = "/path/to/extra-tools"
args    = ["--codex-mode"]
```

A per-agent MCP entry with `disabled = true` removes the global server for that agent. Entries that override `command`, `args`, or `env` are merged with the global definition.

### Custom agents

Define additional agents beyond the built-in five:

```toml
[agents.my-agent]
command     = "/usr/local/bin/my-agent"
args        = ["--session", "{agent_session_id}", "--model", "{model}"]
resume_args = ["--resume", "{agent_session_id}"]
env         = { MY_CONFIG = "production" }
idle_timeout = "2h"

[agents.my-agent.sandbox]
read_dirs  = ["~/.my-agent"]
write_dirs = ["~/.my-agent"]
```

Use with `gr new my-task --agent my-agent`.

## Repository configuration

Per-repo settings:

```toml
[[repos]]
path             = "~/Code/my-project"
allow_concurrent = false  # allow multiple in-place sessions
singleton        = false  # allow only one session at a time
includes         = ["~/Code/shared-lib"]  # include other repos in the session
```

`singleton` and `allow_concurrent` are mutually exclusive.

### Includes

When `includes` is set, the daemon creates worktrees for the included repos alongside the main worktree. The included repo paths are exposed as environment variables:

```
GRAITH_INCLUDE_<BASENAME>_PATH=/path/to/included/worktree
```

The basename is uppercased, with `-` and `.` replaced by `_`. For example, `~/Code/shared-lib` becomes `GRAITH_INCLUDE_SHARED_LIB_PATH`.

Validation rules:
- A repo cannot include itself
- Included repo basenames must be unique
- Environment variable names derived from basenames must not collide

## Default agent configurations

### Claude

```toml
[agents.claude]
command     = "claude"
args        = ["--session-id", "{agent_session_id}"]
resume_args = ["--resume", "{agent_session_id}"]
fork_args   = ["--resume", "{fork_source_agent_session_id}", "--fork-session", "--session-id", "{agent_session_id}"]
```

### Codex

```toml
[agents.codex]
command     = "codex"
args        = []
resume_args = ["resume", "--last"]
fork_args   = ["fork", "{fork_source_agent_session_id}"]
```

### OpenCode

```toml
[agents.opencode]
command     = "opencode"
args        = []
resume_args = ["--session", "{agent_session_id}"]
```

### Cursor

```toml
[agents.cursor]
command        = "agent"
args           = []
resume_args    = ["resume"]
validate_model = "agent --list-models"
```

### Agy

```toml
[agents.agy]
command     = "agy"
args        = []
resume_args = ["--conversation", "{agent_session_id}"]
```

## File locations

graith follows the XDG base directory spec:

| Path | Contents |
|------|----------|
| `~/.config/graith/config.toml` | Configuration file |
| `~/.local/share/graith/state.json` | Persisted session state |
| `~/.local/share/graith/messages.sqlite` | Inter-agent message store |
| `~/.local/share/graith/daemon.log` | Daemon log (slog, JSON format) |
| `~/.local/share/graith/worktrees/<repo>/<hash>/<id>/` | Session worktrees |
| `~/.local/share/graith/store/<repo-name>-<hash>/` | Per-repo document stores |
| `~/.local/share/graith/store/shared/` | Shared document store |
| `~/.local/share/graith/tmp/<repo-name>/<hash>/` | Per-repo temp directories |
| `$XDG_RUNTIME_DIR/graith/graith.sock` | Unix control socket |
| `$XDG_RUNTIME_DIR/graith/graith.pid` | Daemon PID file |

Override `data_dir` in config to change the base data directory.
