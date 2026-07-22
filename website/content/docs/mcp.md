---
weight: 900
title: "MCP Server"
description: "Expose graith to agents over the Model Context Protocol."
icon: "cable"
toc: true
draft: false
---

graith can run as a [Model Context Protocol](https://modelcontextprotocol.io) server, exposing session management as agent-callable tools.

## Usage

```bash
gr mcp
```

Starts an MCP server on stdio. Configure it in an agent's config (e.g. Claude's `.claude/settings.json`).

## Exposed tools

| Tool | Description |
|------|-------------|
| `list_sessions` | List all sessions with status |
| `session_status` | Get detailed status of a specific session |
| `create_session` | Create a new session |
| `publish_message` | Publish a message to a topic |
| `read_inbox` | Read the calling session's inbox |
| `read_messages` | Read messages from a topic |
| `subscribe` | Wait for the next message on a topic |
| `todo_list`, `todo_add`, `todo_claim` | Read, add, and claim caller-scoped todo work |
| `todo_done`, `todo_block`, `todo_reopen`, `todo_update` | Update todo items |

`publish_message` accepts `no_reply: true` for a one-way message — stored and
returned by reads, distinct from `reply_to`, which only identifies a reply stream.

## Configuration example

Add graith to Claude Code's settings:

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

## MCP proxy

The MCP proxy (`gr mcp-proxy`) handles session-scoped connections: the daemon proxies through the control socket, multiplexing MCP traffic alongside PTY data on separate channels.

Global and per-agent MCP servers are configured in `config.toml`:

```toml
[[mcp_servers]]
name    = "my-tools"
command = "/usr/local/bin/my-mcp-server"
args    = ["--port", "8080"]
env     = { API_KEY = "..." }
```

Per-agent overrides disable or reconfigure global servers:

```toml
[agents.claude.mcp_servers.my-tools]
disabled = true    # disable for this agent

[agents.codex.mcp_servers.extra-tools]
command = "/path/to/extra-tools"
```

### How servers reach each agent

At launch graith injects the resolved server set — the auto-injected `graith`
server plus your global and per-agent servers — pointing each at
`gr mcp-proxy <name>` so the daemon supervises the real process:

- **Claude** — a generated `--mcp-config` file.
- **Codex** — per-session `-c mcp_servers.<name>.command=…`, `.args=…`, and
  `.env_vars=…` overrides. Codex starts stdio MCP children with a restricted
  environment, so graith names the minimum variables each proxy may inherit:
  `GRAITH_SESSION_ID`, `GRAITH_TOKEN`, `GRAITH_PROFILE`, `XDG_CONFIG_HOME`,
  `XDG_DATA_HOME`, and `XDG_RUNTIME_DIR`. Missing optional profile/XDG variables
  stay absent. Only variable names appear in the launch arguments; credential
  values remain in the process environment.

  Named environment inheritance requires Codex 0.47.0 or later. Older Codex
  releases ignore `env_vars` and cannot safely receive this session context.

  The injected `env_vars` list replaces a same-named Codex server's own
  environment-variable whitelist because that child is now the graith proxy,
  not the original server. Other per-server controls for a matching stdio
  server in `~/.codex/config.toml` — including `startup_timeout_sec`,
  `tool_timeout_sec`, `enabled`, literal `env` entries, or enabled/disabled tools
  — are preserved and merged. (If a same-named server in your Codex config is a
  remote/HTTP transport, the injected stdio fields conflict — pick a distinct
  name.)

  Codex identifies servers by a dotted config-key path, so a name must be a TOML
  bare key (`A–Z`, `a–z`, `0–9`, `_`, `-`) to be injectable. A name with a `.`,
  space, or other special character is skipped for Codex (with a daemon-log
  warning), since an un-representable name would stop it starting. Ordinary names
  and the `graith` server are fine; Claude has no such restriction.

Agents without MCP injection support (e.g. `cursor`, `opencode`) don't receive
them automatically.

### Caller identity and credentials

The auto-injected `graith` server preserves the identity of the session using
it. The outer proxy authenticates as that session, and the daemon delegates the
exact credential that authenticated the request only to its built-in `gr mcp`
child. As a result:

- `create_session` creates a child of the calling session;
- message sender names and IDs are forced to the calling session, even when a
  tool input supplies another sender;
- `read_inbox` and todo tools use the calling session's own context; and
- normal self/descendant authorization applies to every tool connection.

The daemon removes any ambient `GRAITH_TOKEN` from all MCP child environments.
It then injects the caller token only for the effective built-in `graith`
backend. A configured server called `graith` is still user configuration and
does not gain delegation merely from its name. The built-in backend refuses to
fall back to local-human credentials if its delegated identity is missing.

The launch snapshot is tied to the request that passed authentication. If a
resume rotates the session token concurrently, that in-flight MCP connection
may fail; it is never upgraded to the replacement token.

Running `gr mcp` directly is unmanaged and follows the normal CLI credential
rules: inside a session it uses that session's `GRAITH_TOKEN`; outside a session
it acts as the local human.

## Managing MCP servers

The daemon supervises one MCP server process per proxy connection, started
lazily on first connect. Being daemon-owned, you can inspect and control them
without touching agents:

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

Each server's stderr is captured to `<state-dir>/mcp/<name>-<proxy-id>.log`.
`gr mcp list` and `gr mcp logs` are read-only; `gr mcp restart` is restricted to
the human, the orchestrator, or its descendant sessions.
