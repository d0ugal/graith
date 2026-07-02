---
weight: 1100
title: "Sandbox"
description: "Run agents in an isolated sandbox."
icon: "shield"
toc: true
draft: false
---

graith can wrap agent processes in a deny-by-default OS sandbox that restricts
file access, environment, and — depending on backend — the network. There are
two backends:

| Backend | Platforms | Primitive |
|---------|-----------|-----------|
| `safehouse` | macOS only | `sandbox-exec` / Seatbelt (via [safehouse](https://github.com/eugene1g/agent-safehouse)) |
| `nono` | **Linux + macOS** | [nono](https://github.com/nolabs-ai/nono): Landlock LSM + seccomp on Linux, Seatbelt on macOS |

## Why sandbox

AI coding agents often request broad permissions (e.g.
`--dangerously-skip-permissions` for Claude,
`--dangerously-bypass-approvals-and-sandbox` for Codex). Sandboxing lets you
grant those agent-level permissions while confining the process at the OS level.
The agent thinks it has full access; the kernel enforces boundaries. This also
isolates sessions from each other — without a sandbox, one agent can read
graith's `state.json` and impersonate another session.

## Choosing a backend

The `backend` field is **required** when the sandbox is enabled — there is no
default. If you enable the sandbox without choosing a backend, session creation
fails closed with an actionable error. Pick:

- `backend = "safehouse"` on macOS if you already use safehouse.
- `backend = "nono"` on Linux (the only cross-platform option) or on macOS.

```toml
[sandbox]
enabled = true
backend = "nono"          # or "safehouse" (macOS only)
read_dirs  = ["~/Code"]
write_dirs = []
```

> **Migration (pre-1.0 breaking change).** Earlier versions defaulted to
> safehouse implicitly. `backend` is now required when `sandbox.enabled = true`.
> **To keep your current behaviour, add `backend = "safehouse"` to your
> `[sandbox]` block.** `gr doctor` flags a missing backend.

## Setup

### safehouse (macOS)

```bash
brew install eugene1g/safehouse/agent-safehouse
gr doctor            # checks safehouse is on $PATH
```

### nono (Linux / macOS)

```bash
# Homebrew (macOS or Linuxbrew)
brew install nono
# or the install script
curl -fsSL https://nono.sh/install.sh | sh

gr doctor            # checks the nono binary, its version, and Landlock support
```

nono needs Linux kernel **5.13+** for Landlock filesystem enforcement (its
practical floor is **5.14+**, which it uses for the seccomp supervisor-notify
layer); network filtering, when graith grows it, needs 6.7+. On macOS, nono uses
Seatbelt, which is always present. graith requires a minimum nono version and
refuses to run below it (see `gr doctor`).

Then enable in config with a backend, as above.

## How it works

When `sandbox.enabled = true`, the daemon resolves the merged sandbox policy
(global + per-agent), expands `~` and globs to absolute paths, and wraps the
agent command with the selected backend before spawning it under the PTY.

**safehouse** runs `safehouse wrap ... -- <agent>` (macOS `sandbox-exec`):
denies file access by default, allows the worktree (read-write) plus
`read_dirs`/`write_dirs`, strips the environment to an allowlist, and gates
`features`.

**nono** generates a per-session JSON profile and runs
`nono run --profile <file> -- <agent>`. The profile:

- `extends: "default"` — inherits nono's audited deny groups (`deny_credentials`,
  `deny_shell_history`, `deny_shell_configs`, browser/keychain denies) and base
  system-read paths, so credentials and shell history are denied out of the box.
- worktree → `filesystem.allow` (read+write) + `workdir: readwrite`
- `write_dirs` → `filesystem.allow` (read+write). graith deliberately does **not**
  use nono's `filesystem.write`, which is *write-only* (no read-back or delete)
  and would break agents that read files they just wrote.
- `read_dirs` → `filesystem.read` (read-only)
- the env allowlist → `environment.allow_vars`, including `PATH`, `HOME`, and
  graith's injected `GRAITH_*` vars plus your configured keys. When the allowlist
  is non-empty nono scrubs every other variable, so host secrets don't leak into
  the agent (this mirrors safehouse's env allowlist).
- the **agent binary's directory** → `filesystem.read`. nono does not auto-grant
  the launched command's location (only system paths like `/usr/bin`), so graith
  resolves the agent command via `$PATH` and grants read on its directory.
- feature `ssh` → `filesystem.unix_socket` for `$SSH_AUTH_SOCK` (agent socket)

The profile is written under graith's runtime dir (readable inside the sandbox)
and lives for the session's lifetime, including resume.

### The `/tmp` default-writable caveat

nono's built-in `system_write_linux` group makes `/tmp`, `$TMPDIR`,
`/dev/null`, and `/proc/self/fd` writable by default. That means a `read_dirs`
entry located under `/tmp` or `$TMPDIR` would be **silently writable**,
breaking the read-only guarantee. graith detects read-only paths under those
prefixes and adds an explicit `filesystem.deny` for them (and warns), so a
read-only grant stays read-only. Paths that are meant to be writable (the
worktree, `write_dirs`) are exempt. graith's own data dir defaults to
`~/.local/share/graith` (not `/tmp`), so this only bites custom configs that
point policy paths at `/tmp`.

## Config-only enforcement

Sandboxing is **config-only**. There are no CLI flags to enable or disable it.
This prevents a sandboxed agent from spawning a child process that escapes the
sandbox — Landlock/Seatbelt restrictions are inherited by all descendants.

## Fail closed

If the sandbox is enabled but cannot be enforced, session creation is refused —
graith never silently runs unsandboxed. The rules:

| Condition | Result |
|-----------|--------|
| `backend` unset | **Hard error** — choose `safehouse` or `nono`. |
| Backend binary not on `$PATH` | **Hard error** (with an install hint). |
| nono version below the required minimum | **Hard error** (profile schema may not match). |
| Linux kernel too old for Landlock (`NotEnforced`) | **Hard error** — a warning here would be a fail-open regression. |
| Landlock present but no network-filtering ABI (`PartiallyEnforced`) | **Runs** — filesystem confinement holds (v1 emits no network policy). `gr doctor` notes the degraded state. |
| safehouse selected on non-macOS | **Hard error.** |

## `gr doctor` checks

`gr doctor` reports, for the configured backend:

- **safehouse:** macOS + binary on `$PATH`.
- **nono:** the `nono` binary on `$PATH`, its version against graith's minimum,
  and the Landlock enforcement state on Linux (`FullyEnforced` /
  `PartiallyEnforced` → degraded / `NotEnforced` → fail).
- A **fail** if the sandbox is enabled with no backend selected.
- Existence of every configured `read_dirs`/`write_dirs` path.

## Debugging denials with `gr sandbox why`

When an agent hits a confusing "permission denied", `gr sandbox why` explains
whether a given access *would* be allowed or denied under your configured
policy — without launching an agent or changing anything. It builds the exact
nono profile graith would generate from your config and asks nono's policy
oracle (`nono why`), then renders the decision.

```bash
# Would the agent be able to read your SSH key? (no — deny_credentials)
gr sandbox why --path ~/.ssh/id_rsa --op read

# Is a write into a read-only read_dir allowed? (no — read grant only)
gr sandbox why --path ~/Code/shared --op write

# Would an outbound connection to github.com:443 be allowed?
gr sandbox why --host github.com --port 443

# Resolve the *merged* policy for a specific agent (global + per-agent)
gr sandbox why --agent codex --path /etc/hosts --op read
```

`--op` is one of `read`, `write`, or `readwrite`. Add `--json` for a
machine-readable decision (`allowed`, `status`, `reason`, `details`, `source`,
`suggested_flag`). The command **only targets the `nono` backend** — it is the
only backend with a policy oracle; on a `safehouse` config it returns an error.

The answer reflects graith's generated profile (including the `/tmp`/`$TMPDIR`
re-deny and the `environment.allow_vars` allowlist), so it is a faithful preview
of what a real session would enforce. Note that querying a `read_dir` located
directly under `/tmp` surfaces nono's Landlock deny-overlap error (a read-only
grant there is re-denied, which nono refuses to combine with its broad `/tmp`
allow) — a reason to keep sandbox dirs outside `/tmp`.

## Configuration

### Global sandbox

```toml
[sandbox]
enabled    = false            # wrap all agents in the sandbox
backend    = "nono"           # REQUIRED when enabled: "safehouse" | "nono"
command    = "nono"           # path/name of the backend binary (default: backend name)
features   = ["ssh"]          # feature gates (see caveats below)
read_dirs  = ["~/Code"]       # additional read-only paths
write_dirs = []               # additional read-write paths
```

### Per-agent overrides

```toml
[agents.claude.sandbox]
enabled    = true             # enable even if global is disabled
backend    = "nono"           # override the backend for this agent
features   = ["ssh"]          # merged with global features
read_dirs  = ["~/.claude"]    # merged with global read_dirs
write_dirs = ["~/.claude"]    # merged with global write_dirs

[agents.codex.sandbox]
disabled = true               # force-disable for this agent
```

### Merge behavior

- `features`, `read_dirs`, and `write_dirs` are merged (global + agent, deduplicated)
- `backend` and `command` are overridable per-agent (agent takes precedence)
- `disabled = true` on an agent overrides `enabled = true` on the global config
- `enabled = true` on an agent enables sandboxing even if the global config has `enabled = false`

## Feature gate caveats

`features` map differently onto each backend:

| Feature | safehouse | nono |
|---------|-----------|------|
| `ssh` | grants `SSH_AUTH_SOCK` access | grants the `$SSH_AUTH_SOCK` Unix socket (agent socket only; raw `~/.ssh` key access is not granted in v1). Warns if `SSH_AUTH_SOCK` is unset. |
| `process-control` | allows signal sending | **no-op** — nono's default already permits same-sandbox signals. This is a documented cross-backend divergence: the feature gates behavior under safehouse but not under nono. |
| anything else (e.g. `clipboard`) | passed to safehouse | **not mapped** — nono has no equivalent; graith warns and ignores it rather than silently dropping it. |

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
read_dirs  = ["~/.claude"]
write_dirs = ["~/.claude"]
```

The agent runs with `--dangerously-skip-permissions` (no interactive approval
prompts), but the kernel sandbox restricts it to the worktree, `~/Code`
(read-only), and `~/.claude` (read-write), and denies credentials/shell history
via nono's default deny groups.

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

Neither backend restricts outbound network in v1 (safehouse's gates are coarse;
graith emits no nono network policy yet). A compromised agent with network
access can still exfiltrate data it can read. Egress allowlisting via nono's L7
proxy is planned for a later phase.

## Limitations

- `safehouse`: macOS only; requires safehouse installed separately.
- `nono`: requires the nono binary installed separately; Linux needs kernel
  5.13+ (Landlock). Network filtering and credential injection are not wired up
  yet.
- `process-control` is a no-op under nono (see feature caveats).
