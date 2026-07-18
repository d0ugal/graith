---
title: "Design Doc: libghostty-vt daemon backend spike"
authors: Dougal Matthews
created: 2026-07-18
status: Implemented (spike only; production migration rejected)
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1432
---

# libghostty-vt daemon backend spike

`libghostty-vt` can implement Graith's narrow terminal interface and is much
faster and more memory-efficient for parsing and scrollback reconstruction, but
it is **not ready to replace Charm as the production default**. This spike's
recommendation is no-go until semantic differences, native crash containment,
release reproducibility, and native dependency reporting are resolved. The
experimental implementation remains opt-in behind `-tags=libghostty`; ordinary
builds and releases continue to use Charm without a native dependency.

## Background

The daemon feeds PTY output into the backend-neutral `Terminal` interface in
`internal/pty/terminal.go`. The production adapter in
`internal/pty/terminal_charm.go` wraps `github.com/charmbracelet/x/vt`; callers
only depend on `Write`, `Resize`, `Size`, `Cursor`, `Cell`, and `Close`.

The native clients already use `libghostty-vt` through the public C terminal and
render-state APIs. `gui/shared/build-libghostty.sh` pins Ghostty commit
`91f66da24527fa02d92b5fd0b41cd020f553a64c` and Zig 0.15.2. SwiftPM consumes a
universal Apple artifact whose SHA-256 is
`25c1620e63311cc687637c8e3bdfae1fe3e070892966c07d0d91065ccda541c0`.

The adapter in `internal/pty/terminal_ghostty.go` reuses those headers and that C
ABI. A small C-side screen cache makes render-state extraction one cgo crossing
per frame rather than one crossing per cell. The `libghostty` build tag selects
the experiment only when cgo is also available. Without the tag—or with
`CGO_ENABLED=0`—the Charm selector remains in place.

## Problem

Charm's parser reached an upstream panic on the reduced synthetic sequence from
#1430. The daemon now contains that Go panic and reconstructs its screen, but a
more mature parser might remove this failure class and reduce reconstruction
cost. Ghostty is a plausible candidate because the interface fit is small and
the project describes its terminal behavior as mature.

The candidate is native code with an explicitly unstable C API, however. A
replacement would change Graith's pure-Go release properties, cross-compilation
workflow, dependency inventory, and crash boundary. A fast parser is not enough:
screen semantics, release artifacts, failure containment, and rollback must all
be acceptable.

## Goals

- Prove the C ABI can implement every method of `Terminal` without changing its
  production callers.
- Compare both backends with the same reduced synthetic compatibility corpus.
- Measure parsing, cell extraction, resize, reconstruction, and peak RSS.
- Verify default and experimental build consequences across the supported
  Darwin/Linux and amd64/arm64 matrix.
- Establish an exact dependency pin and identify licensing, update, security,
  and SBOM work.
- Make an explicit production go/no-go decision with migration and rollback
  conditions.

### Non-Goals

- Changing the production default backend in this issue.
- Capturing or committing real user, agent, or session output.
- Coupling daemon screen reconstruction to the GUI renderer.
- Solving every Ghostty API or packaging issue inside the spike.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI/daemon | Experimental only | `-tags=libghostty` selects the adapter for evidence; normal and release builds remain on Charm. |
| iOS | No behavior change | The existing pinned library/header assets are reused and synchronized, but the app's terminal backend is not changed by this daemon spike. |
| macOS | No behavior change | As on iOS, the client keeps its existing renderer and only supplies the already-pinned build asset. |

## Proposals

### Proposal 0: Keep Charm as the production default (Recommended)

Land the opt-in adapter, synthetic corpus, repeatable benchmarks, matrix checks,
and this decision record. Keep `.goreleaser.yaml` and the normal daemon free of
cgo. This preserves the existing operational and rollback properties while the
blocking risks below are addressed in separate work.

This does not discard the Ghostty path. The measured parser and memory gains are
large enough to justify a staged follow-up, but not a default switch today.

### Proposal 1: Switch the daemon default immediately

Build all release archives with cgo and statically link per-platform Ghostty
libraries. This captures the performance benefit quickly, but it accepts known
screen differences, makes a C/Zig fault daemon-fatal, breaks the current pure-Go
cross-build, and ships native components that the current Go module/SBOM path
cannot see. This proposal is rejected.

### Proposal 2: Process-isolated staged migration

Build a helper process that owns Ghostty terminal state, shadow it beside Charm,
and compare normalized frames before serving its output. Once compatibility and
packaging gates are met, enable Ghostty for an opt-in cohort, then broaden it.
The helper is more work than an in-process cgo adapter, but it provides a real
crash boundary: an abort or segmentation fault loses a reconstructable screen
model rather than the daemon and every session manager operation.

This is the recommended direction for a future migration, not part of this
spike.

## Other Notes

### C API fit

| Graith method | Ghostty mapping | Result |
|---------------|-----------------|--------|
| `Write` | `ghostty_terminal_vt_write` | Clean fit. The C API reports no parse error, so successful calls return `len(p), nil`. |
| `Resize` | `ghostty_terminal_resize` | Clean call shape, but Ghostty reflows and retains newest rows differently from Charm. |
| `Size` | Adapter's last successful `uint16_t` geometry | Clean for real PTY sizes; the adapter clamps values above `uint16_t`. |
| `Cursor` | `GHOSTTY_TERMINAL_DATA_CURSOR_X/Y/VISIBLE` | Clean fit for the active viewport. |
| `Cell` | Render-state update, row/cell traversal, raw cell/style, UTF-8 grapheme buffer | Clean data fit. Palette and RGB identity can be preserved instead of flattening to resolved RGB. |
| `Close` | Free row cells, iterator, render state, terminal, and snapshot buffers | Clean and idempotent in the Go wrapper. |

The render snapshot uses Ghostty's raw cell to distinguish wide-character tail
cells and background-only colors. `GhosttyStyle` supplies bold, faint, italic,
underline, blink, inverse, strikethrough, and tagged palette/RGB colors. This
maps every field Graith currently renders.

There are two interface mismatches a production adapter must fix. `Resize` and
`Cell` cannot return errors, although Ghostty can report allocation failure; the
spike retains the last size or returns a blank cell. Also, the tagged selector
falls back to Charm if native initialization fails because `newTerminal` cannot
return an error. A production factory must make this fallback observable and
must not silently mask initialization or snapshot failure.

### Compatibility evidence

`TestTerminalSpikeCompatibilityCorpus` runs both factories through identical,
generic input generated in `internal/pty/terminal_spike_test.go`. It covers the
128 KiB adoption-hydration size, grow and shrink resize, cursor visibility,
wide characters, emoji, combining graphemes, all currently represented style
bits, palette and RGB colors, alternate screen behavior, device queries, and
the reduced #1430 sequence.

Measured on the pinned Apple arm64 artifact:

| Behavior | Charm | libghostty-vt |
|----------|-------|----------------|
| 128 KiB hydration, grow resize, cursor hide, styles, colors, wide characters, emoji, device queries | Pass | Pass |
| `e` plus combining acute | Combining mark is dropped; cell contains `e` | One `é` grapheme, matching `Terminal`'s documented model |
| Enter alternate screen from cursor column 11 | Homes cursor; new text starts at column 0 | Retains column 11; new text is indented |
| Shrink `20x2` to `4x2` after `keep me canny` | Truncates first row to `keep` | Reflows and retains newest rows; first row is `cann` |
| Reduced #1430 malformed region | Upstream panic becomes `errTerminalParserPanic` at the Go containment boundary | Consumed without panic or error; subsequent output remains writable |

The full existing `internal/pty` suite under the Ghostty selector produced four
expected failures: two Charm-specific tests that require #1430 to panic, plus
the alternate-screen and shrink-resize expectations above. All other current
terminal characterizations passed. The tagged CI therefore runs the explicit
cross-backend corpus rather than pretending the backends are byte-for-byte
identical.

No captured output is used. The only regression fixture is the existing 12-byte
synthetic sequence encoded as hex.

### Performance evidence

Methodology: Go 1.26.5, Darwin arm64, Apple M5, pinned ReleaseFast Ghostty
artifact, `-benchmem`, `-benchtime=3x`, five independent benchmark samples. The
table reports the median. Parsing repeatedly feeds a 64 KiB synthetic stream;
cell extraction reads a dirty `120x40` screen; resize alternates `80x24` and
`120x40`; reconstruction creates a terminal, writes 4 MiB, and extracts the
visible `120x40` screen.

| Operation | Charm median | Ghostty median | Observation |
|-----------|--------------|----------------|-------------|
| Parse 64 KiB | 11.56 ms, 5.67 MB/s, 5.98 MB/51,391 Go allocs | 144.8 µs, 452.8 MB/s, 0 Go allocs | Ghostty about 80x the throughput |
| Extract dirty 120x40 cells | 51.95 µs, 5 B/1 Go alloc | 102.7 µs, 66.0 KB/118 Go allocs | Ghostty adapter about 2x slower |
| Resize | 26.24 µs, 591.9 KB/41 Go allocs | 519.3 µs, 0 Go allocs | Ghostty about 20x slower because it reflows |
| Reconstruct 4 MiB + extract | 938.8 ms, 4.47 MB/s, 389.7 MB/3,287,802 Go allocs | 8.69 ms, 482.5 MB/s, 197 KB/119 Go allocs | Ghostty about 108x faster |

Go allocation counters exclude Ghostty's native allocations. Peak memory was
therefore measured by compiling one test binary per backend and wrapping only
the 4 MiB reconstruction/extraction process with `/usr/bin/time -l`:

| Backend | Maximum RSS | Test-body time |
|---------|-------------|----------------|
| Charm | 152.4 MiB | 0.96 s |
| libghostty-vt | 22.2 MiB | 0.02 s |

These are representative local measurements, not Linux results or universal
performance claims. `scripts/libghostty-spike.sh bench` and `memory` reproduce
the exact commands and corpus.

### Build and release evidence

| Target/property | Evidence | Status |
|-----------------|----------|--------|
| Normal Darwin amd64/arm64 and Linux amd64/arm64 | `CGO_ENABLED=0 go build` for all four targets | Verified locally; no tagged file or native library is selected |
| `CGO_ENABLED=0` with `libghostty` tag | Tagged no-cgo selector builds and runs Charm corpus | Verified locally |
| Darwin arm64 tagged adapter | Shared corpus and full daemon link against checksummed static artifact | Verified locally |
| Darwin amd64 tagged adapter | Cross-compiled with `clang -arch x86_64` and the universal static artifact | Verified locally |
| Linux amd64 tagged adapter | Exact-source static build, exact-header diff, and native corpus | Verified in PR #1440 CI |
| Linux arm64 tagged adapter | Exact-source static build, exact-header diff, and cgo cross-link | Verified in PR #1440 CI |

The current GoReleaser builds are intentionally `CGO_ENABLED=0`. Production
adoption would require per-OS/architecture C compilers and native archives, and
would give up the present single-host pure-Go cross-build unless those archives
and cross toolchains were supplied explicitly.

The spike statically links Ghostty. On local arm64 builds, the stripped daemon
grew from 33,278,322 bytes to 34,761,298 bytes (about 1.48 MB or 4.5%). The
result has only normal macOS system-library/framework dependencies; no Ghostty
dylib is required. Dynamic linking was analyzed but not executed: it would add
`.dylib`/`.so` packaging, loader-path, signing, version-skew, and rollback
concerns without improving the API or crash boundary, so static linking is the
only reasonable release option.

An exact source rebuild on this macOS 26 environment is blocked even with the
required Zig 0.15.2: Zig cannot link its build runner against the installed SDK
and reports missing system symbols such as `__availability_version_check` and
`_abort`. The checksummed prebuilt universal artifact works. This is a measured
local-development/rebuild blocker, not evidence that another macOS SDK fails.

The repository now carries a path-scoped CI workflow that builds the exact
Ghostty pin for both Linux release architectures and verifies the committed
headers against that source. PR #1440 proved both Linux builds, the amd64 native
corpus, and the arm64 cgo cross-link. The workflow also verifies the universal
Apple archive and both Darwin Go/cgo links.

### Pinning and native supply chain

The source pin, toolchain, and Apple artifact are reproducible inputs:

- Ghostty source is fetched by full 40-character commit, never a branch or tag.
- Ghostty's own `build.zig.zon` requires Zig 0.15.2 and content-hashes its
  external packages.
- The existing Apple xcframework is fetched by an immutable release URL and
  verified against its SHA-256 digest before use.
- `scripts/libghostty-spike.sh source-build` rejects any other Zig version.

The verification found that Graith's committed split headers were stale even
though the artifact contained symbols from the stated pin. This spike
resynchronized them from the official `91f66da` source and added CI that diffs
them against the exact checkout. That finding reinforces the no-go decision:
the pin must cover source, headers, library, toolchain, and provenance as one
reviewed unit.

Ghostty is MIT licensed. The static archive also incorporates native code or
data from projects including Highway (Apache-2.0), simdutf (Apache-2.0/MIT), and
uucode (MIT, with Unicode and Bjoern Hoehrmann notices). A production change
must generate and ship a complete third-party notice inventory; the current
release packages only install Graith's own license.

Go module metadata, `go version -m`, Go vulnerability scanners, and Go-derived
SBOMs do not see statically linked Ghostty or its Zig/C/C++ dependencies.
Production adoption therefore requires an explicit native SBOM component for
the Ghostty commit and each bundled dependency, plus a scheduled upstream
advisory/update process. Security fixes require rebuilding and republishing all
static release archives; updating `go.mod` alone cannot deliver them. The
existing artifact has a checksum but no verified build provenance in this
spike, so release-grade attestations remain a gate.

### Failure containment

The current Charm wrapper can recover a Go panic, suppress raw terminal data in
the error, replace the screen, and keep the daemon alive. Go `recover` cannot
contain a native abort, segmentation fault, memory corruption, or fatal Zig
safety trap reached through cgo. Any such fault in the in-process adapter can
terminate or corrupt the daemon, affecting every managed session.

The public C API treats VT input as untrusted and documents `vt_write` as
non-failing, which is valuable, and the reduced #1430 input was safe. It is not
a process isolation guarantee. Before migration, adversarial/fuzz corpora must
run against the exact release library under sanitizers where supported, and
the live backend should be hosted in a restartable helper process. The daemon
can then recreate the screen from its existing persistent scrollback after a
helper failure without logging terminal contents.

### Migration and rollback plan

A future go decision should use these gates and stages:

1. Resolve or explicitly normalize the alternate-screen and resize semantic
   differences; extend the shared corpus with any newly discovered cases.
2. Make terminal construction/snapshot errors observable and add a process
   boundary for native faults.
3. Produce attested static archives and native SBOM/license notices for all four
   release targets; prove builds on native Linux and Darwin runners.
4. Run Ghostty as a shadow model from the same synthetic and then quarantined
   PTY byte stream. Compare normalized frames and resource metrics without
   serving Ghostty output.
5. Offer an opt-in cohort with automatic reconstruction from persistent
   scrollback when the helper exits or a frame mismatch threshold is crossed.
6. Promote gradually only after compatibility, crash, CPU, RSS, and packaging
   gates remain green for a defined observation window.

Rollback is deliberately simple: stop selecting the helper/tag, reconstruct
each screen from the existing on-disk scrollback using Charm, and ship the
unchanged pure-Go release path. No persistent state or wire format depends on
the backend, and Charm remains compiled/tested throughout migration.

### Testing

- `go test ./internal/pty -run TestTerminalSpikeCompatibilityCorpus`
- `scripts/libghostty-spike.sh test`
- `scripts/libghostty-spike.sh bench`
- `scripts/libghostty-spike.sh memory`
- `CGO_ENABLED=0 go test -tags=libghostty ./internal/pty`
- Default `go test -race ./...`, `go vet ./...`, lint, workflow lint, and GUI
  shared tests after synchronizing the pinned headers.
- The path-scoped `libghostty-vt spike` workflow for source builds and the
  Darwin/Linux architecture checks.

### References

- [Issue #1432](https://github.com/d0ugal/graith/issues/1432)
- [Ghostty at the exact source pin](https://github.com/ghostty-org/ghostty/tree/91f66da24527fa02d92b5fd0b41cd020f553a64c)
- [libghostty-vt public header at the pin](https://github.com/ghostty-org/ghostty/blob/91f66da24527fa02d92b5fd0b41cd020f553a64c/include/ghostty/vt.h)
- [Ghostling minimal C integration](https://github.com/ghostty-org/ghostling)
- `internal/pty/terminal.go` and `internal/pty/terminal_charm.go`
- `gui/shared/Sources/GraithTerminalCore/GhosttyTerminalState.swift`
- `gui/shared/build-libghostty.sh`
