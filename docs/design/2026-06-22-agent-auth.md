# Agent Token Authentication

**Author:** Dougal Matthews
**Date:** 2026-06-22
**Status:** Draft (revised after 7-judge review tribunal)

## Problem

graith sessions identify themselves to the daemon using `GRAITH_SESSION_ID`, a plain environment variable that any process can read, copy, or spoof. The daemon trusts whatever session ID appears in message payloads without verification.

This means any agent can:
- Set status on another session
- Publish messages with a spoofed sender ID
- Send commands (type, stop, delete) targeting any session by guessing or reading its ID

Session IDs are short (8 hex chars) and visible in `gr list` output, making impersonation trivial. While the Unix socket is protected by filesystem permissions (mode 0o700), there is no session-level access control within a connected client.

This is especially relevant for the orchestrator and scenarios features, where agents coordinate and must trust that messages come from who they claim to be.

## Goals

- Agents cannot impersonate other sessions without actively circumventing the system (unsetting both `GRAITH_SESSION_ID` and `GRAITH_TOKEN`)
- The daemon can verify the identity of the session behind each request
- Human CLI users (outside any graith session) retain full unrestricted access
- Tokens survive daemon restarts
- `gr doctor` warns when the security boundary is weak (e.g. sandbox disabled)

### Non-Goals

- Defending against an agent that deliberately unsets both `GRAITH_SESSION_ID` and `GRAITH_TOKEN` — this cripples the agent's session awareness (`--parent`, `--children`, `gr status`, auto-sender) for marginal gain, and is "actively circumventing"
- Encrypting tokens at rest — the threat model is casual impersonation by AI agents, not local privilege escalation
- Per-operation authorization (e.g. "session X can only message, not delete") — this is a future concern that builds on identity verification

## Proposals

### Proposal 0: Do Nothing

Agents continue using `GRAITH_SESSION_ID` for identity. Any agent can claim to be any session. As graith adds orchestration features (orchestrator session, scenarios, inter-agent messaging), the lack of identity verification becomes increasingly problematic — agents could accidentally or naively impersonate each other, leading to confused coordination.

### Proposal 1: Per-Request Token on the Envelope (Recommended)

Add a `Token` field to the protocol `Envelope` so every control message can carry a bearer-style credential. The daemon validates the token against the claimed session on each request independently.

#### Token lifecycle

1. **Generation**: When creating a session, the daemon generates a 32-byte random token using `crypto/rand` (hex-encoded = 64 chars). The token is stored in `SessionState.Token`. Token generation **must** check the `rand.Read` error and fail closed — do not follow the `generateID()` pattern of ignoring errors. Session creation fails if a token cannot be generated.

2. **Distribution**: The token is set as `GRAITH_TOKEN` in the agent process environment, alongside `GRAITH_SESSION_ID`. Once `GRAITH_TOKEN` is in the `env` map, it is automatically included in the sandbox `envKeys` list (the existing Create/Fork/Resume paths already append all keys from the `env` map).

3. **Transmission**: The CLI reads `GRAITH_TOKEN` from the environment and includes it in every outgoing control message via the `Envelope.Token` field. Token injection happens in `Client.SendControl` (not in `protocol.EncodeControl`, which is also used for daemon responses and must not attach tokens).

4. **Validation**: The daemon extracts the token from the envelope after decoding. See "Validation rules" below for the complete policy.

5. **Persistence**: Tokens are stored in `state.json` as part of `SessionState`. They survive daemon restarts. On resume/restart of a session, the daemon sets the stored token in the new process env.

#### Protocol change

```go
type Envelope struct {
    Type    string          `json:"type"`
    Payload json.RawMessage `json:"payload,omitempty"`
    Token   string          `json:"token,omitempty"`
}
```

No changes to individual message types. The token is orthogonal to the message payload.

#### Validation rules

The core rule: **if a valid token is present, identity fields are forced from the token and the authorization matrix is enforced. If an invalid token is present, the request is rejected. If no token is present, the connection is treated as a human CLI with full access.**

| Scenario | Token? | `GRAITH_SESSION_ID`? | Behavior |
|----------|--------|---------------------|----------|
| Human CLI (outside session) | No | No | Full access — treated as human operator |
| Agent CLI (inside session) | Yes, valid | Yes | Validated per authorization matrix |
| Agent CLI (inside session) | Yes, invalid | Yes | Rejected |
| Agent CLI (token stripped) | No | Yes | Treated as human — full access (see limitations) |
| Agent CLI (both stripped) | No | No | Treated as human — agent loses all session awareness |

The daemon cannot distinguish a human CLI from an agent that stripped its token at the protocol level. The `GRAITH_SESSION_ID` environment variable is client-side only and not transmitted in the protocol. This means **token-stripping is not rejected server-side** — an agent that unsets `GRAITH_TOKEN` gains human-level access. This is an accepted limitation: the sandbox prevents agents from unsetting environment variables in the initial process, and an agent that actively circumvents auth by spawning a child process with modified env is outside the casual-impersonation threat model. A future enhancement could add a `ClaimedSessionID` field to the envelope to close this gap.

#### Authorization matrix

Every control message type must have an explicit rule. The daemon derives the **authenticated session** from the token, then checks the rule for the message type:

| Message type | Identity fields | Auth rule | Notes |
|---|---|---|---|
| `handshake` | none | Always allowed | Pre-auth by definition |
| `list` | none | Always allowed | Read-only fleet enumeration |
| `diagnostics` | none | Always allowed | Read-only health check |
| `create` | `parent_id` | Allowed; `parent_id` forced to authenticated session | Agents can only create children of themselves |
| `fork` | `source_session_id` | Self or descendant | Source must be caller's own session or descendant |
| `attach` | `session_id` | Self or descendant | Grants PTY; subsequent `ChannelData` frames authorized by the attach |
| `detach` | none | Always allowed | Only affects current connection |
| `stop` | `session_id` | Self or descendant | |
| `delete` | `session_id` | Self or descendant | |
| `type` | `session_id` | Self or descendant | |
| `resume` | `session_id` | Self or descendant | |
| `restart` | `session_id` | Self or descendant | |
| `rename` | `session_id` | Self or descendant | |
| `star` / `unstar` | `session_id` | Self or descendant | |
| `set_status` | `session_id` | Self only | Agent sets its own status |
| `status_report` | `session_id` | Self only | Hook reporting — daemon should force from token |
| `status` | `session_id` | Self or descendant | Status query |
| `logs` | `session_id` | Self or descendant | Terminal output is sensitive |
| `screen_preview` | `session_id` | Self or descendant | Terminal contents are sensitive |
| `screen_snapshot` | `session_id` | Self or descendant | Terminal contents are sensitive |
| `msg_pub` | `sender_id`, `stream` | Sender forced to self; inbox targets: self, descendant, or direct parent | See messaging rules below |
| `msg_inbox` | none (derived from token) | Authenticated only; derives `inbox:<session-id>` from token | Preferred way for agents to read their own inbox |
| `msg_sub` | `subscriber`, `stream` | Subscriber forced to self; inbox streams rejected for authenticated callers | Use `msg_inbox` for inbox reads |
| `msg_ack` | `subscriber`, `stream` | Subscriber forced to self; inbox streams rejected for authenticated callers | |
| `msg_topics` | `subscriber` | Subscriber forced to self; inbox streams filtered out for authenticated callers | |
| `approval_request` | `session_id` | Self only | Hook path |
| `approval_respond` | `request_id` | Human-only (reject if token present) | Agents must not approve other sessions' tool calls |
| `approval_list` | none | Always allowed | Read-only |
| `mcp_connect` | `session_id` | Self only | Session ID must match token; empty session_id rejected for authenticated callers |
| `resize` | none | Always allowed | Only affects current attached connection |
| `reload` | none | Human-only (reject if token present) | Daemon admin operation |
| `upgrade` | none | Human-only (reject if token present) | Daemon admin operation |

"Self" means the authenticated session matches the target. "Descendant" includes self (consistent with existing `collectDescendants` which includes the root). "Human-only" means the operation is rejected when a token is present — these are admin operations.

**Key design choice: the daemon overrides identity fields from the token** rather than merely comparing them. For `sender_id`, `subscriber`, and `status_report.session_id`, the daemon replaces the payload value with the token's session. This is defense-in-depth: even if validation has a bug, identity claims can't be spoofed.

#### Messaging rules

Messaging has nuanced target semantics because the target is encoded in the stream name:

- **Arbitrary topic publish** (`gr msg pub --topic X`): Allowed for any authenticated session. `sender_id` forced to self.
- **Inbox publish** (stream = `inbox:<session_id>`): Parse the target session ID from the stream. Allowed if target is self, a descendant, or the **direct parent** of the authenticated session. This preserves `gr msg send --parent`.
- **Inbox subscribe** (stream = `inbox:<session_id>`): Only own inbox. Agents cannot read other sessions' inboxes.
- **Topic subscribe**: Allowed for any topic. `subscriber` forced to self.
- **System streams** (`_system.*`): Read-only access same as topics.

The child→parent messaging carve-out is essential for the existing hierarchical coordination workflow documented in AGENTS.md.

#### State migration

State version v9 → v10. The migration generates tokens for all existing sessions using `crypto/rand`.

**Error handling**: Token generation failure must not cause state loss. The current `LoadState` returns `NewState()` on migration failure — for token migration, this would discard all sessions. Instead, token generation errors should fail the migration (and thus daemon startup) loudly. Use `io.ReadFull(rand.Reader, b)` and propagate errors.

**Uniqueness**: Collisions with 32 random bytes are astronomically unlikely, but the migration and `Create`/`Fork` paths should verify uniqueness against existing tokens and regenerate on collision.

#### Running sessions after migration

Already-running agent processes will have `GRAITH_SESSION_ID` but no `GRAITH_TOKEN` in their live environment (env is fixed at exec time). These sessions will be treated as "token stripped" and rejected for identity-claiming operations.

**Rollout strategy**: After upgrading the daemon, existing running sessions must be restarted/resumed to receive their token. `gr doctor` should detect and warn about sessions that have a token in state but were started before the token was generated (compare `LastStartedAt` with migration timestamp or check for a `TokenDeliveredAt` field).

#### Sandbox interaction

The token is an environment variable, not a file. Sandboxed agents can read their own `$GRAITH_TOKEN` but cannot read `state.json` (which contains all sessions' tokens) because the data dir is not in the sandbox read/write dirs.

**Important**: The sandbox does NOT prevent an agent from modifying its own environment or the environment of child processes it spawns. An agent could run `env -u GRAITH_TOKEN gr ...` — but because the daemon also checks for `GRAITH_SESSION_ID`-claiming messages without tokens, this alone doesn't grant access. The agent would need to also unset `GRAITH_SESSION_ID`, which is "active circumvention" (non-goal).

Unsandboxed agents can read `state.json` and extract other sessions' tokens. This is acknowledged and surfaced via `gr doctor`.

#### `gr doctor` checks

1. **Token presence**: Verify all non-deleted sessions have a token in state (not just running — stopped sessions need tokens for resume)
2. **Token delivery**: Warn about running sessions that were started before token migration (need restart to receive `GRAITH_TOKEN`)
3. **File permissions**: Assert `state.json` parent dir is mode 0o700; check file mode too
4. **Sandbox warning**: If sandbox is disabled and there are multiple sessions, warn: "Agents can read state.json and impersonate other sessions. Enable sandbox for session isolation."
5. **Data dir exposure**: If sandbox is enabled, verify no sandbox read/write dir is equal to or an ancestor of the `state.json` path (accounting for symlinks and path resolution). Note: subdirectories of the data dir (store paths, tmp) are legitimately granted — the check must be precise to avoid false positives.

#### Token hygiene

- Tokens must **never** appear in daemon logs, `SessionInfo`, `DiagnosticsMsg`, error messages, or responses
- `slog` fields should redact token values
- Token comparison should use a reverse map (`map[string]string` token→sessionID) for O(1) lookup; `subtle.ConstantTimeCompare` is optional under the local-socket threat model but preferred

#### Architecture

```
Agent Process                    CLI (gr)                         Daemon
─────────────                    ────────                         ──────
GRAITH_TOKEN=abc123    →    reads $GRAITH_TOKEN
GRAITH_SESSION_ID=x1        from env, includes in
                            every Envelope.Token
                                     │
                                     ▼
                            ┌──────────────────┐
                            │ {"type":"set_status",    │
                            │  "token":"abc123",       │
                            │  "payload":{             │
                            │    "session_id":"x1",    │
                            │    "text":"Working"}}    │
                            └──────────┬───────┘
                                       │ Unix socket
                                       ▼
                              ┌─────────────────┐
                              │ Validate token:  │
                              │ abc123 → x1? ✓   │
                              │ x1 == target? ✓  │
                              │ Process message  │
                              └─────────────────┘

Human (no GRAITH_SESSION_ID):
                            ┌──────────────────┐
                            │ {"type":"stop",          │
                            │  "payload":{             │
                            │    "session_id":"x1"}}   │
                            └──────────┬───────┘
                                       │
                                       ▼
                              ┌─────────────────┐
                              │ No token, no     │
                              │ session claim →   │
                              │ Human, allow all  │
                              └─────────────────┘
```

#### Pros

- Stateless per-request auth — no connection state to track
- Single validation point in the handler (after envelope decode, before dispatch)
- Individual message types don't need modification
- Natural fit with the existing protocol (messages already carry session_id in payload)
- Prevents impersonation by cooperative agents: identity is forced from the token when present
- Works identically for sandboxed and unsandboxed agents at the protocol level

#### Cons

- Slight wire overhead (64-char token on every message) — negligible for control messages
- Tokens in `state.json` are readable by unsandboxed agents (mitigated by `gr doctor` warning)
- An agent that unsets both `GRAITH_SESSION_ID` and `GRAITH_TOKEN` appears as human — but loses all session functionality
- No token rotation — a leaked token is valid for the session's lifetime (rotation-on-resume is future work)

### Proposal 2: Handshake-Level Authentication

Send the token once during the handshake, associate it with the connection, and validate subsequent messages against the connection's authenticated identity.

#### Pros

- Token sent once, not per-message
- Slightly less wire overhead

#### Cons

- Requires per-connection state tracking in the handler
- More complex: handler must maintain `authenticatedSessionID` alongside `attachedSessionID`
- Validation logic is spread across handshake and message dispatch
- Connection state can get stale if sessions are deleted mid-connection
- Handler hands off to goroutines for `logs --follow`, `msg_sub --follow`, `approval_request`, `mcp_connect` — threading connection identity through these takeover loops adds complexity

### Considered alternatives

**Per-session Unix sockets**: Give each session its own socket path inside a per-session directory; the sandbox only exposes that session's socket. Identity = which socket you connected on. More robust (OS-enforced) but significantly more complex: multiple listeners, routing, session lifecycle management of sockets, and changes to client connection logic.

**`LOCAL_PEERPID` / process ancestry**: On connect, the daemon reads the peer PID from the socket and walks the process tree to find the owning session. Identity derived from kernel-provided credentials. More robust but OS-specific (macOS `LOCAL_PEERPID`, Linux `SO_PEERCRED`), fragile against process tree changes, and complex to implement reliably.

Both are worth considering as future hardening but are significantly more invasive than the token approach. The token design provides a good foundation that these could build on later.

## Consensus

Proposal 1 (per-request token on the envelope) is recommended. It is simpler to implement, easier to reason about, and aligns with the existing stateless message protocol. The "require token when claiming identity" rule closes the main bypass without complex OS-level mechanisms.

## Implementation Notes

### Files to modify

| File | Change |
|------|--------|
| `protocol/messages.go` | Add `Token` field to `Envelope` |
| `daemon/state.go` | Add `Token` field to `SessionState`, bump version to 10, add migration with error-checked `crypto/rand` |
| `daemon/daemon.go` | Generate token in `Create`/`Fork`/`Resume`, set in env; add reverse token map; add `authorizeMessage` helper |
| `daemon/orchestrator.go` | Generate token for orchestrator session, add to env and envKeys |
| `daemon/handler.go` | Call `authorizeMessage` after envelope decode, before dispatch; override identity fields from token |
| `client/client.go` | Add `Token string` field to `Client`, set from `os.Getenv("GRAITH_TOKEN")` at connect time, inject in `SendControl` |
| `cli/doctor.go` | Add token/sandbox security checks (5 checks listed above) |

### Client encode path

Token injection must happen in `Client.SendControl`, **not** in `protocol.EncodeControl`. The `EncodeControl` function is also used by:
- Daemon responses in `handler.go` (must not attach tokens)
- `gr doctor` raw probes (intentionally tokenless)
- Integration tests (need token fixtures or test bypass)

Add a `protocol.EncodeControlWithToken(msgType, payload, token)` or have `Client` inject the token into the marshaled envelope before writing the frame.

### Approval hook fail-closed

`gr approve-request` (cli/approve_request.go) currently defaults to allow-all if it cannot connect or gets an unexpected response. Under token auth, if the approval request is rejected due to auth failure, the hook must **block** (deny the tool call), not allow. This is critical because approvals are a security control.

### ChannelData authorization

Raw PTY data frames (`ChannelData`) carry no envelope and cannot include a token. Authorization for `ChannelData` is established by the preceding `attach` message. Once a connection is authenticated via `attach`, subsequent data frames on that connection are implicitly authorized for the attached session. This is the one place where connection-level state matters.

Similarly, nested handler loops (`logs --follow`, `msg_sub --follow`, `mcp_connect`) read additional control frames but only `detach` — which is safe to allow without re-validation as it only affects the current connection.

### Rollout

The token field is optional on the envelope (`omitempty`). The validation rule "reject identity-claiming messages without tokens" means:
- New daemon + new client: full auth enforcement
- New daemon + old client inside session: old client sends `session_id` without token → rejected. Session must be restarted with new client binary.
- New daemon + human CLI: no `session_id` claim → full access as before

This is NOT a silent rollout. After upgrading the daemon, agents need the new `gr` binary and sessions need restart. `gr doctor` should detect and report this state.

### References

- Safehouse sandbox: `internal/sandbox/sandbox.go`
- State persistence: `internal/daemon/state.go`
- Existing `generateID`: `internal/daemon/daemon.go:279` (uses `crypto/rand`)
- Handler dispatch: `internal/daemon/handler.go:66` (main switch)
- Orchestrator session creation: `internal/daemon/orchestrator.go`
- Approval hook: `internal/cli/approve_request.go`
- Message sender detection: `internal/cli/msg.go:395` (`detectSender`)
- Descendant collection: `internal/daemon/daemon.go:2261` (`collectDescendants`)
