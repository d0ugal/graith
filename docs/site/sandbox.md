# Sandbox

graith can wrap agent processes with [safehouse](https://github.com/nicholasgasior/safehouse), a macOS kernel-level sandbox built on `sandbox-exec`. This confines agents to a deny-by-default policy that restricts file access, network, and system calls.

## Why sandbox

AI coding agents often request broad permissions (e.g. `--dangerously-skip-permissions` for Claude, `--dangerously-bypass-approvals-and-sandbox` for Codex). Sandboxing lets you grant those agent-level permissions while confining the process at the OS level. The agent thinks it has full access; the kernel enforces boundaries.

## Setup

1. Install safehouse:

```bash
brew install eugene1g/tools/agent-safehouse
```

2. Verify:

```bash
gr doctor    # checks for safehouse on $PATH
```

3. Enable in config:

```toml
[sandbox]
enabled  = true
features = ["ssh", "process-control"]
read_dirs  = ["~/Code"]
write_dirs = []
```

## How it works

When `sandbox.enabled = true`, the daemon wraps the agent command with `safehouse wrap`. The resulting process runs inside a macOS `sandbox-exec` policy that:

- **Denies all file access by default**, then allows the session worktree (read-write) plus any paths in `read_dirs`/`write_dirs`
- **Strips the environment** (`/usr/bin/env -i`) and re-adds only what the agent needs
- **Gates capabilities** behind `features` (e.g. `ssh` grants `SSH_AUTH_SOCK` access, `process-control` allows signal sending)

## Config-only enforcement

Sandboxing is **config-only**. There are no CLI flags to enable or disable it. This prevents a sandboxed agent from spawning a child process that escapes the sandbox. The daemon resolves the merged sandbox config (global + per-agent), expands `~` paths to absolute, and passes them as safehouse options.

## Fail closed

If `sandbox.enabled = true` but safehouse is not installed, session creation is refused with an error. The system fails closed rather than silently running unsandboxed.

## Configuration

### Global sandbox

```toml
[sandbox]
enabled    = false            # wrap all agents with safehouse
command    = "safehouse"      # path to safehouse binary
features   = ["ssh"]          # safehouse feature gates to enable
read_dirs  = ["~/Code"]       # additional read-only paths
write_dirs = []               # additional read-write paths
```

### Per-agent overrides

Each agent can extend or disable the global sandbox config:

```toml
[agents.claude.sandbox]
enabled    = true             # enable even if global is disabled
features   = ["clipboard"]   # merged with global features
read_dirs  = ["~/.claude"]   # merged with global read_dirs
write_dirs = ["~/.claude"]   # merged with global write_dirs

[agents.codex.sandbox]
disabled = true               # force-disable for this agent
```

### Merge behavior

- `features`, `read_dirs`, and `write_dirs` are merged (global + agent, deduplicated)
- `command` is overridable per-agent (agent takes precedence)
- `disabled = true` on an agent overrides `enabled = true` on the global config
- `enabled = true` on an agent enables sandboxing even if the global config has `enabled = false`

## Path restrictions

`allowed_repo_paths` limits which directories the daemon accepts for `--repo` / `-C`. This is separate from the sandbox but complements it:

```toml
allowed_repo_paths = ["~/Code", "~/Work"]
```

When empty (the default), any repo path is accepted. When set, paths outside these prefixes are rejected before the sandbox is even invoked.

## Example: sandboxed Claude with full permissions

```toml
allowed_repo_paths = ["~/Code"]

[sandbox]
enabled  = true
features = ["ssh", "process-control"]
read_dirs  = ["~/Code"]
write_dirs = []

[agents.claude]
command     = "claude"
args        = ["--dangerously-skip-permissions", "--session-id", "{agent_session_id}"]
resume_args = ["--dangerously-skip-permissions", "--resume", "{agent_session_id}"]

[agents.claude.sandbox]
read_dirs  = ["~/.claude"]
write_dirs = ["~/.claude"]
```

The agent runs with `--dangerously-skip-permissions` (no interactive approval prompts), but the kernel sandbox restricts it to the worktree, `~/Code` (read-only), and `~/.claude` (read-write).

## Per-MCP-server sandbox

Individual MCP servers can override the sandbox setting:

```toml
[[mcp_servers]]
name    = "my-tools"
command = "/usr/local/bin/my-mcp-server"
sandbox = false    # run this MCP server outside the sandbox
```

## Limitations

- macOS only (uses `sandbox-exec`)
- Requires safehouse to be installed separately
- Network access control is coarse-grained (safehouse feature gates, not per-domain)
