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
enabled     = false           # default (no backend assumed); true is strongly recommended once a backend is installed
backend     = "nono"          # no default — required when enabled: "safehouse" | "nono"
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

- `features`, `read_dirs`, `write_dirs`, `read_files`, and `write_files` merge (global + agent, deduplicated)
- `backend`, `command`, and `signal_mode` are per-agent overridable (agent wins)
- `network` is per-agent overridable — an agent's `[agents.*.sandbox.network]` replaces the global policy wholesale, not merged element-wise
- `enabled = false` or `disabled = true` starts the agent without Graith's sandbox and emits a startup warning — use it only with deliberate native or external isolation

## Feature gate caveats

`features` map differently onto each backend:

| Feature | safehouse | nono |
|---------|-----------|------|
| `ssh` | grants `SSH_AUTH_SOCK` access | grants the `$SSH_AUTH_SOCK` Unix socket (agent socket only; raw `~/.ssh` key access isn't granted). Warns if `SSH_AUTH_SOCK` is unset. |
| `ssh-keys` | (n/a — use safehouse's own key handling) | grants **read-only** `~/.ssh` access for agents that use raw key files instead of the agent socket (via `filesystem.read` + `filesystem.bypass_protection`, since `~/.ssh` is denied by nono's `deny_credentials` group). Opt-in companion to `ssh`, which stays agent-socket-only. Warns and grants nothing if the home directory can't be resolved. |
| `process-control` | allows signal sending | **no-op on its own** — nono's default already permits same-sandbox signals. Set `signal_mode = "isolated"` (below) to actually gate signalling under nono. Documented cross-backend divergence. |
| anything else (e.g. `clipboard`) | passed to safehouse | **not mapped** — nono has no equivalent, so graith warns and ignores it rather than silently dropping it. |

### `signal_mode` (nono only)

`signal_mode` maps to nono's `security.signal_mode` and controls whether the
sandboxed process can signal other processes: `isolated` (no signalling outside
the sandbox), `allow_same_sandbox` (nono's default), or `allow_all`. Setting it
to `isolated` is what makes the `process-control` feature meaningful under nono.
Left unset, it inherits nono's base default. `safehouse` ignores it.

## Semantic command filtering and removal migration

Graith no longer provides centralized semantic denial of selected shell
commands independently of native agent prompts. Agent-native approvals and
sandboxes retain their defaults, and Graith's `[sandbox]` remains an optional,
independent OS capability boundary. Neither is equivalent to a command-language
rule: an OS sandbox cannot generally allow `gh` while denying only `gh api`.

Configure an agent-native hook or an external policy tool directly when you
need semantic filtering. Graith does not install or supervise that policy.

Legacy `[command_policy]` configuration is rejected rather than ignored. To
upgrade from a release which supported it:

1. Archive the old policy configuration, then remove its entire table from the
   active config. Configure any direct hook or external policy replacement.
2. Upgrade Graith. During a normal daemon exec upgrade, a live session launched
   with the old policy is stopped at the compatibility boundary while Graith
   still owns the exact child; it then removes only its generated policy
   artifacts.
3. Explicitly resume the session. Graith regenerates any generic lifecycle
   hooks still configured, without the removed policy hook.

After a cold daemon restart, Graith will not signal a live non-child process
group whose generation it cannot reserve atomically. Startup fails closed with
cleanup pending and reports the affected session and PID. Inspect that PID and
stop it externally only after verifying it is the old agent process, then start
Graith again. If the PID belongs to a newer, unrelated process, Graith detects
the generation mismatch on retry and leaves that process untouched.

Use the reported PID to inspect both the executable and working directory; do
not stop it based on the number alone:

```sh
ps -p <PID> -o pid=,ppid=,lstart=,command=
lsof -a -p <PID> -d cwd -Fn  # optional, when lsof is installed
```

They must match the old agent and the session worktree. If the process has
disappeared, changed, or cannot be identified, do not signal that PID or its
process group; retry Graith startup so it can re-check the recorded generation.
Once identity is confirmed, stop that process through the operating system or
the supervisor which launched it, confirm it has exited, and retry startup.

The state migration writes the usual pre-migration `state.json.v<version>.bak`.
An older binary refuses the migrated state. A downgrade therefore requires
stopping the daemon and restoring both the matching state backup and the
archived old configuration before starting the older release. Restoring only
the older binary does not restore command filtering.

Native agent prompting remains independent:

| `non_interactive_args` | Native behavior | Graith behavior |
|---|---|---|
| Empty (bundled default) | The agent can pause in its own approval TUI. | Treats the session as ordinarily running; the agent renders and resolves the prompt. |
| Set to the agent's unattended flag(s) | Starts the agent in its non-prompting/unattended mode. | Doesn't create or answer an approval workflow. |

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
`-C`. It's separate from the sandbox but complements it:

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

Native permission prompting is disabled, but the kernel sandbox restricts the
agent to the worktree, `~/Code` (read-only), and `~/.claude` (read-write), and
denies credentials/shell history via nono's default deny groups. The
`write_files` grant is what keeps Claude **logged in**: its OAuth login lives in
`~/.claude.json` (a file next to the `~/.claude` directory), which the
`deny_credentials` group would otherwise block — see [File grants]({{< relref "how-it-works.md#file-grants" >}}).

## Browser automation

Use `agent-browser` for browser automation from a sandboxed agent. Inside a
Graith/Safehouse sandbox on macOS, Chrome's nested sandbox must be disabled while
Graith remains the outer OS boundary:

```bash
agent-browser --session docs-check --args '--no-sandbox' open https://example.com
agent-browser --session docs-check snapshot -i -u
agent-browser --session docs-check close
```

`--args '--no-sandbox'` is a Chrome launch argument; don't use it outside an
existing OS sandbox. Reuse one named session for a task and close it when done.
Graith does not translate this workflow into agent-runtime configuration or
manage unrelated native integrations.

## Network egress

By default neither backend restricts outbound network. Under `nono` you can add
an egress policy with `[sandbox.network]` (`block` / `allow_domains`) — see the
[Network egress](#network-egress-nono-only) section above. Without a policy, a
compromised agent with network access can still exfiltrate data it can read, so
set an egress policy for untrusted workloads. `safehouse` can't filter egress.
Credential injection (nono's `--credential` proxy) is a later phase.
