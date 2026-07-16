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

```toml
[agents.claude]
command        = "claude"
args           = ["--session-id", "{agent_session_id}"]
resume_args    = ["--resume", "{agent_session_id}"]
fork_args      = ["--resume", "{fork_source_agent_session_id}", "--fork-session", "--session-id", "{agent_session_id}"]
env            = {}             # extra environment variables
idle_timeout   = ""             # auto-stop after idle (default: 1h when resume_args is set, 0 otherwise)
inject_prompt  = true           # inject agent_prompt into the session
prompt_injection = ""           # how to inject: append_system_prompt | cursor_rules | developer_instructions | none
validate_model = ""             # command to validate --model values
headless_capable = false        # agent can run in headless (stream-json) mode (experimental)
```

`headless_capable` marks whether an agent supports [headless mode]({{< relref "sessions.md#headless-sessions" >}}). Only Claude supports it in v1; a session can't be asked to go headless on an agent that isn't capable.

`inject_prompt` is the on/off switch for prompt injection; `prompt_injection` selects the *mechanism*. When `prompt_injection` is empty (the default), graith picks the mechanism from the agent name — `claude` → `append_system_prompt`, `cursor` → `cursor_rules`, `codex` → `developer_instructions`, and any other name → `none`. Set it explicitly to override that mapping or, most usefully, to give a [custom agent](#custom-agents) a mechanism it would otherwise not get. The values are:

| Value | Mechanism |
|-------|-----------|
| `append_system_prompt` | Pass the prompt via Claude's `--append-system-prompt` flag |
| `cursor_rules` | Write the prompt to `.cursor/rules/graith.mdc` in the worktree (Cursor) |
| `developer_instructions` | Pass the prompt as Codex's `-c developer_instructions=...` override |
| `none` | Do not inject a prompt |

An unknown value is rejected at config load. This applies to ordinary sessions and to the [orchestrator]({{< relref "/docs/orchestrator.md" >}}) alike, so a Codex, Cursor, or custom orchestrator agent gets the right mechanism instead of an unsupported Claude flag.

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

The daemon also passes `--add-dir <worktree>` for each included repo when launching the agent, so it can access the sibling worktrees without an extra prompt to grant them. This is applied only for agents whose CLI accepts the flag — `claude`, `codex`, and `cursor`; other agents rely on the environment variables above. The flag is re-added on resume and fork, so it survives restarts.

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
