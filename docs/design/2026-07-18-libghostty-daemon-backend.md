---
title: "Design Doc: Native libghostty daemon backend"
authors: Dougal Matthews
created: 2026-07-18
status: Accepted (native rollout candidate implemented; promotion pending soak)
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1432
---

# Native libghostty daemon backend

Graith will adopt `libghostty-vt` as its daemon terminal-screen model through
the public `go-libghostty` API and a restartable helper-process boundary. The
decision is **GO for staged adoption**: this change supplies a non-default
native candidate for extensive testing, while the current Go model remains
available only as rollback until the observation window is complete.

## Background

The daemon stores PTY bytes in persistent scrollback and separately feeds them
to a small terminal-screen model for previews and coherent screen captures.
Callers depend on the backend-neutral `Terminal` interface in
`internal/pty/terminal.go`, not on emulator-specific types.

The existing model wraps `github.com/charmbracelet/x/vt`. Graith also already
ships `libghostty-vt` to its native clients. The shared build pins Ghostty
commit `91f66da24527fa02d92b5fd0b41cd020f553a64c`, Zig 0.15.2, committed public
headers, and a checksum-verified universal Apple archive.

The original spike proved that Ghostty fits the narrow interface and showed
large parse and reconstruction gains, but recommended no-go because native
faults were in-process, construction and render errors were hidden, the binding
was handwritten, and native release metadata was incomplete. Those are design
problems rather than inherent blockers. This revision implements the stronger
architecture instead of accepting them.

## Problem

The Charm parser reaches an upstream panic on the reduced synthetic sequence
from #1430. Graith contains that Go panic, but reconstruction is expensive and
Charm also drops combining marks that the interface documents as one grapheme.
Keeping Charm indefinitely means retaining a known parser failure class and a
slower reconstruction path.

Calling Ghostty directly from `graithd` is not acceptable either. Go `recover`
cannot contain a C/Zig abort, segmentation fault, or memory corruption. A
native replacement must preserve daemon availability, make every fallible
operation observable, remain reproducibly buildable for supported targets, and
keep rollback independent of terminal state or protocol migrations.

## Goals

- Adopt the mature Ghostty VT model without exposing its API to daemon callers.
- Keep untrusted VT parsing and native terminal allocations outside `graithd`.
- Preserve graphemes, wide cells, cursor state, styles, palette/RGB colors,
  resize, alternate screen, and bounded scrollback reconstruction.
- Make construction, write, resize, snapshot, timeout, protocol, and exit
  failures observable and recoverable.
- Use the same synthetic corpus and operational workloads for both models.
- Produce exact-pin Darwin/Linux amd64/arm64 testing artifacts with native
  licensing and SBOM data.
- Retain a simple rollback until native soak and opt-in observation gates pass.

### Non-Goals

- Making the native backend the production default before this branch is soak
  tested. Issue #1432 authorizes the decision package and candidate, not that
  final release flip.
- Changing the wire protocol, scrollback format, iOS renderer, or macOS app
  renderer.
- Maintaining a second Graith-specific C binding beside `go-libghostty`.
- Removing the rollback implementation before the rollout window closes.
- Committing captured terminal output, native build products, or machine-local
  paths as evidence.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI/daemon on macOS and Linux | Targeted, staged | Explicit `libghostty` builds use the native helper candidate; ordinary releases remain the rollback until promotion. |
| iOS app | No behavior change | It already uses the shared Ghostty pin through its native renderer; daemon selection does not change the app renderer. |
| macOS app | No behavior change | The app renderer is independent, while a local daemon may use the tagged candidate. |

## Proposals

### Proposal 0: Do Nothing

Keep Charm indefinitely and retain only its panic containment. This preserves
the pure-Go release, but leaves the #1430 failure class, incomplete grapheme
behavior, and costly reconstruction in the serving path. It also duplicates
terminal semantics with the native clients. This is rejected.

### Proposal 1: Link libghostty directly into the daemon

Use `go-libghostty` in-process and statically link the pinned archive. This is
the smallest implementation and avoids IPC, but one native fault can terminate
the daemon and affect every session. VT data is untrusted, so process-wide
failure is the wrong containment boundary. This is rejected.

### Proposal 2: Process-isolated go-libghostty backend (Recommended)

Compile the same static library into the tagged Graith executable, but invoke
the executable as a private child for each terminal model. The daemon sends
bounded, versioned requests over a Unix socket. Only the child constructs
`go-libghostty` terminal/render objects and calls the C ABI. A crash loses one
reconstructable derived screen, not the daemon or persistent scrollback.

The helper is not a user-facing command. Package initialization recognizes an
exact private argument plus a private marker, serves inherited descriptor 3,
and exits. The parent passes no terminal bytes on argv or environment, discards
child stdio, and copies only sanitizer/race settings into an otherwise minimal
environment.

Requests cover create, write, resize, coherent snapshot, and close. Frames have
magic bytes, a protocol version, operation/status fields, and bounded lengths.
The implementation limits writes to 16 MiB, replies to 128 MiB, and viewports
to four million cells, validates color/style/UTF-8 fields, applies deadlines,
and kills the child after protocol, I/O, or timeout failure.

The public `Terminal` contract now returns resize errors, construction returns
an error, and an optional `Snapshot` capability provides one coherent viewport.
Rendering uses the bulk snapshot so the helper boundary is crossed once per
dirty frame. Compatibility `Cursor` and `Cell` methods read the cached frame.

When a write, resize, or snapshot fails, `Session` creates a new helper and
hydrates it from at most the configured persistent scrollback tail (128 KiB by
default). It swaps models only after successful construction and replay. If the
retained bytes reproduce the fault, it creates an empty helper rather than
looping on poison input. Errors contain operation/status classes, never PTY
content.

This proposal adds process startup and IPC cost, and one process per live
terminal has an operational footprint. Those costs are measurable and bounded;
they buy the failure isolation required for a native parser.

### Proposal 3: Maintain a narrow handwritten C shim

The first spike used a Graith-owned bulk C snapshot shim. It reduced cgo
crossings but duplicated ownership, lifetime, style, and evolving ABI logic
already covered by `go-libghostty`. Depending on the public wrapper gives the
project a stronger long-term maintenance boundary and a broader upstream test
suite. Optional bulk extraction belongs upstream; #1441 tracks that performance
improvement. The local shim has been removed.

## Other Notes

### Explicit decision

The result is **GO** for `libghostty-vt` through the isolated architecture. The
build tag is a rollout gate, not uncertainty about the chosen backend. This PR
should merge as the testing candidate; production promotion follows the gates
below. Charm is retained only because a rollback that has already compiled and
passed the same corpus is more useful than a theoretical rollback.

### API fit

| Graith operation | go-libghostty mapping | Result |
|------------------|-----------------------|--------|
| `Write` | `Terminal.VTWrite` | Clean fit; the C API consumes bytes without a parse result. Helper/protocol errors remain observable. |
| `Resize` | `Terminal.Resize` | Clean fit with explicit errors; Ghostty intentionally reflows retained content. |
| `Size` | Last acknowledged geometry | Exact for supported `uint16` PTY sizes and bounded by the viewport limit. |
| `Cursor` | `CursorX`, `CursorY`, `CursorVisible` | Included in the coherent snapshot. |
| `Cell`/snapshot | `RenderState`, row and cell iterators, raw cell, style, graphemes | Maps all rendered fields, wide tails, and background-only palette/RGB cells. |
| `Close` | Close cell iterator, row iterator, render state, terminal, then helper | Idempotent at the Graith boundary and child-reaped. |

Graith disables Kitty image storage and file, temporary-file, and shared-memory
image media because the daemon consumes text cells only. This narrows effects
available to untrusted terminal input.

### Compatibility evidence

`TestTerminalBackendCompatibilityCorpus` drives both factories with identical
generic data. It covers the default 128 KiB hydration size, grow/shrink resize,
cursor visibility, wide characters, emoji, combining graphemes, represented
styles, palette/RGB colors, alternate screen, device queries, and the reduced
#1430 sequence. #1446 expands fragmented control strings, margins, erase/wrap,
ZWJ/variation sequences, and background-only cells before handoff.

Current intentional differences are explicit assertions rather than hidden
normalization:

| Behavior | Charm rollback | Native candidate | Decision |
|----------|----------------|------------------|----------|
| `e` plus combining acute | Drops the combining mark | Returns one `é` grapheme | Ghostty matches the interface and is preferred. |
| Shrink `20x2` to `4x2` after `keep me canny` | Truncates to `keep` | Reflows and retains newer rows (`cann`) | Accept Ghostty reflow. |
| Enter alternate screen at column 11 | Homes the cursor | Retains the column | Preserve Ghostty semantics and characterize them. |
| Reduced #1430 bytes | Contained Go panic | Consumed; subsequent writes continue | Ghostty removes the known failure class. |

The full tagged PTY suite passes with backend-specific expectations. No fixture
contains captured output; the #1430 input remains a reduced 12-byte hexadecimal
sequence.

### Performance evidence

The direct in-process spike showed that Ghostty parsing and 4 MiB reconstruction
were orders of magnitude faster and used substantially less memory than Charm,
while resize and viewport extraction were slower. Those numbers are not reused
as final claims because the accepted design adds helper startup, IPC, coherent
encoding, and the public wrapper iterator path.

#1444 owns the operational measurement of helper start/close, 64 KiB parsing,
dirty `120x40` snapshots, alternating resize, 4 MiB reconstruction, Go
allocations, parent RSS, and child peak RSS. The final medians and exact commands
will replace this paragraph before the branch is handed off. This prevents a
faster superseded shim from being presented as evidence for the selected design.

### Build and release consequences

The candidate statically links Ghostty. Static linkage avoids loader paths,
side-by-side `.dylib`/`.so` version skew, and a second signed payload. Dynamic
linking adds operational failure modes without changing the crash boundary and
is rejected.

The path-scoped native workflow performs these checks:

- ordinary `CGO_ENABLED=0` builds for Darwin/Linux amd64/arm64, proving the
  rollback build does not select cgo or require a native archive;
- tagged Darwin arm64 execution and amd64/arm64 linking against the
  checksum-pinned universal archive;
- exact-source Linux amd64 execution and arm64 cross-link using Zig 0.15.2;
- committed-header diff against the exact Ghostty checkout;
- upstream `go-libghostty` tests against Graith's newer Ghostty pin;
- tagged PTY, focused race, and candidate executable builds; and
- testing artifacts that include the native SPDX and notice inventory.

An explicit tagged build without cgo, or on an unsupported OS, returns a
configuration error rather than silently changing emulator. Ordinary
GoReleaser remains pure Go during soak. Production promotion therefore needs a
native build matrix or promotion of the already-proven candidate workflow into
release packaging; it cannot use the current single-host pure-Go cross-build.

Local macOS development can use the checksum-pinned universal archive. An exact
source rebuild on the current macOS 26 host is blocked by Zig linking its build
runner against that SDK, while the archive links and runs. Linux source builds
are reproduced in CI. This is a documented local toolchain limitation, not a
claim that the library is unsupported on macOS.

### Pinning, licensing, security, and SBOM

Graith pins `go.mitchellh.com/libghostty` to pseudo-version
`v0.0.0-20260527181217-e9e1010f80b1`, whose exact commit is
`e9e1010f80b1ced0b7efcdb300f4838513c0816e`. The canonical source is
`https://tangled.org/mitchellh.com/go-libghostty`; the GitHub mirror remains
useful for tooling. Its API is not yet version-stable, so upgrades are reviewed
changes rather than floating module updates.

`libghostty-native.spdx.json` records the native closure that Go tooling cannot
see: Ghostty, uucode 0.2.0, Highway 1.2.0 at exact upstream commit, and the
vendored simdutf 5.2.8 amalgamation. `THIRD_PARTY_NOTICES.libghostty.md` carries
the elected MIT/BSD-3-Clause/Unicode notices. The helper script checks the Go
module, SPDX entries, notice pins, source manifests, Git commit, toolchain,
headers, and Apple checksum as one unit.

A pin rotation is atomic:

1. select the Ghostty and `go-libghostty` commits together;
2. rebuild every static target with the exact required Zig version;
3. synchronize and diff the public headers;
4. update `go.mod`, artifact digests, script constants, SPDX entries, licenses,
   and notices in one review;
5. run Ghostty lib-vt tests, the complete wrapper suite, shared compatibility,
   fuzz/race, benchmarks, and the four-target workflow; and
6. publish candidate archives only after provenance and binary linkage checks.

Native advisories are not visible to Go vulnerability scanners. Dependency
monitoring must therefore track Ghostty, uucode, Highway, simdutf, and the
wrapper sources explicitly. A critical fix requires rebuilding all static
artifacts; changing only `go.mod` is insufficient.

### Rollout and removal plan

1. Merge this non-default candidate and all issue #1444-#1448 evidence into the
   feature branch. Require default/native tests, race/fuzz, workflow matrix,
   privacy scan, and independent review to pass.
2. Run at least 1,000 tagged create/write/snapshot/resize/close cycles and a
   one-hour concurrent synthetic soak on supported native runners. Require no
   daemon exit, helper/FD leak, unbounded RSS growth, or unreconstructed final
   marker.
3. Offer an opt-in native cohort. Observe for seven consecutive days with zero
   native-attributed daemon crashes, successful bounded recovery for every
   injected helper failure, and resource/latency metrics within the documented
   benchmark envelope.
4. Make native the supported default for macOS/Linux and keep one tested
   pure-Go rollback release available.
5. After that rollback window closes, remove Charm, its selector, and
   backend-specific expectations. Keep the backend-neutral interface and
   persistent reconstruction path.

Rollback never converts state. Deploy the pure-Go build, stop creating native
helpers, and reconstruct each screen from its existing on-disk scrollback.
Sessions, PTYs, protocol messages, and stored bytes are unchanged. If a helper
fails during the candidate phase, the daemon performs the same replacement for
that one screen automatically.

### Testing

- `go test ./internal/pty`
- `scripts/libghostty-native.sh test`
- `scripts/libghostty-native.sh bench`
- `scripts/libghostty-native.sh memory`
- `scripts/libghostty-native.sh verify-metadata`
- tagged helper protocol fuzz targets and targeted `-race` tests
- default `go test -race ./...`, `go vet ./...`, repository lint, actionlint,
  shell validation, generated-file checks, and integration tests
- Darwin/Linux amd64/arm64 candidate linkage plus native execution where the
  runner architecture permits it
- full diff scan for binaries, captured output, identifiers, credentials,
  machine paths, and native build directories

### References

- [Issue #1432](https://github.com/d0ugal/graith/issues/1432)
- [Parent PR #1440](https://github.com/d0ugal/graith/pull/1440)
- [go-libghostty canonical repository](https://tangled.org/mitchellh.com/go-libghostty)
- [go-libghostty bulk snapshot follow-up #1441](https://github.com/d0ugal/graith/issues/1441)
- [Ghostty exact source pin](https://github.com/ghostty-org/ghostty/tree/91f66da24527fa02d92b5fd0b41cd020f553a64c)
- [libghostty-vt header at the pin](https://github.com/ghostty-org/ghostty/blob/91f66da24527fa02d92b5fd0b41cd020f553a64c/include/ghostty/vt.h)
- `internal/pty/terminal.go`, `internal/pty/terminal_ghostty.go`, and
  `internal/pty/terminal_ghostty_process.go`
- `scripts/libghostty-native.sh`, `libghostty-native.spdx.json`, and
  `THIRD_PARTY_NOTICES.libghostty.md`
