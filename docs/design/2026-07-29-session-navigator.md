---
title: "Design Doc: Session Navigator"
authors: Codex
created: 2026-07-29
status: Implemented (v1)
reviewers: (none yet)
informed: (TBD)
issue: https://github.com/d0ugal/graith/issues/1454
---

# Session Navigator

Rename the `ctrl+b w` session-management surface to **Session Navigator** and
polish the existing TUI instead of adding a separate Command Center. The v1
implementation kept the existing tree, view modes, columns, live preview,
shortcuts, protocol, and daemon state model, while making the name and
selected-session context clearer in the CLI and docs. The follow-up cleanup for
#1871 deliberately breaks the old internal Go/config names so Navigator-owned
surfaces have one canonical namespace.

## Background

The attached client moves between raw passthrough mode and a full-screen Bubble
Tea overlay in `internal/client/overlay.go`. That overlay already does more than
choose a session: it navigates the fleet tree, cycles views, shows live preview
scrollback, renders shared `SessionColumns`, and drives create, attach, delete,
restore, stop, restart, fold, star, filter, and group-jump actions.

## Problem

The user-facing docs call this surface the "session picker overlay", while
implementation names such as `overlayModel`, `RunOverlay`, and `PickerState`
mix generic overlay language with old picker language. "Picker" undersells the
fleet navigation and management role, but a separate Command Center would
duplicate the same state projection with less operational context.

## Goals

- Pick one user-facing product name for `ctrl+b w`.
- Inventory current terminology and capabilities before changing names.
- Give Navigator-owned internal Go APIs and config keys canonical names.
- Improve selected-session context and discoverability without removing tree
  views, columns, or live preview.
- Validate the v1 rendering at compact `80x24` and wide `160x40` sizes.

### Non-Goals

- No separate Command Center, dashboard, or duplicate state model.
- No new "Needs Attention" or "Active" view.
- No daemon, protocol, persistence, iOS, or macOS work for the rename.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | `ctrl+b w`, `gr attach`, command help, default config comments, and website docs use the named surface. |
| iOS | Excluded | Native sidebars already have platform-native navigation and do not expose `ctrl+b w`. |
| macOS | Excluded | The GUI sidebar is related but not the terminal overlay being renamed here. |

## Proposals

### Proposal 0: Do Nothing

Leaving "session picker" in place avoids churn, but it keeps hiding the most
important behavior: this surface is already the central fleet navigator. It also
leaves stale docs: the overlay has stop and restart-menu actions, despite the
keybinding docs saying it does not.

### Proposal 1: Session Navigator (Recommended)

Use **Session Navigator** for the user-facing TUI name. It describes moving
through a session tree and managing sessions without implying a separate
dashboard.

Comparison:

| Name | Fit |
|------|-----|
| Session Picker | Accurate for attach, too narrow for tree navigation and management actions. |
| Session Switcher | Emphasizes attach/switch only, not create/delete/restart/filter/status. |
| Workspace Navigator | Too broad; graith sessions may be system, mirror, headless, or no-repo. |
| Command Center | Sounds like a dashboard/control plane and risks duplicating the overlay. |
| Session Navigator | Clear, scoped to sessions, and broad enough for tree/view/action workflows. |

Breaking cleanup plan:

- User-facing docs and inline help say "Session Navigator".
- `session_navigator = "w"` is the prefix key config for opening the Navigator.
- `[session_navigator]` owns Navigator options such as `shortcut_keys`.
- `[keybindings.tui]` owns shared message-viewer and scroll-pager TUI keys; the
  Navigator consumes only the shared `cancel` aliases from that table.
- Internal exported Go names use `RunSessionNavigator`,
  `RunSessionNavigatorOpts`, `SessionNavigatorResult`,
  `SessionNavigatorState`, and `SessionNavigatorKeys`.
- The old `session_list`, `[overlay]`, `[keybindings.overlay]`, `RunOverlay`,
  `OverlayResult`, `PickerState`, and `OverlayKeys` names are not retained.

### Proposal 2: Command Center

Do not build it. A separate dashboard would need to duplicate session ordering,
view filters, PR/CI/review/git/status rendering, action policy, and preview
selection. The current overlay already carries that context; improving it keeps
one source of truth.

## Other Notes

### Current terminology inventory

| Area | Current names | V1 action |
|------|---------------|-----------|
| User docs | "session picker", "picker overlay", "overlay" | Rename the `ctrl+b w` surface to Session Navigator, while keeping generic "overlay" for message viewer and scroll pager. |
| Prefix key | `session_list = "w"` | Rename to `session_navigator = "w"` in #1871. |
| Navigator config | `[overlay]` | Rename to `[session_navigator]` in #1871. |
| Shared TUI config | `[keybindings.overlay]` | Rename to `[keybindings.tui]` in #1871. |
| Go client | `RunOverlay`, `OverlayResult`, `PickerState`, `OverlayKeys` | Rename exported API names to `RunSessionNavigator`, `SessionNavigatorResult`, `SessionNavigatorState`, and `SessionNavigatorKeys` in #1871. |
| Docs history/design | Existing design docs mention overlay/picker | Leave historical records unless a user-facing page is stale. |

### Current capability inventory

The existing TUI already supports:

- parent/child trees, collapse/expand, fold-all, group jumps, and persisted
  in-attach view/selection state;
- All, Repo, Starred, Labels, Scenarios, and Deleted views;
- live preview scrollback behind the panel;
- status, summary, git, PR, review, CI, and output-age columns;
- create, attach, restore, delete, stop, restart one, restart visible sets,
  star/unstar, filter, and number-key attach.

### Implemented v1 changes

| Change | Before | After | Value |
|--------|--------|-------|-------|
| Name in TUI/docs | The title row started at the view tabs, and docs called it a session picker. | The title row starts with `Session Navigator`, followed by visible session count and existing view tabs. | Makes the primary surface name obvious without changing shortcuts or data model. |
| Prefix help label | The attached-session prefix help described `ctrl+b w` as `sessions`. | The one-line help bar uses the compact label `navigator`. | Keeps the high-frequency shortcut hint aligned with the new product name while preserving the existing key. |
| Selected-session context | The detail block jumped straight to branch/base/agent/path. | A `Selected:` line names the highlighted session and shows attached/repo/label/status context. | Reduces visual tracing on wide terminals and clarifies when the selected row is not the attached session. |
| Docs accuracy | Keybinding docs missed the stop action and described `R` as a direct restart-all shortcut. | Docs list `S` stop and `R` restart menu, matching the implementation. | Improves discoverability of existing actions without adding new state. |

Compact validation: `go test ./internal/client -run 'TestView_(SessionNavigatorContextAtCompactAndWideSizes|SessionNavigatorCompactRichDetailsKeepHelpVisible|SelectedContextAddsRepoInGroupedView)|TestShowHelpBarReflectsConfiguredKeys'`
locks the title, selected context, rich-detail footer, and prefix help label at
`80x24` and `160x40`. The wide case keeps the current live preview background
and column set; the compact cases keep the key context visible before truncating
the right side of the title or list body.

### Follow-up implementation issues

The remaining accepted work should stay split:

1. [**Adaptive Navigator help**](https://github.com/d0ugal/graith/issues/1869):
   replace the single long help line with compact
   first-line actions plus an expanded in-overlay help state. This improves
   discoverability without changing views or adding a dashboard.
2. [**Wide Navigator layout**](https://github.com/d0ugal/graith/issues/1870):
   for very wide terminals, keep the tree and live
   preview but use spare width for a richer selected-session detail area instead
   of only centering the same panel.
3. [**Internal naming cleanup**](https://github.com/d0ugal/graith/issues/1871):
   implemented as a breaking Go/config cleanup so the Session Navigator owns
   the `session_navigator` config namespace and exported client API names.
   Unexported implementation details such as `overlayModel` and `overlay.go`
   remain until a later refactor can split shared overlay machinery cleanly.

### Testing

- Focused client model tests for title/count/context at compact and wide sizes.
- Existing overlay tests continue to cover tree views, filtering, PR/CI/review
  rendering, deletion, restart, starring, shortcuts, and refresh behavior.
- Website docs should be built with the normal docs workflow before release.
