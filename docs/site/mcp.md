# MCP Server

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
