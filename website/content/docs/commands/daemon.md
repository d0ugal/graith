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

Start the daemon. This is normally automatic and rarely needed.

### `gr daemon stop`

Stop the daemon gracefully.

### `gr daemon restart`

Restart the daemon, preserving live sessions via exec.

| Flag | Description |
|------|-------------|
| `--force` | Clean stop/start that kills running sessions |

After rebuilding `gr`, run `gr daemon restart` to pick up the new daemon binary.
Before it hands off any live terminal, graith checks that the exact replacement
binary can adopt every session and understands the current helper handoff. It
refuses the attempt while session creation or another lifecycle launch is in
progress. Such a refusal leaves the existing daemon and agents running; retry
after the reported work finishes. If preparation or exec fails, inheritable terminal
descriptors are restored and the existing daemon continues serving.
If the preserve request is accepted but the replacement is still not ready after
the configured startup wait, graith rechecks the live daemon. It only falls back
to a clean start after proving that the exact process which answered the
pre-upgrade socket handshake has exited; a stale PID file alone is not enough.
The clean result is then checked for the requested version and a fresh daemon
generation. Otherwise graith leaves the possible in-progress replacement alone.
Retry once startup finishes, or use `--force` when killing the sessions is
intentional.

### `gr daemon reload`

Reload configuration without restarting the daemon. Invalid settings or a
runtime apply failure return an error and leave the previous config generation
visible. Remote transport replacement closes the old listener first and stays
closed if the replacement fails; correct the setting and reload again through
the local socket. See [remote hot reload]({{< relref "/docs/configuration/access.md#hot-reload-and-revocation" >}}).

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

Subcommands inspect and control the daemon-managed MCP servers declared under
`[[mcp_servers]]`:

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

These are used by graith internally and are not intended for direct use:

| Command | Purpose |
|---------|---------|
| `gr report-status` | Report agent status (used by hooks) |
| `gr check-inbox` | Check unread inbox messages (used by hooks) |
| `gr approve-request` | Handle approval requests |
| `gr mcp-proxy` | MCP proxy for session-scoped MCP connections |
