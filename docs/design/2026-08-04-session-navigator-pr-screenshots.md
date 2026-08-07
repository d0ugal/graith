---
title: "Design Doc: TUI PR Screenshots"
authors: Codex
created: 2026-08-04
status: Implemented (v1)
reviewers: (none yet)
informed: (TBD)
---

# TUI PR Screenshots

Terminal UI rendering changes should be reviewable in GitHub before merge. This design adds deterministic fake-data TUI renders at multiple terminal sizes, screenshots them in CI, publishes the images to the existing `screenshots` branch, and maintains a separate sticky PR comment.

## Background

Docs preview already solves most of the infrastructure problem: it classifies changed files, renders screenshots, diffs base and head PNGs with `cmd/docsdiff`, publishes artifacts to the orphan `screenshots` branch with `cmd/docspreview`, cleans up closed PRs, and prunes old run directories.

The Session Navigator itself is rendered by Bubble Tea and Lip Gloss in `internal/client/overlay.go`. Its model can already render from in-memory `protocol.SessionInfo` values once it receives a terminal size message.

## Problem

Navigator UI changes are hard to review from text diffs. Existing tests assert individual strings and layout properties, but reviewers cannot see how the complete TUI looks at small, normal, and wide terminal sizes. Local real session state is also unsuitable for CI because it is non-deterministic and may contain private data.

## Goals

- Render the live Session Navigator view path non-interactively.
- Use deterministic fake session data only.
- Capture small, normal, and wide terminal layouts.
- Reuse the existing screenshot branch, image diff, cleanup, pruning, and sticky-comment pattern.
- Keep fork PRs read-only while still allowing same-repo PRs to publish previews.

### Non-Goals

- Capturing real local sessions.
- Replacing the docs preview workflow.
- Building an interactive browser version of the Navigator.
- Guaranteeing exact font metrics for every user terminal.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | The Session Navigator is a CLI/TUI surface and CI renders its Go model directly. |
| iOS | Excluded | Native iOS does not render this Bubble Tea TUI. |
| macOS | Excluded | The macOS app may display session data, but this preview is specifically for the terminal TUI. |

## Proposals

### Proposal 0: Do Nothing

Reviewers continue to infer TUI changes from Go diffs and focused string tests. This keeps CI simple but misses regressions in density, wrapping, side-panel behavior, and color/status combinations.

### Proposal 1: Render ANSI Through xterm In Xvfb (Recommended)

Add a small exported snapshot helper in `internal/client` that constructs the existing overlay model with fake `protocol.SessionInfo` values, injects a fixed terminal size, and returns the ANSI string from `View()`. A command, `cmd/sessionnavshots`, reads a JSON fixture and emits ANSI, plain text, `pages.json`, and `viewports.json`.

The workflow starts `xterm` under `Xvfb`, displays each ANSI snapshot in a real terminal window with stable geometry and font settings, then captures PNGs with ImageMagick. `cmd/docsdiff` and `cmd/docspreview` handle image diffs, branch storage, cleanup, pruning, and the sticky PR comment.

Snapshots render the full terminal frame, not just the Navigator child view. The command reserves one row for the same attached-session status bar/chrome renderer used by the CLI and renders the Navigator into the remaining rows. This makes status-bar-adjacent UI changes visible in the TUI preview instead of producing a silent all-same manifest. The rendered composite is a deliberate CI preview surface: the live Navigator normally owns the full overlay after attach chrome has been torn down, so the Navigator content in these screenshots is one row shorter than the live full-screen overlay. That trade-off is accepted so one preview suite covers the Session Navigator footer, surrounding terminal chrome, and status-bar signals that influence whether a PR should show visual output.

This avoids maintaining a second browser/HTML renderer for terminal behavior. The terminal screenshot script only displays ANSI files; it does not know about Session Navigator rows, columns, labels, borders, or colors.

### Proposal 2: Render ANSI HTML From The Go Model

HTML screenshots are lighter than a terminal emulator and align with the docs-preview k6/Chromium path. This was rejected for v1 because the HTML conversion would still need to approximate terminal cell painting, border glyph widths, color handling, and font metrics. That creates a second visual renderer that can drift from what reviewers see when they run the real CLI.

### Proposal 3: Golden Text Snapshots Only

Text snapshots are easy to diff and review in tests, but GitHub PR comments would not show color, selected-row emphasis, or visual density. This remains useful as test coverage, but it does not satisfy the reviewer screenshot goal.

## Other Notes

### References

- `.github/workflows/docs-preview.yml`
- `cmd/docsdiff/main.go`
- `cmd/docspreview/main.go`
- `internal/docspreview/docspreview.go`
- `internal/client/overlay.go`
- `internal/client/sessioncols.go`

### Implementation Notes

The fake fixture should cover long names and labels, parent/child sessions, PR and CI state, stale config, dirty and unpushed git state, stopped/running/error sessions, deleted sessions, scenario/system rows, mirror sessions, read-only sessions, in-place sessions, and the attached-session status bar. The status-bar fixture stores fleet state as raw JSON before decoding it into the checked-out branch's `protocol.FleetSummary`, so fixture fields for newer fleet signals are ignored by older base commits but become visible when a PR adds support for those fields. A scene can replace the default status-bar fixture when it needs a different attached-session shape; the default scene set uses that to keep both compact fleet signals and long branch/PR/ahead status-bar sections covered. The default scenes render the all, repo, labels, and deleted views so reviewers see the most common repository-grouped workflow alongside edge-case views.

The first matrix is:

| Label | Terminal |
|-------|----------|
| small | 80x24 |
| normal | 120x30 |
| wide | 240x40 |

The matrix is owned by `cmd/sessionnavshots` rather than copied into the workflow. Workflow policy tests assert the Action uses the command defaults so geometry tests and CI screenshots cannot drift to different sizes.

`docspreview` remains the publisher name because it already owns the `screenshots` branch. It gains suite metadata so docs and TUI previews use separate sticky markers and comment titles while sharing storage, cleanup, and pruning behavior. No-difference preview runs do not leave sticky PR comments behind; if a previous sticky comment exists, the publish step deletes it instead of replacing it with a no-change report.

The live interactive navigator and the snapshot helper both construct the model through `newSessionNavigatorModel`. Regression tests compare `RenderSessionNavigatorSnapshot` against the configured live model's `View().Content` byte for byte at the same terminal size, populate every snapshot option in that equality test, verify custom keybindings show up in the rendered footer, and fail when `RunSessionNavigatorOpts` gains a render-affecting field without a matching snapshot option. This forces future model-construction changes to keep the screenshot path in sync with the real CLI view or make a deliberate non-rendering classification in test code.

`scripts/session-navigator-terminal-screenshot.sh` is intentionally generic: it validates artifact names, opens an ANSI file in `xterm`, waits for an in-terminal ready signal after the ANSI has been written, rejects undersized, invalid, implausibly small, and blank/uniform PNGs, captures the window, and exits. It is not allowed to duplicate Navigator-specific layout or style rules. The trusted publisher validates PNG assets before writing them to the `screenshots` branch. PR thumbnails link to the raw full-resolution PNGs so dense terminal captures remain reviewable after GitHub scales the table view.

### Alternatives considered

SVG output would be easy to embed and deterministic, but it would require a second renderer for styled terminal text. Plain ANSI artifacts are preserved for debugging, but PNGs captured from a real terminal are the primary PR review surface.

### Testing

Unit tests cover the non-interactive renderer, the snapshot command output and terminal geometry invariants, expected scene/view markers, explicit bottom-border/footer clipping guards, the byte-for-byte live-model sync guard, render-affecting option coverage, custom viewport labels in `docsdiff`, suite-specific publisher/comment behavior, trusted PNG asset validation, classifier routing, terminal-capture readiness safeguards, and workflow policy/security checks. Focused package tests are sufficient for this v1 because the workflow reuses existing GitHub publishing code paths.
