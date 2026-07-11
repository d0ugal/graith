---
weight: 300
title: "Configuration"
description: "Configure graith via config.toml."
icon: "settings"
toc: true
draft: false
---

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
enabled     = true   # desktop notifications (status changes AND `gr notify`)
on_approval = true   # notify when a session needs approval
on_stopped  = false  # notify when a session stops
command     = ""     # custom notification command (optional)

# Proactive `gr notify` push notifications:
backend           = "macos"   # "macos" (osascript) or "command"; default "macos"
max_per_hour      = 12         # rolling-hour cap on low/normal pushes (high bypasses)
quiet_hours_start = "22:00"    # suppress low/normal pushes in this window (24h "HH:MM")
quiet_hours_end   = "07:00"    # window may wrap past midnight; high priority bypasses
```

When `command` is set, graith executes it via `sh -c` instead of using the system notification API. For status-change notifications the command receives `GRAITH_SESSION_NAME`, `GRAITH_STATUS`, and `GRAITH_MESSAGE`; for `gr notify` push notifications (`backend = "command"`) it receives `GRAITH_NOTIFY_TITLE`, `GRAITH_NOTIFY_MESSAGE`, and `GRAITH_NOTIFY_PRIORITY`.

### Proactive push notifications (`gr notify`)

The orchestrator (and triggers) can proactively get your attention — a morning
briefing, a CI failure, a review needed — rather than leaving it sitting silently
in an inbox:

```bash
gr notify "Morning briefing ready" --priority low
gr notify "CI failing on main after 3 retries" --priority high
```

Priority levels: `low`, `normal` (default), and `high`. `high` plays a sound and
**bypasses quiet hours and the rate limit**; `low`/`normal` are subject to both.
Only the orchestrator session and the human may send notifications — plain agent
sessions are rejected to prevent spam. Identical notifications within 30s are
coalesced. The `macos` backend uses `osascript`; other backends (ntfy, Pushover,
Slack) are planned follow-ups.

Triggers can fire a notification when their action completes:

```toml
[trigger.action]
type               = "session"
notify_on_complete = true
notify_message     = "Morning briefing ready"   # templated; optional
notify_priority    = "low"                        # low|normal|high; optional
```

## Approvals

```toml
[approvals]
backend  = ""        # who decides (see below); default "" = always prompt the human
timeout  = "10m"     # how long to wait for a human decision
auto_pop = false     # auto-open the approval overlay when a request is queued
command  = ""        # required for backend "command"/"external"; path override for "localmost"

[approvals.builtin]
config   = ""        # localmost-format config.json (backend "builtin")
```

The approval system integrates with agent hooks. When an agent requests approval (e.g. for a dangerous tool call), the `backend` decides who resolves it:

| `backend` | Who decides |
|-----------|-------------|
| `""` (default, equivalent to `"prompt"`) | Always prompt the human via the overlay |
| `"command"` / `"external"` | Delegate to `command` over graith's JSON contract (one JSON object on stdin — `{tool_name,tool_input,session_id,session_name}` — and one on stdout — `{decision:allow\|block\|deny\|defer,reason}`) |
| `"localmost"` | Delegate to the real localmost binary via its native protocol (`command` optionally overrides the binary path) |
| `"builtin"` | graith's built-in localmost-compatible engine — configured via `[approvals.builtin] config` (a localmost-format `config.json` path) **or** inline rules (`allow`, `deny`, `allowSafeXargs`, `askNoninteractive`) |

`mode` is deprecated. With no `backend` set, legacy `mode = "command"`, `mode = "external"`, and `mode = "localmost"` all resolve to `backend = "command"` (graith's JSON contract) for compatibility — `mode = "localmost"` does **not** select the native-protocol `backend = "localmost"`. Set `backend = "localmost"` explicitly to run the real localmost binary. See `ResolveBackend` in `internal/config/config.go` for the full resolution order.

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

## Delete retention

```toml
[delete]
retention = "24h"  # how long soft-deleted sessions are kept before purge
```

`gr delete` is a soft delete: it stops the agent and hides the session but keeps its worktree, branch, and state for this window, so `gr restore` can bring it back. A background loop hard-deletes sessions once their retention expires. Setting `retention = "0"` makes soft delete keep sessions **forever** (never auto-purged) — it does not turn `gr delete` into a destructive delete. `gr purge` is the only immediate, destructive verb; it bypasses the window.

## Git pull

```toml
[git_pull]
enabled  = false  # periodically fast-forward maintenance repos' default branches
interval = "1h"   # how often to pull (minimum: 1 minute)
```

When enabled, the daemon fetches and fast-forward merges the default branch of each repo registered with `git maintenance`. The first pull runs shortly after the daemon starts, then on the configured `interval` — so a daemon restart doesn't leave repos stale for a full interval before the next pull.

Sessions run in their own worktrees on feature branches, which share only the object store with the source checkout, so fast-forwarding the default branch cannot disturb them — those sessions do **not** block the pull. A repo is only skipped when a session works directly on the source checkout (in-place) or has the default branch itself checked out in its worktree. This keeps default branches up to date for future session creation without ever pulling into an active worktree.

## PR & CI awareness

When enabled, the daemon resolves each session's GitHub PR via `gh`, polls its CI checks and comments, and delivers a structured notification to the session's inbox on a meaningful change — auto-resuming a stopped agent so it reacts to review feedback and CI results without you relaying them.

```toml
[pr_watch]
enabled                  = false  # opt in to PR/CI awareness
notify_ci_failures       = true   # notify on a CI transition to failing
notify_pr_comments       = true   # notify on new PR conversation comments
notify_review_comments   = true   # notify on new inline review comments
poll_pending             = "1m"   # poll interval while checks are still running
debounce                 = "2m"   # minimum cooldown between notifications to a session
```

### Comment author-trust gate

PR comments are free text from arbitrary GitHub users. Because they reach the agent verbatim and can auto-resume a stopped session, an untrusted comment is a **prompt-injection vector**. graith gates comment notifications on the author's trust: a comment only notifies if its author is trusted.

```toml
[pr_watch]
# Trust individual authors by login (case-insensitive). This is the ONLY way to
# trust a bot or GitHub App — a bot's author_association is unreliable and never
# confers trust on a "<slug>[bot]" login.
comment_author_allowlist = ["github-actions[bot]", "coderabbitai[bot]"]

# Trust anyone whose GitHub author_association is in this set. Omit the key to
# keep the default tier below (CONTRIBUTOR is deliberately excluded — on a public
# repo it only means "merged a commit once", and bots can carry it).
trusted_author_associations = ["OWNER", "MEMBER", "COLLABORATOR"]

# Surface a not-yet-trusted author to the orchestrator once, as metadata only
# (login/type/association/PR + a `gh pr view` pointer) — NEVER the comment body,
# so the human can decide whether to allowlist them.
notify_untrusted_authors = true
```

An untrusted comment is **dropped** from notifications (the comment cursor still advances, so a later trusted comment isn't reported alongside the whole untrusted backlog). For the common case — an owner working on their own repos — every comment is from the owner, so behaviour is unchanged.

Two trust knobs, fail-closed:

- **`trusted_author_associations`** defaults to `OWNER`/`MEMBER`/`COLLABORATOR` when the key is **absent**. Setting it to an explicit empty list (`trusted_author_associations = []`) is honoured as **allowlist-only** mode — no association is trusted — and is *not* silently widened back to the default.
- **`comment_author_allowlist`** is the only way to trust a bot/App. Allowlisting a bot login trusts that workflow to not echo untrusted PR text back into its comments; fine for a repo's own first-party CI, a documented caveat on public repos.

## Triggers

Daemon-fired automation: a trigger is `(source) → (action)`. Define them as
`[[trigger]]` blocks (each with exactly one `[trigger.schedule]` or
`[trigger.watch]` source and one `[trigger.action]`), plus an optional
`[triggers]` table for daemon-wide settings.

```toml
[triggers]
max_concurrent = 4          # cap on concurrently-running trigger actions

# Schedule → command: run tests nightly and report to the orchestrator.
[[trigger]]
name = "nightly-tests"
[trigger.schedule]
cron = "0 3 * * *"          # 5-field cron / @daily; or: every = "15m"
[trigger.action]
type    = "command"
command = "go test ./..."
repo    = "~/Code/graith"
[trigger.action.deliver]
inbox = "orchestrator"

# Watch → session: keep a reviewer reacting to an implementer's changes.
[[trigger]]
name = "review-go"
[trigger.watch]
role  = "implementer"       # policy selector: repo or role, never a live session
paths = ["**/*.go"]
debounce = "30s"
[trigger.action]
type   = "session"
ensure = true               # message the owned reactor if it exists, else spawn
agent  = "claude"
prompt = "Review the changes since your last look; send feedback via gr msg."
```

Actions: `command` (sandboxed by default; `sandbox`/`sandbox_config` mirror
MCP-server config), `session`, `scenario`, `message`. Delivery routes to
`inbox`/`topic`/`store`. Policy: `catch_up` (default false), `overlap` (default
`skip`), `rate_limit` (default `5/30m`). See [Triggers](triggers.md) for the full
reference.

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

## Overlay

```toml
[overlay]
shortcut_keys = "1234567890"  # keys that jump straight to the Nth session in the picker
```

In the session picker (`ctrl+b w`), each of these keys jumps directly to the corresponding session — the 1st key selects session 1, the 2nd key session 2, and so on.

## Input

```toml
[input]
drag_arrow_keys      = false  # translate a left-click hold-and-drag into arrow-key presses
drag_arrow_threshold = 2      # cells of drag movement per emitted arrow-key press (values < 1 use the default)
```

`drag_arrow_keys` lets you press-and-hold the left mouse button and drag up/down/left/right to emit discrete arrow-key presses to the focused pane — handy on touch/mobile terminals. It is off by default because it repurposes left-drag, which terminals otherwise use for text selection. Mouse-wheel scrolling always passes through unchanged. It only takes effect when the focused app has SGR mouse reporting enabled (e.g. a TUI tracking the mouse); graith translates those reports, it does not enable mouse tracking itself.

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

## Remote access

An optional control listener that lets you reach the daemon from another device over your [Tailscale](https://tailscale.com) tailnet. **Disabled by default and fail-closed**: when enabled, an invalid `[remote]` block is a hard config-load error rather than a silent downgrade.

```toml
[remote]
enabled             = false          # expose the remote control listener over the tailnet
mode                = "tsnet"         # transport: "tsnet" (embedded Tailscale) or "interface" (bind an existing tailnet IP)
port                = 4823            # TCP port the listener binds
require_pairing     = true           # require per-device pairing for human-level rights
# hostname          = "graith"       # tsnet node name / MagicDNS label (tsnet mode)
# auth_key_file     = "~/.config/graith/tsnet.key"  # tsnet auth key path (tsnet mode)
# tags              = ["tag:graith"] # tsnet ACL tags applied to the node (tsnet mode)
# allow_tailnet_users = ["you@example.com"]  # WhoIs allowlist; "tag:"-prefixed entries opt tagged nodes in
# pair_request_rate = "5/min"        # anti-flood limit on pending pair requests ("<n>/<sec|min|hour>")
```

Access is gated in two layers: a WhoIs **allowlist** (`allow_tailnet_users` — who on the tailnet may connect at all) and per-device **pairing** (`require_pairing` — each device proves possession of a paired key before it gets human-level rights).

> **Warning:** `require_pairing = false` is **UNSAFE** — it trusts the tailnet identity alone with no per-device proof, so it is restricted to **read-only** access. Leave pairing on for any device that should control sessions.

The orchestrator can also be given extra filesystem access scoped to itself via `[orchestrator.sandbox]` (`read_dirs`/`write_dirs`), layered on top of the global and per-agent sandbox config. See [Authentication & remote access](auth.md) for the full authorization model, token lifecycle, and pairing flow.

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
write_files = ["~/.claude.json", "~/.claude.json.lock", "~/.claude.lock"]  # login file (read+write)
features   = ["clipboard"]
```

Features, directories, and files (`read_files`/`write_files`, for single files that can't be a directory grant without over-sharing — e.g. Claude's `~/.claude.json` login file) are merged with the global sandbox config. Setting `disabled = true` overrides `enabled = true` on the global config. See the [Sandbox](/docs/sandbox/#file-grants) page for file grants.

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
