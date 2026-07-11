---
title: "Design Doc: Auth Identity Hardening Beyond the Cooperative Token"
authors: Dougal Matthews
created: 2026-07-11
status: Draft
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1044
---

# Auth Identity Hardening Beyond the Cooperative Token

## Background

The v1 agent-auth model (`docs/design/2026-06-22-agent-auth.md`, shipped) gives
each session a per-session bearer token (`GRAITH_TOKEN`), validated on every
control message in `resolveAuth` (`internal/daemon/auth.go:56`). It is a
**cooperative** scheme with a **fail-open default**: a request that arrives on
the local Unix socket with no token is treated as the local human and granted
full access.

This doc addresses issue #1044 — the deferred hardening the v1 doc itself named
as future work.

### Premise: the sandbox is the boundary

graith runs every agent as the same Unix user as the human. An **unsandboxed**
agent can already read `state.json`, extract other sessions' tokens, and do
anything the human can — no auth scheme changes that. This design therefore
hardens the **cooperative + sandboxed** case (the real threat model) and does
not attempt to defend a sandbox-disabled setup. `gr doctor` already warns when
the sandbox is off; that remains the mitigation there.

## Problem

The daemon cannot distinguish a human CLI from an agent that stripped its
`GRAITH_TOKEN`. Because the tokenless-local default is "human", an agent can run
`env -u GRAITH_TOKEN gr stop <victim>` and act with full human privilege against
any session. The v1 doc accepted this and made "an agent that unsets both
`GRAITH_TOKEN` and `GRAITH_SESSION_ID`" an explicit non-goal.

## Goals

- Remove the fail-open default: a caller with no valid credential is denied, not
  treated as the human.
- Keep the human CLI experience transparent — no manual credential setup.
- Bound the exposure of a leaked session token.
- No wire-protocol change if avoidable.

### Non-Goals

- Defending a sandbox-disabled deployment (see Premise).
- Kernel-enforced identity for an unsandboxed adversarial agent (sketched as
  Phase 3, recommended deferred).

## Proposal

Three phases, shipped in order. Phase 1 is the recommended first slice; Phase 3
is expected to stay deferred.

### Phase 1 — Daemon-written human token + fail-closed default

Replace the fail-open default with a **positive human credential**: prove you're
the human, or you're denied.

**Mechanism.**

- **Daemon startup** generates a human token and persists it to the data dir
  next to `state.json` (`~/.local/share/graith/human.token`, mode 0600). That
  dir is already 0700 and already excluded from every agent sandbox read-set, so
  there is no new trust surface. The token is persisted and reused across
  restarts — **not** regenerated per start, so a long-lived attached passthrough
  client is not cut off by `gr daemon restart`.
- **CLI** extends the existing credential read at `client.go:42`. The client is
  stateless (one process per command), so it re-reads per invocation:

  ```go
  token = os.Getenv("GRAITH_TOKEN")               // in a session → session token
  if token == "" { token = readHumanTokenFile() } // outside a session → human token
  ```

- **Daemon `resolveAuth`** becomes a two-step lookup, then fails closed:

  ```
  SessionForToken(token) hits   → roleSession / roleOrchestrator   (unchanged)
  token == humanToken           → roleLocalHuman
  else (incl. empty, local)     → roleNone / denied                ← the flip
  ```

  The only behavioural change is the last line: the `token == "" && !origin.Remote`
  branch (auth.go:70-74) that returns `roleLocalHuman` today now denies. A new
  CLI always carries *some* token (session or human), so the callers newly denied
  are old client binaries and non-graith processes — the intended lockout.

**No protocol change.** The human token rides the existing `Envelope.Token`
field. The remote/tailnet path (PoP + paired devices) is untouched.

**Coverage.** Closes both the strip-one case and the strip-both case: an agent
that unsets everything is now "nobody", not "human" — the case the v1 doc listed
as a non-goal, closed here for free.

**Breakage risk — low, not silent.** New CLIs pick up the file transparently.
Newly denied: pre-upgrade client binaries and tokenless callers — surfaced by
`gr doctor`, resolved by upgrading the client. Same "not a silent rollout" note
the v1 doc carries.

**Accepted edge.** An agent the human launches *directly* in a terminal (not via
`gr new`, so outside graith's sandbox) can read the file — but that process is
outside graith's control and already has full local access.

### Phase 2 — Session token rotation on resume/restart

On `Resume`/`Restart`, mint a fresh session token (`generateToken()`,
`state.go:378`), swap it into `SessionState.Token`, update the reverse
`tokenIndex` (drop old key, add new — the map at `daemon.go:279`/`:3336`), and
inject the new value as `GRAITH_TOKEN` into the respawned process (resume already
sets it at `daemon.go:2224`). Bounds a leaked session token to a single agent
lifetime.

**Breakage risk — very low.** Transparent: the new process only sees the new
token; any connection holding the old token belongs to the process being torn
down. `tokenIndex` is rebuilt from state on load (`daemon.go:279`), so a crash
mid-rotation self-heals. The Phase 1 human token is separate and unaffected.

### Phase 3 — OS-enforced identity (deferred)

Given Phase 1's fail-closed default and the accepted sandbox premise, Phase 3
only adds value for a kernel-level identity that survives an **unsandboxed
adversarial** agent — explicitly out of scope. Recorded for completeness;
**recommend deferring indefinitely**:

- **Option A — peer credentials.** Read the peer PID via `SO_PEERCRED` (Linux) /
  `LOCAL_PEERPID` (macOS) at accept in `HandleConnection` (`handler.go:52`), walk
  ancestry to a session's recorded `PID`/`PIDStartTime`. Fragile (process-tree
  reshaping, PID reuse, sandbox re-exec); must fail closed on any unmatched walk.
- **Option B — per-session Unix socket** inside each session's sandbox-exposed
  dir; identity = which socket you connected on. Robust but invasive (N
  listeners, socket lifecycle, client connection changes).

## Considered alternatives

- **`ClaimedSessionID` envelope field** (the issue's original Phase 1): client
  sends `GRAITH_SESSION_ID` as a verified claim; daemon rejects a claim without a
  matching token. **Rejected as redundant.** It only catches "claims a session
  but has no token", whereas Phase 1's fail-closed default denies *any*
  missing/invalid token regardless of claim — strictly more coverage, and no
  protocol change.

## Testing

- **Phase 1:** table-driven `resolveAuth` tests — valid human token → local
  human; session token → session; empty/garbage token locally → denied
  (regression-first: assert old code returns human, watch it fail, then flip).
  Doctor test for file mode 0600 and sandbox-exclusion. Integration: human CLI
  works transparently; an in-session caller with the token stripped is denied.
- **Phase 2:** unit test that resume rotates `SessionState.Token`, the old token
  no longer resolves via `SessionForToken`/`tokenIndex`, the new one does;
  integration test that a captured pre-resume token is rejected post-resume.
- **Phase 3 (if ever):** peer-cred mapping tests with fabricated
  PID/start-time fixtures including the fail-closed path; syscall glue behind an
  interface.
- Fixtures use Scots words (`braw`/`canny` sessions, `thrawn`/`dreich` for the
  denial cases).

## Implementation notes

| File | Change | Phase |
|------|--------|-------|
| `config/paths.go` | Add `HumanTokenFile` to `Paths` (under `DataDir`) | 1 |
| `daemon/daemon.go` | Generate/persist `human.token` at startup (`Run`/`LoadState`); expose the value for validation | 1 |
| `daemon/auth.go` | `resolveAuth`: two-step lookup + fail-closed local default | 1 |
| `client/client.go` | `:42` — fall back to reading `human.token` when `GRAITH_TOKEN` is empty | 1 |
| `cli/doctor.go` | Check `human.token` presence, mode 0600, sandbox-exclusion | 1 |
| `daemon/daemon.go` | Rotate session token in Resume + Restart before building env | 2 |
| `daemon/orchestrator.go` | Rotate if the orchestrator resumes via a separate path | 2 |

## Open questions

1. **Human token file location** — data dir (`human.token`, proposed) vs config
   dir. Data dir is already 0700 + sandbox-excluded, so leaning there.
2. **Phase 2 rotation triggers** — resume + restart only (issue text), or also a
   periodic/idle schedule? Fork already mints a fresh token.
3. **Confirm Phase 3 is out of scope** for #1044 given the sandbox premise — i.e.
   Phase 1 + 2 is the intended ceiling.
