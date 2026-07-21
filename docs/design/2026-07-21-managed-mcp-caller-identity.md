---
title: "Design Doc: Preserve Caller Identity in Managed Graith MCP"
authors: Dougal Matthews
created: 2026-07-21
status: Implemented
reviewers: (none yet)
informed: issue-1377
issue: https://github.com/d0ugal/graith/issues/1377
---

# Preserve Caller Identity in Managed Graith MCP

The daemon-managed `graith` MCP backend will reconnect as the authenticated
session that opened its proxy, rather than inheriting an ambient credential or
falling back to the local-human token. The delegation remains an internal,
launch-only value and is available only to the effective built-in backend.

## Background

Graith injects `gr mcp-proxy graith` into supported agents. The proxy connects
to the daemon with the agent's normal `GRAITH_TOKEN`, sends `mcp_connect`, and
then transports MCP frames between the agent and a daemon-owned child process.
The daemon forces the request's `session_id` from the authenticated envelope and
checks the self-only authorization rule before `MCPManager.Connect` starts that
child.

The built-in child is a separate `gr mcp` process. Each MCP tool opens a fresh
daemon connection through `client.ConnectPassiveContext`. Client credential
resolution prefers `GRAITH_TOKEN`, but falls back to `human.token` when the
environment has no session token. Configured MCP children otherwise inherit the
daemon environment unless their config has an `env` map.

## Problem

The authenticated identity terminates at `mcp_connect`. The manager currently
starts `gr mcp` without transferring it, so tool calls usually reconnect with
the human token. If the daemon happens to carry an ambient `GRAITH_TOKEN`, the
backend instead uses that unrelated session nondeterministically.

Human authority removes the expected session boundaries: MCP-created sessions
are unparented, message identity supplied by the tool is trusted as local-human
input, inbox and todo operations lack the caller's context, and descendant
authorization no longer describes the agent that invoked the tool. Passing the
daemon's ambient environment to other configured MCP servers also risks leaking
an unrelated session credential.

## Goals

- Preserve the authenticated proxy session as the identity of every managed
  `graith` MCP tool connection.
- Make the built-in backend fail closed rather than use local-human fallback
  when delegation is absent.
- Strip the daemon's ambient Graith session bearer token from every MCP child.
- Keep the delegated token out of protocol messages, configuration, template
  variables, persisted state, status output, and logs.
- Preserve lock discipline by copying the current token before process launch.

### Non-Goals

- Change the identity rules of a directly configured, unmanaged `gr mcp`.
- Delegate session credentials to arbitrary user-configured MCP servers.
- Add a general-purpose token delegation feature or public configuration key.
- Change the wire protocol or token lifecycle.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | `gr mcp-proxy`, `gr mcp`, and the daemon-managed process boundary implement the feature. |
| iOS | Excluded | iOS does not launch local stdio MCP child processes. |
| macOS | Excluded | The native app is not an MCP proxy frontend; local CLI sessions receive the fix automatically. |

## Proposals

### Proposal 0: Do Nothing

Keep terminating authentication at `mcp_connect` and let the backend resolve a
credential from the daemon environment. This leaves session-scoped tools with
human or unrelated-session authority and makes authorization depend on daemon
startup history, so it is not acceptable.

### Proposal 1: Internal Launch-Only Delegation (Recommended)

After authentication forces the proxy's session ID, the handler reads that
same session's current token from daemon state. It copies the ID and token into
a private launch identity passed to `MCPManager.Connect`; process launch remains
outside the session-manager lock.

The MCP manager records provenance for auto-injected servers separately from
their public `MCPServerConfig`. Only the built-in `graith` registration is
marked as accepting caller identity. A configured server with the same name
replaces the built-in effective config and clears that marker, so a
user-controlled name cannot select delegation. The built-in refuses to start
without a non-empty identity whose session ID matches the connection's template
identity.

Every MCP child gets an explicit copy of the daemon environment with ambient
`GRAITH_TOKEN` and the private managed-backend marker removed. Configured server
environment values are then applied. The trusted built-in finally receives the
authenticated session token plus the marker. In marked mode, `gr mcp` checks
that `GRAITH_TOKEN` is present before connecting, preventing the normal human
token fallback if launch delegation is ever omitted.

The token exists only in the child environment, matching ordinary agent token
delivery. It is never included in an error or structured manager state. Token
rotation after the launch snapshot can make a connection fail authentication,
but cannot elevate it; the proxy/backend lifecycle will reconnect using the
current caller process and token.

### Proposal 2: Add Delegation to `MCPConnectMsg`

The proxy could send its token or a daemon-minted delegation credential in the
wire request. Sending the bearer token duplicates the envelope credential and
widens accidental logging and protocol exposure. Minting a scoped credential
adds lifecycle, persistence, conformance, and Swift work without improving the
local daemon-to-child boundary. The daemon already resolved the exact caller,
so an internal value is smaller and safer.

## Other Notes

### References

- [Issue #1377](https://github.com/d0ugal/graith/issues/1377)
- `internal/daemon/handler.go` — authenticated `mcp_connect` dispatch
- `internal/daemon/mcpmanager.go` — effective server provenance and child launch
- `internal/mcp/server.go` — per-tool daemon connections
- `internal/client/client.go` — session-token then human-token resolution
- `docs/design/2026-07-11-auth-identity-hardening.md`

### Implementation Notes

The manager must construct effective server configuration and delegation
provenance together on startup and reload. User overrides and disables win in
both collections. Environment construction must remove prior values by key so
duplicate `GRAITH_TOKEN` entries cannot leave selection to platform behavior.

### Testing

Manager tests cover token injection, ambient-token removal, fail-closed launch,
configured-name non-trust, and reload provenance changes. Handler tests cover
authenticated session forcing and token transfer. A tagged
integration test runs an MCP SDK client through the real `mcp-proxy` command,
the daemon-managed real `gr mcp` backend, and fresh backend-to-daemon tool
connections. It proves forced message attribution, child-session parenting,
caller-scoped inbox access, and caller-scoped todo creation, with an unrelated
ambient daemon token present.
Focused daemon and integration suites run under the race detector before the
broad Go and documentation checks.
