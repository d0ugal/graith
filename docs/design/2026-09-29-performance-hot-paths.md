---
title: "Design Doc: Reduce repeated transcript and viewport work"
authors: Codex
created: 2026-09-29
status: Draft
reviewers: (none yet)
informed: repository maintainer
---

# Reduce repeated transcript and viewport work

Reduce unnecessary filesystem reads during token polling and cell extraction
when terminal output changes only a small part of the viewport.

## Background

Token polling resolves Codex transcripts before checking the size/mtime cache.
Captured session IDs still require opening rollout metadata in directory order.
The native terminal helper extracts every visible cell after any write, even
when the write only moves the cursor. Both paths run repeatedly.

## Problem

During a local slowdown investigation, resolving 35 Codex transcripts against
2,474 rollouts took 22.6 seconds; parsing usage from the first three took 50 ms.
A short live metrics interval reported a mean screen snapshot duration of
18.6 ms, compared with 20 microseconds for interactive PTY writes. These are
component measurements, not proof that either explains every observed delay.

## Goals

- Avoid opening unrelated rollouts when Codex's standard filename identifies
  the requested session, while still verifying metadata.
- Extract only changed viewport rows when the native render state allows it.
- Preserve legacy transcript names, rendering semantics, and helper isolation.
- Compare representative benchmarks before and after the changes.

### Non-Goals

- Changing prompt submission delay, live daemon configuration, or protocol.
- Replacing the upstream binding with a private bulk extraction interface.
- Claiming machine load or live lock contention has been conclusively profiled.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | Token polling and terminal-owned attach use these paths. |
| iOS | Targeted | Benefits from shared daemon token and snapshot work. |
| macOS | Targeted | Benefits from shared daemon token and snapshot work. |

No frontend capability or wire shape changes.

## Proposals

### Proposal 0: Do Nothing

Retains measured repeated filesystem work and full-viewport extraction for
small updates. Existing input-lock fixes do not eliminate this work.

### Proposal 1: Use existing identity and dirty metadata (Recommended)

First look for rollout filenames ending in the requested session ID, verifying
`session_meta.id` before accepting a candidate. Fall back to the original
metadata scan for renamed/legacy files and stop walking immediately on success.
No persistent index or unvalidated filename becomes authoritative.
If both a native-named and a legacy-named file claim the same ID, the verified
native filename now wins; previously the first metadata match in walk order
won. Missing IDs, unresolved IDs, and legacy fallback still require metadata
scans. Negative caching is deferred because root-directory mtime alone cannot
invalidate changes inside existing date subdirectories.

Retain the helper's converted cells between refreshes. Refresh every cell on
initialization, geometry change, or full native invalidation; otherwise refresh
only dirty rows. Clear native dirty flags only after extraction succeeds, and
continue to read cursor/input modes for every snapshot. Resize invalidates the
cell cache even if the resulting cell count is unchanged. The native helper
continues to own all VT parsing.

### Proposal 2: Persistent transcript index and bulk viewport API

An index adds invalidation and lifecycle complexity. An upstream bulk API
remains worthwhile (#1441), but requires a separately reviewed dependency
upgrade. The existing metadata supports a smaller optimization now.

## Other Notes

### References

- #1955: terminal-owned attach latency and the already implemented poll-lock fix.
- #1441: upstream bulk viewport extraction.
- #1960, #1962, #1996: existing observability.
- #1947: terminal history limits; history extraction is separate from refresh.
- #1750: macOS file watching; this change does not alter watchers.

### Testing

Synthetic rollout lookup benchmark with thousands of files, verified filename
candidates and legacy fallback tests, viewport benchmarks at multiple sizes,
and incremental-versus-forced-full snapshot comparisons including resize,
scrolling, erasure, alternate screens, palette/style changes, and wide glyphs.
Run affected packages and native race coverage. Keep private logs out of GitHub.

### Measurements

Paired precompiled benchmarks on an Apple M5 under concurrent machine load
(median of three runs; timings are approximate):

| Workload | Before | After | Bytes allocated before / after |
|----------|--------|-------|--------------------------------|
| Locate the last ID among 2,500 rollouts | 693 ms | 14.7 ms | 166 MB / 0.73 MB |
| One changed row, 80×24 | 6.64 ms | 0.57 ms | 456 KB / 98 KB |
| One changed row, 120×40 | 9.95 ms | 1.01 ms | 1.13 MB / 220 KB |
| One changed row, 240×72 | 37.8 ms | 3.19 ms | 4.03 MB / 743 KB |

Full-redraw allocations remain effectively unchanged; the optimization targets
in-place TUI updates. Scrolling output moves the native viewport and still
takes the full extraction path. Full-redraw wall times varied with host load and
are not evidence of improvement. These measure in-helper extraction, not final
terminal painting or a deployed daemon. The live daemon was not restarted.

Reproduce using `BenchmarkFindCodexRolloutByID` in `internal/agent/transcript`
and `BenchmarkGhosttySnapshot` in `internal/pty` (the latter requires the
pinned native dependency and `-tags=libghostty`). The cursor-only allocation
regression fails on the old full-extraction implementation. Snapshot parity
checks compare cached rows with forced full extraction after each update.

The paired measurements used baseline implementation `59ff50419` with the new
benchmark files copied in, and the implementation first committed in
`304414ca5`. Each package was compiled with `go test -c` before timing; the
native package also used `-tags=libghostty` and the pinned dependency's
`PKG_CONFIG_PATH`. Run the resulting binaries sequentially with:

```bash
./transcript.test -test.run='^$' -test.bench='^BenchmarkFindCodexRolloutByID$/last$' -test.benchmem -test.benchtime=300ms -test.count=3
./pty.test -test.run='^$' -test.bench='^BenchmarkGhosttySnapshot/(80x24|120x40|240x72)/(row|full)$' -test.benchmem -test.benchtime=300ms -test.count=3
```

Both runs used Go 1.27.1 on macOS arm64. Repeat under a quiet, comparable machine
workload for stable wall-time estimates.

The lookup benchmark now also includes first and middle targets; the table
reports the last-target case, which represents a recent rollout in a sorted
history rather than an average across all session ages.
