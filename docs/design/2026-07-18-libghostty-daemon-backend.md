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
headers, and the arm64 slice of a checksum-verified Apple XCFramework.

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
- Produce an exact-pin macOS arm64 testing artifact with native licensing and
  SBOM data. Linux-native validation is deferred to #1475.
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
| CLI/daemon on macOS arm64 | Targeted, staged | Explicit `libghostty` builds use the native helper candidate; ordinary releases remain the rollback until promotion. |
| CLI/daemon on macOS amd64 | Rollback only | Native libghostty is not a supported target; ordinary pure-Go builds remain supported. |
| CLI/daemon on Linux | Deferred | The selector and source-build tooling remain available, but native validation and promotion are tracked in #1475. |
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
The implementation limits writes to 1 MiB, replies to 32 MiB, graphemes to 1
KiB, viewports to 262,144 cells, and live helpers to 64. It validates
operation-specific lengths, status/payload combinations, color values, style
flags, cursor geometry, and UTF-8 before retaining decoded state. Every RPC has
a five-second deadline; protocol, native, I/O, and timeout failures poison the
connection. Failure handling synchronously closes the socket and requests
termination, then makes bounded graceful and forced reap attempts. A helper
that cannot be reaped within those waits remains registered and retains its
capacity slot until its actual `Wait` completion.

The public `Terminal` contract now returns resize errors, construction returns
an error, and an optional `Snapshot` capability provides one coherent viewport.
Rendering uses the bulk snapshot so the helper boundary is crossed once per
dirty frame. Compatibility `Cursor` and `Cell` methods read the cached frame.

When a write, resize, or snapshot fails, `Session` creates a new helper and
hydrates it from at most the configured persistent scrollback tail (128 KiB by
default). Replay is streamed in 512 KiB writes so a configured tail above 1 MiB
does not weaken or trip the per-request bound. It swaps models only after
successful construction and replay. If the
retained bytes reproduce the fault, it creates an empty helper rather than
looping on poison input. Errors contain operation/status classes, never PTY
content.

### Hardened helper boundary (#1445)

The private socket is created and marked `FD_CLOEXEC` while holding the same
fork lock used by `os/exec`, closing Darwin's create-versus-fork inheritance
window. Descriptor 3 is the only inherited non-stdio endpoint. Stdin is the
null device, stdout and stderr are discarded, and the environment contains only
the private marker plus explicitly allowlisted sanitizer/race settings. The
helper creates a new session so it has no controlling terminal.

Graceful close has a 250 ms exit grace period before kill and a bounded final
reap wait; repeated and concurrent close are idempotent. Dirty writes and
resizes release the parent's old snapshot cache. Helper slots are released only
after actual `Wait` completion, so a pathological unreapable process consumes capacity instead
of allowing the process bound to grow silently.

`Session` serializes write, preview, snapshot, resize, replacement, and close;
the process terminal also serializes every RPC and teardown operation. Session
construction reserves the terminal helper and opens scrollback before starting
the user PTY command, so capacity/setup failure cannot execute a rejected
command. All pre-start resources are closed if the later PTY start fails.

Daemon exec upgrade is a transactional boundary. A hidden, bounded, versioned
target-binary probe reports backend availability, session capacity, and helper
handoff schema; the native probe actually starts and reaps a tiny helper. The
old daemon resolves one exact executable, validates its file identity, reserves
session lifecycle under the manager lock, refuses any in-flight creation, and
freezes helper generation. A limiter-owned registry snapshots every
started-but-not-yet-waited helper, including failed/replaced screens. The
private manifest is written atomically with mode 0600 and contains sorted PTY
FD/process identities, the exact target identity, and every helper PID/start
identity. Descriptor inheritance mutations retain and restore the original
flags on every failure. The handler says `upgrading` only after preparation and
must acknowledge before exec proceeds.

After exec, the new image securely reads a bounded, owner-checked, non-symlink
manifest and reaps every inherited exact helper child before creating any new
terminal. Capacity and process-start identities are checked again. Failed
adoption closes the transferred FD and performs verified TERM/KILL plus exact
wait outside the manager lock; only verified absence/reap becomes stopped,
while unresolved or mismatched identity stays errored with PID identity
retained. If exec returns, the old daemon restores descriptor flags, thaws
helper generation, reconstructs a screen that failed during the freeze from
raw scrollback, clears the lifecycle reservation, and remains available.

Before constructing native state, the helper disables core dumps so terminal
or native heap bytes cannot be persisted by the kernel, and irreversibly caps
its descriptor table at 64. Failure to apply either control exits before the
binding is called. Helper and binding failures cross the boundary only as fixed
classifications—never as terminal bytes, environment values, native error
strings, credentials, session output, or local paths.

Portable hard address-space and CPU controls are deliberately excluded. Darwin
does not provide Linux-equivalent address-space/cgroup enforcement; cumulative
`RLIMIT_CPU` eventually kills a healthy long-lived terminal; and per-UID
`RLIMIT_NPROC` affects unrelated processes. Cross-platform allocation/process
caps and kill-on-RPC-deadline behavior are predictable on both supported
kernels. Linux cgroups or platform sandbox profiles remain a possible
deployment-layer defense. The helper still has the daemon user's OS privileges,
and the 64-process cap can reject new tagged sessions under extreme concurrency;
neither is represented as a complete sandbox.

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
| `Close` | Close cell iterator, row iterator, render state, terminal, then helper | Idempotent; bounded close/kill/reap attempts, with registry/slot retention until actual Wait. |

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

Ordinary builds compile and test the Charm rollback. Production
`libghostty` builds compile only the native backend and exclude x/vt and
Ultraviolet from the `internal/pty` terminal-screen dependency graph and its
isolated binary. The full `gr` binary legitimately retains Ultraviolet through
its Bubble Tea/Lip Gloss UI, but no longer retains x/vt. Default and
comparison-only graphs and binaries contain no go-libghostty module; the native
and dual-backend variants contain it. The explicit
`libghostty,libghostty_compare` tag pair compiles both backends for this shared
corpus and its comparative benchmarks. `libghostty_compare` alone does not
select the native backend and intentionally remains a default Charm build.

The native helper retains zero historical rows. Persistent raw `Scrollback` is
the authority for reconstruction; the helper owns only the visible viewport.
The corpus therefore measures grow, shrink, alternate-screen, and hydrated
reconstruction behavior with `WithMaxScrollback(0)`. Strict adoption tests
transfer both the PTY and append-writer descriptors, kill or poison the helper,
and prove pre/post markers remain in the raw log while a replacement screen is
reconstructed and subsequent PTY output remains serviceable. Hydration poison
falls back to an empty screen at the inherited geometry without losing the PTY
or raw bytes. Replays, including the 4 MiB benchmark/RSS fixture, use the shared
512 KiB chunk path below the helper's 1 MiB request limit.

Current intentional differences are explicit assertions rather than hidden
normalization:

| Behavior | Charm rollback | Native candidate | Decision |
|----------|----------------|------------------|----------|
| `e` plus combining acute | Drops the combining mark | Returns one `é` grapheme | Ghostty matches the interface and is preferred. |
| Repeated grow/shrink after `canny brae bide` | First `4x2` shrink leaves `cann`; later grows keep that truncated viewport | First `4x2` shrink reflows to `ae b` / `ide`; later resizes retain only that visible subset because native scrollback is zero | Accept measured Ghostty reflow while reconstructing history only from raw Graith scrollback. |
| Enter alternate screen at column 11 | Homes the cursor | Retains the column | Preserve Ghostty semantics and characterize them. |
| ZWJ, VS16, and regional indicators fragmented byte-by-byte | Commits completed codepoints before later writes extend the cluster, changing cells/cursor and dropping fragmented VS16 | Preserves grapheme assembly across writes | Prefer Ghostty's cluster-accurate cells, cursor, and preview; whole-write semantics match. |
| Reduced #1430 bytes | Contained Go panic | Consumed; subsequent writes continue | Ghostty removes the known failure class. |

No additional differences were found for margins/scroll regions, erase,
tabs/wrap, malformed control strings and device queries, whole-write ZWJ/VS16,
wide cells, represented styles, or background-only palette/RGB cells. The full
tagged PTY suite passes with backend-specific expectations. Every fixture is
small, deterministic, generic old Scots data rather than captured output; the
#1430 input remains a reduced 12-byte hexadecimal sequence.

### Performance evidence

The [focused benchmark evidence](2026-07-18-libghostty-daemon-backend-benchmarks.md)
measures the accepted helper process and public `go-libghostty` wrapper, not the
superseded in-process shim. On the representative Apple M5 host, five-sample
medians show a 30.62 ms helper start/close cost, 838.15 µs per 65,550-byte
parse, 2.58 ms per dirty coherent `120x40` snapshot, 71.47 µs per alternating
resize, and 88.15 ms for production-chunked 4 MiB reconstruction plus snapshot
and close.

The same record separates parent Go allocations from helper/native memory and
reports a measured same-checkpoint 4 MiB aggregate of 38.86 MiB (22.25 MiB
parent and 16.61 MiB helper), versus 93.36 MiB for Charm. At `240x40`, doubling
three concurrent helpers from 12,000 to 24,000 scrolled lines changes their
total current RSS by only 0.06 MiB with `WithMaxScrollback(0)`, while Charm's
three-terminal process grows by 232.89 MiB. It pins the host, toolchain,
revisions, ReleaseFast artifact, sample count, benchtime, commands, validation
checksum, spread, and measurement limitations. Raw captured output and
machine-local paths are deliberately not committed.

### Build and release consequences

The candidate statically links Ghostty. Static linkage avoids loader paths,
side-by-side `.dylib`/`.so` version skew, and a second signed payload. Dynamic
linking adds operational failure modes without changing the crash boundary and
is rejected. The verified macOS arm64 candidate has no Ghostty dylib dependency;
its remaining platform dependencies are `/usr/lib/libresolv.9.dylib`,
CoreFoundation, Security, and `/usr/lib/libSystem.B.dylib`.

The path-scoped native workflow performs these checks:

- ordinary `CGO_ENABLED=0` builds for Darwin/Linux amd64/arm64, proving the
  rollback build does not select cgo or require a native archive;
- tagged Darwin arm64 execution and linking against the arm64 slice of the
  checksum-pinned Apple archive;
- fail-closed tagged Darwin amd64 selection without native linkage;
- an explicit, non-gating exact-source Linux amd64/arm64 matrix for later #1475
  validation using Zig 0.15.2;
- committed-header diff against the exact Ghostty checkout;
- upstream `go-libghostty` tests against Graith's newer Ghostty pin;
- tagged PTY, focused race, and candidate executable builds; and
- testing artifacts that include the native SPDX and notice inventory.

An explicit tagged build without cgo, on macOS amd64, or on an unsupported OS,
returns a configuration error rather than silently changing emulator. Ordinary
GoReleaser remains pure Go during soak. Production promotion therefore needs a
native build matrix or promotion of the already-proven candidate workflow into
release packaging; it cannot use the current single-host pure-Go cross-build.

Local Apple Silicon development uses the arm64 slice of the checksum-pinned
Apple archive. An exact source rebuild on the current macOS 26 host is blocked
by Zig linking its build runner against that SDK, while the archive links and
runs. Linux source builds remain an explicit manual workflow for #1475. This is
a documented local toolchain limitation, not a claim that the library is
unsupported on macOS arm64.

### Pinning, licensing, security, and SBOM

Graith pins `go.mitchellh.com/libghostty` to pseudo-version
`v0.0.0-20260527181217-e9e1010f80b1`, whose exact commit is
`e9e1010f80b1ced0b7efcdb300f4838513c0816e`. The canonical source is
`https://tangled.org/mitchellh.com/go-libghostty`; the GitHub mirror remains
useful for tooling. Its API is not yet version-stable, so upgrades are reviewed
changes rather than floating module updates.

`libghostty-native.spdx.json` records the native closure that Go tooling cannot
see: Ghostty, uucode 0.2.0, Highway 1.2.0 at exact upstream commit, the vendored
simdutf 9.0.0 amalgamation, and the bundled Zig compiler/UBSan runtime. Ghostty's
simdutf package manifest still says 5.2.8, so the inventory verifies the
compiled header version and exact source hashes rather than trusting that stale
field. `THIRD_PARTY_NOTICES.libghostty.md` carries the elected
MIT/BSD-3-Clause/Unicode/Apache notices. The helper script checks the Go module
sum, SPDX entries and relationships, notice pins, source manifests and hashes,
Git commit, toolchain, headers, and Apple checksum as one unit.

The dependency inventory records the pinned Apple build's actual
`emit-lib-vt=true`, `emit-xcframework=true`, and `ReleaseFast` inputs without
inventing a SIMD override. The macOS arm64 workflow does not upload the generic
dependency inventory as if
it described a particular executable. It materializes a candidate-specific
SPDX document containing the clean Git revision, target, and exact binary
SHA-256; verifies the same copied bytes still contain the pinned wrapper and
defined Ghostty symbol, rejects Ghostty dylib linkage, and rejects a
tampered-byte binding; validates the document with checksum-pinned official
SPDX Java tooling; publishes the package directory with a Darwin atomic
no-replace rename; and uploads exactly `gr`, the bound SPDX document, and the
notice file.

A pin rotation is atomic:

1. select the Ghostty and `go-libghostty` commits together;
2. rebuild every static target with the exact required Zig version;
3. synchronize and diff the public headers;
4. update `go.mod`, artifact digests, script constants, SPDX entries, licenses,
   and notices in one review;
5. run Ghostty lib-vt tests, the complete wrapper suite, shared compatibility,
   fuzz/race, benchmarks, and every supported native workflow target; and
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
4. Make native the supported default for macOS arm64 and keep one tested
   pure-Go rollback release available. macOS amd64 remains pure Go; Linux
   promotion requires #1475.
5. After that rollback window closes, remove Charm, its selector, and
   backend-specific expectations. Keep the backend-neutral interface and
   persistent reconstruction path.

Rollback never converts state. Deploy the pure-Go build, stop creating native
helpers, and reconstruct each screen from its existing on-disk scrollback.
Sessions, PTYs, protocol messages, and stored bytes are unchanged. If a helper
fails during the candidate phase, the daemon performs the same replacement for
that one screen automatically.

### Testing

Local Darwin arm64 validation against the checksum-pinned archive on 2026-07-18
ran each target for five seconds with four workers and found no crash or saved
failure: `FuzzGhosttySnapshotDecoder` completed 610,443 executions,
`FuzzGhosttyRequestDecoder` completed 621,504, and the native
`FuzzGhosttyHelperWrite` completed 933 isolated helper lifecycles. The
exact-source Linux workflow runs the same targets for ten seconds and repeats
the resource-limit tests on that kernel; execution counts are intentionally not
treated as stable performance claims.

A final integrated audit on 2026-07-21 found and corrected five narrow edge
cases: Darwin `/dev/fd` enumeration, inherited-helper group reaping, failed
scrollback-descriptor adoption, helper-stall lock amplification, and a capacity
probe bound that was too close to measured race-build latency. Each correction
has a focused normal and race regression. The same pass aligned the native SPDX
copyright notice and preview-delivery wording. An unrestricted macOS arm64
exercise then ran real sessions under `GRAITH_PROFILE=dev` successfully. The
operator's report that sessions felt more responsive agrees with the measured
parse/reconstruction results, but remains qualitative rather than an additional
benchmark claim.

- `go test ./internal/pty`
- `scripts/libghostty-native.sh test`
- `scripts/libghostty-native.sh race`
- `scripts/libghostty-native.sh fuzz`
- `scripts/libghostty-native.sh bench`
- `scripts/libghostty-native.sh memory`
- `scripts/libghostty-native.sh verify-metadata`
- tagged helper protocol fuzz targets and targeted `-race` tests
- backend-neutral and tagged >1 MiB hydration, poison-replay, repeated-close,
  close-versus-render/resize, helper RSS polling, limiter prelaunch, and
  failure-during-freeze recovery tests
- Charm→native 64/65 capacity, native→native exec, native→Charm compatible
  handoff, unavailable tagged builds, target replacement, malformed/bounded
  probe and manifest, transactional descriptor rollback, and exact helper/agent
  reaping tests
- default `go test -race ./...`, `go vet ./...`, repository lint, actionlint,
  shell validation, generated-file checks, and integration tests
- macOS arm64 candidate linkage and execution, tagged macOS amd64 fail-closed
  selection, and the deferred manual Linux source-build checks from #1475
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
