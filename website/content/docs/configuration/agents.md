---
weight: 310
title: "Agents & repositories"
description: "Agent definitions, template variables, and per-repo settings."
icon: "smart_toy"
toc: true
draft: false
---

## Agent definitions

Each agent is configured under `[agents.<name>]`. Five agents ship by default: `claude`, `codex`, `opencode`, `cursor`, and `agy`.

```toml
[agents.claude]
command        = "claude"
args           = ["--session-id", "{agent_session_id}"]
resume_args    = ["--resume", "{agent_session_id}"]
fork_args      = ["--resume", "{fork_source_agent_session_id}", "--fork-session", "--session-id", "{agent_session_id}"]
non_interactive_args = []       # empty: keep the agent's native approval TUI (see below)
env            = {}             # extra environment variables
idle_timeout   = ""             # auto-stop after idle (default: 1h when resume_args is set, 0 otherwise)
inject_prompt  = true           # inject agent_prompt into the session
prompt_injection = ""           # how to inject: append_system_prompt | cursor_rules | developer_instructions | none
validate_model = ""             # command to validate --model values
headless_capable = false        # agent can run in headless (stream-json) mode (experimental)
add_dir_args   = ["--add-dir", "{dir}"]  # flag for granting an extra directory (see Includes)
headless_args  = []             # argv prefix prepended in headless mode (see below)
```

`headless_capable` marks whether an agent supports [headless mode]({{< relref "sessions.md#headless-sessions" >}}). Only Claude supports it in v1; you can't ask a session to go headless on an agent that isn't capable.

Every agent-specific flag graith appends is defined here — a custom agent can adopt (or drop) each pattern from config alone, without waiting on a graith release:

- **`add_dir_args`** — the flag template graith uses to grant the agent an extra directory (each [included repo](#includes)'s co-located worktree). It is expanded once per directory with `{dir}` bound to that path. Leave it unset for an agent whose CLI has no such flag; those agents rely on the `GRAITH_INCLUDE_*_PATH` environment variables instead.
- **`non_interactive_args`** — optional argv prepended on every create, resume, and fork. It is **empty by default** for every bundled agent, so each keeps its own approval TUI (and, for Codex, its own sandbox) out of the box; Graith treats time spent in that TUI as ordinary running state and never answers on your behalf. Set it to the agent's unattended flag(s) — e.g. `["--dangerously-skip-permissions"]` for Claude, `["--ask-for-approval", "never", "--sandbox", "danger-full-access"]` for Codex, `["--force"]` for Cursor, `["--auto"]` for OpenCode — to run without those prompts. Doing so disables the agent's native safeguards, so only enable it behind a boundary you control (Graith's `[sandbox]`, an external sandbox, or a VM). These flags are independent of `[sandbox]` and `[command_policy]`.
- **`headless_args`** — the control-channel argv prefix graith prepends when launching the agent in [headless mode]({{< relref "sessions.md#headless-sessions" >}}); the agent's own args follow it. Claude's default is `["-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose"]`.
- **`option_args`** — conditional flag groups appended on every launch. Each group is emitted only when its `when` template variable is set, so an unset option leaves the agent's own default untouched (see [Conditional option flags](#conditional-option-flags)).

`inject_prompt` is the on/off switch for prompt injection; `prompt_injection` selects the *mechanism*. When `prompt_injection` is empty (the default), graith picks the mechanism from the agent name — `claude` → `append_system_prompt`, `cursor` → `cursor_rules`, `codex` → `developer_instructions`, and any other name → `none`. Set it explicitly to override that mapping, or — most usefully — to give a [custom agent](#custom-agents) a mechanism it wouldn't otherwise get. The values are:

| Value | Mechanism |
|-------|-----------|
| `append_system_prompt` | Pass the prompt via Claude's `--append-system-prompt` flag |
| `cursor_rules` | Write the prompt to `.cursor/rules/graith.mdc` in the worktree (Cursor) |
| `developer_instructions` | Pass the prompt as Codex's `-c developer_instructions=...` override |
| `none` | Do not inject a prompt |

An unknown value is rejected at config load. Both `inject_prompt` and `prompt_injection` apply to ordinary sessions and the [orchestrator]({{< relref "/docs/orchestrator.md" >}}) alike: a Codex, Cursor, or custom orchestrator agent gets the right mechanism instead of an unsupported Claude flag, and `inject_prompt = false` opts the orchestrator out entirely — it launches with no injected role prompt and no Cursor rule file, just like an ordinary session.

### Template variables

These are substituted in `args`, `resume_args`, `fork_args`, `non_interactive_args`, and `headless_args`:

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

Two more variables are scoped to specific fields. `{dir}` is available only in `add_dir_args`, bound to each granted directory in turn. The Codex option values — `{profile}`, `{reasoning_effort}`, `{service_tier}`, and `{web_search}` (a boolean rendering as `true`/empty) — are available in `option_args`, alongside `{model}`.

### Conditional option flags

`option_args` moves per-session choices (Codex's `--model`, `--profile`, reasoning-effort, service-tier, and `--search`) into config, so a custom agent can define its own. Each group lists the argv to append and a `when` template variable that gates it — the group is emitted only when that variable resolves to a non-empty value (`true` for a boolean such as `web_search`). An empty `when` emits the group unconditionally. Non-interactive launch flags belong in `non_interactive_args`, not here.

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

This is why an unset option can't just be a `{model}` template inside `args`: an empty model would expand to a literal `--model ""`. The groups are appended after the base args on create, resume, and fork alike. A `when` naming an unknown template variable, or a group with no `args`, is rejected at config load.

### Per-agent sandbox

```toml
[agents.claude.sandbox]
read_dirs  = ["~/.claude"]
write_dirs = ["~/.claude"]
write_files = ["~/.claude.json", "~/.claude.json.lock", "~/.claude.lock"]  # login file (read+write)
features   = ["clipboard"]
```

Features, directories, and files (`read_files`/`write_files`, for single files that can't be a directory grant without over-sharing — e.g. Claude's `~/.claude.json` login file) merge with the global sandbox config. Per-agent settings can choose a backend, add grants, or explicitly disable Graith's sandbox for that agent. Disabled sessions start with a warning; `gr doctor` reports the missing Graith boundary. See the [Sandbox]({{< relref "/docs/sandbox/how-it-works.md#file-grants" >}}) page for file grants.

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

Use with `gr new my-task --agent my-agent`. Since a custom agent's name matches none of the built-ins, set `prompt_injection` if you want it to receive `agent_prompt` — otherwise the name-based default is `none` and no prompt is injected.

### Agent-owned native integrations

Graith does not configure, inject, supervise, proxy, or inspect MCP servers.
Delete former Graith keys (`[[mcp_servers]]`,
`[agents.<name>.mcp_servers.*]`, and `limits.mcp_log_read_bytes`) before
upgrading; startup and reload reject them with migration guidance so obsolete
security or lifecycle settings cannot become inert accidentally.

An agent runtime may still support MCP through its own configuration. Keep that
configuration in the runtime's native files or flags, outside Graith's schema;
Graith preserves ordinary configured agent arguments but does not interpret or
secure native MCP integrations. For browser automation in a sandboxed session,
use [`agent-browser`]({{< relref "/docs/sandbox/configuration.md#browser-automation" >}}).

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

On launch, the daemon also grants each included worktree to the agent via its [`add_dir_args`](#agent-definitions) flag, so it can reach the sibling worktrees without an extra grant prompt. This applies only to agents that define `add_dir_args` — `claude`, `codex`, and `cursor` ship with `["--add-dir", "{dir}"]`; other agents rely on the environment variables above. The flags are re-added on resume and fork, so they survive restarts.

#### Config path rewriting

Relative references between repos (`../shared-lib`) resolve correctly because the worktrees are arranged as siblings. Absolute references (`~/Code/shared-lib` or `/Users/you/Code/shared-lib`) don't — they still point at your main checkout, not the session's worktree.

To help, after creating the worktrees (on both create and fork) the daemon rewrites known orchestrator config files in each worktree, substituting each source repo path with its session worktree path:

- `.env.local`
- `docker-compose.override.yml`

Both the resolved absolute path and its `~/`-relative form are matched, at path boundaries only — so `~/Code/grafana` is rewritten while a sibling like `~/Code/grafana-enterprise` (or `grafana.bak`, `grafana@next`) is left untouched. A path that continues into the repo (`~/Code/grafana/conf`) keeps its suffix, and when one included repo is nested under another the more specific path wins.

Only files present in the worktree are touched; a gitignored file (absent from a fresh checkout) is skipped, and the `GRAITH_INCLUDE_*_PATH` env vars remain the mitigation there. Symlinks are never read or replaced — a config symlink could otherwise pull an external file's contents into the worktree. When a *tracked* file is rewritten it's marked `--skip-worktree`, so the session-specific path isn't reported as a change or committed by accident. Rewriting is best-effort — a read or write failure is logged, never fatal to session creation.

Validation rules:
- A repo cannot include itself
- Included repo basenames must be unique
- Environment variable names derived from basenames must not collide

## Default agent configurations

Every built-in agent also sets the shared lifecycle and prompt-delivery
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
non_interactive_args = []   # keep Claude's approval TUI; set ["--dangerously-skip-permissions"] to run unattended
args         = ["--session-id", "{agent_session_id}"]
resume_args  = ["--resume", "{agent_session_id}"]
fork_args    = ["--resume", "{fork_source_agent_session_id}", "--fork-session", "--session-id", "{agent_session_id}"]
add_dir_args = ["--add-dir", "{dir}"]
headless_args = ["-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose"]
```

### Codex

```toml
[agents.codex]
command      = "codex"
# Empty by default: Codex keeps its own approvals AND its own sandbox. Setting
# these flags disables both, so only do it behind a guaranteed outer boundary.
non_interactive_args = []   # e.g. ["--ask-for-approval", "never", "--sandbox", "danger-full-access"] to run unattended
args         = []
resume_args  = ["resume", "{agent_session_id}"]
fork_args    = ["fork", "{fork_source_agent_session_id}"]
add_dir_args = ["--add-dir", "{dir}"]

# The model and typed Codex options (--model, --profile, reasoning effort,
# service tier, --search) are emitted via option_args groups
# gated on the matching template variable. See "Conditional option flags" above.
[[agents.codex.option_args]]
when = "model"
args = ["--model", "{model}"]
# … profile, reasoning_effort, service_tier, web_search …
```

### OpenCode

```toml
[agents.opencode]
command     = "opencode"
non_interactive_args = []   # keep OpenCode's prompts; set ["--auto"] to run unattended
args        = []
resume_args = ["--session", "{agent_session_id}"]
```

OpenCode's TUI keeps its native prompts by default. Set
`non_interactive_args = ["--auto"]` to approve requests that would otherwise
ask; explicit OpenCode deny rules still apply.

### Cursor

```toml
[agents.cursor]
command        = "agent"
non_interactive_args = []   # keep Cursor's prompts; set ["--force"] to run unattended
args           = []
resume_args    = ["resume"]
validate_model = "agent --list-models"
add_dir_args   = ["--add-dir", "{dir}"]
```

When lifecycle hooks are enabled, graith publishes `.cursor/hooks.json` in the
worktree and records an ownership marker in its per-session data. Concurrent
sessions that intentionally share a worktree also share this file when their
generated hook definitions are identical. The file remains until the last
owning session is deleted. A session that requires a different definition
fails before launch while another owner remains.

An existing file graith doesn't own fails the session launch rather than being
overwritten; move it aside before retrying. Cleanup removes only the unchanged
file object graith published, so a file you edit or replace is preserved. This
protection applies equally to ordinary and `--in-place` sessions and survives a
daemon restart.

### Agy

```toml
[agents.agy]
command     = "agy"
non_interactive_args = []   # keep Agy's prompts; set ["--dangerously-skip-permissions"] to run unattended
args        = []
resume_args = ["--conversation", "{agent_session_id}"]
```
