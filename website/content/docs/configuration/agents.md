---
weight: 310
title: "Agents & repositories"
description: "Agent definitions, template variables, MCP servers, and per-repo settings."
icon: "smart_toy"
toc: true
draft: false
---

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

The macOS and iOS New Session forms fetch this effective catalog and
`default_agent` from the selected host. Catalogs are host-scoped: switching hosts
can change both the available names and the default. On macOS, a locally saved
preference overrides the daemon default only when that host still offers the
agent; a fresh profile and a removed preference follow the daemon default. The
saved preference is global and read-only when inspected: opening Settings against
a host that does not offer the agent shows that host's daemon default without
erasing a choice that is still valid on another host. Only picking an agent (or
"Daemon default") in Settings changes the stored preference. If a catalog is
still loading or unavailable (including from an older daemon), the apps do not
invent built-in names. They leave the agent unspecified so the daemon resolves
its own default.

```toml
[agents.claude]
command        = "claude"
args           = ["--session-id", "{agent_session_id}"]
resume_args    = ["--resume", "{agent_session_id}"]
fork_args      = ["--resume", "{fork_source_agent_session_id}", "--fork-session", "--session-id", "{agent_session_id}"]
env            = {}             # extra environment variables
idle_timeout   = ""             # auto-stop after idle (default: 1h when resume_args is set, 0 otherwise)
inject_prompt  = true           # inject agent_prompt into the session
prompt_injection = "append_system_prompt" # built-ins set their mechanism explicitly; custom agents may choose one
validate_model = ""             # command to validate --model values
headless_capable = false        # agent can run in headless (stream-json) mode (experimental)
add_dir_args   = ["--add-dir", "{dir}"]  # flag for granting an extra directory (see Includes)
headless_args  = []             # argv prefix prepended in headless mode (see below)
prompt_injection_args = ["--append-system-prompt", "{prompt}"]  # how the prompt is delivered (see below)
empty_id_resume_args  = []      # resume fallback when no native id was captured (see below)
```

`headless_capable` marks whether an agent supports [headless mode]({{< relref "sessions.md#headless-sessions" >}}). Built-in Claude is enabled; a custom wrapper may opt in only when it speaks the same pinned stream-json control contract. A session can't be asked to go headless on an agent that isn't capable.

Every agent-specific flag graith appends is defined here, so a custom agent can adopt (or drop) each pattern from config alone rather than waiting on a graith release:

- **`add_dir_args`** — the flag template graith uses to grant the agent an extra directory (each [included repo](#includes)'s co-located worktree). It is expanded once per directory with `{dir}` bound to that path. Leave it unset for an agent whose CLI has no such flag; those agents rely on the `GRAITH_INCLUDE_*_PATH` environment variables instead.
- **`headless_args`** — the control-channel argv prefix graith prepends when launching the agent in [headless mode]({{< relref "sessions.md#headless-sessions" >}}); the agent's own args follow it. Claude's default is `["-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose", "--permission-prompt-tool", "stdio"]`. It may be empty for a `headless_capable` wrapper whose base command/args already speak the pinned control-channel protocol.
- **`option_args`** — conditional flag groups appended on every launch. Each group is emitted only when its `when` template variable is set, so an unset option leaves the agent's own default untouched (see [Conditional option flags](#conditional-option-flags)).
- **`prompt_injection_args`** — the argv template graith uses to deliver the operating prompt, with `{prompt}` bound to it, for the `append_system_prompt` and `developer_instructions` mechanisms (see below). Unset falls back to the built-in spelling for the selected mechanism.
- **`empty_id_resume_args`** — the fallback resume argv used when `resume_args` templates `{agent_session_id}` but graith never captured a native session id. Codex's default is `["resume", "--last"]` (cwd-scoped, best-effort); unset means start fresh.
- **`[agents.<name>.hooks]`** and **`[agents.<name>.mcp]`** — how graith wires its generated lifecycle hooks and daemon-managed MCP servers into the launch (see [Hook and MCP adapters](#hook-and-mcp-adapters)).

Dynamic values — the generated settings/MCP files, the encoded prompt, the Codex hook TOML — are still built by graith; only the capability declaration and argv spelling live in config, and the security checks (e.g. skipping an MCP server whose name isn't a valid Codex config key) stay in graith.

`inject_prompt` is the on/off switch for the generic top-level `agent_prompt`; `prompt_injection` selects the *delivery mechanism*. The separate orchestrator role prompt is always delivered through that mechanism when the agent is used as the orchestrator, even when `inject_prompt = false`. The embedded built-ins set the mechanism explicitly (`claude` → `append_system_prompt`, `cursor` → `cursor_rules`, `codex` → `developer_instructions`, OpenCode/Agy → `none`), so `gr config show` reports the effective adapter. An empty mechanism retains name-based compatibility for partial overrides; a custom agent with an empty value resolves to `none`. Set it explicitly to give a [custom agent](#custom-agents) a supported mechanism. The values are:

| Value | Mechanism |
|-------|-----------|
| `append_system_prompt` | Pass the prompt via Claude's `--append-system-prompt` flag |
| `cursor_rules` | Write the prompt to `.cursor/rules/graith.mdc` in the worktree (Cursor) |
| `developer_instructions` | Pass the prompt as Codex's `-c developer_instructions=...` override |
| `none` | Do not inject a prompt |

An unknown value is rejected at config load. This applies to ordinary sessions and to the [orchestrator]({{< relref "/docs/orchestrator.md" >}}) alike, so a Codex, Cursor, or custom orchestrator agent gets the right mechanism instead of an unsupported Claude flag.

### Template variables

These are substituted in `args`, `resume_args`, `fork_args`, `headless_args`,
and `empty_id_resume_args`:

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

Two more variable groups are scoped to specific fields. `{dir}` is available only in `add_dir_args`, bound to each granted directory in turn; that field also receives the session variables above. The option values — `{profile}`, `{reasoning_effort}`, `{service_tier}`, `{approval_policy}`, and `{web_search}` (a boolean rendering as `true`/empty) — are available only in `option_args`, alongside the session variables above (except `{dir}`).

The adapter templates each bind their own values: `{prompt}` in `prompt_injection_args`; `{path}` in `hooks.settings_args` and `mcp.config_args`; `{hook_event}`/`{hook_value}` in `hooks.event_args`; and `{mcp_name}`/`{mcp_command}`/`{mcp_args}` in `mcp.server_args`. Every launch-template field is checked against its context at config load, including `option_args[].args`, `add_dir_args`, and `headless_args`; an unsupported placeholder prevents the daemon from starting or reloading that config rather than failing only when the conditional path launches.

### Conditional option flags

`option_args` moves the per-session flags that used to be hard-coded (Codex's `--model`, `--profile`, reasoning-effort, service-tier, `--search`, and `--ask-for-approval`) into config, so a custom agent can define its own. Each group lists the argv to append and a `when` template variable that gates it — the group is emitted only when that variable resolves to a non-empty value (`true` for a boolean such as `web_search`). An empty `when` emits the group unconditionally.

```toml
[[agents.codex.option_args]]
when = "model"                     # emit only when a model is set
args = ["--model", "{model}"]

[[agents.codex.option_args]]
when = "reasoning_effort"          # Codex has no flag for this, so ride -c
args = ["-c", "model_reasoning_effort={reasoning_effort}"]

[[agents.codex.option_args]]
when = "web_search"                # boolean: emitted when true
args = ["--search"]
```

This is why an unset option can't just be a `{model}` template inside `args`: an empty model would expand to a literal `--model ""`. The groups are appended after the base args on create, resume, and fork alike. A `when` that names an unknown template variable, or a group with no `args`, is rejected at config load.

### Hook and MCP adapters

graith injects its lifecycle hooks (status reporting, inbox checks, approval bridging) and daemon-managed MCP servers by generating an artifact and wiring it into the launch. The `mechanism` selects the strategy graith uses to build that artifact; the argv templates carry the flag spelling. Both are per-agent so a custom agent can adopt a supported mechanism from config alone, and a built-in agent's exact flags are visible rather than hidden in code.

```toml
# Claude: a settings file passed with --settings, and an MCP config file.
[agents.claude.hooks]
mechanism     = "claude_settings"
settings_args = ["--settings", "{path}"]

[agents.claude.mcp]
mechanism   = "claude_config"
config_args = ["--mcp-config", "{path}"]

# Codex: hooks as repeatable -c overrides plus a trust-bypass flag, and MCP as
# per-server -c overrides.
[agents.codex.hooks]
mechanism  = "codex_config"
event_args = ["-c", "hooks.{hook_event}={hook_value}"]
trust_args = ["--dangerously-bypass-hook-trust"]

[agents.codex.mcp]
mechanism   = "codex_config"
server_args = ["-c", "mcp_servers.{mcp_name}.command={mcp_command}", "-c", "mcp_servers.{mcp_name}.args={mcp_args}"]

# Cursor: reads hooks from a .cursor/hooks.json graith writes in the worktree,
# so there is no launch flag — the mechanism only selects that builder.
[agents.cursor.hooks]
mechanism = "cursor_project"
```

The hook mechanisms are `claude_settings` (write a settings JSON, pass `settings_args` with `{path}` bound), `codex_config` (emit `event_args` once per hook event with `{hook_event}`/`{hook_value}` bound, then `trust_args`), and `cursor_project` (write `.cursor/hooks.json`, no args). The MCP mechanisms are `claude_config` (write an MCP config JSON, pass `config_args` with `{path}` bound) and `codex_config` (emit `server_args` once per server with `{mcp_name}`, `{mcp_command}`, and `{mcp_args}` bound). An agent with no `[agents.<name>.hooks]`/`[agents.<name>.mcp]` block gets no hooks/MCP wiring. An unknown mechanism is rejected at config load.

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

Features, directories, and files (`read_files`/`write_files`, for single files that can't be a directory grant without over-sharing — e.g. Claude's `~/.claude.json` login file) are merged with the global sandbox config. Setting `disabled = true` overrides `enabled = true` on the global config. See the [Sandbox]({{< relref "/docs/sandbox/how-it-works.md#file-grants" >}}) page for file grants.

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
prompt_injection = "append_system_prompt"  # how to inject agent_prompt (else the prompt is skipped)

[agents.my-agent.sandbox]
read_dirs  = ["~/.my-agent"]
write_dirs = ["~/.my-agent"]
```

Use with `gr new my-task --agent my-agent`. Because a custom agent's name matches none of the built-ins, set `prompt_injection` if you want it to receive `agent_prompt` — otherwise the name-based default is `none` and no prompt is injected.

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

The daemon also grants each included worktree to the agent via its [`add_dir_args`](#agent-definitions) flag when launching, so it can access the sibling worktrees without an extra prompt to grant them. This is applied only for agents that define `add_dir_args` — `claude`, `codex`, and `cursor` ship with `["--add-dir", "{dir}"]`; other agents rely on the environment variables above. The flags are re-added on resume and fork, so they survive restarts.

#### Config path rewriting

Relative references between repos (`../shared-lib`) resolve correctly because the worktrees are arranged as siblings. Absolute references (`~/Code/shared-lib` or `/Users/you/Code/shared-lib`) do not — they still point at your main checkout, not the session's worktree.

To help, after creating the worktrees (on both create and fork) the daemon rewrites known orchestrator config files in each worktree, substituting each source repo path with its session worktree path:

- `.env.local`
- `docker-compose.override.yml`

Both the resolved absolute path and its `~/`-relative form are matched, at path boundaries only — so `~/Code/grafana` is rewritten while a sibling such as `~/Code/grafana-enterprise` (or `grafana.bak`, `grafana@next`) is left untouched. A path that continues into the repo (`~/Code/grafana/conf`) keeps its suffix, and when one included repo is nested under another the more specific path wins.

Only files present in the worktree are touched; a file that is gitignored (and so absent from a fresh checkout) is skipped, and the `GRAITH_INCLUDE_*_PATH` env vars remain the mitigation for those cases. Symlinks are never read or replaced (a config symlink could otherwise pull an external file's contents into the worktree). When a *tracked* file is rewritten it is marked `--skip-worktree` so the session-specific path is not reported as a change or committed by accident. Rewriting is best-effort — a read or write failure is logged, never fatal to session creation.

Validation rules:
- A repo cannot include itself
- Included repo basenames must be unique
- Environment variable names derived from basenames must not collide

## Default agent configurations

Every built-in agent also sets the shared lifecycle and prompt-delivery policy
defaults explicitly, so they show up in `gr config show`: `idle_timeout = "1h"`,
`inject_prompt = true`, and `pre_trust_workspace = true`. Each also sets
`prompt_injection` to its native mechanism — `append_system_prompt` (Claude),
`developer_instructions` (Codex), `cursor_rules` (Cursor), and `none` (OpenCode,
Agy). The blocks below omit these shared keys and show only the per-agent
command, args, and resume/fork settings.

### Claude

```toml
[agents.claude]
command      = "claude"
args         = ["--session-id", "{agent_session_id}"]
resume_args  = ["--resume", "{agent_session_id}"]
fork_args    = ["--resume", "{fork_source_agent_session_id}", "--fork-session", "--session-id", "{agent_session_id}"]
add_dir_args = ["--add-dir", "{dir}"]
headless_args = ["-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose", "--permission-prompt-tool", "stdio"]
```

### Codex

```toml
[agents.codex]
command      = "codex"
args         = []
resume_args  = ["resume", "{agent_session_id}"]
fork_args    = ["fork", "{fork_source_agent_session_id}"]
add_dir_args = ["--add-dir", "{dir}"]

# The model and typed Codex options (--model, --profile, reasoning effort,
# service tier, --search, --ask-for-approval) are emitted via option_args groups
# gated on the matching template variable. See "Conditional option flags" above.
[[agents.codex.option_args]]
when = "model"
args = ["--model", "{model}"]
# … profile, reasoning_effort, service_tier, web_search, approval_policy …
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
add_dir_args   = ["--add-dir", "{dir}"]
```

### Agy

```toml
[agents.agy]
command     = "agy"
args        = []
resume_args = ["--conversation", "{agent_session_id}"]
```
