---
title: "Design Doc: macOS FSEvents file-watch backend"
authors: d0ugal
created: 2026-07-27
status: Implemented (v1)
reviewers: (none yet)
informed: (none yet)
issue: https://github.com/d0ugal/graith/issues/1750
---

# macOS FSEvents file-watch backend

macOS file-watch triggers should use Apple's recursive FSEvents API instead of
fsnotify's kqueue backend when cgo is available. Graith keeps the existing
matcher, ignore, debounce, binding lifecycle, retry, and trigger delivery code,
but the backend cost drops from one kqueue-style registration per watched
directory tree to one FSEvents stream per binding.

## Background

The v1 file-watch resource bounds in
`docs/design/2026-07-25-file-watch-resource-bounds.md` narrowed recursive
fsnotify registration and added a daemon-wide budget. That kept the daemon from
running into `EMFILE`, but it did not change macOS's underlying cost model:
fsnotify uses kqueue, and kqueue can consume descriptors for watched directories
and their entries.

FSEvents is different. It is path based, recursive by design, and reports
changes through an FSEventStream instead of one descriptor-oriented registration
per directory. The Go package `github.com/fsnotify/fsevents` is macOS-only,
cgo-based, and maintained by the same project family as fsnotify.

## Problem

Broad repository watches such as `**/*.go` still make macOS sessions expensive
even after include-aware pruning. A handful of live Graith worktrees can reserve
or consume thousands of kqueue registrations, forcing operators either to narrow
every trigger aggressively or accept degraded bindings. The daemon already has
the correct trigger semantics above the backend; the expensive part is the
backend registration mechanism.

## Goals

- Avoid one kqueue registration per directory entry on macOS.
- Keep non-macOS behavior on the existing fsnotify backend.
- Preserve include/ignore matching, `.gitignore` reloads, debounce, binding
  lifecycle, retry/backoff, and trigger delivery.
- Handle edits, creates, renames, deletes, and moved-in directory trees.
- Document FSEvents coalescing, latency, and path/rename limits.

### Non-Goals

- Sharing watch backends across bindings.
- Rewriting trigger matching or debounce.
- Adding a user-facing backend selection config in v1.
- Adding new diagnostic surfaces beyond the existing watcher attribution exposed
  by trigger status and `gr doctor`.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | Trigger behavior and status remain visible through existing commands. |
| iOS | Excluded | File-watch triggers are daemon-side automation, not an iOS capability. |
| macOS | Targeted | This is the platform where kqueue descriptor multiplication causes the reported resource exhaustion. |

## Proposals

### Proposal 0: Do Nothing

Keep fsnotify everywhere. This avoids dependency and cgo changes but leaves
macOS resource use proportional to worktree directory and entry count.

### Proposal 1: Automatic FSEvents root stream on macOS+cgo (Recommended)

Introduce a small daemon-internal backend interface. The default backend remains
fsnotify except on `darwin && cgo`, where each binding starts one FSEvents stream
for the resolved worktree root with `FileEvents`, `WatchRoot`, and a short
FSEvents latency. The event loop converts backend events into the existing
trigger event path and keeps all matching, ignores, debounce, lifecycle, retry,
and delivery logic shared.

FSEvents directory events for newly-created, renamed, or moved-in trees call the
existing subtree scan path so files already present in that tree are not missed.
If FSEvents reports `MustScanSubDirs`, `UserDropped`, `KernelDropped`, or
`RootChanged`, Graith reloads ignore rules and scans the named subtree
conservatively. When that lossy scan finds no surviving matching file but the
subtree can contain an included path, Graith records the directory itself so
deletion-only bursts still arm the debounce.

The existing `watch_max_directories` knob remains for compatibility, but the
count is now an estimated backend cost. fsnotify registrations keep their
existing platform-specific cost estimate; an FSEvents binding costs one unit for
one recursive stream.

### Proposal 2: Configurable backend selection

Expose a `[triggers.advanced] watch_backend = "auto|fsnotify|fsevents"` option.
This could help debug platform-specific problems, but it creates a new support
surface before Graith has evidence that users need to switch back. The v1 keeps
selection automatic and can add a knob later if the backend needs operational
escape hatches.

## Decision

Implement Proposal 1 for issue #1750. Automatic selection gives macOS users the
resource improvement without changing existing configuration, while non-macOS
and Darwin builds without cgo keep the current fsnotify path.

## Other Notes

### References

- Issue #1750: FSEvents backend for recursive macOS file watching.
- Issue #1666: file-watch descriptor exhaustion.
- Issue #1749: per-session watcher status and diagnostics.
- `internal/daemon/filewatch.go`: trigger matching, ignore, debounce, and
  delivery semantics.
- `internal/daemon/filewatch_backend*.go`: backend selection and adapters.

### Implementation Notes

FSEvents reports paths, not file descriptors. If the watched root is a symlink,
the backend watches the resolved target and translates event paths back to the
logical Graith worktree root before matching. FSEvents rename events do not tell
Graith both old and new names; the event's reported path is matched as-is, and
moved-in directories are scanned when they appear as created or renamed
directories.

FSEvents has its own latency and coalescing before Graith's debounce window. The
backend uses a short latency and `NoDefer` so interactive triggers still feel
prompt, but bursts can still arrive as batches and loss/coalescing flags can
force conservative subtree scans.

### Testing

Existing fsnotify tests continue to cover pruning, budget degradation,
ignore-rule reload, lifecycle teardown, retry recovery, and synthetic create /
remove / rename handling. Darwin+cgo tests assert that the production backend is
FSEvents, that a broad tree costs one stream, that directory rename events ask
for a subtree scan, and that a nested matching file creation fires through the
full trigger path. Synthetic subtree scan tests cover coalesced and missing-path
lossy FSEvents cases without depending on OS timing.
