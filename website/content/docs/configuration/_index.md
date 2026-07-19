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

- **[Agents & repositories]({{< relref "agents.md" >}})** — agents, template variables, MCP servers, sandbox, per-repo settings.
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

### `agent_prompt`

A multiline string injected into the agent's environment, teaching agents to use `gr status`, `gr msg`, `gr store`, and other graith primitives. Claude gets it via `--append-system-prompt`; Cursor via `.cursor/rules/graith.mdc`; Codex via a per-session `-c developer_instructions=...` override (never written to a repository `AGENTS.md`). Other agents (OpenCode, Agy) and custom agents get no injection by default but can opt in via `prompt_injection` under `[agents.<name>]` (see [Agents]({{< relref "agents.md" >}})). Set `inject_prompt = false` per-agent to disable.

For Codex, `developer_instructions` is single-valued, so graith's override **replaces** (not appends to) any set in `~/.codex/config.toml`, a project `.codex/config.toml`, or a selected profile — the CLI override is highest-precedence. To keep your own, set `inject_prompt = false` under `[agents.codex]`.

### `allowed_repo_paths`

When non-empty, the daemon rejects `--repo` / `-C` paths not under one of these prefixes. Paths support `~` expansion and resolve to absolute before comparison. They also feed the repo autocomplete in the create-session form (`ctrl+b c` or `n` in the overlay) — each is scanned one level deep for git repositories.

```toml
allowed_repo_paths = ["~/Code", "~/Work"]
```

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
