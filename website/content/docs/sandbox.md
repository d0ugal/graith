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
`read_dirs`/`write_dirs` (and the single-file grants `read_files`/`write_files`,
folded into the same read-only / read-write path lists), strips the environment
to an allowlist, and gates `features`.

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
- `write_files` → `filesystem.allow_file` (read+write, single file) and
  `read_files` → `filesystem.read_file` (read-only, single file). These grant
  individual files for paths that can't be a directory grant without
  over-sharing — most importantly single files directly in `$HOME` such as an
  agent's `~/.claude.json` login file. An explicit file grant also punches
  through the inherited `deny_credentials` group, which is exactly what a
  login/token file needs. See [File grants](#file-grants) below.
- the env allowlist → `environment.allow_vars`, including `PATH`, `HOME`, and
  graith's injected `GRAITH_*` vars plus your configured keys. This block is
  **always emitted** for the nono backend, even when the allowlist is empty:
  nono scrubs the environment down to exactly the allowlist, so any variable not
  listed (including host secrets) is stripped and does not leak into the agent
  (this mirrors safehouse's env allowlist). Env is scrubbed by default — the
  environment fails closed. Omitting the block would make nono inherit the
  daemon's entire environment, so graith never omits it.
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
prefixes (both `read_dirs` and `read_files`) and adds an explicit
`filesystem.deny` for them (and warns), so a read-only grant stays read-only.
Paths that are meant to be writable (the worktree, `write_dirs`) are exempt.
graith's own data dir defaults to `~/.local/share/graith` (not `/tmp`), so this
only bites custom configs that point policy paths at `/tmp`.

### File grants

`read_dirs`/`write_dirs` grant whole directories (recursive). `read_files` and
`write_files` grant **individual files**. Use them for paths that can't be a
directory grant without over-sharing — most importantly single files that live
directly in `$HOME`, where granting the parent would expose every dotfile
secret.

The motivating case is agent login state. Claude Code stores its OAuth login in
`~/.claude.json` (plus `~/.claude.json.lock` / `~/.claude.lock`). Granting `~`
would expose `.ssh`, `.aws`, `.env` files, etc.; granting just the files keeps
the blast radius to the login file. Because the agent rewrites that file to
refresh tokens, it needs **read+write** (`write_files`), not read-only:

```toml
[agents.claude.sandbox]
write_files = ["~/.claude.json", "~/.claude.json.lock", "~/.claude.lock"]
```

Without this, a sandboxed Claude agent starts **logged out** — `~/.claude`
(the directory) is granted, but the login lives in the sibling file
`~/.claude.json`, and nono's inherited `deny_credentials` group blocks it. An
explicit `write_files` grant both allows the file and overrides that deny.

`read_files` is the read-only equivalent (nono `filesystem.read_file`), for
single files an agent only needs to read (e.g. a shared `~/.gitconfig`). As with
`write_dirs`, `write_files` maps to nono's read+write `filesystem.allow_file`,
never its write-only `filesystem.write_file`.

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
| Landlock present but no network-filtering ABI (`PartiallyEnforced`), **no** network policy set | **Runs** — filesystem confinement holds. `gr doctor` notes the degraded state. |
| Landlock present but no network-filtering ABI (`PartiallyEnforced`), **network policy set** | **Hard error** — filtering egress needs Landlock ABI v4 (kernel 6.7+); graith refuses rather than pretend to block. |
| Network policy set with `backend = "safehouse"` | **Hard error** — safehouse has no network primitive; use `nono`. |
| safehouse selected on non-macOS | **Hard error.** |

## `gr doctor` checks

`gr doctor` reports, for the configured backend:

- **safehouse:** macOS + binary on `$PATH`.
- **nono:** the `nono` binary on `$PATH`, its version against graith's minimum,
  and the Landlock enforcement state on Linux (`FullyEnforced` /
  `PartiallyEnforced` → degraded / `NotEnforced` → fail).
- A **fail** if the sandbox is enabled with no backend selected.
- Existence of every configured `read_dirs`/`write_dirs`/`read_files`/`write_files` path.

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

### Per-agent overrides

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

### Merge behavior

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
policy on an older kernel fails closed (see the fail-closed table). `safehouse`
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
block — see [File grants](#file-grants).

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

## Limitations

- `safehouse`: macOS only; requires safehouse installed separately; cannot
  filter network egress.
- `nono`: requires the nono binary installed separately; Linux needs kernel
  5.13+ (Landlock) for filesystem enforcement and **6.7+ (Landlock ABI v4)** for
  network filtering. Credential injection is not wired up yet.
- `process-control` is a no-op under nono unless `signal_mode = "isolated"` is
  set (see feature caveats).
