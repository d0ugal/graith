# graith

**Run a fleet of AI coding agents in parallel — each in its own git worktree, each in a session that outlives your terminal.**

graith is a terminal multiplexer built for AI coding agents (Claude, Codex, OpenCode, Cursor, Agy). Spin up an agent per task, let them work isolated and unattended, and jump between them with a tmux-style prefix key. A long-lived daemon owns the sessions, so closing your terminal — or losing your SSH connection — doesn't stop the work.

**graith** (Scots) — *noun:* equipment, tools, gear for a specific trade. *verb:* to make ready, prepare, equip. Your agents, graithed and ready to work.

📖 **[Documentation](https://d0ugal.github.io/graith/)** — full guide, CLI reference, configuration, and architecture.

## Why

Running several agents at once shouldn't mean juggling terminal tabs and stepping on your own branches. graith gives you:

- **Isolation** — every agent gets its own git worktree and branch, so parallel work never collides
- **Persistence** — a daemon owns the PTYs; sessions survive terminal closures, daemon restarts, and SSH drops
- **Switching** — hop between agents instantly with a tmux-style prefix key
- **Visibility** — see every session at a glance, with a "Needs Attention" view that surfaces what's blocked or waiting
- **Coordination** — agents message each other over pub/sub, and you drive them remotely with `type`, `logs`, and a live dashboard

It owns the PTY, manages the worktrees, and otherwise gets out of your way.

## Install

The binary is called `gr`.

### Homebrew

```bash
brew install d0ugal/tap/graith
```

### Debian / Ubuntu (apt)

Add the signing key and repository once, then install with `apt-get`:

```bash
# add the signing key
curl -fsSL https://d0ugal.github.io/graith-repo/gpg/graith.gpg \
  | sudo tee /usr/share/keyrings/graith.gpg > /dev/null

# add the repo (signed-by pins it to our key only)
echo "deb [signed-by=/usr/share/keyrings/graith.gpg] \
https://d0ugal.github.io/graith-repo/deb stable main" \
  | sudo tee /etc/apt/sources.list.d/graith.list

sudo apt-get update
sudo apt-get install graith
```

`apt-get upgrade` then picks up new releases automatically.

### Fedora / RHEL (dnf)

```bash
sudo tee /etc/yum.repos.d/graith.repo <<'EOF'
[graith]
name=graith
baseurl=https://d0ugal.github.io/graith-repo/rpm
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=https://d0ugal.github.io/graith-repo/gpg/graith.asc
EOF

sudo dnf install graith
```

`dnf upgrade` picks up new releases.

### From a release

Download a prebuilt binary for your platform from the [releases page](https://github.com/d0ugal/graith/releases), extract it, and put `gr` on your `$PATH`.

On Debian/Ubuntu, Fedora/RHEL and Alpine you can instead grab a prebuilt
`.deb`, `.rpm` or `.apk` package for linux `amd64` or `arm64` (package name
`graith`, binary `gr`, with shell completions installed) from the same
[releases page](https://github.com/d0ugal/graith/releases) and install it
manually:

```bash
# Debian / Ubuntu
sudo dpkg -i graith_*_linux_amd64.deb

# Fedora / RHEL
sudo rpm -i graith_*_linux_amd64.rpm

# Alpine
sudo apk add --allow-untrusted graith_*_linux_amd64.apk
```

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

# Rename / delete (soft — recoverable for 24h) / restore / purge (destroy now)
gr rename fix-auth-bug auth-rewrite
gr delete auth-rewrite            # soft delete: hidden, but recoverable
gr restore auth-rewrite           # bring it back within the retention window
gr purge auth-rewrite             # hard-delete now (irrecoverable)
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
| `gr delete <name>` (`rm`) | Soft-delete a session — hide it but keep the worktree for the recovery window (default 24h); `--children` deletes descendants |
| `gr restore <name>` | Restore a soft-deleted session within its recovery window |
| `gr purge <name>` | Permanently delete a session now (worktree + branch gone), bypassing the recovery window |
| `gr list --deleted` | List soft-deleted sessions and their expiry |
| `gr rename <old> <new>` | Rename a session |
| `gr fork <source> <name>` | Fork a session (new worktree + agent conversation history) |
| `gr info` | Show info for the current session (when inside a worktree) |
| `gr logs <name>` (`l`) | Show a session's output without attaching |
| `gr type <name> <text>` (`t`) | Type text into a session's stdin |
| `gr interrupt <name>` | Send Ctrl-C to a session's agent (agent-aware) |
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
| `--yolo` | Auto-approve all tool requests for this session (no approval prompts) |
| `--mirror <session>` | Mount another session's worktree read-only |
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
# From inside a session, message all descendant sessions
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

# Interrupt the agent's current operation (sends Ctrl-C) without stopping it
gr interrupt fix-auth-bug

# Watch a session's output without attaching
gr logs fix-auth-bug --follow
gr logs fix-auth-bug --lines 500

# See which sessions are blocked waiting for you to approve something
gr approvals

# A live TUI dashboard of every session (attach/stop/delete/resume inline)
gr dashboard
```

`gr interrupt` gently cancels whatever the agent is currently doing — the
session stays alive and ready for the next instruction — whereas `gr stop`
kills the session entirely. Because it's agent-aware (Claude gets two rapid
presses, other agents one), it's handy for orchestration and scripting: cancel
a runaway operation programmatically without ending the session.

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

## Sandbox

graith can wrap agent processes in a deny-by-default OS sandbox. This lets you run agents with their "skip permissions" flags (e.g. `--dangerously-skip-permissions` for Claude, `--dangerously-bypass-approvals-and-sandbox` for Codex) while confining them at the kernel level. Two backends are available:

| Backend | Platforms | Primitive |
|---------|-----------|-----------|
| `safehouse` | macOS only | `sandbox-exec` / Seatbelt ([safehouse](https://github.com/eugene1g/agent-safehouse)) |
| `nono` | **Linux + macOS** | [nono](https://github.com/nolabs-ai/nono): Landlock + seccomp on Linux, Seatbelt on macOS |

Sandboxing is **config-only** — there are no CLI flags to enable or disable it. This prevents a sandboxed agent from spawning a child agent that escapes the sandbox (Landlock/Seatbelt restrictions are inherited by descendants).

> **Migration (pre-1.0 breaking change):** `backend` is now **required** when `sandbox.enabled = true` — there is no default. To keep existing behaviour, **add `backend = "safehouse"`** to your `[sandbox]` block. On Linux, use `backend = "nono"`. Enabling the sandbox without a backend fails closed with an actionable error; `gr doctor` flags it.

### Setup

**safehouse (macOS):** `brew install eugene1g/safehouse/agent-safehouse`

**nono (Linux/macOS):** `brew install nono` (or download the pinned release from <https://github.com/nolabs-ai/nono/releases> and verify it with `gh attestation verify <tarball> --repo nolabs-ai/nono` before installing). nono needs Linux kernel 5.13+ for Landlock (practical floor 5.14+); on macOS it uses Seatbelt. graith enforces a minimum nono version.

Verify with `gr doctor`, then configure:

```toml
allowed_repo_paths = ["~/Code"]         # restrict which repos the daemon will create sessions in

[sandbox]
enabled  = true
backend  = "nono"                       # REQUIRED: "safehouse" (macOS) or "nono" (Linux/macOS)
features = ["ssh"]                      # feature gates (see caveats below)
read_dirs  = ["~/Code"]                 # additional read-only paths (directories)
write_dirs = []                         # additional read-write paths (directories)
read_files  = []                        # additional read-only single files
write_files = []                        # additional read-write single files

[agents.claude]
command     = "claude"
args        = ["--dangerously-skip-permissions", "--session-id", "{agent_session_id}"]
resume_args = ["--dangerously-skip-permissions", "--resume", "{agent_session_id}"]
```

### How it works

When `sandbox.enabled = true`, the daemon resolves the merged policy, expands `~`/globs, and wraps the agent with the selected backend.

**safehouse** runs `safehouse wrap` (macOS `sandbox-exec`): denies file access by default, allows the worktree + `read_dirs`/`write_dirs` (and `read_files`/`write_files`, folded into the same path lists), strips the environment to an allowlist, and gates `features`.

**nono** generates a per-session JSON profile and runs `nono run --profile <file> --workdir <dir> -- <agent>` (the `--workdir` pins nono's read-write workdir to the session's worktree/scratch dir rather than letting it resolve from the process cwd — important for `--mirror`, where the cwd is the read-only source). The profile `extends: "default"` (inheriting nono's audited credential/shell-history deny groups), maps the worktree and `write_dirs` to `filesystem.allow` (read+write — never nono's write-only `filesystem.write`), `read_dirs` to read-only, and the single-file grants `read_files`/`write_files` to `filesystem.read_file` / `filesystem.allow_file`. It grants read on the agent binary's directory (nono does not auto-grant it), and maps the **environment to an allowlist** (`environment.allow_vars`, including `PATH`/`HOME`/`GRAITH_*`) so host secrets aren't leaked — nono otherwise inherits all env. nono cannot make a path under its default-writable `/tmp`/`$TMPDIR` read-only (Landlock has no deny-under-an-allowed-parent, and macOS deny would remove read too), so graith **rejects** a read-only `read_dirs`/`read_files` grant located there with a clear config error rather than emit a profile that can't enforce it — keep read-only sandbox paths outside `/tmp` and `$TMPDIR`.

**File grants (`read_files`/`write_files`)** exist for paths that can't be a directory grant without over-sharing — most importantly single files that live directly in `$HOME`. For example, Claude Code stores its login in `~/.claude.json`; granting `~` would expose every dotfile secret, so grant just the file (read+write, since the agent rewrites it to refresh tokens):

```toml
[agents.claude.sandbox]
write_files = ["~/.claude.json", "~/.claude.json.lock", "~/.claude.lock"]
```

An explicit file grant also punches through the inherited `deny_credentials` group, which is what a login/token file needs.

The sandbox **fails closed**: if enabled but the backend can't enforce (no backend chosen, binary missing, nono below the minimum version, or a Linux kernel too old for Landlock), session creation is refused. A Linux kernel with Landlock filesystem support but no network-filtering ABI runs in a *degraded* mode (filesystem confinement still holds, but see network below). `gr doctor` reports all of this.

### Network (nono only)

By default agents keep unrestricted outbound network (matching nono's default). You can add an egress policy under `[sandbox.network]`, mapped onto nono's profile `network` section:

```toml
[sandbox.network]
block = true                                # deny all outbound network
# or, instead of blocking everything, restrict to an allowlist:
allow_domains = ["github.com", "https://api.anthropic.com/**"]
```

- `block = true` → `network.block` (no outbound access at all).
- `allow_domains` → `network.allow_domain` (nono runs its L7 filtering proxy and only these hosts/URL globs are reachable). A plain hostname allows the whole host; a URL glob restricts to matching endpoints.

Network filtering needs **Landlock ABI v4 (Linux kernel 6.7+)**. If a network policy is requested on a kernel that can only do filesystem enforcement, the sandbox **fails closed** — graith refuses rather than pretend to block egress. `safehouse` has no network primitive, so setting a network policy with `backend = "safehouse"` also fails closed (use `nono` for network filtering). A network policy can be set globally or per-agent (an agent's `[sandbox.network]` replaces the global one wholesale).

### Feature gate caveats

`features` map differently per backend. Under **nono**: `ssh` grants the `$SSH_AUTH_SOCK` agent socket (socket only; raw `~/.ssh` keys are not granted); `process-control` is a **no-op on its own** (nono's default already permits same-sandbox signals, whereas it gates under safehouse) — set `signal_mode = "isolated"` (below) to make it actually gate signalling under nono; any unmapped feature (e.g. `clipboard`) is **warned and ignored**, not silently dropped. nono has no clipboard capability and graith defines no clipboard semantics, so `clipboard` stays a warned no-op.

### Process isolation: `signal_mode` (nono only)

`signal_mode` controls whether the sandboxed process may signal other processes. It maps to nono's `security.signal_mode`:

```toml
[sandbox]
signal_mode = "isolated"                    # "isolated" | "allow_same_sandbox" (nono default) | "allow_all"
```

Setting `signal_mode = "isolated"` makes graith's `process-control` feature meaningful under nono (the process can no longer signal anything outside its own sandbox). Leaving it unset inherits nono's base-profile default (`allow_same_sandbox`). `safehouse` ignores this field.

### Debugging denials: `gr sandbox explain` / `gr sandbox watch`

Two subcommands, split by the question you're asking:

**`gr sandbox explain`** — *would* an access be allowed? A predictive check against the profile graith would generate, via the backend's policy oracle (the `nono` backend; on `safehouse` it errors and points you at `watch`):

```bash
gr sandbox explain --path ~/.ssh/id_rsa --op read     # denied (deny_credentials)
gr sandbox explain --path ~/Code/shared --op write    # denied if read-only read_dir
gr sandbox explain --host github.com --port 443        # network reachability
gr sandbox explain --agent codex --path /etc/hosts --op read   # merged per-agent policy
```

`--op` is `read`, `write`, or `readwrite`; add `--json` for machine-readable output.

**`gr sandbox watch`** — what *did* the sandbox deny? Reads real macOS Seatbelt denials from the unified log — live-tail by default, or a recent aggregated window with `--recent`. macOS-only; run it from your normal shell (not inside a sandboxed session):

```bash
gr sandbox watch                 # live-tail denials (Ctrl-C to stop)
gr sandbox watch --recent --since 1h
gr sandbox watch my-session --proc node   # scope to a session's process tree
```

### Per-agent overrides

Each agent can extend or disable the global sandbox config:

```toml
[sandbox]
enabled  = true
backend  = "nono"
features = ["ssh"]

[agents.claude.sandbox]
write_dirs = ["~/.claude"]             # agent-specific write access

[agents.codex.sandbox]
disabled = true                         # opt this agent out of sandboxing
```

`backend`, `command`, `features`, `signal_mode`, `network`, and directories all merge (global + agent), with the agent's `backend`/`command`/`signal_mode`/`network` taking precedence (an agent's `network` block replaces the global one wholesale). Setting `disabled = true` on an agent overrides `enabled = true` on the global config.

### Path restrictions

`allowed_repo_paths` limits which directories the daemon will accept for `--repo` / `-C`. If set, any repo path outside these prefixes is rejected. Paths support `~` expansion and are resolved to absolute paths before comparison.

```toml
allowed_repo_paths = ["~/Code", "~/Work"]
```

When empty (the default), any repo path is accepted.

## Configuration

Config lives at `~/.config/graith/config.toml` (or `$XDG_CONFIG_HOME/graith/config.toml`). All fields are optional — sensible defaults are provided. The block below shows common options at their default values.

Inspect and manage config from the CLI:

```bash
gr config show     # print the full effective (merged) config
gr config path     # print the config file path
gr config init     # generate a sample config.toml with built-in defaults
gr config diff     # show your changes vs the built-in defaults
```

(`gr config reset` is an alias for `init` — both write the defaults; neither overwrites an existing file without `--force`.)

```toml
default_agent   = "claude"              # agent used when --agent isn't given
github_username = ""                    # used by {username} in branch_prefix
branch_prefix   = "{username}/graith"   # template for new branch names
fetch_on_create = true                  # fetch origin before creating a worktree
# allowed_repo_paths = ["~/Code"]       # restrict which repos the daemon allows (empty = any)

[sandbox]
enabled    = false                      # wrap agents in an OS sandbox
# backend  = "nono"                     # REQUIRED when enabled: "safehouse" (macOS) | "nono" (Linux/macOS)
# command  = "nono"                     # path to the backend binary (default: the backend name)
# features = ["ssh", "process-control"] # feature gates (mapping differs per backend; see the Sandbox section)
# read_dirs  = []                       # additional read-only paths (directories)
# write_dirs = []                       # additional read-write paths (directories)
# read_files  = []                      # additional read-only single files
# write_files = []                      # additional read-write single files (e.g. ~/.claude.json)
# signal_mode = "isolated"              # nono only: "isolated" | "allow_same_sandbox" | "allow_all"

# [sandbox.network]                     # nono only; needs Landlock ABI v4 (kernel 6.7+)
# block = true                          # deny all outbound network
# allow_domains = ["github.com"]        # OR restrict to a proxy allowlist (host or URL glob)

[status_bar]
enabled  = true                         # show a status bar while attached
position = "bottom"

[notifications]
enabled     = true                      # desktop notifications
on_approval = true                      # notify when a session needs approval
on_stopped  = false                     # notify when a session stops
command     = ""                        # custom notification command (optional)

[approvals]
backend = ""                            # who decides: "" (prompt the human), "command"/"external"
                                        #   (delegate to a command), "localmost" (real localmost binary),
                                        #   "builtin" (graith's built-in localmost-compatible engine),
                                        #   or "auto" (auto-approve every request without prompting).
                                        #   "mode" is the deprecated predecessor (still works).
timeout = "10m"                         # how long to wait for a human decision
auto_pop = false                        # auto-open the approval overlay when a request is queued
# command = ""                          # for backend "command"/"external" (or a localmost path override)
# For a single unattended session, prefer per-session opt-in: gr new --yolo
# (auto-approves that session only, regardless of the global backend above).
# [approvals.builtin]
# config = ""                           # localmost-format config.json (backend "builtin")

[messages]
max_age        = ""                     # prune messages older than e.g. "7d" / "168h" (empty = keep)
max_per_stream = 0                      # cap messages per stream (0 = unlimited)

[delete]
retention      = "24h"                  # how long soft-deleted sessions are kept before purge
# retention    = "0"                    # disable soft delete: gr delete is rejected, use gr purge

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

[input]
# Touch/hold-and-drag arrow keys: press-and-hold the left mouse
# button then drag up/down/left/right to emit discrete arrow-key presses to the
# focused pane. Off by default because it repurposes left-drag (otherwise used
# for text selection); mouse-wheel scrolling always passes through unchanged.
# Only translates drags when the focused app already has SGR mouse reporting on
# (a mouse-tracking TUI or a touch/mobile terminal); graith won't enable it.
drag_arrow_keys      = false            # enable the drag-to-arrow gesture
drag_arrow_threshold = 2                # cells of drag per arrow press (min 1)

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
# write_files = ["~/.claude.json", "~/.claude.json.lock", "~/.claude.lock"]  # login file (needs read+write)

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
| `h` / `l` (or left/right) | Cycle view: All → Needs Attention → Active → Starred → Scenarios → Deleted |
| `n` / `p` | Next / previous session |
| `/` | Filter by name |
| `x` then `y` | Delete (soft, with confirmation) |
| `enter` (in Deleted view) | Restore the highlighted soft-deleted session |
| `q` / `esc` | Close the overlay |

**Views:**
- **All** — every session, grouped by repo (default)
- **Needs Attention** — sessions waiting for approval, errored, idle, or stopped with uncommitted/unpushed changes, sorted by time in current state (oldest first)
- **Active** — running sessions only, sorted newest first
- **Starred** — starred sessions only
- **Scenarios** — sessions grouped by scenario
- **Deleted** — soft-deleted sessions, most-recently-deleted first, with their expiry; `enter` restores the highlighted one (the only action in this view)

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

When you stop a session, the agent process is killed but the worktree and branch are kept (resume restarts the agent in place).

When you **delete** a session it is *soft-deleted*: the agent is stopped and the session is hidden from `gr list` and the picker, but its worktree, branch, and state are preserved for a recovery window (default 24h, configurable via `[delete] retention`). Within that window `gr restore <name>` brings it back as a stopped session, and `gr list --deleted` (or the picker's *Deleted* view) shows what's pending purge and when. A background loop hard-deletes sessions once their window elapses. To reclaim the disk immediately, `gr purge <name>` hard-deletes now — process killed, worktree removed, branch deleted — bypassing the window; it prompts on unsaved work. Set `retention = "0"` to disable soft delete (then `gr delete` is rejected and you use `gr purge`).

## Environment variables

The daemon sets these in every agent process:

| Variable | Value |
|----------|-------|
| `GRAITH_SESSION_ID` | unique session ID |
| `GRAITH_SESSION_NAME` | human-readable session name |
| `GRAITH_AGENT_TYPE` | agent type (e.g. `claude`, `codex`) |
| `GRAITH_WORKTREE_PATH` | absolute path to the worktree |
| `GRAITH_REPO_PATH` | absolute path to the source repository (canonical) |
| `GRAITH_TMPDIR` | per-repo temporary directory (persists across sessions) |
| `TMPDIR` | set to `GRAITH_TMPDIR` so `mktemp` etc. use the per-repo tmp dir |

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
