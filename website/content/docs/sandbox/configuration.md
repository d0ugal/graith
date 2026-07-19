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
enabled     = true            # default; false relies on native/external isolation
backend     = "nono"          # required when enabled: "safehouse" | "nono"
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

```

## Merge behavior

- `features`, `read_dirs`, `write_dirs`, `read_files`, and `write_files` are merged (global + agent, deduplicated)
- `backend`, `command`, and `signal_mode` are overridable per-agent (agent takes precedence)
- `network` is overridable per-agent — an agent's `[agents.*.sandbox.network]` replaces the global policy wholesale (not merged element-wise)
- `enabled = false` or `disabled = true` starts the agent without Graith's sandbox and emits a startup warning; use this only with deliberate native or external isolation

## Feature gate caveats

`features` map differently onto each backend:

| Feature | safehouse | nono |
|---------|-----------|------|
| `ssh` | grants `SSH_AUTH_SOCK` access | grants the `$SSH_AUTH_SOCK` Unix socket (agent socket only; raw `~/.ssh` key access is not granted). Warns if `SSH_AUTH_SOCK` is unset. |
| `ssh-keys` | (n/a — use safehouse's own key handling) | grants **read-only** access to `~/.ssh` for agents that use raw key files instead of the agent socket (via `filesystem.read` + `filesystem.bypass_protection`, since `~/.ssh` is denied by nono's `deny_credentials` group). Opt-in companion to `ssh`, which stays agent-socket-only. Warns and grants nothing if the home directory can't be resolved. |
| `process-control` | allows signal sending | **no-op on its own** — nono's default already permits same-sandbox signals. Set `signal_mode = "isolated"` (below) to make it actually gate signalling under nono. Documented cross-backend divergence. |
| anything else (e.g. `clipboard`) | passed to safehouse | **not mapped** — nono has no equivalent; graith warns and ignores it rather than silently dropping it. |

### `signal_mode` (nono only)

`signal_mode` maps to nono's `security.signal_mode` and controls whether the
sandboxed process may signal other processes: `isolated` (no signalling outside
the sandbox), `allow_same_sandbox` (nono's default), or `allow_all`. Setting it
to `isolated` is what makes the `process-control` feature meaningful under nono.
Leaving it unset inherits nono's base default. `safehouse` ignores it.

## Command policy

```toml
[command_policy]
backend = ""          # disabled by default; "builtin" or "localmost"
timeout = "5s"        # hard bound for one synchronous check (maximum 60s)
command = ""          # optional localmost executable override

[command_policy.builtin]
config = ""           # localmost-format config.json
# allow = ["git status", "go test *"]
# deny  = ["rm *"]
```

Command policy is an optional, synchronous restriction applied before shell
commands. It can only subtract from the agent's effective capabilities:

- `backend = ""` installs no policy hook; commands proceed directly to normal agent execution.
- `backend = "builtin"` uses graith's localmost-compatible parser and rules.
- `backend = "localmost"` invokes the native localmost binary, optionally selected with `command`.

Only shell tools are in policy scope. Other tools go directly to normal agent
execution. An allow grants nothing: it bypasses neither an enabled Graith
sandbox, the agent's own policy, nor external isolation. A deny blocks
immediately. Interactive
results (`ask`/`defer`), malformed output, timeouts, backend errors, and an
unavailable configured backend all fail closed without waiting for a human.
The generated hook uses a shorter hard-deadline worker inside the agent hook
runner's deadline. Worker crashes, signals, malformed responses, and transport
timeouts become native deny responses; failure to start the supervisor returns
the hook runner's blocking exit code instead of continuing the command.

Configured policy availability is checked at session creation and resume. The
session does not start if enforcement cannot be established. `timeout` defaults
to `5s`, must be positive, and may not exceed `60s`.

Command policy is currently enforceable for the built-in Claude and Codex
agents. Graith rejects policy-enabled Cursor, OpenCode, Agy, and custom-agent
sessions at startup because those integrations do not currently provide a
verified synchronous deny contract. They remain fully usable with command
policy disabled, with whatever Graith sandbox, native controls, or external
isolation you configured.

Enabling, disabling, or otherwise changing `[command_policy]` marks existing
sessions config-stale. Restart those sessions to install the exact policy hook
recorded for their launch; rule checks remain synchronous and fail closed.

Rule tooling lives under the sandbox namespace:

```bash
printf '%s\n' 'git status' | gr sandbox policy check
gr sandbox policy validate
```

### How the controls combine

Graith's sandbox and command policy are independent:

| Graith sandbox | `command_policy` | Shell command result |
|---|---|---|
| On | Off | No policy pre-check; the command runs only if the OS sandbox permits it. |
| On | On | Policy deny blocks immediately; policy allow continues inside the OS sandbox. |
| Off | Off | Graith adds no capability boundary or shell restriction; only agent-native or external controls apply. |
| Off | On | Policy deny blocks immediately; policy allow continues without a Graith sandbox, under any agent-native or external controls. |

Native agent prompting is a third independent control:

| `non_interactive_args` | Native behavior | Graith behavior |
|---|---|---|
| Bundled value retained | Starts the agent in its non-prompting/unattended mode. | Does not create an approval workflow. |
| Set to `[]` or omitted | The agent may pause in its own approval TUI. | Treats the session as ordinarily running; the agent renders and resolves the prompt. |

The generated `PreToolUse` hooks are independent of both sandbox selection and
native prompting. “Lifecycle” below means the session's Graith lifecycle hooks
are enabled (the normal `gr new` behavior):

| Agent | Lifecycle | Policy | Graith-generated `PreToolUse` entry |
|---|---:|---:|---|
| Claude | Off or on | Off | None. |
| Claude | Off or on | On | Bash matcher → bounded `gr command-policy-check`. |
| Codex | Off | Off | None. |
| Codex | On | Off | Match-all → `gr report-status --event PreToolUse`. |
| Codex | Off | On | Bash matcher → bounded `gr command-policy-check`. |
| Codex | On | On | Two groups: match-all status reporting, plus the Bash policy check. |

On policy allow, the hook emits the agent's native “continue” result (empty
output for the verified Claude/Codex contracts). On deny, ask/defer, malformed
data, evaluation failure, or timeout, it emits a native blocking deny; failure
to run the supervisor uses the hook runner's blocking exit status. Graith never
installs a hook that asks a human.

For example, this runs Codex unattended without Graith's sandbox while still
blocking every `gh api` shell call (including POST) synchronously. The broad
deny is intentional: a command-string policy cannot reliably infer HTTP methods
that a CLI may select implicitly.

```toml
[sandbox]
enabled = false

[agents.codex]
non_interactive_args = ["--ask-for-approval", "never", "--sandbox", "danger-full-access"]

[command_policy]
backend = "builtin"

[command_policy.builtin]
allow = ["@*"]
deny = ["gh api @*"]
```

With this configuration, Graith prints an unsandboxed startup warning and
`gr doctor` reports the missing Graith sandbox. The command policy still fails
closed if its hook or backend cannot be established.

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
non_interactive_args = ["--dangerously-skip-permissions"]
args        = ["--session-id", "{agent_session_id}"]
resume_args = ["--resume", "{agent_session_id}"]

[agents.claude.sandbox]
read_dirs   = ["~/.claude"]
write_dirs  = ["~/.claude"]
write_files = ["~/.claude.json", "~/.claude.json.lock", "~/.claude.lock"]
```

The agent runs with native permission prompting disabled, but the kernel sandbox restricts it to the worktree, `~/Code`
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
