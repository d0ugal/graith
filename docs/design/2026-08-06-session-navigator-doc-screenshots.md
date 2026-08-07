---
title: "Design Doc: Session Navigator Documentation Screenshots"
authors: Codex
created: 2026-08-06
status: Implemented (v1)
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/2088
---

# Session Navigator Documentation Screenshots

Graith's public docs should show stable, realistic Session Navigator images
without maintaining a second terminal renderer. The docs screenshot suite reuses
`cmd/sessionnavshots` and the full-terminal status-bar render path added for PR
preview screenshots, then checks the generated PNGs into the Hugo site.

## Background

PR-preview screenshots already render deterministic fake TUI states through the
Go model and capture the ANSI output in `xterm`. Those images
are published to the `screenshots` branch for short-lived review. Public docs
need the same visual fidelity but need stable asset paths, source-controlled
ownership, and a contributor workflow for refreshing images.

## Problem

Embedding PR-preview image URLs in docs would make public pages depend on
ephemeral review artifacts. Hand-authored screenshots would drift from the real
TUI and can accidentally expose local session state. The docs need canonical
fake scenes that explain user workflows, including the status bar, labels,
repository grouping, jailed-comment attention, and orchestrator-attention states.

## Goals

- Reuse the Session Navigator model and full-terminal status-bar screenshot path.
- Keep documentation images deterministic and safe to regenerate locally or in CI.
- Give public docs stable URLs.
- Make visual drift reviewable in the PR that changes the fixture or renderer.
- Document the refresh command and asset ownership.

### Non-Goals

- Capturing real user sessions.
- Replacing the PR-preview screenshot workflow.
- Supporting multiple docs image sizes in v1.
- Implementing daemon-side attention persistence or production jailed-comment
  accounting beyond the `FleetSummary` fields already exposed to the status bar.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | The screenshots document the CLI/TUI Session Navigator. |
| iOS | Excluded | The native iOS app does not render the Bubble Tea terminal UI. |
| macOS | Excluded | The macOS app may show sessions, but these assets document the terminal TUI. |

## Proposals

### Proposal 0: Do Nothing

Docs continue describing the Navigator with text only. This avoids image
maintenance, but users cannot see the density, grouping, status bar, or warning
states before trying the TUI.

### Proposal 1: Checked-In Static PNGs (Recommended)

Add a docs suite to `cmd/sessionnavshots` with a docs fixture, one canonical
terminal size (`120x30`), and stable scene names. `make
docs-session-nav-screenshots` renders ANSI through the existing snapshot helper
and captures PNGs into `website/static/images/docs/session-navigator/`. Hugo
shortcodes reference those static files and fail the docs build if an image is
missing.

The trade-off is that screenshot refreshes create binary diffs. That is
intentional for v1: public docs need stable paths, and reviewers should see
visual drift alongside the code or fixture change that caused it.

### Proposal 2: Publish Generated Docs Images To A Branch

The screenshot workflow could publish docs images to the existing `screenshots`
branch or a separate static branch. That would avoid binary files in the source
branch, but it introduces URL stability, cache invalidation, branch cleanup, and
trust-boundary questions for public docs. It also makes local documentation
builds depend on remote image state.

### Proposal 3: Responsive Picture Variants

The docs could render multiple terminal geometries and use `<picture>` variants.
That adds maintenance and review volume before there is evidence that one
carefully chosen terminal size is insufficient. V1 keeps one docs size and
leaves responsive variants as a later extension.

## Other Notes

### References

- `cmd/sessionnavshots/main.go`
- `scripts/session-navigator-terminal-screenshot.sh`
- `website/layouts/shortcodes/session-nav-screenshot.html`
- `website/content/docs/keybindings.md`
- `website/content/docs/contributing/_index.md`
- `docs/design/2026-08-04-session-navigator-pr-screenshots.md`

### Implementation Notes

The docs fixture sets the same `FleetSummary` attention fields used by live
status bars for jailed-comment and orchestrator-attention scenes. The screenshot
path renders those fields through `formatStatusLine`, so docs screenshots and
attached-session chrome share one formatter.

### Alternatives considered

Plain text snapshots remain valuable for geometry tests, but they cannot
document color, row density, or terminal chrome. SVG or HTML terminal rendering
was rejected for the same reason as PR previews: it would create another visual
renderer that can drift from the live CLI.

### Testing

Tests cover docs suite defaults, canonical scene names, the `120x30` viewport,
status-bar inclusion, warning-scene fixture inputs, and shortcode asset
references through the Hugo docs build. The screenshot command also keeps the
existing preview-suite geometry tests so PR-preview defaults do not drift.
