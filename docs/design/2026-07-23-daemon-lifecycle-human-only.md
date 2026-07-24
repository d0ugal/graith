---
title: "Design Doc: Human-only daemon lifecycle control"
authors: Dougal Matthews
created: 2026-07-23
status: Accepted
reviewers: (none)
informed: (none)
issue: https://github.com/d0ugal/graith/issues/1552
---

# Human-only daemon lifecycle control

Daemon lifecycle control must remain exclusively human authority while session
credentials retain ordinary session-management authority.

## Background

The CLI authenticates to the daemon, obtains an exact Unix peer identity, and
then signals that process for stop and clean restart. Service-manager paths also
launch, stop, replace, or remove the managed daemon. These host mutations are
outside the ordinary session authorization boundary.

## Problem

A session can invoke the CLI or lower-level lifecycle helpers from inside its
sandbox. A Cobra-only denial is insufficient because helper calls can bypass
the command. Environment markers are useful audit context but are not
unforgeable credentials, so the sandbox must independently prevent direct host
process signalling.

## Goals

- Reject stop, restart, replacement, and service removal from session contexts.
- Keep human CLI lifecycle commands working and preserve `gr stop <session>`.
- Enforce the decision at process/service mutation primitives and protocol
  handlers, with bounded audit logging that never includes credentials.

### Non-Goals

- Restricting ordinary session lifecycle operations authorized by session
  credentials.
- Replacing the existing exact-PID/start-time signalling protections.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | Owns daemon and service lifecycle commands. |
| iOS | Excluded | No host process/service signalling surface. |
| macOS | Targeted | Managed service registration and controller are protected. |
| Linux | Targeted | Direct daemon process signalling is protected. |

Safehouse cannot enforce `process-control` signal isolation. Graith therefore
fails closed when that feature is requested with Safehouse; it does not
silently remove it. Operators needing process-control must select nono, whose
`signal_mode = "allow_same_sandbox"` preserves child management while denying
cross-sandbox signalling.

## Proposals

### Proposal 0: Do Nothing

Keep command validation and sandbox signal policy as the boundary. This leaves
direct helper calls able to terminate the host daemon.

### Proposal 1: Process-local positive lifecycle capability (Recommended)

Use the existing guard installed at every host daemon/service mutation entry
point, but require a positive capability established after the process has
opened and validated the protected human credential or launchd receipt. The
capability is an in-memory, process-local bit: it is not in argv, ordinary
environment, config, logs, or inherited child state. Direct helper calls from
an agent therefore fail closed even when every environment marker is removed.
The daemon protocol continues to resolve the protected human token as
`roleLocalHuman` and explicitly denies session tokens for host lifecycle
messages; the CLI establishes the local capability only after reading that
credential. Service bootstrap establishes it from the protected receipt.

This requires no wire change and covers direct lower-level calls, Linux direct
spawn, and macOS service-manager mutations. Private test seams can inject an
allow guard for hermetic tests without creating a production bypass.

### Proposal 2: New daemon lifecycle capability protocol

Add a separate capability exchange before the CLI signals the daemon. This
could make the daemon the sole issuer of lifecycle authority, but adds wire and
compatibility surface while still requiring local helper guards against direct
process signalling. The protected-token capability is smaller and preserves
the existing protocol.

## Other Notes

### Trust-model precondition

The boundary assumes the configured sandbox prevents an agent from reading the
human credential and service receipt. Same-UID unsandboxed execution can read
those files and is not distinguishable from a human by user-space Go code; on
that surface lifecycle helpers fail closed unless the process itself has
positively established the capability. `gr doctor` and sandbox configuration
remain the enforcement precondition and warning path.

### References

- Issue #1552.
- `internal/testprocess/testprocess.go` — shared host-mutation guard.
- `internal/daemon/auth.go` and handler lifecycle cases — protocol decisions.
- `docs/design/2026-07-11-auth-identity-hardening.md` — credential model.

### Testing

Regression coverage exercises clean-environment refusal, explicit human
authority, direct host-mutation guard callers, session-token denial, capability
non-inheritance and reset across restart, and authenticated protocol denials.
Focused daemon, client, and daemon-service tests run with race coverage before
the full repository checks.
