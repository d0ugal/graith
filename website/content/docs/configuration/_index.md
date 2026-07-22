---
weight: 300
title: "Configuration"
description: "Configure graith via config.toml."
icon: "settings"
toc: true
draft: false
---

Configuration lives at `~/.config/graith/config.toml` (or `$XDG_CONFIG_HOME/graith/config.toml`). Every field is optional, with sensible defaults.

Manage config with:

```bash
gr config show     # print effective (merged) config
gr config diff     # show changes from defaults
gr config reset    # write built-in defaults to config file
```

The daemon reloads config on `gr daemon reload` without restarting, and also
watches `config.toml` to reload in place when you save it:

```toml
[config]
reload_debounce = "200ms"  # quiet period after the last write before reloading
```

`reload_debounce` coalesces an editor's write-truncate-write burst into a single
reload. Read at daemon start, so changing it takes effect only after a
`gr daemon restart` (every other setting re-reads on reload).

A reload is applied as one config generation. If a runtime change can't be
applied, the command or watcher logs an error and keeps the previous generation
visible. Remote listener replacement is stricter and fail-closed: graith closes
the old listener before the new bind, so a failed remote-transport reload keeps
the previous config but leaves remote access closed until a corrected reload or
restart succeeds. See
[Orchestrator & remote access]({{< relref "access.md#hot-reload-and-revocation" >}}).

> **Full default config.** The complete, annotated defaults live in
> [`internal/config/default_config.toml`](https://github.com/d0ugal/graith/blob/main/internal/config/default_config.toml)
> — the file `gr config reset` writes, and the authoritative reference.

This reference is organized by area:

- **[Agents & repositories]({{< relref "agents.md" >}})** — agents, template variables, sandbox, per-repo settings.
- **[Session behavior]({{< relref "sessions.md" >}})** — headless sessions, delete retention, launch throttle, git pull.
- **[Notifications & messages]({{< relref "notifications.md" >}})** — status bar, notifications, messages.
- **[Automation & PR awareness]({{< relref "automation.md" >}})** — PR/CI watching, author-trust gate, triggers.
- **[TUI & input]({{< relref "interface.md" >}})** — keybindings, overlay, input handling.
- **[Orchestrator & remote access]({{< relref "access.md" >}})** — orchestrator session, tailnet remote listener.

## Global settings

```toml
default_agent      = "claude"             # agent used when --agent is not given
github_username    = ""                   # expands {username} in branch_prefix
branch_prefix      = "{username}/graith"  # template for new branch names
fetch_on_create    = true                 # fetch origin before creating a worktree
data_dir           = ""                   # override data directory (default: XDG data home)
allowed_repo_paths = []                   # restrict which repo paths the daemon accepts
```

`default_agent` may be empty or must match a configured `[agents.<name>]` key,
including a built-in agent. An unknown name is rejected during startup and
reload; a rejected reload keeps the previous configuration active.

### `agent_prompt`

A multiline string injected into the agent's environment, teaching agents to use `gr status`, `gr msg`, `gr store`, and other graith primitives. Claude gets it via `--append-system-prompt`; Cursor via `.cursor/rules/graith.mdc`; Codex via a per-session `-c developer_instructions=...` override (never written to a repository `AGENTS.md`). Other agents (OpenCode, Agy) and custom agents get no injection by default but can opt in via `prompt_injection` under `[agents.<name>]` (see [Agents]({{< relref "agents.md" >}})). Set `inject_prompt = false` per-agent to disable.

For Codex, `developer_instructions` is single-valued, so graith's override **replaces** (not appends to) any set in `~/.codex/config.toml`, a project `.codex/config.toml`, or a selected profile — the CLI override is highest-precedence. To keep your own, set `inject_prompt = false` under `[agents.codex]`.

### `allowed_repo_paths`

When non-empty, the daemon rejects `--repo` / `-C` paths not under one of these prefixes. Paths support `~` expansion and resolve to absolute before comparison. They also feed the repo autocomplete in the create-session form (`ctrl+b c` or `n` in the overlay) — each is scanned one level deep for git repositories.

```toml
allowed_repo_paths = ["~/Code", "~/Work"]
```

## macOS daemon service environment

Signed packaged installs on macOS 13 or newer start the daemon through launchd,
which does not inherit the first terminal's full environment. Graith projects a
small validated base into the one-use startup request:

- `PATH` (absolute, non-empty entries), `SHELL`, and `TMPDIR` (unless its
  canonical path would expose Graith's protected macOS service tree);
- `LANG` and `LC_*`; and
- absolute `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `XDG_DATA_HOME`,
  `XDG_STATE_HOME`, and `XDG_RUNTIME_DIR` overrides.

`HOME`, `USER`, and `LOGNAME` come from the effective user's OS account, and
the canonical profile comes from the protected service lease. To opt additional
variable names into the daemon and its eligible agent processes:

```toml
[daemon_service]
inherit_env = ["SSH_AUTH_SOCK", "ANTHROPIC_API_KEY"]
```

This is an explicit credential grant. Values are not stored in the durable
service receipt or logs, but they briefly exist in an owner-only startup file
below `~/Library/Application Support/Graith/services/control/bootstrap` until
the daemon consumes and unlinks it; macOS does not promise secure deletion. A variable absent from
the shell that wins a startup race is simply omitted. `gr doctor` prints the
effective variable **names**, never values, and calls out common current-shell
variables that were not opted in.

Identity, loader, and launch-service variables cannot be opted in: `HOME`,
`USER`, `LOGNAME`, `GRAITH_PROFILE`, all `GRAITH_*`, `DYLD_*`, `LD_*`, `XPC_*`,
and `__CF*` names are rejected. Linux, macOS 11/12, source/`go install` builds,
and unmanaged development artifacts retain the full direct-spawn environment,
so the same configuration intentionally differs across managed and fallback
installs. Restart a dormant service after changing `inherit_env`; reload cannot
replace a running process environment.

## File locations

graith follows the XDG base directory spec (override `data_dir` to change the base data directory):

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
| `~/Library/Application Support/Graith/services/` | macOS signed service generations, global profile-slot receipt, and bootstrap control (owner-only, independent of `HOME`, XDG settings, and `data_dir`) |

On managed macOS, Graith resolves that location from the effective user's OS
account record, not the `HOME` environment variable. One-use requests live in
the `services/control/bootstrap` subtree. Graith-generated safehouse and nono
policies do not grant that subtree or an enclosing directory; disabling the
sandbox or explicitly granting a broad home directory remains an operator
exposure. `TMPDIR`, XDG settings, profile paths, runtime paths, caller config,
and `data_dir` never choose the service-control boundary.

Graith also rejects a managed daemon start before registration if inherited
`TMPDIR` is the services tree, an enclosing directory, or a symlink to either;
this prevents sandbox base policies from implicitly making service state
writable.
