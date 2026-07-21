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
remains the rollback comparison. The superseded in-process spike and all
pre-integration measurements are excluded because they exercised different
failure, compatibility, chunking, and IPC boundaries.

## Reproducible inputs

The measurements were taken on 2026-07-20 from held candidate `b7b7108`, based
directly on feature commit `1cd612d`. The evidence was then rebased without
resampling onto the frozen macOS-arm64 feature checkpoint
`bac11ea6accc6ede93454c3671025f23d4589c08`. Inspection of the intervening
changes found only target selection, packaging, SBOM, and design updates: the
selected XCFramework input path and checksum, native helper implementation,
production chunking, and benchmark/RSS harness are unchanged. The retained
samples therefore exercise the same macOS-arm64 helper path as this tree.

| Property | Value |
|----------|-------|
| Hardware | MacBook Pro `Mac17,2`, Apple M5, 10 cores (4 performance, 6 efficiency), 32 GiB RAM |
| OS | macOS 26.5.2, build 25F84, Darwin `arm64` 25.5.0 |
| Go | Go 1.26.5, `darwin/arm64`, cgo enabled, standard compiler optimization |
| Benchmark concurrency | `runtime.NumCPU=10`; parent pinned to `GOMAXPROCS=10`; the sanitized helper receives no `GOMAXPROCS` override and uses the 10-CPU host default; benchmark suffix `-10` confirms the parent setting |
| Native compiler/linker | Apple clang 21.0.0 (`clang-2100.1.1.101`), pkg-config 3.0.3 |
| Ghostty | `91f66da24527fa02d92b5fd0b41cd020f553a64c` |
| Native artifact | ReleaseFast Apple XCFramework archive (arm64 slice selected), SHA-256 `25c1620e63311cc687637c8e3bdfae1fe3e070892966c07d0d91065ccda541c0` |
| `go-libghostty` wrapper | `v0.0.0-20260527181217-e9e1010f80b1`, commit `e9e1010f80b1ced0b7efcdb300f4838513c0816e` |
| Timing samples | Five Go benchmark samples, `-benchtime=1s`, `-benchmem`, fixed `GOMAXPROCS=10` |
| RSS samples | Five fresh test processes for each backend/workload pair, fixed `GOMAXPROCS=10` |

Before the retained run, exact-name process checks found no unrelated `go`,
benchmark-test, or native-daemon test process. Consecutive one-second `iostat`
observations were stable at 64–72% CPU idle. No values from earlier contended
runs were retained.

`scripts/libghostty-native.sh` verifies the artifact and dependency pins. Its
benchmark mode uses `libghostty_compare` so the production native backend and
Charm rollback coexist only in the comparison binary; exact production checks
continue to use `-tags=libghostty`. Temporary native libraries, binaries, build
caches, and raw benchmark streams are not committed.

The reproduction commands are:

```bash
scripts/libghostty-native.sh test
scripts/libghostty-native.sh race
scripts/libghostty-native.sh bench
scripts/libghostty-native.sh memory
```

The timing command expands to:

```bash
GOMAXPROCS=10 go test -run '^$' \
  -tags=libghostty,libghostty_compare ./internal/pty \
  -bench '^BenchmarkTerminalBackends$' -benchmem \
  -benchtime=1s -count=5
```

The RSS mode builds fresh disposable Charm and comparison test binaries plus a
repository-owned macOS current-RSS probe, then runs each pair as:

```bash
/usr/bin/time -l env GOMAXPROCS=10 \
  GRAITH_TERMINAL_MEMORY_BACKEND=<backend> \
  GRAITH_TERMINAL_MEMORY_WORKLOAD=<workload> \
  GRAITH_TERMINAL_RSS_PROBE=<temporary-probe> \
  <temporary-test-binary> \
  -test.run '^TestTerminalBackendPeakMemoryWorkload$' -test.v
```

For every metric, `median [min–max]` is the third value after sorting all five
samples numerically. Selection was mechanical and no outlier was discarded.
Linux performance was not executed and no Linux performance claim is made.

## Workloads and validity

Both factories receive the same reduced, generated stream containing numbered
text, SGR colors, a combining grapheme, a wide character, and an emoji. The
64 KiB parse input is exactly 65,550 bytes (1,425 lines); the 4 MiB input is
exactly 4,194,326 bytes (91,181 lines). It contains no captured output,
identifiers, paths, or session data.

The operations have these boundaries:

- `start_close` constructs a `120x40` terminal and closes it. The native sample
  includes socketpair creation, executable pinning/resolution, process start,
  create RPC, close RPC, and child reap.
- `parse` constructs one terminal outside the timer, then repeatedly writes the
  identical 65,550-byte stream. The validation snapshot, checksum, and close
  occur outside the wall and parent-allocation timer.
- `dirty_snapshot_120x40` populates the screen outside the timer. Every timed
  operation alternates a visible first-cell content/color mutation and requests
  a coherent snapshot. It includes the dirtying write RPC, snapshot RPC, native
  render iteration, serialization, transport, and parent decode; it cannot use
  the clean snapshot cache.
- `resize` populates the terminal outside the timer, then alternates `80x24` and
  `120x40`. Final geometry and a coherent snapshot are validated outside the
  timer.
- `reconstruct_4MiB` includes terminal construction, production
  `writeTerminalChunks` replay in 512 KiB requests below the helper's 1 MiB RPC
  maximum, coherent `120x40` extraction, and close/reap in every operation.

The process-level RSS matrix repeats reconstruction and adds 12,000- and
24,000-line scrolling at `240x40`, with one terminal and three terminals. Each
line uses the same reduced grapheme/color vocabulary. The scrolling chunks are
948,000 and 1,896,000 bytes per terminal; the three-terminal writes run
concurrently. Ghostty uses the production `WithMaxScrollback(0)` setting because
Graith's raw log is authoritative. The 4 MiB reconstruction case keeps its
fixture live in the parent as a representative retained raw tail; the scrolling
cases intentionally isolate derived-screen retention so 12k versus 24k exposes
whether helper memory plateaus.

Every write checks either its exact returned byte count or the production
chunker's short-write error. Dirty snapshots immediately assert the alternating
first-cell value. Final snapshots check geometry and cell count and feed every
cell's content, foreground/background kind and value, style bits, cursor, and
geometry into a stable FNV-1a checksum stored in a package-level sink. Unit tests
prove content, style, cursor, and visibility changes alter the checksum, and a
separate test proves aggregate RSS is selected from one checkpoint rather than
by adding unrelated parent/helper maxima. These checks resist compiler
elimination, clean-cache shortcuts, and invalid aggregate accounting.

## Measured timing medians

These tables contain measurements only. Wall time and throughput are reported
separately from CPU. All brackets show the full five-sample range.

| Operation | Charm wall | Native helper wall | Charm throughput | Native throughput |
|-----------|-----------:|-------------------:|-----------------:|------------------:|
| Start + close | 288.589 µs [263.327–294.990] | 30.617 ms [29.793–31.775] | n/a | n/a |
| Parse 65,550 bytes | 45.336 ms [44.802–45.776] | 838.151 µs [800.507–1,055.610] | 1.45 MB/s [1.43–1.46] | 78.21 MB/s [62.10–81.89] |
| Dirty coherent `120x40` snapshot | 117.529 µs [112.772–118.812] | 2.585 ms [2.524–2.609] | n/a | n/a |
| Alternating resize | 25.070 µs [24.968–28.985] | 71.468 µs [64.540–78.982] | n/a | n/a |
| Reconstruct 4,194,326 bytes + snapshot | 2.962 s [2.937–3.011] | 88.146 ms [83.764–88.286] | 1.42 MB/s [1.39–1.43] | 47.58 MB/s [47.51–50.07] |

Parent CPU is `RUSAGE_SELF` user plus system time over the timed region. Helper
CPU is the reaped process's user plus system time. For parse, helper lifetime
CPU also contains the one-time create, validation snapshot, and close amortized
over the timed writes. Live-child CPU is unavailable for dirty snapshot and
resize, so no helper value is claimed for those operations.

| Operation | Charm parent CPU | Native parent CPU | Native helper lifetime CPU |
|-----------|-----------------:|------------------:|---------------------------:|
| Start + close | 616.581 µs [590.068–628.324] | 21.071 ms [20.967–21.458] | 6.853 ms [6.817–6.901] |
| Parse 65,550 bytes | 45.231 ms [45.010–45.912] | 112.617 µs [103.932–125.053] | 787.428 µs [779.469–825.360] |
| Dirty coherent `120x40` snapshot | 142.968 µs [142.773–144.030] | 286.278 µs [274.363–290.785] | unavailable while child is live |
| Alternating resize | 62.201 µs [61.975–62.720] | 26.612 µs [23.069–26.781] | unavailable while child is live |
| Reconstruct 4,194,326 bytes + snapshot | 2.964 s [2.960–2.971] | 26.592 ms [26.580–27.098] | 58.212 ms [57.863–58.504] |

Go allocation counters describe only the parent Go runtime. They do not include
the helper Go heap or native allocations.

| Operation | Charm parent B/op | Charm parent allocs/op | Native parent B/op | Native parent allocs/op |
|-----------|------------------:|-----------------------:|-------------------:|------------------------:|
| Start + close | 5,422,642 [5,422,557–5,422,775] | 350 [350–350] | 139,667 [139,472–139,699] | 95 [95–95] |
| Parse 65,550 bytes | 5,130,998 [5,130,293–5,131,252] | 42,750 [42,750–42,750] | 16 [16–16] | 1 [1–1] |
| Dirty coherent `120x40` snapshot | 196,612 [196,612–196,612] | 2 [2–2] | 279,024 [279,024–279,024] | 121 [121–121] |
| Alternating resize | 222,121 [222,120–222,126] | 19 [19–19] | 16 [16–16] | 2 [2–2] |
| Reconstruct 4,194,326 bytes + snapshot | 333,202,408 [333,202,104–333,202,472] | 2,735,797 [2,735,795–2,735,798] | 419,926 [419,912–421,086] | 225 [225–225] |

## Measured RSS medians

The macOS probe calls `proc_pid_rusage(RUSAGE_INFO_V2)` for the parent and every
live helper in one short invocation. It samples at baseline, after each 512 KiB
reconstruction chunk or approximately every 2,048 scroll lines, and after final
snapshot validation plus parent GC. Reconstruction has 11 checkpoints; 12k and
24k scrolling have 8 and 14. The values below are the greatest aggregate from
one checkpoint in each run, with its contemporaneous parent/helper components.
They are measured aggregate RSS, not sums of lifetime peaks. The calls within a
checkpoint are sequential rather than kernel-atomic, which is the residual
sampling limitation.

In every retained run the greatest sampled aggregate was the final checkpoint,
so its parent and helper components also equal the sampled component peaks.

| Workload | Backend | Sampled parent peak | Sampled helper peak total | Measured same-checkpoint aggregate peak |
|----------|---------|--------------------:|--------------------------:|----------------------------------------:|
| Reconstruct 4 MiB, 1 terminal | Charm | 93.36 MiB [89.48–97.56] | n/a | 93.36 MiB [89.48–97.56] |
| Reconstruct 4 MiB, 1 terminal | Native | 22.25 MiB [21.66–22.66] | 16.61 MiB [16.22–16.84] | 38.86 MiB [38.41–39.14] |
| Scroll 12k, `240x40`, 1 terminal | Charm | 111.22 MiB [111.08–111.31] | n/a | 111.22 MiB [111.08–111.31] |
| Scroll 12k, `240x40`, 1 terminal | Native | 15.95 MiB [15.80–16.12] | 16.30 MiB [16.09–16.78] | 32.28 MiB [31.89–32.69] |
| Scroll 24k, `240x40`, 1 terminal | Charm | 171.06 MiB [166.39–182.36] | n/a | 171.06 MiB [166.39–182.36] |
| Scroll 24k, `240x40`, 1 terminal | Native | 16.94 MiB [16.91–17.50] | 16.59 MiB [16.42–16.75] | 33.62 MiB [33.33–34.06] |
| Scroll 12k, `240x40`, 3 concurrent terminals | Charm | 299.78 MiB [294.25–317.09] | n/a | 299.78 MiB [294.25–317.09] |
| Scroll 12k, `240x40`, 3 concurrent terminals | Native | 17.05 MiB [16.86–17.38] | 49.92 MiB [49.53–50.03] | 66.89 MiB [66.62–67.30] |
| Scroll 24k, `240x40`, 3 concurrent terminals | Charm | 532.67 MiB [531.17–548.86] | n/a | 532.67 MiB [531.17–548.86] |
| Scroll 24k, `240x40`, 3 concurrent terminals | Native | 17.50 MiB [17.12–17.75] | 49.98 MiB [49.52–50.41] | 67.31 MiB [66.81–68.16] |

For the key 4 MiB native case, the exact measured aggregate median is
40,747,008 bytes: 23,330,816 parent bytes plus 17,416,192 helper bytes in the
same checkpoint.

The next table reports process-lifetime peaks instead. Parent values come from
`getrusage(RUSAGE_SELF)` and helper values from each reaped child's
`ProcessState`. These peaks can occur at different times. Their sum is therefore
an explicitly labelled estimate, included only alongside the measured
same-checkpoint aggregate above. `/usr/bin/time -l` independently wrapped every
fresh parent process; its median was within 0.07 MiB of self-rusage in every row.

| Workload | Backend | Parent lifetime peak | Helper independent peak sum | Independent peak-sum estimate |
|----------|---------|---------------------:|----------------------------:|------------------------------:|
| Reconstruct 4 MiB, 1 terminal | Charm | 93.41 MiB [89.53–97.61] | n/a | 93.41 MiB [89.53–97.61] |
| Reconstruct 4 MiB, 1 terminal | Native | 22.28 MiB [21.66–22.66] | 16.62 MiB [16.23–16.86] | 38.91 MiB [38.45–39.16] |
| Scroll 12k, `240x40`, 1 terminal | Charm | 111.27 MiB [111.16–111.39] | n/a | 111.27 MiB [111.16–111.39] |
| Scroll 12k, `240x40`, 1 terminal | Native | 15.97 MiB [15.80–16.12] | 16.31 MiB [16.11–16.80] | 32.31 MiB [31.91–32.70] |
| Scroll 24k, `240x40`, 1 terminal | Charm | 171.11 MiB [166.44–182.41] | n/a | 171.11 MiB [166.44–182.41] |
| Scroll 24k, `240x40`, 1 terminal | Native | 16.94 MiB [16.91–17.50] | 16.61 MiB [16.44–16.77] | 33.64 MiB [33.34–34.08] |
| Scroll 12k, `240x40`, 3 concurrent terminals | Charm | 299.83 MiB [294.30–317.14] | n/a | 299.83 MiB [294.30–317.14] |
| Scroll 12k, `240x40`, 3 concurrent terminals | Native | 17.05 MiB [16.88–17.39] | 49.97 MiB [49.58–50.08] | 66.95 MiB [66.72–67.36] |
| Scroll 24k, `240x40`, 3 concurrent terminals | Charm | 532.75 MiB [531.22–548.91] | n/a | 532.75 MiB [531.22–548.91] |
| Scroll 24k, `240x40`, 3 concurrent terminals | Native | 17.50 MiB [17.12–17.75] | 50.03 MiB [49.56–50.45] | 67.36 MiB [66.86–68.20] |

Across the three-helper runs, median individual helper lifetime peaks ranged
from 16.42 to 16.78 MiB at 12k lines and from 16.55 to 16.75 MiB at 24k lines.

## Derived analysis, not measurements

The ratios and deltas below are calculated from the measured medians. They are
not additional samples and are not universal performance claims:

- Process isolation makes helper start/close about 106.1x slower than Charm.
  This 30.6 ms fixed cost is material for short-lived terminals and is reported
  separately from steady-state parse, snapshot, and reconstruction.
- The native helper parses the reduced stream about 54.1x faster by wall time.
  Adding native parent CPU to lifetime-amortized helper CPU gives an estimated
  900.0 µs total CPU per write, about 50.3x below Charm parent CPU. The native
  total remains an estimate because its helper component includes lifecycle and
  validation work.
- Dirty update plus coherent extraction is about 22.0x slower for the helper,
  while resize is about 2.85x slower. These are operational IPC/public-wrapper
  costs, not the superseded in-process shim.
- Full 4 MiB reconstruction is about 33.6x faster by wall time despite paying
  helper start, nine production-size write RPCs, snapshot transport, and close.
- The measured native 4 MiB same-checkpoint aggregate is about 2.40x below the
  Charm parent aggregate (38.86 versus 93.36 MiB). This comparison does not use
  independent process peaks.
- Doubling sustained scroll from 12k to 24k changes current native helper RSS by
  0.29 MiB for one helper and 0.06 MiB total for three helpers. The measured
  three-terminal aggregate changes by 0.42 MiB, while Charm grows by 232.89 MiB.
  This supports the claim that `WithMaxScrollback(0)` removes retained helper
  scrollback growth; Graith's separately bounded raw log remains authoritative.

The results support staged adoption for parsing and recovery workloads but show
why helper lifetime and dirty-frame frequency matter operationally. Charm stays
compiled and tested as the rollback until the accepted design's soak and
promotion gates are satisfied.

## Restart/adoption validation, not benchmark results

Final-head adoption does not synchronously construct or hydrate helpers before
service. It adopts raw PTY/log ownership with `DegradedScreen=true` and
`DeferWait=true`, then performs derived-screen recovery in an owned background
task. A helper-construction latency number would therefore not be a
time-to-listener measurement.

The deterministic daemon test creates three retained sessions under one 150 ms
absolute adoption deadline, asserts all three take the degraded raw-first path,
then writes unique markers and proves every raw reader drains them. A separate
test blocks the screen factory, proves degraded adoption never enters it, and
proves raw output continues draining while asynchronous recovery is blocked.
The exact native tagged exec-upgrade test then reconstructs a real libghostty
screen using the production 1 MiB hydration bound. The 150 ms and drain
deadlines are test thresholds, not measured medians, so they are deliberately
excluded from the tables above.

## References

- [Accepted native backend design](2026-07-18-libghostty-daemon-backend.md)
- [Issue #1444](https://github.com/d0ugal/graith/issues/1444)
- [Parent PR #1440](https://github.com/d0ugal/graith/pull/1440)
- `internal/pty/terminal_backend_test.go`
- `internal/pty/terminal_ghostty_process.go`
- `scripts/libghostty-native.sh`
