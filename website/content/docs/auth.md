---
weight: 1000
title: "Agent Authentication"
description: "Authenticate coding agents."
icon: "key"
toc: true
draft: false
---

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

### The human token and the fail-closed default

Local auth is **fail-closed**: a connection is treated as the human operator only if it presents a valid credential. On startup the daemon writes a **human token** to `human.token` in the data dir (mode `0600`, alongside `state.json`, and excluded from every agent sandbox), reusing it across restarts. A local connection resolves to the human role only when it presents either a valid session token or that human token; anything else is rejected rather than granted human access.

The `gr` CLI handles this transparently:

- **Inside a session**, `GRAITH_TOKEN` is sent and takes precedence — the caller is that session.
- **Outside a session** (the human at a terminal), `gr` reads `human.token` automatically and sends it — the caller is the human.

This closes the token-stripping gap: a sandboxed agent that unsets `GRAITH_TOKEN` can no longer masquerade as the human, because it cannot read `human.token` (the data dir is outside its sandbox). The security boundary is the sandbox — an *unsandboxed* agent can read either credential, so keep the sandbox on (and `gr doctor` warns when it is off).

## Token lifecycle

| Event | What happens |
|-------|-------------|
| `gr new` | Token generated, stored in state, injected as `GRAITH_TOKEN` |
| `gr fork` | New token generated for the forked session (the source's token is unchanged) |
| Session resume/restart | Token **rotated**: a fresh token is generated, the old one invalidated, and the new one injected into the new process (bounds a leaked token to one agent lifetime) |
| Daemon startup | Human token loaded from `human.token`, or created (`0600`) on first run |
| Session delete | Token removed from the daemon's reverse lookup index |
| Daemon restart | Token index rebuilt from persisted state |
| State migration (v9 to v10) | Tokens backfilled for all existing sessions |

## Interaction with sandbox

Token auth and [sandbox]({{< relref "sandbox" >}}) are complementary:

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

- **The sandbox is the boundary**: all sessions run as the same OS user, so an *unsandboxed* agent can read `human.token` and `state.json` directly and act as any identity. graith hardens the cooperative + sandboxed case; keep the sandbox on for the guarantees above to hold. `gr doctor` warns when it is disabled.
- **No encryption at rest**: tokens are stored in plaintext in `state.json` and `human.token`. The sandbox prevents agents from reading these files; unsandboxed agents can access them.
- **Local only**: the Unix socket is protected by filesystem permissions (user-only). Token auth does not protect against other OS users — it protects sessions from each other, and agents from the human role, within the same user.
- **OS-enforced identity is future work**: kernel peer credentials or per-session sockets (to defend even the unsandboxed case) are deferred — see the design doc `docs/design/2026-07-11-auth-identity-hardening.md`.

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
