---
title: "Design Doc: Transactional Remote Runtime Reload"
authors: Dougal Matthews
created: 2026-07-17
status: Implemented
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1316
---

# Transactional Remote Runtime Reload

Remote configuration reload becomes a transaction across the published config,
tailnet listener, TLS identity, and live connection policy. Security policy is
checked on every remote frame, while transport changes replace the whole runtime
generation fail-closed before the new config becomes visible.

## Background

The daemon serves its local Unix socket for its whole lifetime. When
`[remote].enabled` is true at startup, `internal/daemon/run.go` additionally
loads TLS material, creates either a tsnet or interface listener, and passes a
copy of `config.RemoteConfig` into the accept loop. `handleRemoteConn` resolves
WhoIs once and uses that same copy for the initial allowlist gate. The handler
then resolves device authorization for each control message, but does not
re-check the live remote enablement or allowlist, and attached input frames do
not pass through control-message authorization.

Configuration reload currently publishes a new `*config.Config` from
`SessionManager.applyConfig`. The file watcher callback cannot return an error,
so it has no way to reject a generation whose runtime side effects failed.

## Problem

The listener, TLS pin, accept gate, and authenticated connections all retain the
startup remote snapshot after a successful reload. Disabling the remote surface
or removing an allowed identity therefore does not revoke access, and transport
edits do not change the listening topology. Publishing those unapplied values
through config introspection gives operators a false security signal. A failed
replacement must not keep the old listener open or publish the candidate config.

## Goals

- Re-check live `remote.enabled`, the WhoIs allowlist, runtime generation, and
  effective paired-device role on every remote control or data frame.
- Replace the listener, TLS identity, pin, tsnet settings, and connections as one
  runtime generation whenever listener-derived state changes.
- Close the old generation before binding a replacement and leave no remote
  surface active when replacement fails.
- Publish a candidate config only after its remote runtime is ready to serve.
- Give automatic file reloads the same failure/rollback semantics as explicit
  `gr daemon reload`.
- Define conservative live semantics for `require_pairing` changes.

### Non-Goals

- Changing the remote protocol, pairing wire messages, or persisted device
  schema.
- Keeping remote connections alive across a transport or TLS replacement.
- Adding a second listener during a port migration for zero-downtime handover;
  that would violate the required close-old-first fail-closed ordering.
- Refactoring unrelated configuration reload side effects.

## Proposals

### Proposal 0: Do Nothing

Operators must restart the daemon after every remote policy edit. This conflicts
with the documented reload contract and leaves a security revocation silently
ineffective, so it is not acceptable.

### Proposal 1: Transactional Runtime Generations (Recommended)

Add a daemon-owned remote runtime controller. One runtime generation contains
the raw/TLS listener, transport/credential fingerprint, generation number, TLS
pin, cancellation scope, and every accepted connection (including connections
whose WhoIs or pairing authentication has not completed).

`applyConfig` is serialized. For a transport change it first invalidates the
active generation, closes the listener and all of its connections, and only then
loads TLS/tsnet state and synchronously binds the candidate listener. Binding is
the fallible boundary: on failure the method returns an explicit error, leaves
the old config published, and leaves remote access closed. On success it
publishes the candidate config, generation, and TLS pin under the session-manager
lock before starting the accept loop. Disabling follows the same path without a
replacement. Enabling prepares the first generation before publishing enabled.

Listener-derived equality includes mode, hostname, port, auth-key path and file
contents, tags, and the persisted TLS certificate/key generation. A hostname
change reissues the self-signed certificate with the existing key, updating its
name while preserving the SPKI pin. The tsnet server receives the configured
advertised tags.

An active tsnet generation has already consumed its auth key. If the unchanged
auth-key path later becomes unreadable, policy-only and unrelated reloads keep
that generation so revocation cannot be blocked by a spent credential. Any
change that actually requires a new listener still reads the key strictly and
fails closed if it is unavailable.

Allowlist and pairing-policy edits do not need a listener replacement. The new
policy is published atomically; the runtime then proactively closes connections
whose resolved identity is no longer allowed. Independently, the handler checks
the live policy before every frame, including attached input, closing the race
between config publication, WhoIs, authentication, and proactive connection
closure.

The connection origin carries its runtime generation and TLS pin. Production
connections must match the active generation. Proof-of-possession is verified
against the pin of the TLS connection that received the proof, rather than a
mutable daemon-global pin.

For `require_pairing`, the live value is an authority ceiling. Switching it to
false immediately downgrades every already-paired device to the read-only guest
role. Switching it back to true restores full authority only for devices whose
persisted record was originally approved for full access. Devices approved while
pairing was disabled remain read-only and must be re-paired for full authority.
This avoids turning a reload into an implicit privilege grant.

Local pairing approval is serialized with generation replacement. It either
returns the active generation's non-empty TLS pin or rejects the approval while
remote access is fail-closed; it never persists a device against an invalidated
generation.

The config watcher callback returns an error. It logs success only after the
runtime and config transaction succeeds; errors retain the previous published
generation.

The main trade-off is an intentional outage on listener replacement, including
failed replacement. That ordering is required for fail-closed security and also
avoids a second listener temporarily retaining revoked policy.

### Proposal 2: Live Policy Pointer with Restart-Only Transport

The handler could read the live allowlist while mode, port, TLS, and tsnet state
remain restart-only. This would fix the highest-risk revocation path with less
code, but contradicts the current reload contract and does not meet enable,
disable, or transport reconciliation requirements. Rejecting all transport edits
as restart-required is possible, but unnecessary because the listener can be
recreated safely with close-old-first ordering.

## Other Notes

### References

- [Issue #1316](https://github.com/d0ugal/graith/issues/1316)
- `internal/daemon/run.go` — daemon listener startup and config watcher wiring
- `internal/daemon/remote.go` — remote listeners, WhoIs, accept gate
- `internal/daemon/handler.go` — per-frame authorization and attach input
- `internal/daemon/session_config.go` — config publication
- `docs/design/2026-07-07-native-ios-app-design.md` — remote trust model

### Implementation Notes

The remote controller is attached only while `Run` owns a daemon context. Unit
tests that construct a bare `SessionManager` still exercise live policy through
generation-zero injected origins, while controller tests install fake listeners.
No slow listener, file, or connection operations run while `SessionManager.mu`
is held.

### Testing

Regression coverage exercises allowlist tightening and expansion for new and
authenticated connections; enable and disable; every listener-derived config
field; auth-key and TLS material changes; replacement bind failure and retry;
`require_pairing` downgrade/restore rules; and accept/WhoIs/auth races. Focused
daemon tests run under the race detector. Watcher tests prove a callback error is
not reported as a successful reload, and TLS tests prove hostname reissue keeps
the pin stable.
