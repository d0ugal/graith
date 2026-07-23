---
title: "Design Doc: Push-driven GitHub PR/CI awareness"
authors: graith maintainers
created: 2026-07-23
status: Draft
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1602
---

# Push-driven GitHub PR/CI awareness

This design adds an explicitly opt-in, experimental local webhook accelerator to
PR watching. It wakes only matching sessions and coalesces bursts, while the
existing `gh` reconciliation remains authoritative and continues at a slower
cadence whenever the accelerator is unavailable.

## Background

`internal/daemon/prwatch.go` resolves a worktree branch to a pull request and
uses `gh` to fetch checks, reviews, and comments. A cursor and existing trust
and jail paths turn authoritative differences into notifications. The ref
watcher already provides a transport-independent kick boundary for local Git
changes. GitHub webhook payloads can provide a similar hint, but are not a safe
source of aggregate CI or comment content.

## Problem

Polling every eligible session consumes API quota and delays feedback across a
fleet. GitHub deliveries are also duplicateable, delayed, and out of order, so
using payload state directly could regress cursors or bypass comment trust.

## Goals

- Wake eligible sessions quickly for check, PR, review, and comment changes.
- Authenticate, validate, deduplicate, bound, and coalesce deliveries.
- Keep polling authoritative, enabled, observable, and resilient to push failure.
- Make the default installation expose no listener or public hook.

### Non-Goals

- Hosted Graith relay infrastructure, a GitHub App, or a public listener.
- Treating `gh webhook forward` as production handling. GitHub documents it as
  testing/development only, repository/organization scoped, permissioned, and
  limited to one active forwarder per repository or organization.
- Passing webhook comment/review bodies to agents.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | Configuration and `gr doctor` are the safe operator surface. |
| iOS | Excluded | The daemon and its loopback process own this transport; mobile clients only consume state. |
| macOS | Excluded | No GUI listener or hook administration is needed for the experimental slice. |

## Proposals

### Proposal 0: Do Nothing

Polling remains robust and simple, but pays the API and latency cost for every
session. It is retained as the fallback, not selected as the only approach.

### Proposal 1: Opt-in `cli/gh-webhook` forwarding to loopback (Recommended)

For an explicit repository allowlist, the daemon binds an unguessable route on
loopback, generates a high-entropy secret, and owns one configured
`gh webhook forward --secret ...` process. The receiver verifies the exact body
with constant-time HMAC-SHA256, delivery/event headers, repository identity, and
configured repository target before parsing a minimal hint. A bounded delivery
cache rejects replays; a bounded debounce queue sends repository/PR/head kicks
through the existing PR poll loop. The refresh fetches all state with configured
`gh`, preserving cursor monotonicity, author trust, and jail quarantine.

The process is best effort: missing extension or permissions, hook conflicts,
exit, cancellation, restart, and disconnect mark push degraded and schedule
bounded retries. Poll cadence remains enabled (with a configurable slower
terminal cadence) and diagnostics expose state, errors, accepted deliveries,
dedupe/coalescing/drop counts, kicks, and authoritative refresh counters.

This is deliberately bounded by GitHub's official constraints: `gh webhook
forward` is a testing/development tool, supports repository or organization
webhooks, requires webhook administration permission, allows only one active
forwarder per repository or organization, and forwards original delivery
headers. Production relay/tunnel infrastructure is not implied.

### Proposal 2: User-managed HTTPS relay/tunnel

An operator could forward GitHub HTTPS deliveries to the daemon. This adds
certificate, ingress, address, and tunnel lifecycle complexity and still needs
the same validation. It is deferred until a supported production transport is
designed.

### Proposal 3: GitHub App, hosted relay, and outbound authenticated stream

This would solve production reachability and permission lifecycle centrally, but
requires hosted infrastructure, an App, tenant isolation, enrollment, and
protocol work. It is explicitly deferred from #1602.

## Other Notes

### Security and trust boundaries

Only loopback is bindable, the route and secret are generated per daemon
generation, request bodies and queues are bounded, and malformed/untrusted
deliveries are rejected without affecting polling. Payload text is discarded;
`gh` fetches comments and the existing allowlist/association and jail code makes
the delivery decision. Repository, PR, and head matching prevents unrelated
sessions from waking.

### Lifecycle, configuration, and privacy

Push is disabled by default. Configuration is a small `[pr_watch.push]` block:
`enabled`, an explicit repository allowlist, bind host restricted to loopback,
body/queue/dedupe limits, debounce, and retry cadence. Forwarders are shared by
repository, stop on daemon cancellation/restart, and restart with backoff.
The push block is read at daemon startup; changing its allowlist, route, bind
address, or secret requires a daemon restart, which closes the old listener and
forwarders before replacing them. Config rendering redacts the secret. The
`gh-webhook` extension currently requires `--secret`, so child-process argv can
expose it to local process inspectors; this is a bounded local threat of forged
hints only, and is an experimental limitation. Payload bodies are never
persisted or logged; only delivery IDs and bounded operational counters are
retained in memory.

### Diagnostics and failure fallback

Push state is reported as disabled, starting, ready, degraded, or stopped;
degraded push is a warning while polling is healthy. Counters permit comparison
of push hints, authoritative `gh` refreshes, API commands, notifications,
duplicates, coalesced bursts, rejected deliveries, and dropped queue work.

### References

- Issue #1602 and `internal/daemon/prwatch.go`, `ghpr.go`, `prrefwatch.go`.
- `docs/design/2026-06-25-pr-ci-awareness-design.md`.
- `docs/design/2026-07-11-pr-comment-author-trust-design.md`.
- `docs/design/2026-07-13-pr-comment-jail-design.md`.
- `docs/design/2026-07-14-pr-ref-watch-design.md`.
- [GitHub webhook forwarding](https://docs.github.com/en/webhooks/testing-and-troubleshooting-webhooks/using-the-github-cli-to-forward-webhooks-for-testing).
- [GitHub webhook events and deliveries](https://docs.github.com/en/webhooks/webhook-events-and-payloads).

### Testing

Hermetic tests cover HMAC, loopback/route bounds, malformed and oversized
deliveries, replay expiry, repository/PR targeting, coalescing, lifecycle retry,
and authoritative refresh separation. Daemon and config tests run with `-race`;
integration and documentation checks run before shipping.
