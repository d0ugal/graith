---
weight: 900
title: "MCP Server"
description: "Expose graith to agents over the Model Context Protocol."
icon: "cable"
toc: true
draft: false
---

graith can run as a [Model Context Protocol](https://modelcontextprotocol.io) server, exposing session management as tools that an AI agent can call.

## Usage

```bash
gr mcp
```

This starts an MCP server over stdin/stdout using the stdio transport. It is designed to be configured as an MCP server in an agent's configuration (e.g. Claude's `.claude/settings.json`).

## Exposed tools

| Tool | Description |
|------|-------------|
| `list_sessions` | List all sessions with status |
| `session_status` | Get detailed status of a specific session |
| `create_session` | Create a new session |
| `publish_message` | Publish a message to a topic |
| `read_messages` | Read messages from a topic |
| `subscribe` | Wait for the next message on a topic |

## Configuration example

Add graith as an MCP server in Claude Code's settings:

```json
{
  "mcpServers": {
    "graith": {
      "command": "gr",
      "args": ["mcp"]
    }
  }
}
```

This gives Claude access to graith session management as part of its tool set. The agent can create sessions, check status, and coordinate with other agents through the MCP interface.

## MCP proxy

graith also includes an MCP proxy (`gr mcp-proxy`) used internally for session-scoped MCP connections. When a session needs to connect to an MCP server, the daemon can proxy the connection through the control socket, multiplexing MCP traffic alongside PTY data on separate channels.

Global and per-agent MCP servers are configured in `config.toml`:

```toml
[[mcp_servers]]
name    = "my-tools"
command = "/usr/local/bin/my-mcp-server"
args    = ["--port", "8080"]
env     = { API_KEY = "..." }
```

Per-agent overrides can disable or reconfigure global servers:

```toml
[agents.claude.mcp_servers.my-tools]
disabled = true    # disable for this agent

[agents.codex.mcp_servers.extra-tools]
command = "/path/to/extra-tools"
```

### How servers reach each agent

graith injects the resolved server set (the auto-injected `graith` server plus
your global and per-agent servers) into the agent at launch, pointing each one
at `gr mcp-proxy <name>` so the daemon supervises the real process:

- **Claude** — a generated `--mcp-config` file.
- **Codex** — per-session `-c mcp_servers.<name>.command=…` / `.args=…`
  config overrides. Because these are overrides (not a full config file), any
  extra Codex per-server controls you set in `~/.codex/config.toml` — such as
  `startup_timeout_sec`, `tool_timeout_sec`, `enabled`, enabled/disabled tools,
  or per-tool approval mode — are preserved and merged.

Agents without MCP injection support (e.g. `cursor`, `opencode`) don't receive
these servers automatically.

## Managing MCP servers

The daemon supervises one MCP server process per proxy connection, started
lazily when a session first connects. Because these processes are daemon-owned,
you can inspect and control them without touching the agents themselves:

```bash
# List configured servers with sandbox state, source, live connections, uptime
gr mcp list

# Restart a server: stops its running processes so proxies reconnect and the
# daemon starts fresh ones with the current config (no session restart needed)
gr mcp restart my-tools

# Show a server's captured stderr (one section per proxy connection)
gr mcp logs my-tools
gr mcp logs my-tools -n 50
```

Each server's stderr is captured to `<state-dir>/mcp/<name>-<proxy-id>.log`;
`gr mcp logs` reads those files. `gr mcp list` and `gr mcp logs` are read-only.
`gr mcp restart` is a management action, restricted to the human, the
orchestrator, or its descendant sessions.
