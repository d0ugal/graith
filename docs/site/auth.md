# Agent Authentication

graith uses per-session bearer tokens to prevent agent sessions from impersonating each other. Each session gets a unique token at creation time, and the daemon validates it on every control message.

## Why authentication

Without auth, any process that can reach the daemon's Unix socket can send control messages claiming to be any session. An agent could stop, delete, or type into a sibling session, read another session's inbox, or spoof messages with a fake sender identity. Token auth binds each agent to its own session identity.

## How it works

1. When a session is created, the daemon generates a 32-byte random token using `crypto/rand`
2. The token is stored in the session state and injected as `GRAITH_TOKEN` into the agent's environment
3. The CLI reads `GRAITH_TOKEN` and includes it on every control message sent to the daemon
4. The daemon validates the token and enforces an authorization matrix

No configuration is needed. Tokens are generated automatically for all sessions. Existing sessions receive tokens when the daemon upgrades to a version with auth support (state migration v9 to v10).

## Authorization rules

When a valid token is present, the daemon enforces these rules:

| Rule | Message types | Effect |
|------|--------------|--------|
| Always allowed | `handshake`, `list`, `diagnostics`, `detach`, `resize`, `approval_list` | No restriction |
| Self only | `set_status`, `status_report`, `approval_request`, `mcp_connect` | Agent can only target its own session |
| Self or descendant | `fork`, `attach`, `stop`, `delete`, `type`, `resume`, `restart`, `rename`, `star`, `unstar`, `logs`, `screen_preview`, `screen_snapshot`, `status` | Agent can target itself or any session it created (including transitive children) |
| Human only | `reload`, `upgrade`, `approval_respond` | Rejected when a token is present; reserved for human operators |

### Identity forcing

When an agent authenticates with a valid token, the daemon overrides identity fields in the message payload (e.g. `sender_id`, `subscriber`, `session_id`) with the session ID from the token. This prevents an agent from claiming to be a different session even if it manipulates the payload.

### Messaging rules

- **Topic publish/subscribe**: any authenticated session can publish to or subscribe to topics
- **Inbox publish**: any authenticated session can publish to any session's inbox
- **Inbox read**: an agent can only read its own inbox

### Unauthenticated connections

Connections without a token (the human CLI outside a session) retain full access. The daemon cannot distinguish a human CLI from an agent that stripped its token at the protocol level. This is an accepted limitation — see [Limitations](#limitations) below.

## Token lifecycle

| Event | What happens |
|-------|-------------|
| `gr new` | Token generated, stored in state, injected as `GRAITH_TOKEN` |
| `gr fork` | New token generated for the forked session |
| Session resume/restart | Stored token re-injected into the new process environment |
| Session delete | Token removed from the daemon's reverse lookup index |
| Daemon restart | Token index rebuilt from persisted state |
| State migration (v9 to v10) | Tokens backfilled for all existing sessions |

## Interaction with sandbox

Token auth and [sandbox](sandbox.md) are complementary:

- **Token auth** prevents impersonation at the protocol level — an agent with a valid token can only act as itself or its descendants
- **Sandbox** prevents filesystem access — a sandboxed agent cannot read `state.json` (which contains all tokens) or other sessions' worktrees

Together they provide defense in depth. Token auth is useful even without sandbox (prevents accidental impersonation by cooperative agents), and sandbox is useful even without token auth (restricts filesystem and network access).

## Health checks

`gr doctor` reports auth-related issues:

```
$ gr doctor
...
[sessions] "my-agent" (abc123): missing auth token — session may need restart to receive token
  hint: Run: gr restart my-agent
[sessions] sandbox disabled with 3 running sessions — tokens in state.json are readable by all agents
  hint: Enable sandbox in config or limit to single-agent workflows
```

## Limitations

- **Token stripping**: an agent that unsets `GRAITH_TOKEN` and connects without it is treated as a human CLI with full access. The sandbox prevents agents from modifying their initial process environment, but a determined agent could spawn a child process with modified env. This is outside the casual-impersonation threat model.
- **No token rotation**: tokens are fixed for the lifetime of a session. A leaked token remains valid until the session is deleted.
- **No encryption at rest**: tokens are stored in plaintext in `state.json`. Sandbox prevents agents from reading this file, but unsandboxed agents can access all tokens.
- **Local only**: the Unix socket is protected by filesystem permissions (user-only). Token auth does not protect against other OS users — it protects sessions from each other within the same user.

## Environment variables

The daemon sets `GRAITH_TOKEN` in every agent process alongside the other session environment variables:

| Variable | Description |
|----------|-------------|
| `GRAITH_TOKEN` | Bearer token for this session (64 hex characters) |
| `GRAITH_SESSION_ID` | Unique session identifier |
| `GRAITH_SESSION_NAME` | Human-readable session name |
| `GRAITH_AGENT_TYPE` | Agent type (e.g. `claude`, `codex`) |
| `GRAITH_WORKTREE_PATH` | Absolute path to the session worktree |
| `GRAITH_REPO_PATH` | Absolute path to the source repository |

`GRAITH_TOKEN` is read automatically by the `gr` CLI. Agents and tools that use `gr` commands do not need to handle it explicitly.
