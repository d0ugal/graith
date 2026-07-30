---
title: "Design Doc: Input Gestures"
authors: Codex
created: 2026-07-30
status: Implemented (v1)
reviewers: (none yet)
informed: d0ugal
issue: https://github.com/d0ugal/graith/issues/1906
---

# Input Gestures

Graith attach can map a small set of semantic terminal input gestures to Graith
actions on a per-agent basis. The v1 scope is mouse-wheel gestures mapped to
`scroll_mode` or `none`, with defaults that help coding agents without taking
wheel input away from custom interactive agents.

## Background

Terminal-owned attach already mirrors the child terminal's mouse, focus,
bracketed-paste, application cursor-key, keypad, alternate-screen, and
alternate-scroll modes. That gives the client enough state to decide whether a
wheel event belongs to the child or can safely trigger Graith UI.

## Problem

Wheel-up is a useful shortcut for entering the scrollback pager in coding-agent
sessions, but unconditional capture breaks child programs that intentionally own
mouse input or alternate-screen alternate-scroll behavior. Raw terminal bytes are
also a poor configuration surface because mouse reporting format, coordinates,
modifiers, and terminal modes change the byte stream.

## Goals

- Support global input gesture defaults and per-agent overrides.
- Keep unknown or custom agents on the previous wheel behavior unless configured.
- Represent gestures and actions as Graith-owned enums, not raw escape bytes.
- Preserve child-owned mouse tracking and alternate-scroll in the default
  `respect_terminal_modes` policy.

### Non-Goals

- General key remapping beyond the existing prefix and overlay keybinding
  surfaces.
- Arbitrary terminal escape-code bindings.
- GUI gesture configuration for iOS or macOS in v1.

## Platform support

| Surface | Decision | Rationale |
|---------|----------|-----------|
| CLI | Targeted | `gr attach` owns terminal input routing and scroll mode. |
| iOS | Excluded | Native touch gesture physics are configured separately. |
| macOS | Excluded | Native menu shortcuts and app gestures are not daemon config. |

## Proposals

### Proposal 0: Do Nothing

Keep wheel input routed only to child mouse tracking or alternate-scroll and
leave the scroll pager reachable through the prefix binding. This avoids risk
but misses the ergonomic win for the common coding-agent case where the child has
not claimed wheel input.

### Proposal 1: Typed Gesture Bindings (Recommended)

Add `[input] mouse_wheel_policy` with `off`, `respect_terminal_modes`, and
`always`, plus `[input.bindings]` from semantic gesture names to Graith action
names. Add `[agents.<name>.input]` for per-agent overrides.
`respect_terminal_modes` triggers a configured Graith action only when the child
has not enabled mouse tracking and is not in alternate-screen alternate-scroll.
`always` triggers the action first and is documented as potentially stealing
wheel events from child apps.

This keeps config stable across terminal encoding details and leaves room for
new Graith actions or gestures later. The trade-off is a deliberately small v1:
only wheel gestures and `scroll_mode`/`none` actions are supported.

### Proposal 2: Raw Escape Bindings

Let users bind byte strings such as SGR mouse sequences directly. This was
rejected because the same physical gesture can produce different bytes depending
on terminal mode, coordinates, modifiers, and reporting format. It would make
configs brittle and mode-dependent.

## Other Notes

### References

- Issue: https://github.com/d0ugal/graith/issues/1906
- Config model: `internal/config/config.go`
- Attach loop: `internal/cli/attach_loop.go`
- Terminal input router: `internal/client/terminal_owned_input.go`

### Testing

Unit coverage should validate config enums, global and per-agent effective
resolution, attach-loop refresh on session agent changes, and terminal-owned
input routing for `off`, `respect_terminal_modes`, `always`, and `none`.
