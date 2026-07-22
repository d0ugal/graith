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

Graith will remove its built-in Model Context Protocol server, configuration,
agent injection, proxy transport, process management, and management commands.
Browser automation in sandboxed sessions moves to `agent-browser`; any other
native MCP configuration belongs entirely to the selected agent runtime.

## Background

Graith originally added MCP injection and a daemon-managed proxy so sandboxed
agents could use Chrome DevTools MCP. The subsystem grew to include global and
per-agent configuration, generated Claude and Codex settings, an MCP tool
server, a reconnecting proxy, daemon-owned child processes, sandbox and token
delegation policy, management commands and logs, a dedicated wire channel,
protocol declarations, and generated native artifacts.

`agent-browser` now provides browser automation inside Graith's sandbox without
that lifecycle or trust boundary. Agent runtimes can still load their own native
MCP configuration, but Graith need not interpret, rewrite, or supervise it.

## Problem

The MCP subsystem is a second process and protocol control plane inside the
session multiplexer. It increases upgrade, authentication, sandbox, reload, and
adoption risk even though its motivating browser workflow has a direct
replacement. Keeping only part of it would preserve most of those risks while
leaving an ambiguous ownership boundary between Graith and agent runtimes.

## Goals

- Remove every Graith-owned MCP command, config field, process, injection path,
  protocol message/channel, auth rule, native declaration, and dependency.
- Reject obsolete lifecycle/security configuration instead of silently
  ignoring it.
- Preserve ordinary sessions, daemon adoption, messaging, store, scenarios,
  sandboxing, and remote control.
- Make upgrade, downgrade, and mixed-version behavior explicit.
- Direct sandboxed browser automation to `agent-browser` without taking
  responsibility for unrelated native agent configuration.

### Non-Goals

- Disable or inspect MCP support configured directly in an agent runtime.
- Provide a compatibility proxy, deprecated hidden command, or protocol shim.
- Migrate Graith MCP definitions into any agent's native configuration.
- Redesign the remaining agent configuration or sandbox model.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Remove | The server, proxy, and management commands are Graith-owned lifecycle surfaces. |
| iOS | Remove declarations | Native clients must not advertise or reserve an MCP channel or protocol surface. |
| macOS | Remove declarations | Native clients must match the reduced daemon protocol and capability manifest. |

## Proposals

### Proposal 0: Do Nothing

Retain the current manager, proxy, server, injection, and security boundary.
This keeps a large cross-cutting subsystem for a use case now served directly by
`agent-browser`, so it does not meet the product goal.

### Proposal 1: Immediate, Fail-Closed Removal (Recommended)

Delete the MCP implementation and all registrations in one atomic change. The
configuration schema no longer has global `mcp_servers`, per-agent
`mcp_servers`, or `limits.mcp_log_read_bytes`. Loading or reloading a file that
contains one of those removed keys fails with an actionable migration error;
the keys are not accepted as inert compatibility data. `gr mcp` and
`gr mcp-proxy` become ordinary unknown commands.

Remove the MCP control messages and dedicated frame channel together with their
daemon handlers, remote authorization rows, Go manifest entries, and Swift
constants. An old client speaking an MCP message to a new daemon receives the
normal `unsupported control message` error. The new daemon does not retain a
special deprecated-message handler. A new client never sends those messages and
continues to operate non-MCP features against an old daemon.

Increment durable state from v26 to v27. MCP manager/process state was never
persisted, but `CreationConfig.Agent` could contain the former per-agent map.
Decoding the old typed snapshot projects that removed field out; the explicit
v26→v27 migration records the schema change, and the established pre-migration
backup preserves the original bytes for recovery. The next state save writes
only the v27 shape. An older binary refuses v27 state; downgrade therefore
requires restoring the v26 backup and a pre-removal configuration.

For an in-place upgrade, the old daemon performs its existing freeze-and-drain
step before it execs the new binary, so daemon-owned MCP children do not cross
the boundary. The new target preflight rejects obsolete config before exec.
Agent PTYs remain adoptable and keep running. A proxy already launched inside an
agent can observe only the unsupported-message failure after adoption; the new
daemon neither reconnects it nor starts a replacement server. Restarting or
resuming that session launches the agent without Graith MCP arguments. Migration
documentation tells users to restart long-lived sessions after upgrading.

There is no warning-only phase. Warnings would allow obsolete security and
lifecycle policy to appear active when it is not. Errors are used for stale
config and unsupported old protocol calls; removed CLI names use the standard
unknown-command error.

### Proposal 2: Keep a Deprecated Compatibility Shell

Retain commands, config fields, or protocol handlers that only print removal
warnings. This improves short-term discoverability but leaves dead product
surface, weakens schema and authorization completeness checks, and conflicts
with the requirement that Graith stop owning MCP. Migration documentation and
the fail-closed config error provide the necessary guidance without a shim.

## Other Notes

### References

- [Issue #1575](https://github.com/d0ugal/graith/issues/1575)
- `docs/design/2026-06-11-mcp-server-injection-design.md`
- `docs/design/2026-07-21-managed-mcp-caller-identity.md`
- `docs/design/2026-07-14-protocol-conformance-design.md`
- `internal/daemon/state.go`
- `internal/protocol/manifest.go`

### Implementation Notes

Remove handler cases and auth-matrix rows in the same edit, then regenerate the
protocol fixture. Remove the CLI capability entry from the source manifest and
regenerate its docs and Swift fixture. Delete the SDK dependency with
`go mod tidy`, regenerate the package graph, and keep historical changelog
entries as history rather than pretending the removed feature never shipped.

The user documentation should contain one concise migration note: delete the
removed config keys before upgrading/reloading, restart pre-upgrade sessions,
use `agent-browser` for sandboxed browser automation, and configure any other
native MCP integration directly in the agent runtime.

### Testing

Regression tests must prove that removed config is rejected, removed commands
are absent, old MCP control messages follow the generic unsupported path, v26
state migrates with its old bytes backed up and rewrites without the removed
field, and new/resumed/forked agents receive no MCP arguments or config files.
Config cases cover the global, per-agent, and removed limit keys, assert precise
migration guidance, and prove secret values are not echoed. Agent launch tests
also prove arbitrary user-owned native runtime arguments remain untouched; the
removal strips only Graith-generated wiring.

An upgrade integration fixture must build commit
`3fdb037103f6f32ef9d35210a7d920d44d2d18b7` as the exact old daemon and use the
branch binary as its target. It starts managed children before upgrading, then
proves the old server processes, daemon connections, stderr pipes/log writers,
and reconnect route are drained or unreachable without an orphan. The adopted
PTY remains live, an already-injected proxy receives only the unsupported
control-message failure, and a subsequent restart/resume launches with no
Graith-generated MCP argument, environment marker, or config file. This fixture
is decisive: if it contradicts the adoption design, the design must change
instead of adding a compatibility handler.

Protocol tests keep the removed message names out of the manifest and exercise
remote rejection through the generic unsupported path; auth-matrix completeness
must pass with no obsolete rows. Swift tests prove the removed channel and
manifest entries are absent. State tests cover the v26 backup, removed field,
v27 rewrite, and v26 binary downgrade refusal.

Existing daemon adoption, lifecycle, auth-matrix completeness, protocol
manifest, sandbox, remote, integration, Swift, docs, race, lint, vet, and broad
Go suites provide the non-MCP regression boundary.
