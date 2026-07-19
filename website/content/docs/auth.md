---
weight: 1000
title: "Agent Authentication"
description: "Authenticate coding agents."
icon: "key"
toc: true
draft: false
---

graith uses per-session bearer tokens so agent sessions can't impersonate each other. Each session gets a unique token at creation, and the daemon validates it on every control message.

## Why authentication

Without auth, any process that can reach the daemon's Unix socket can send control messages claiming to be any session — stopping, deleting, or typing into a sibling, reading another session's inbox, or spoofing messages with a fake sender identity. Token auth binds each agent to its own session identity.

## How it works

1. When a session is created, the daemon generates a 32-byte random token using `crypto/rand`
2. The token is stored in the session state and injected as `GRAITH_TOKEN` into the agent's environment
3. The CLI reads `GRAITH_TOKEN` and includes it on every control message sent to the daemon
4. The daemon validates the token and enforces an authorization matrix

No configuration needed — tokens are generated automatically for all sessions. Existing sessions receive tokens when the daemon upgrades to a version with auth support (state migration v9 to v10).

## Authorization rules

When a valid token is present, the daemon enforces these rules:

| Rule | Message types | Effect |
|------|--------------|--------|
| Always allowed | `handshake`, `list`, `diagnostics`, `config`, `detach`, `resize` | No restriction |
| Self only | `set_status`, `status_report`, `command_policy_check`, `mcp_connect` | Agent can only target its own session |
| Self or descendant | `fork`, `attach`, `stop`, `delete`, `type`, `resume`, `restart`, `update`, `logs`, `screen_preview`, `screen_snapshot`, `status` | Agent can target itself or any session it created (including transitive children) |
| Human only | `reload`, `upgrade` | Rejected when a token is present; reserved for human operators |

For `update --parent`, an authenticated session must also have authority over
the new parent. Only the orchestrator or a human CLI connection can clear a
parent — this stops a child from orphaning itself to escape its parent's control.

### Identity forcing

When an agent authenticates with a valid token, the daemon overrides identity fields in the message payload (e.g. `sender_id`, `subscriber`, `session_id`) with the session ID from the token, so an agent can't claim to be a different session by manipulating the payload.

### Messaging rules

- **Topic publish/subscribe**: any authenticated session can publish to or subscribe to topics
- **Inbox publish**: any authenticated session can publish to any session's inbox
- **Inbox read**: an agent can only read its own inbox

### The human token and the fail-closed default

Local auth is **fail-closed**: a connection is treated as the human operator only if it presents a valid credential. On startup the daemon writes a **human token** to `human.token` in the data dir (mode `0600`, alongside `state.json`, and excluded from each enabled Graith agent sandbox), reusing it across restarts. A local connection resolves to the human role only when it presents a valid session token or that human token; anything else is rejected, not granted human access.

The `gr` CLI handles this transparently:

- **Inside a session**, `GRAITH_TOKEN` is sent and takes precedence — the caller is that session.
- **Outside a session** (the human at a terminal), `gr` reads `human.token` automatically and sends it — the caller is the human.

The macOS app uses the same local-human credential for its built-in **This
Mac** connection. It resolves `human.token` alongside the active profile's data
directory and re-reads it for each new local connection, so an app opened before
the daemon starts recovers without relaunching. Local access doesn't use device
pairing — pairing in the app applies only when **Add Host** connects to another
daemon over the tailnet.

With Graith's sandbox enabled, an agent that unsets `GRAITH_TOKEN` can't
masquerade as the human, because the sandbox excludes `human.token` and the data
directory. If you disable Graith's sandbox, your agent-native controls, external
sandbox, or VM must protect those files; protocol authentication can't help once
an agent reads the human token. The startup warning and `gr doctor` make that
responsibility visible.

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

Together they provide defense in depth when the sandbox is enabled. When it's
disabled, token auth still narrows ordinary requests, but external isolation
must stop agents from reading other bearer tokens at rest.

## Health checks

`gr doctor` reports auth-related issues:

```
$ gr doctor
...
[sessions] "my-agent" (abc123): missing auth token — session may need restart to receive token
  hint: Run: gr restart my-agent
```

## Limitations

- **The sandbox is the recommended boundary**: all sessions run as the same OS user. When Graith's sandbox is enabled, creation and resume fail if its backend can't enforce the configured policy. When explicitly disabled, Graith warns but can't verify your external boundary.
- **No encryption at rest**: tokens are stored in plaintext in `state.json` and `human.token`. An enabled Graith sandbox excludes these files; an external sandbox or VM must do so when Graith's sandbox is off.
- **Local only**: the Unix socket is protected by filesystem permissions (user-only). Token auth doesn't protect against other OS users — it protects sessions from each other, and agents from the human role, within the same user.
- **OS-enforced identity is future work**: kernel peer credentials or per-session sockets would add another boundary beyond process isolation; see `docs/design/2026-07-11-auth-identity-hardening.md`.

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

The `gr` CLI reads `GRAITH_TOKEN` automatically. Agents and tools that use `gr` commands don't need to handle it explicitly.
