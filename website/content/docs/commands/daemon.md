---
weight: 450
title: "Daemon & other commands"
description: "Daemon lifecycle, config, MCP, completion, and internal commands."
icon: "dns"
toc: true
draft: false
---

## Daemon management

### `gr daemon start`

Start the daemon — normally automatic and rarely needed.

### `gr daemon stop`

Stop the daemon gracefully.

### `gr daemon restart`

Restart the daemon, preserving live sessions via exec.

| Flag | Description |
|------|-------------|
| `--force` | Clean stop/start that kills running sessions |

After rebuilding `gr`, run `gr daemon restart` to pick up the new daemon binary.
Crossing from the approval-era protocol to the non-interactive Graith protocol is
an intentional breaking restart: rather than preserving them, graith gracefully
stops the old daemon and all its PTY and headless sessions once it confirms the
exact old socket peer has exited and its socket has disappeared — if either check
fails, no competing daemon starts. Resume stopped sessions to relaunch under the
new security model.

Live adoption requires persisted state and the handoff manifest to prove the
exact process identity; the manifest records every live process, not just
transferable PTYs. Whether that process uses Graith's sandbox or the agent's
native approval TUI is preserved, not an adoption requirement. Sessions from
pre-transition releases — and headless sessions, which have no adoptable PTY —
are identity-checked, terminated, and marked stopped rather than left unmanaged.

The replacement arms cleanup before loading configuration, paths, state, or
authentication, so an early failure still identity-checks and terminates
inherited agents. If preserve is accepted but the replacement isn't ready after
the configured startup wait, graith rechecks the live daemon and falls back to a
clean start only after proving the exact process that answered the pre-upgrade
handshake has exited — a stale PID file isn't enough — then checks that result
for the requested version and a fresh daemon generation. Otherwise it leaves the
possible in-progress replacement alone: retry once startup finishes, or use
`--force` to kill the sessions intentionally.

### `gr daemon reload`

Reload configuration without restarting the daemon. Invalid settings or a
runtime apply failure return an error and leave the previous config generation in
place. Remote transport replacement closes the old listener first and stays
closed if the replacement fails — fix the setting and reload again through the
local socket. See [remote hot reload]({{< relref "/docs/configuration/access.md#hot-reload-and-revocation" >}}).

## Other commands

### `gr config show`

Print the effective (merged) configuration.

### `gr config diff`

Show changes from built-in defaults.

### `gr config reset`

Write built-in defaults to the config file.

| Flag | Description |
|------|-------------|
| `--force` | Overwrite without confirmation |

### `gr mcp`

Run graith as an MCP (Model Context Protocol) server over stdio. See [MCP Server]({{< relref "/docs/mcp.md" >}}).

Subcommands manage the daemon's MCP servers declared under `[[mcp_servers]]`:

| Command | Description |
|---------|-------------|
| `gr mcp list` | List configured MCP servers with sandbox state, source (config/auto), live connection count, and uptime |
| `gr mcp restart <name>` | Stop the running processes for a server; agent proxies reconnect and the daemon starts fresh processes with the current config |
| `gr mcp logs <name>` | Show the captured stderr for a server (one section per proxy connection). Use `-n/--lines` to cap the lines shown |

`gr mcp list` and `gr mcp logs` are read-only; `gr mcp restart` requires the
caller to be the human, the orchestrator, or one of its descendants.

### `gr completion <shell>`

Generate a shell completion script. Supported shells: `bash`, `zsh`, `fish`, `powershell`.

### `gr version`

Print version information.

## Hidden/internal commands

Used by graith internally, not intended for direct use:

| Command | Purpose |
|---------|---------|
| `gr report-status` | Report agent status (used by hooks) |
| `gr check-inbox` | Check unread inbox messages (used by hooks) |
| `gr command-policy-check` | Perform a bounded synchronous shell-policy check |
| `gr mcp-proxy` | MCP proxy for session-scoped MCP connections |
