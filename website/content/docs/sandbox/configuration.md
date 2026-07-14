---
weight: 1120
title: "Sandbox configuration"
description: "Global and per-agent sandbox config, features, and network egress."
icon: "tune"
toc: true
draft: false
---

## Global sandbox

```toml
[sandbox]
enabled     = false           # wrap all agents in the sandbox
backend     = "nono"          # REQUIRED when enabled: "safehouse" | "nono"
command     = "nono"          # path/name of the backend binary (default: backend name)
features    = ["ssh"]         # feature gates (see caveats below)
read_dirs   = ["~/Code"]      # additional read-only paths (directories)
write_dirs  = []              # additional read-write paths (directories)
read_files  = []              # additional read-only single files
write_files = []              # additional read-write single files
signal_mode = "isolated"      # nono only: "isolated" | "allow_same_sandbox" | "allow_all"

[sandbox.network]             # nono only; needs Landlock ABI v4 (kernel 6.7+)
block = true                  # deny all outbound network
# allow_domains = ["github.com", "https://api.anthropic.com/**"]  # OR a proxy allowlist
```

## Per-agent overrides

```toml
[agents.claude.sandbox]
enabled    = true             # enable even if global is disabled
backend    = "nono"           # override the backend for this agent
features   = ["ssh"]          # merged with global features
read_dirs  = ["~/.claude"]    # merged with global read_dirs
write_dirs = ["~/.claude"]    # merged with global write_dirs
write_files = ["~/.claude.json", "~/.claude.json.lock", "~/.claude.lock"]  # login file (read+write)

[agents.codex.sandbox]
disabled = true               # force-disable for this agent
```

## Merge behavior

- `features`, `read_dirs`, `write_dirs`, `read_files`, and `write_files` are merged (global + agent, deduplicated)
- `backend`, `command`, and `signal_mode` are overridable per-agent (agent takes precedence)
- `network` is overridable per-agent — an agent's `[agents.*.sandbox.network]` replaces the global policy wholesale (not merged element-wise)
- `disabled = true` on an agent overrides `enabled = true` on the global config
- `enabled = true` on an agent enables sandboxing even if the global config has `enabled = false`

## Feature gate caveats

`features` map differently onto each backend:

| Feature | safehouse | nono |
|---------|-----------|------|
| `ssh` | grants `SSH_AUTH_SOCK` access | grants the `$SSH_AUTH_SOCK` Unix socket (agent socket only; raw `~/.ssh` key access is not granted in v1). Warns if `SSH_AUTH_SOCK` is unset. |
| `process-control` | allows signal sending | **no-op on its own** — nono's default already permits same-sandbox signals. Set `signal_mode = "isolated"` (below) to make it actually gate signalling under nono. Documented cross-backend divergence. |
| anything else (e.g. `clipboard`) | passed to safehouse | **not mapped** — nono has no equivalent; graith warns and ignores it rather than silently dropping it. |

### `signal_mode` (nono only)

`signal_mode` maps to nono's `security.signal_mode` and controls whether the
sandboxed process may signal other processes: `isolated` (no signalling outside
the sandbox), `allow_same_sandbox` (nono's default), or `allow_all`. Setting it
to `isolated` is what makes the `process-control` feature meaningful under nono.
Leaving it unset inherits nono's base default. `safehouse` ignores it.

### Network egress (nono only)

By default agents keep unrestricted outbound network. `[sandbox.network]` adds an
egress policy, mapped onto nono's profile `network` section:

- `block = true` → `network.block`: deny all outbound access.
- `allow_domains = [...]` → `network.allow_domain`: nono runs its L7 filtering
  proxy and only these hosts / URL globs are reachable.

Network filtering requires **Landlock ABI v4 (Linux kernel 6.7+)**. A network
policy on an older kernel fails closed (see the [fail-closed table]({{< relref "how-it-works.md#fail-closed" >}})). `safehouse`
has no network primitive, so a network policy with `backend = "safehouse"` also
fails closed — use `nono` for egress control.

## Path restrictions

`allowed_repo_paths` limits which directories the daemon accepts for `--repo` /
`-C`. This is separate from the sandbox but complements it:

```toml
allowed_repo_paths = ["~/Code", "~/Work"]
```

When empty (the default), any repo path is accepted. When set, paths outside
these prefixes are rejected before the sandbox is even invoked.

## Example: sandboxed Claude with full permissions (Linux, nono)

```toml
allowed_repo_paths = ["~/Code"]

[sandbox]
enabled  = true
backend  = "nono"
features = ["ssh"]
read_dirs  = ["~/Code"]
write_dirs = []

[agents.claude]
command     = "claude"
args        = ["--dangerously-skip-permissions", "--session-id", "{agent_session_id}"]
resume_args = ["--dangerously-skip-permissions", "--resume", "{agent_session_id}"]

[agents.claude.sandbox]
read_dirs   = ["~/.claude"]
write_dirs  = ["~/.claude"]
write_files = ["~/.claude.json", "~/.claude.json.lock", "~/.claude.lock"]
```

The agent runs with `--dangerously-skip-permissions` (no interactive approval
prompts), but the kernel sandbox restricts it to the worktree, `~/Code`
(read-only), and `~/.claude` (read-write), and denies credentials/shell history
via nono's default deny groups. The `write_files` grant is what keeps Claude
**logged in**: its OAuth login lives in `~/.claude.json` (a file next to the
`~/.claude` directory), which the `deny_credentials` group would otherwise
block — see [File grants]({{< relref "how-it-works.md#file-grants" >}}).

## Per-MCP-server sandbox

Individual MCP servers can override the sandbox setting. When the global sandbox
is enabled, MCP servers are wrapped with the same backend and also fail closed
if it can't enforce:

```toml
[[mcp_servers]]
name    = "my-tools"
command = "/usr/local/bin/my-mcp-server"
sandbox = false    # run this MCP server outside the sandbox
```

## Network egress

By default neither backend restricts outbound network. Under `nono` you can add
an egress policy with `[sandbox.network]` (`block` / `allow_domains`) — see the
[Network egress](#network-egress-nono-only) section above. Without a policy, a
compromised agent with network access can still exfiltrate data it can read, so
set an egress policy for untrusted workloads. `safehouse` cannot filter egress.
Credential injection (nono's `--credential` proxy) is a later phase.
