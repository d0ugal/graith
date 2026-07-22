---
weight: 1000
title: "Agent Authentication"
description: "Authenticate coding agents."
icon: "key"
toc: true
draft: false
---

graith gives each session a unique bearer token at creation, validated on every control message, so sessions can't impersonate each other.

## Why authentication

Without auth, any process on the daemon's Unix socket could pose as any session — stopping, deleting, or typing into a sibling, reading its inbox, or spoofing a sender identity.

## How it works

1. On creation, the daemon generates a 32-byte random token via `crypto/rand`
2. It's stored in session state and injected as `GRAITH_TOKEN` into the agent's environment
3. The CLI includes `GRAITH_TOKEN` on every control message
4. The daemon validates it and enforces an authorization matrix

For the auto-injected [graith MCP server]({{< relref "mcp" >}}), the daemon
also carries the authenticated proxy caller into its private `gr mcp` child
environment. Each MCP tool connection therefore authenticates as the calling
session, not as the daemon's ambient process identity or the local human.
Codex launches stdio MCP children with a restricted environment, so graith's
per-session Codex overrides explicitly allow the proxy to inherit the session
ID and token by name, plus the optional profile and exact daemon socket path.
The token and socket values are never written into launch arguments or Codex
configuration. Graith also forces this credential-bearing proxy to execute in
Codex's local environment and clears same-named literal environment overrides.

No configuration needed. Existing sessions receive tokens when the daemon upgrades to an auth-supporting version (state migration v9 to v10).

## Authorization rules

With a valid token, the daemon enforces:

| Rule | Message types | Effect |
|------|--------------|--------|
| Always allowed | `handshake`, `list`, `diagnostics`, `config`, `detach`, `resize` | No restriction |
| Self only | `set_status`, `status_report`, `command_policy_check`, `mcp_connect` | Agent can only target its own session |
| Self or descendant | `fork`, `attach`, `stop`, `delete`, `type`, `resume`, `restart`, `update`, `logs`, `screen_preview`, `screen_snapshot`, `status` | Agent can target itself or any session it created (including transitive children) |
| Human only | `reload`, `upgrade` | Rejected when a token is present; reserved for human operators |

For `update --parent`, the session must also have authority over the new parent. Only the orchestrator or a human CLI connection can clear a parent, stopping a child from orphaning itself to escape control.

### Identity forcing

The daemon overrides payload identity fields (`sender_id`, `sender_name`,
`subscriber`, `session_id`) with identity derived from a valid token, so an
agent can't spoof a different session. This includes calls arriving through the
managed graith MCP backend.

### Messaging rules

- **Topic publish/subscribe**: any authenticated session
- **Inbox publish**: any authenticated session, to any session's inbox
- **Inbox read**: own inbox only

### The human token and the fail-closed default

Local auth is **fail-closed**: the human role requires a valid session token or the human token; anything else is rejected. On startup the daemon writes the **human token** to `human.token` (mode `0600`, alongside `state.json`, excluded from each enabled Graith agent sandbox), reused across restarts.

The `gr` CLI handles this transparently:

- **Inside a session**, `GRAITH_TOKEN` takes precedence — the caller is that session.
- **Outside a session** (human at a terminal), `gr` reads and sends `human.token` — the caller is the human.
- **Inside the managed graith MCP backend**, the daemon injects the exact token
  that authenticated the proxy request and disables human-token fallback if
  that delegation is absent. Concurrent rotation can invalidate the request but
  never upgrades it to the replacement credential.

The macOS app uses the same credential for its **This Mac** connection,
resolving `human.token` from the active profile's data directory and re-reading
it per connection, so an app opened before the daemon recovers without
relaunching. Local access uses no device pairing — that's only for **Add Host**
over the tailnet.

An agent that unsets `GRAITH_TOKEN` still can't masquerade as the human, since
the sandbox excludes those files. Disable Graith's sandbox and your agent-native
controls, external sandbox, or VM must protect them — protocol auth can't help
once an agent reads the human token, as the startup warning and `gr doctor` note.

## Token lifecycle

| Event | What happens |
|-------|-------------|
| `gr new` | Token generated, stored in state, injected as `GRAITH_TOKEN` |
| `gr fork` | New token for the forked session (source's token unchanged) |
| Session resume/restart | Token **rotated**: fresh token generated, old one invalidated, new one injected into the new process (bounds a leaked token to one agent lifetime) |
| Daemon startup | Human token loaded from `human.token`, or created (`0600`) on first run |
| Session delete | Token removed from the daemon's reverse lookup index |
| Daemon restart | Token index rebuilt from persisted state |
| State migration (v9 to v10) | Tokens backfilled for existing sessions |

## Interaction with sandbox

Token auth and [sandbox]({{< relref "sandbox" >}}) are complementary:

- **Token auth** prevents protocol-level impersonation
- **Sandbox** prevents filesystem access — a sandboxed agent can't read `state.json` (which holds all tokens) or other sessions' worktrees

Together they provide defense in depth; with the sandbox disabled, token auth still narrows requests but can't protect tokens at rest.

## Health checks

`gr doctor` reports auth-related issues:

```
$ gr doctor
...
[sessions] "my-agent" (abc123): missing auth token — session may need restart to receive token
  hint: Run: gr restart my-agent
```

## Limitations

- **The sandbox is the recommended boundary**: all sessions run as the same OS user. Enabled, creation and resume fail if the backend can't enforce the policy; disabled, Graith warns but can't verify your external boundary.
- **No encryption at rest**: tokens are plaintext in `state.json` and `human.token`. An enabled Graith sandbox excludes these files; otherwise an external sandbox or VM must.
- **Local only**: the Unix socket is protected by filesystem permissions (user-only). Token auth doesn't guard against other OS users — it protects sessions from each other, and agents from the human role, within one user.
- **OS-enforced identity is future work**: kernel peer credentials or per-session sockets would add a boundary beyond process isolation; see `docs/design/2026-07-11-auth-identity-hardening.md`.

## Environment variables

The daemon sets these in every agent process:

| Variable | Description |
|----------|-------------|
| `GRAITH_TOKEN` | Bearer token for this session (64 hex characters) |
| `GRAITH_SESSION_ID` | Unique session identifier |
| `GRAITH_SESSION_NAME` | Human-readable session name |
| `GRAITH_AGENT_TYPE` | Agent type (e.g. `claude`, `codex`) |
| `GRAITH_WORKTREE_PATH` | Absolute path to the session worktree |
| `GRAITH_REPO_PATH` | Absolute path to the source repository |

The `gr` CLI reads `GRAITH_TOKEN` automatically; agents and tools using `gr` needn't handle it.
