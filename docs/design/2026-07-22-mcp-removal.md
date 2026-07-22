---
title: "Design Doc: Remove Graith-Owned MCP Support"
authors: issue-1575-remove-mcp (agent)
created: 2026-07-22
status: Accepted
reviewers: (none yet)
informed: graith-bug-prioritization
issue: https://github.com/d0ugal/graith/issues/1575
---

# Remove Graith-Owned MCP Support

## Background

Graith added MCP management so sandboxed agents could use Chrome DevTools.
`agent-browser` now provides browser automation inside the sandbox without a
second Graith-owned process and protocol control plane.

## Problem

The built-in server, proxy, configuration, agent injection, managed children,
authentication, transport, native declarations, and diagnostics form a broad
lifecycle and security boundary whose motivating use case no longer exists.
Partial removal would keep that boundary ambiguous.

## Goals

- Remove every Graith-owned MCP surface and dependency.
- Reject obsolete lifecycle configuration rather than silently ignoring it.
- Preserve ordinary sessions, adoption, messaging, store, scenarios, sandbox,
  remote control, and agent-native configuration.
- Use `agent-browser` for sandboxed browser automation.

### Non-Goals

- Configure, inspect, secure, or migrate an agent runtime's native MCP setup.
- Retain a compatibility proxy, hidden server, command, or wire path.
- Redesign unrelated agent configuration.

## Platform support

| Surface | Decision |
|---------|----------|
| CLI | Remove server, proxy, and management commands. |
| iOS | Remove protocol and channel declarations. |
| macOS | Remove protocol and channel declarations. |

## Proposals

### Proposal 0: Do Nothing

Keep the subsystem. Rejected because it preserves a large trust boundary for a
use case now served directly by `agent-browser`.

### Proposal 1: Immediate, Fail-Closed Removal (Recommended)

Delete the implementation and registrations atomically. Removed CLI names are
ordinary unknown commands. Old control messages use the generic unsupported
message response; no deprecated handler or protocol-version bump is needed for
surviving messages. A new client never sends those calls to an old daemon.

The schema drops global and per-agent `mcp_servers` plus
`limits.mcp_log_read_bytes`. Startup and reload reject these keys with migration
guidance and never echo their values. Agent-native settings and arguments remain
untouched.

Durable state advances from v26 to v27. Typed decoding projects the removed
per-agent field out before the next save. The exact v26 bytes are backed up
first; backup failure aborts cold start or exec adoption before migration,
state writes, listener publication, or session handoff. An older binary refuses
v27, so downgrade requires restoring the v26 backup and old configuration.

During in-place upgrade, the old daemon drains its managed children before exec.
Live PTYs remain adoptable, but an already-running proxy cannot reconnect.
Restarting or resuming relaunches the agent without Graith-generated wiring.
There is no warning-only phase because obsolete security settings must not
appear active.

### Proposal 2: Deprecated Compatibility Shell

Keep inert commands, config, or protocol handlers that only warn. Rejected
because dead lifecycle surface weakens schema and authorization completeness and
contradicts full ownership removal.

## Other Notes

### References

- [Issue #1575](https://github.com/d0ugal/graith/issues/1575)
- `docs/design/2026-06-11-mcp-server-injection-design.md`
- `docs/design/2026-07-21-managed-mcp-caller-identity.md`

### Implementation Notes

Remove handlers with their auth rows, regenerate protocol and capability
artifacts, remove the SDK dependency, and update the package graph. User docs
tell users to delete obsolete keys, restart pre-upgrade sessions, use
`agent-browser`, and keep unrelated native integration config in the agent
runtime.

### Testing

Cover obsolete-config rejection without value leakage; absent commands,
injection, processes, handlers, auth rows, channel, native declarations, and
generated entries; v26 backup/migration/downgrade; ordinary create/fork/resume;
and generic old-message rejection. An integration fixture builds exact revision
`3fdb037103f6f32ef9d35210a7d920d44d2d18b7`, upgrades it to this branch, and
proves managed resources drain, live PTYs adopt, stale proxies fail, and the
next launch contains no Graith-generated wiring.
