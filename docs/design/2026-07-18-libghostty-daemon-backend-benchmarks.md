---
title: "Evidence: Operational libghostty daemon benchmarks"
authors: Dougal Matthews
created: 2026-07-18
status: Implemented
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1444
---

# Operational libghostty daemon benchmarks

This record measures the accepted process-isolated `go-libghostty` backend,
including helper startup, RPC, public-wrapper iteration, coherent snapshot
serialization, and helper shutdown. Charm receives the same synthetic bytes and
remains the rollback comparison. The earlier in-process spike results are not
used here because they measured a different failure and IPC boundary.

## Reproducible inputs

The measurements were executed on this host without another workload started
by this session:

| Property | Value |
|----------|-------|
| Hardware | MacBook Pro `Mac17,2`, Apple M5, 10 physical/logical CPUs, 32 GiB RAM |
| OS | macOS 26.5.2, build 25F84, Darwin arm64 25.5.0 |
| Go | Go 1.26.5, `darwin/arm64`, cgo enabled, standard compiler optimization |
| Benchmark concurrency | `runtime.NumCPU=10`; parent explicitly pinned to `GOMAXPROCS=10`; the sanitized helper receives no `GOMAXPROCS` override and uses the same 10-CPU host default; parent confirmed by the `-10` benchmark suffix |
| Native compiler/linker | Apple clang 21.0.0 (`clang-2100.1.1.101`), pkg-config 3.0.3 |
| Ghostty | `91f66da24527fa02d92b5fd0b41cd020f553a64c` |
| Native artifact | ReleaseFast universal Apple archive, SHA-256 `25c1620e63311cc687637c8e3bdfae1fe3e070892966c07d0d91065ccda541c0` |
| `go-libghostty` wrapper | `v0.0.0-20260527181217-e9e1010f80b1`, commit `e9e1010f80b1ced0b7efcdb300f4838513c0816e` |
| Timing samples | Five independent Go benchmark samples, `-benchtime=1s`, `-benchmem` |
| RSS samples | Five fresh test-process executions per backend |

`scripts/libghostty-native.sh` verifies the artifact checksum, module version,
SPDX pins, and notice pins before native tests. Its benchmark and memory modes
compile throwaway test binaries under a temporary directory; no library, binary,
build cache, or raw benchmark stream is committed.

The exact reproduction commands are:

```bash
scripts/libghostty-native.sh test
scripts/libghostty-native.sh bench
scripts/libghostty-native.sh memory
```

The `bench` mode expands to:

```bash
GOMAXPROCS=10 go test -run '^$' -tags=libghostty ./internal/pty \
  -bench '^BenchmarkTerminalBackends$' -benchmem -benchtime=1s -count=5
```

For every reported metric, the median is the third value after sorting the five
samples numerically. This selection is mechanical; no outliers were discarded.
Linux performance was not executed and no Linux performance claim is made. The
native workflow compiles and tests exact-pin Linux candidates, but that is build
validation rather than performance evidence.

## Workload and validity

Both factories receive one reduced, generated stream containing numbered text,
SGR colors, a combining grapheme, a wide character, and an emoji. The 64 KiB
parse input is exactly 65,564 bytes (886 lines); the 4 MiB reconstruction input
is exactly 4,194,320 bytes (56,680 lines). It contains no captured terminal
output, identifiers, paths, or session data.

The operations have these boundaries:

- `start_close` constructs a `120x40` terminal and closes it. For the native
  candidate this includes socketpair creation, executable resolution, process
  start, the create RPC, close RPC, and child reap.
- `parse` creates one terminal outside the wall timer, then repeatedly writes
  the identical 65,564-byte stream. The final coherent snapshot, checksum, and
  close validate output but are outside the wall and parent-allocation timer.
- `dirty_snapshot_120x40` first populates the screen outside the timer. Each
  operation alternates a fixed visible content/color mutation and then requests
  one coherent snapshot. It therefore measures the operational dirtying write
  RPC plus snapshot RPC, native render iteration, serialization, transport, and
  parent decode—not a cached `Cell` read.
- `resize` populates the terminal outside the timer, then alternates `80x24` and
  `120x40`. A final coherent snapshot and checksum validate the resulting size
  outside the timer.
- `reconstruct_4MiB` includes terminal construction, one 4,194,320-byte write,
  coherent `120x40` snapshot extraction, and close/reap in every timed operation.

Every write checks its returned byte count. Dirty snapshots immediately assert
the alternating first-cell value. Final snapshots check geometry and cell count
and feed every cell's content, foreground/background kind and value, style bits,
cursor, and geometry into a stable FNV-1a checksum stored in a package-level
sink. Unit tests prove content, style, cursor, and visibility changes alter the
checksum. Tagged tests also prove that a write invalidates the helper parent's
snapshot cache and that the next snapshot refreshes visible content. These
checks keep compiler elimination or a cached-cell shortcut from creating a
valid-looking sample.

## Measured medians

These tables contain measurements only. `ns/op` is wall time. `parent CPU` is
the parent test process's `RUSAGE_SELF` user plus system time over the timed
region. Go allocation counters likewise describe only the parent Go runtime;
they cannot see helper Go allocations or native allocations.

| Operation | Charm wall | Native helper wall | Charm throughput | Native throughput |
|-----------|-----------:|-------------------:|-----------------:|------------------:|
| Start + close | 176.1 µs | 4.951 ms | n/a | n/a |
| Parse 65,564 bytes | 16.67 ms | 319.96 µs | 3.93 MB/s | 204.92 MB/s |
| Dirty coherent `120x40` snapshot | 66.91 µs | 1.353 ms | n/a | n/a |
| Alternating resize | 21.50 µs | 597.96 µs | n/a | n/a |
| Reconstruct 4,194,320 bytes + snapshot | 1.124 s | 24.19 ms | 3.73 MB/s | 173.42 MB/s |

| Operation | Charm parent CPU | Native parent CPU | Native helper lifetime CPU |
|-----------|-----------------:|------------------:|---------------------------:|
| Start + close | 370.13 µs/op | 511.05 µs/op | 3.362 ms/op |
| Parse 65,564 bytes | 17.09 ms/op | 67.80 µs/op | 278.53 µs/op |
| Dirty coherent `120x40` snapshot | 87.33 µs/op | 151.40 µs/op | unavailable while child is live |
| Alternating resize | 47.17 µs/op | 18.98 µs/op | unavailable while child is live |
| Reconstruct 4,194,320 bytes + snapshot | 1.146 s/op | 3.243 ms/op | 21.58 ms/op |

`helper lifetime CPU` is measured from the exited helper's `ProcessState` and
divided by the benchmark operation count. It is exact for start/close and for
reconstruction, where every operation owns one helper. For parse it also
contains the one-time create, validation snapshot, and close, amortized over
thousands of timed writes; it is therefore a conservative lifetime-amortized
number rather than a pure VT-parser CPU counter. Portable live-child CPU
sampling is unavailable, so no helper CPU number is claimed for snapshot or
resize. Total CPU can exceed wall time because rusage sums work across threads.

| Operation | Charm parent B/op | Charm parent allocs/op | Native parent B/op | Native parent allocs/op |
|-----------|------------------:|-----------------------:|-------------------:|------------------------:|
| Start + close | 5,421,632 | 349 | 3,269 | 55 |
| Parse 65,564 bytes | 6,003,321 | 51,388 | 16 | 1 |
| Dirty coherent `120x40` snapshot | 196,612 | 2 | 279,024 | 121 |
| Alternating resize | 222,117 | 19 | 16 | 2 |
| Reconstruct 4,194,320 bytes + snapshot | 389,899,656 | 3,287,806 | 283,083 | 176 |

Peak RSS uses the same 4 MiB reconstruction and full snapshot checksum. Parent
RSS comes from `getrusage(RUSAGE_SELF)` immediately after the workload. Native
helper RSS comes independently from the reaped child's `ProcessState`. Values
are bytes as reported by macOS and converted to MiB below.

| Backend | Parent peak RSS | Helper peak RSS |
|---------|----------------:|----------------:|
| Charm rollback | 160,694,272 bytes (153.25 MiB) | n/a |
| Native helper | 21,970,944 bytes (20.95 MiB) | 19,300,352 bytes (18.41 MiB) |

BSD `/usr/bin/time -l` wrapped each process as an independent cross-check; its
median parent maximum RSS was within 0.1 MiB of self-rusage. Parent and helper
peaks are not guaranteed to occur simultaneously, and neither rusage source
reports a sampled process-tree peak. Adding 20.95 MiB and 18.41 MiB gives a
39.36 MiB upper-bound estimate, not a measured combined-RSS result.

## Derived analysis, not measurements

Ratios below are calculated from the measured medians and should not be read as
additional samples or universal performance claims:

- Helper start/close is about 28.1x slower than Charm construction/close. That
  fixed isolation cost is material for short-lived terminals and is why it is
  reported separately from steady-state parsing.
- The isolated helper parses this stream about 52.1x faster by wall time. Adding
  measured native parent CPU to lifetime-amortized helper CPU gives an estimated
  346.3 µs total CPU per 65,564-byte write, about 49.4x below Charm's measured
  parent CPU on this host. The native value remains an estimate because its
  helper component includes amortized lifecycle and validation work.
- The dirty update plus coherent snapshot path is about 20.2x slower, and resize
  is about 27.8x slower. These costs include the selected helper RPC/public
  wrapper path and must not be replaced with the old in-process shim numbers.
- Full 4 MiB reconstruction is about 46.5x faster by wall time despite paying
  helper startup, snapshot transport, and close in every operation.
- Treating the separately observed parent/helper RSS peaks as an upper bound
  gives 39.36 MiB, about 3.9x below the Charm parent peak. This is an estimate;
  the only measured RSS claims are the separate values in the table above.

The result supports staged adoption for parsing and recovery workloads but also
shows why helper lifetime and dirty-frame frequency matter operationally. Charm
remains the compiled and tested rollback until the accepted design's soak and
promotion gates are satisfied.

## References

- [Accepted native backend design](2026-07-18-libghostty-daemon-backend.md)
- [Issue #1444](https://github.com/d0ugal/graith/issues/1444)
- [Parent PR #1440](https://github.com/d0ugal/graith/pull/1440)
- `internal/pty/terminal_backend_test.go`
- `internal/pty/terminal_ghostty_process.go`
- `scripts/libghostty-native.sh`
