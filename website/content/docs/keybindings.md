---
weight: 500
title: "Keybindings"
description: "Keyboard shortcuts for the TUI."
icon: "keyboard"
toc: true
draft: false
---

## Passthrough mode

When attached to a session, your terminal is in raw passthrough mode. All input goes directly to the agent's PTY except when intercepted by the prefix key.

### Prefix key

Default: `ctrl+b` (configurable via `keybindings.prefix` in config).

Press the prefix key to show a help bar, then press one of the following:

| Key | Action | Configurable |
|-----|--------|:---:|
| `w` | Open the session picker overlay | no |
| `d` | Detach (leave the agent running) | no |
| `s` | Open a shell in the session's worktree | no |
| `c` | Create a new session | yes |
| `f` | Fork the current session | yes |
| `n` | Switch to the next session | yes |
| `p` | Switch to the previous session | yes |
| `l` | Toggle to the last (most recently attached) session | yes |
| `r` | Resume/restart the current session | no |
| `a` | Open the approvals overlay | no |
| `o` | Switch to the orchestrator session | yes |
| `ctrl+b` | Send a literal prefix byte to the agent | -- |

The help bar appears at the bottom of the screen when the prefix key is pressed, showing available commands. It disappears after the next keypress.

Keys marked "configurable" can be remapped in `keybindings.*` config. The others are hardcoded in passthrough. Note: several keybinding config fields (`delete_session`, `rename_session`, `search`, `scroll_mode`, `resume_session`) exist in the config struct but are not currently wired into passthrough. They are reserved for future use.

### Literal prefix

Press the prefix key twice (`ctrl+b ctrl+b`) to send a single `ctrl+b` to the agent. This is necessary when the agent or a program inside the session needs to receive the prefix byte.

### Kitty protocol

graith handles both raw control bytes and Kitty keyboard protocol CSI u sequences. Terminals that use the extended protocol (e.g. Ghostty) send `ESC [ <codepoint> ; 5 u` for ctrl+key combinations. graith normalizes these to raw control bytes for prefix detection, and strips release events.

## Session picker overlay

The overlay is a full-screen TUI (built with Bubble Tea) that shows all sessions. Open it with `ctrl+b w` or by running `gr attach` with no arguments.

### Navigation

| Key | Action |
|-----|--------|
| `j` / Down | Move cursor down |
| `k` / Up | Move cursor up |
| `g` / Home | Jump to top |
| `G` / End | Jump to bottom |
| `h` / Left | Previous view mode |
| `l` / Right | Next view mode |
| Tab | Jump to next group header |
| Enter | Attach to the highlighted session |
| `q` / Esc | Close the overlay |

### View modes

Cycle with `h`/`l` or left/right arrow keys:

| View | Description |
|------|-------------|
| All | Every session, grouped by repo. Starred first, then running, then by name |
| Needs Attention | Sessions needing user action: waiting for approval, errored, idle (running but ready), or stopped with dirty/unpushed changes. Sorted oldest-first by time in current state |
| Active | Running sessions only, sorted newest-first by creation time |
| Starred | Starred sessions only |

### Actions

| Key | Action |
|-----|--------|
| `n` | Create a new session (opens a form with name and repo fields) |
| `x` | Delete session (prompts for confirmation with `y`) |
| `s` | Star/unstar session |
| `r` | Restart session (prompts for confirmation) |
| `R` | Restart all sessions in current view (prompts for confirmation) |
| Space | Fold/unfold children of a parent session |
| `C` | Fold/unfold all parent sessions |
| `/` | Enter filter mode (type to search by name or repo) |
| Esc (in filter) | Clear filter and return to list |

There is no stop, resume, or rename action in the overlay. Use `gr stop`, `gr restart`, or `gr rename` from the CLI.

### Preview panel

The right side of the overlay shows a live preview of the selected session's terminal output, rendered with a VT100 parser (vt10x). The preview updates as the session produces output.

### Session display

Each session row shows:

| Column | Content |
|--------|---------|
| Name | Session name (with star indicator if starred) |
| Status | Running, stopped, errored, or agent status (e.g. approval) |
| Summary | Status text, tool name from hooks, or auto-derived activity |
| Git | Branch name (or "(in-place)"), dirty indicator, unpushed commit count |
| Output | Age of most recent output |

## Dashboard

The dashboard (`gr dashboard`) is a live-updating TUI similar to the session picker but designed for monitoring.

| Key | Action |
|-----|--------|
| `j` / Down | Move cursor down |
| `k` / Up | Move cursor up |
| Enter / `a` | Attach to session |
| `s` | Stop session (with confirmation) |
| `x` / `d` | Delete session (with confirmation) |
| `r` | Resume a stopped session |
| `q` / `ctrl+c` | Quit |

## Shell

Press `ctrl+b s` to open an interactive shell in the current session's worktree. The shell runs as a child process with `GRAITH_WORKTREE` set to the worktree path. When you exit the shell, the terminal is reset (alternate screen buffer cleared, mouse tracking disabled, cursor shown) and you return to the agent session.
