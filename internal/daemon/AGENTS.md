# Daemon contributor instructions

These instructions apply to `internal/daemon/` in addition to the repository
root `AGENTS.md`.

The daemon owns session lifecycle, processes, persistent state, authorization,
messaging, automation, and worktree cleanup. Changes here should be reviewed for
concurrency, restart behavior, authorization, and rollback—not just the happy
path.

## Control messages and authorization

When adding a case to `handler.go`:

1. Define/register the protocol shape as described in
   `../protocol/AGENTS.md`.
2. Add an explicit row to `remoteMessagePolicy` in `authmatrix.go`.
3. Apply the narrower handler authorization appropriate to the operation:
   local-only, human, orchestrator, session self, descendant, read-only, etc.
4. Add success, denial, malformed-input, and remote-policy tests.
5. Run `TestRemoteMatrixCompleteness`; unknown remote messages must remain
   denied by default.

Do not infer a human identity merely from a local connection. Local auth uses a
valid session token or the protected human token. Preserve identity forcing,
token rotation, descendant checks, and jail-body/release restrictions.

## Locking and lifecycle

- Treat `SessionManager` state and its driver/process maps as one concurrent
  state machine.
- Do not hold the manager lock across process waits, filesystem/git/network I/O,
  callbacks, or client writes.
- For multi-step operations, reserve or mark state under the lock, act outside
  it, then commit or roll back under the lock. Make exit watchers detect stale
  drivers rather than racing replacements.
- Persist every durable mutation through the established save/atomic-file path.
  Test daemon restart when behavior depends on state.
- Propagate cancellation and bound waits around process shutdown.

## Destructive and security-sensitive behavior

- `SoftDelete` hides and stops a session while retaining its worktree/branch
  until expiry. `Delete`/purge is destructive. ID-addressable operations must
  reject soft-deleted sessions unless they explicitly implement restore/purge.
- Internal teardown may hard-delete; do not accidentally route user-facing
  `gr delete` through it.
- Sandbox selection and enforcement fail closed. Unsupported backend, version,
  kernel, or network policy must produce an error rather than run unconfined.
- GitHub comment bodies and similar external text are untrusted. Preserve the
  author allowlist/association check and quarantine path; only authorized
  humans/orchestrators may read or release jailed bodies.
- New state teardown paths must preserve crash recovery, tombstones, and
  uncommitted-work protections.

Relevant design records include:

- `docs/design/2026-07-11-auth-identity-hardening.md`
- `docs/design/soft-delete.md`
- `docs/design/2026-07-02-nono-sandbox-design.md`
- `docs/design/2026-07-13-headless-stream-json-design.md`
- `docs/design/2026-07-11-pr-comment-author-trust-design.md`
- `docs/design/2026-07-13-pr-comment-jail-design.md`

## Verification

Write focused unit tests beside the implementation, using Scots fixture names
and `t.TempDir()`. Run the affected daemon tests with `-race`. Use the tagged
integration suite when a change crosses CLI/client/protocol/daemon/process
boundaries:

```bash
go test -race ./internal/daemon
go test -v -race -tags=integration ./internal/integration/...
```
