---
weight: 300
title: "Configuration"
description: "Configure graith via config.toml."
icon: "settings"
toc: true
draft: false
---

Configuration lives at `~/.config/graith/config.toml` (or `$XDG_CONFIG_HOME/graith/config.toml`). All fields are optional. Sensible defaults are provided.

Manage config with:

```bash
gr config show     # print effective (merged) config
gr config diff     # show changes from defaults
gr config reset    # write built-in defaults to config file
```

The daemon reloads config on `gr daemon reload` without restarting. It also
watches `config.toml` and reloads in place after you save it:

```toml
[config]
reload_debounce = "200ms"  # quiet period after the last write before reloading
```

`reload_debounce` coalesces an editor's write-truncate-write burst into a single
reload. It is read when the daemon starts, so a change to `reload_debounce`
itself only takes effect after a `gr daemon restart`. Other settings are
published on reload, but when running components observe them varies: some
apply immediately or on the next operation, some apply only to newly launched
sessions, and loop cadences or constructed stores may require a daemon restart.
Each configuration section below documents its settings' exact reload contract.

A reload is published only after runtime-backed settings have applied. If a
replacement runtime cannot be prepared (for example, a changed remote listener
cannot bind), manual reload reports the error and the file watcher logs it; the
previous effective config remains visible. Security-sensitive runtimes fail
closed while you correct the file and reload again.

> **Full default config.** The complete, annotated set of defaults lives in
> [`internal/config/default_config.toml`](https://github.com/d0ugal/graith/blob/main/internal/config/default_config.toml)
> — the same file `gr config reset` writes. Run `gr config reset` to drop a copy
> into `~/.config/graith/config.toml`, `gr config show` to print the effective
> merged config, or `gr config diff` to see only what you've changed. The pages
> below document each block; the file is the authoritative reference.

This reference is organized by area:

- **[Agents & repositories]({{< relref "agents.md" >}})** — agent definitions, template variables, MCP servers, per-agent sandbox, and per-repo settings.
- **[Session behavior]({{< relref "sessions.md" >}})** — headless sessions, delete retention, the launch throttle, and periodic git pull.
- **[Notifications & approvals]({{< relref "notifications.md" >}})** — the status bar, desktop/push notifications, approvals, and messages.
- **[Automation & PR awareness]({{< relref "automation.md" >}})** — PR/CI watching, the comment author-trust gate, and triggers.
- **[TUI & input]({{< relref "interface.md" >}})** — keybindings, the overlay, and input handling.
- **[Orchestrator & remote access]({{< relref "access.md" >}})** — the orchestrator session and the tailnet remote listener.

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

A multiline string injected into the agent's environment. For Claude, it is passed via `--append-system-prompt`. For Cursor, it is written to `.cursor/rules/graith.mdc`. For Codex, it is passed as a per-session `-c developer_instructions=...` config override (never written to a repository `AGENTS.md`). Other agents (OpenCode, Agy) and custom agents get no prompt injection by default, but can opt in by setting `prompt_injection` to one of `append_system_prompt`, `cursor_rules`, or `developer_instructions` under `[agents.<name>]` (see [Agents]({{< relref "agents.md" >}})). Teaches agents how to use `gr status`, `gr msg`, `gr store`, and other graith primitives. Set `inject_prompt = false` on a per-agent basis to disable.

For Codex specifically, `developer_instructions` is a single-valued config key, so graith's override **replaces** (does not append to) any `developer_instructions` you have set in `~/.codex/config.toml`, a project `.codex/config.toml`, or a selected profile — the CLI override is the highest-precedence layer. If you rely on your own Codex `developer_instructions`, set `inject_prompt = false` under `[agents.codex]` to keep them and skip graith's injection.

### `allowed_repo_paths`

When non-empty, the daemon rejects `--repo` / `-C` paths that are not under one of these prefixes. Paths support `~` expansion and are resolved to absolute paths before comparison. These paths also feed the repo autocomplete in the create-session form (`ctrl+b c` or `n` in the overlay) — each path is scanned one level deep for git repositories.

```toml
allowed_repo_paths = ["~/Code", "~/Work"]
```

## File locations

graith follows the XDG base directory spec:

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

Override `data_dir` in config to change the base data directory.
