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

## Proposals

### Proposal 0: Do Nothing

Keep command validation and sandbox signal policy as the boundary. This leaves
direct helper calls able to terminate the host daemon.

### Proposal 1: Shared trusted lifecycle guard (Recommended)

Use the existing guard installed at every host daemon/service mutation entry
point. Extend it to reject concrete agent/session execution markers even when a
caller sets `GR_AGENT_MODE=0`. The guard logs operation plus a fixed reason and
returns a credential-free error. Protocol reload and upgrade denials use the
same bounded caller description and audit event. Human CLI calls continue using
the protected human token and exact peer identity. Managed nono sandboxes use
`signal_mode = allow_same_sandbox`, preserving child-process management while
preventing an agent that clears its environment markers from signalling the
host daemon directly. Safehouse cannot provide this OS-level guarantee and
ignores `process-control` with a warning.

The fallback daemon launcher scrubs these caller markers from the child daemon
environment so daemon startup housekeeping does not misclassify itself. This
does not weaken the caller-side guard: the process invoking a lifecycle helper
still carries its markers and is refused.

This requires no wire change and covers direct lower-level calls, Linux direct
spawn, and macOS service-manager mutations. Private test seams can inject an
allow guard for hermetic tests without creating a production bypass.

### Proposal 2: New daemon lifecycle capability protocol

Add a separate capability exchange before the CLI signals the daemon. This could
make the daemon the sole issuer of lifecycle authority, but adds wire and
compatibility surface while still requiring local helper guards against direct
process signalling. It is not justified for this boundary.

## Other Notes

### References

- Issue #1552.
- `internal/testprocess/testprocess.go` — shared host-mutation guard.
- `internal/daemon/auth.go` and handler lifecycle cases — protocol decisions.
- `docs/design/2026-07-11-auth-identity-hardening.md` — credential model.

### Testing

Regression coverage exercises session-marker refusal, explicit human-context
allowance, direct host-mutation guard callers, and authenticated protocol
denials. Focused daemon, client, and daemon-service tests run with race coverage
before the full repository checks.
