---
title: "Design Doc: Live Remote Policy and Listener Reconciliation"
authors: Dougal Matthews
created: 2026-07-16
status: Implemented
reviewers: consensus round 2
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1316
---

# Live Remote Policy and Listener Reconciliation

Remote authorization and listener-derived state become one reloadable runtime:
revocations are checked against live policy on every inbound frame, while
transport changes replace the listener generation without leaving the old
generation reachable or publishing configuration that failed to bind.

## Background

The daemon serves its local Unix socket for its whole lifetime. When `[remote]`
is enabled, `internal/daemon/run.go` also creates a Tailscale-backed listener,
wraps accepted connections in TLS, resolves each peer with WhoIs, and passes
the resulting identity to the normal control-message handler. Gate 1 is the
configured tailnet allowlist; Gate 2 is the device pairing and
proof-of-possession flow.

Configuration reload currently swaps `SessionManager.cfg`, but the remote
listener, allowlist passed to its accept goroutine, TLS pin, and transport are
all startup snapshots. Device revocation closes registered connections, but a
config revocation does not own or close the listener generation's connections.

## Problem

An operator can disable remote access or remove a compromised identity, receive
a successful reload, and still leave both existing and future connections
authorized by the startup policy. Mode, hostname, port, auth-key path, tags,
and certificate state similarly remain unapplied. This breaks the documented
reload contract and defeats a primary incident-response control.

## Goals

- Make `enabled` and the WhoIs allowlist a live, fail-closed check on every
  remote control or data frame as well as after every WhoIs resolution.
- Close the old listener and every connection it accepted before attempting a
  transport replacement.
- Publish new configuration and TLS channel-binding state only after the new
  listener has bound successfully.
- Define how `require_pairing` changes affect existing device records and live
  connections.
- Keep reload/auth races data-race-free and preserve the deny-by-default remote
  message matrix.

### Non-Goals

- Preserving remote connections across a transport or pairing-mode change.
- Changing the wire protocol or the local Unix-socket authorization model.
- Making a failed remote replacement fall back to the old listener.

## Proposals

### Proposal 0: Do Nothing

Continue using the startup snapshot. This is operationally simple but leaves
revocations ineffective until restart, so it is unacceptable for an
authorization boundary.

### Proposal 1: Prepared Listener Generations with Live Policy (Recommended)

The `SessionManager` owns a remote runtime and serializes config applications.
A generation owns its transport, bound TLS listener, cancellation context,
accepted connections, and serving goroutines. It tracks a connection before
WhoIs begins so even a stalled authentication path is closed during replacement.

For an enable or listener-derived change, reload first stops and joins the old
generation, then constructs and binds the replacement synchronously. A bind or
TLS failure leaves remote access off and rejects the config application; the
old published config remains visible, accurately indicating that the attempted
generation was not applied. On success the daemon atomically publishes the new
config and TLS pin, then starts accepting. Disable performs only the stop and
publish steps.

Allowlist-only changes do not replace the transport. The config swap is the
authorization linearization point: every subsequently read frame checks the
new `enabled` value and allowlist, while the runtime closes connections whose
resolved identities are no longer allowed. Expanding the allowlist leaves
already-authorized connections alone and admits matching future peers.

`require_pairing=false` is WhoIs-only, read-only access. Toggling it closes live
remote connections so they re-resolve their role. Previously full paired
devices are guests while it is false and regain human rights when it is true;
devices enrolled as read-only while it was false remain guests until they are
re-paired. Remote session tokens retain their session-scoped role.

The config watcher callback returns an error. It logs success only after the
runtime and config commit, and logs a rejected reload otherwise. Manual reload
returns the same error to the caller.

### Proposal 2: Reject Every Remote Change as Restart-Required

Comparing the old and new remote blocks and rejecting transport changes would
avoid stale introspection, but it would make disable and incident-response
revocation depend on a daemon restart. Live per-frame policy is still required,
and generation replacement is tractable, so this is needlessly restrictive.

## Other Notes

### References

- `docs/design/2026-07-07-native-ios-app-design.md` — remote transport, pairing,
  role model, and authorization matrix.
- `internal/daemon/run.go`, `remote.go`, `handler.go`, and `session_config.go`.
- Issue #1316.

### Implementation Notes

Slow listener creation, close, and goroutine joins run outside `sm.mu`. A
separate config-application mutex serializes overlapping fsnotify and manual
reloads. TLS hostname changes reissue the persisted certificate with the same
private key, preserving its SPKI pin; tsnet generations apply configured tags.

### Testing

Unit tests cover allowlist tightening and expansion, enable in both directions,
active-connection closure, transport replacement, replacement failure, WhoIs
and reload races, pairing-mode transitions for existing devices, and live
per-frame denial. Daemon tests run under `-race`; protocol fixtures remain
unchanged because no wire type changes.
