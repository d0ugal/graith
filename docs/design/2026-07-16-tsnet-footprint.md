---
title: "Design Doc: Isolating the optional tsnet dependency footprint"
authors: Dougal Matthews
created: 2026-07-16
status: Implemented
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1225
---

# Isolating the optional tsnet dependency footprint

graith's remote-control surface is disabled by default, yet every build links
the embedded Tailscale node (`tailscale.com/tsnet`) and its full dependency
graph — the gVisor userspace netstack and WireGuard. This doc measures that cost
and puts `tsnet` behind a `no_tsnet` build tag so interface-only deployments can
compile it out, while keeping `tsnet` in the default release artifacts so the
convenience default keeps working out of the box.

## Background

The remote-control feature (`docs/plans/2026-07-07-universal-app-remote-control.md`,
Task 9) lets the daemon expose a tailnet-facing control listener. It supports two
transports, selected by `[remote] mode`:

- **`tsnet`** — an *embedded* Tailscale node run inside the daemon
  (`tailscale.com/tsnet`). No host `tailscaled` required. This is the default
  mode and the most convenient, but `tsnet` links the whole embedded Tailscale
  stack: the gVisor userspace netstack, WireGuard, and ~190 `tailscale.com`
  packages.
- **`interface`** — bind the tailnet IP of an *existing* host `tailscaled`
  (`tailscale.com/client/local`). No embedded netstack.

Both listener implementations lived in `internal/daemon/remote.go`, so importing
`tailscale.com/tsnet` unconditionally pulled the netstack into every build
regardless of whether remote control was enabled or which mode was configured.

The original plan (Task 9, Step 6 — the "measurement gate") explicitly deferred
measuring the `tsnet` binary-size delta and deciding whether to isolate it
behind a build tag. This doc closes that gate.

## Problem

Every `gr` binary — including the overwhelming majority of installs that never
enable remote control — carries the embedded Tailscale dependency graph. That
cost was never measured, so the trade-off between "embedded `tsnet` is
convenient" and "it is a large, always-linked dependency" was implicit rather
than intentional. The plan called for measuring it and deciding deliberately.

## Goals

- Measure the release binary size, clean build time, and compiled package graph
  with and without `tsnet`.
- Based on the measurements, isolate `tsnet` behind the smallest maintainable
  boundary, or record a justified decision not to.
- Preserve fail-closed configuration behaviour when a build omits `tsnet`.
- Keep the default release artifacts' behaviour unchanged (both modes work).
- Test every supported build variant in CI and document which artifacts embed
  `tsnet`.

### Non-Goals

- Removing or deprecating Tailscale / remote-control support. This is about
  making the *optional embedded mode's* cost measured and opt-out, not about
  dropping the feature.
- Shipping a second set of `no_tsnet` release artifacts. The default artifacts
  keep `tsnet`; the tag is offered for those who build their own. (Adding a
  `no_tsnet` release variant is a possible future follow-up if demand appears.)
- Isolating `interface` mode's lighter `tailscale.com/client/local` dependency —
  it carries no netstack and is not the cost driver.

## Measurements

Reproducible on `go1.26.5 darwin/arm64`, from the repo root. Binary size and the
compiled package graph are deterministic; clean build times are machine- and
load-dependent (shown as a representative sequential pair).

```bash
# Binary size
go build -o /tmp/gr-tsnet ./cmd/graith
go build -tags no_tsnet -o /tmp/gr-notsnet ./cmd/graith
ls -l /tmp/gr-tsnet /tmp/gr-notsnet

# Compiled package graph
go list -deps ./cmd/graith | wc -l
go list -tags no_tsnet -deps ./cmd/graith | wc -l
go list -deps ./cmd/graith | grep -c tailscale.com
go list -tags no_tsnet -deps ./cmd/graith | grep -c tailscale.com
go list -deps ./cmd/graith | grep -c gvisor.dev
go list -tags no_tsnet -deps ./cmd/graith | grep -c gvisor.dev

# Clean build time (clear the cache between builds)
go clean -cache && time go build -o /tmp/gr-tsnet ./cmd/graith
go clean -cache && time go build -tags no_tsnet -o /tmp/gr-notsnet ./cmd/graith
```

| Metric                          | Default (`tsnet`) | `no_tsnet` | Delta            |
|---------------------------------|-------------------|------------|------------------|
| Binary size                     | 47.8 MB           | 30.4 MB    | **−17.4 MB (−36%)** |
| Compiled packages               | 707               | 480        | −227 (−32%)      |
| `tailscale.com/*` packages      | 193               | 71         | −122             |
| `gvisor.dev/*` packages         | 42                | 0          | −42              |
| Clean build (representative)    | ~25–37 s          | ~13–21 s   | consistently faster |

The cause is clear from the per-package dependency graphs:
`go list -deps tailscale.com/tsnet` pulls **42** `gvisor.dev` packages and 193
`tailscale.com` packages; `go list -deps tailscale.com/client/local` (interface
mode) pulls **0** `gvisor.dev` and 71 `tailscale.com` packages. The netstack is
entirely a `tsnet`-mode cost.

## Proposals

### Proposal 0: Do Nothing

Leave `tsnet` linked into every build. Simplest, no new build variant to
maintain. Rejected: the measurements show a 17 MB / 32-package-graph cost borne
by every install, including the default-disabled majority — exactly the implicit
trade-off the plan's measurement gate existed to make explicit. "Do nothing"
without measuring was the status quo; having measured, the cost is large enough
to warrant an opt-out.

### Proposal 1: Build tag to compile `tsnet` out (Recommended)

Split the tsnet-mode listener into a build-tagged file pair and keep the
interface listener and the mode dispatch always-compiled:

- `internal/daemon/remote.go` — the `RemoteListener` interface, identity
  helpers, the `interface`-mode listener, and `newRemoteListener` dispatch.
  Imports only `tailscale.com/client/local` + `apitype` (no netstack).
- `internal/daemon/remote_tsnet.go` (`//go:build !no_tsnet`) — the `tsnet`
  listener, `newTSNetListener`, and `const tsnetSupported = true`. This is the
  only file importing `tailscale.com/tsnet`.
- `internal/daemon/remote_notsnet.go` (`//go:build no_tsnet`) — a
  `newTSNetListener` stub that returns a clear fail-closed error, and
  `const tsnetSupported = false`. Imports no Tailscale package.

`newRemoteListener` is unchanged: it dispatches `"tsnet"` to `newTSNetListener`
(resolved by build tag) and `"interface"` to the always-present interface
listener. A `no_tsnet` build therefore still serves `interface` mode in full and
**fails closed** on `tsnet` mode — the daemon logs the error and leaves the
remote surface off (the local Unix socket is unaffected), consistent with how
interface mode already handles a missing tailnet IP at runtime.

Default builds (no tag) are byte-for-byte the same as before: `tsnet` is linked
and both modes work. The release pipeline is unchanged, so the published
artifacts keep embedding `tsnet`.

**Trade-offs.** One extra build variant to keep green (covered by a dedicated CI
job). The `no_tsnet` fail-closed for `tsnet` mode is a *runtime* error rather
than a config-load error, because whether `tsnet` is linked is a property of the
binary, not the config file, and the low-level `config` package must not depend
on the daemon's build variant. This matches the existing convention that remote
*provisioning* failures are runtime (listener) errors while only static config
contradictions are load errors.

### Proposal 2: Separate binary / package for the embedded node

Move the embedded node into its own binary or plugin so the main `gr` never
links it. Rejected as over-engineered for the need: `tsnet` must run *inside* the
daemon process (it owns the listener the daemon accepts on), so a separate binary
would require an IPC/hand-off boundary for what a build tag achieves with three
small files and zero runtime cost. The build tag is the smallest boundary that
the measurements justify.

## Consensus

Not yet reviewed.

## Other Notes

### References

- Issue: https://github.com/d0ugal/graith/issues/1225
- Plan: `docs/plans/2026-07-07-universal-app-remote-control.md` (Task 9, Step 6 —
  the measurement gate this closes).
- Code: `internal/daemon/remote.go`, `internal/daemon/remote_tsnet.go`,
  `internal/daemon/remote_notsnet.go`, `internal/daemon/run.go` (listener
  wiring), `internal/config/config.go` (`RemoteConfig`).
- User docs: `website/content/docs/configuration/access.md`.

### Testing

- Shared behaviour (identity allow/deny, unknown-mode dispatch) is covered in
  `remote_test.go` and runs under both build variants.
- `remote_tsnet_test.go` (`//go:build !no_tsnet`) asserts `tsnetSupported` and
  that `tsnet` mode builds a listener.
- `remote_notsnet_test.go` (`//go:build no_tsnet`) asserts `tsnetSupported` is
  false, that `tsnet` mode fails closed with a clear "not supported in this
  build" error, and that `interface` mode is *not* compiled out.
- CI: a `build-notsnet` job builds `-tags no_tsnet` and runs the daemon + config
  tests with the tag, so a change that breaks the alternate build or the
  fail-closed path goes red.

### Which artifacts embed `tsnet`

All published release artifacts (goreleaser `gr-linux` / `gr-darwin`, Homebrew,
AUR) are built **without** the `no_tsnet` tag and therefore embed `tsnet` — both
remote modes work out of the box. `no_tsnet` is a source-build option
(`go build -tags no_tsnet ./cmd/graith`) for interface-only deployments, not a
shipped artifact.
