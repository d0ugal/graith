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

Every prefix-action key is configurable via the matching `keybindings.*` field
(the defaults are shown below):

| Key | Action | Config key |
|-----|--------|-----------|
| `w` | Open the session picker overlay | `session_list` |
| `d` | Detach (leave the agent running) | `detach` |
| `s` | Open a shell in the session's worktree | `shell` |
| `c` | Create a new session | `new_session` |
| `f` | Fork the current session | `fork_session` |
| `n` | Switch to the next session | `next_session` |
| `p` | Switch to the previous session | `prev_session` |
| `l` | Toggle to the last (most recently attached) session | `last_session` |
| `o` | Switch to the orchestrator session | `orchestrator_session` |
| `,` | Rename the current session | `rename_session` |
| `[` | Open the scrollback pager | `scroll_mode` |
| `m` | Open the message viewer | `messages` |
| `a` | Open the approvals overlay | `approvals` |
| `r` | Restart/resume the current session | `restart_session` |
| `ctrl+b` | Send a literal prefix byte to the agent | -- |

The help bar appears at the bottom of the screen when the prefix key is pressed,
showing the currently-configured commands. It disappears after the next keypress.

Each prefix-action field accepts exactly one printable ASCII byte, or an empty
value to disable that action. Multi-character, multibyte, control, and NUL
values are rejected at config load. A single-byte action value is taken
literally and case-sensitively (`A` is not `a`, and a single space `" "` is a
valid binding). `keybindings.prefix` additionally accepts the `ctrl+<letter>`
syntax (case-insensitive, e.g. `ctrl+b`); a one-byte value there is likewise a
literal. If two commands resolve to the same byte, or an action collides with a
single-byte prefix, graith starts anyway but prints an actionable warning naming
the command that actually wins. The prefix always wins over an action sharing
its byte; otherwise the earliest command in passthrough order wins, so pick
distinct keys.

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

| Key | Action | Config key |
|-----|--------|-----------|
| `j` / Down | Move cursor down | `overlay.down` |
| `k` / Up | Move cursor up | `overlay.up` |
| Enter / `a` | Attach to session | `overlay.dashboard_attach` |
| `s` | Stop session (with confirmation) | `overlay.dashboard_stop` |
| `x` / `d` | Delete session (with confirmation) | `overlay.dashboard_delete` |
| `r` | Resume a stopped session | `overlay.dashboard_resume` |
| `q` / Esc / `ctrl+c` | Quit | `overlay.cancel` |

Stop and delete show `[y/N]`: `y` or `Y` confirms, while Enter, Escape, and any
other key decline and return to the dashboard. These confirmation keys come
from `overlay.confirm`; the shipped value is `"y Y"`.

## Message viewer, approvals, and scroll pager

The message viewer (`ctrl+b m`), approvals overlay (`ctrl+b a`), and scrollback
pager (`ctrl+b [`) share a configurable navigation vocabulary and add their own
action keys.

| Overlay | Keys | Config keys |
|---------|------|-------------|
| Message viewer | `j`/`k` move · `pgdn`/`pgup` scroll · `g`/`G` first/last · `h`/`l` conversation · `enter` pin · `O`/`C` expand/collapse all · `q`/Esc/`ctrl+c` close | `overlay.up`/`down`, `overlay.page_down`/`page_up`, `overlay.top`/`bottom`, `overlay.message_prev_conversation`/`message_next_conversation`, `overlay.message_pin`, `overlay.message_expand_all`/`message_collapse_all`, `overlay.cancel` |
| Approvals | `y` allow · `n`/`x` deny · `a` allow-all · `q`/Esc/`ctrl+c` cancel | `overlay.approval_allow`, `overlay.approval_deny`, `overlay.approval_allow_all`, `overlay.cancel` |
| Scroll pager | `g`/`G` top/bottom · `q`/Esc/`ctrl+c` quit (up/down/page keys are handled by the pager) | `overlay.top`/`bottom`, `overlay.cancel` |

## Configuring overlay keys

The full-screen overlays read their keys from the `[keybindings.overlay]` config
table. Each value is a space-separated list of [Bubble Tea](https://github.com/charmbracelet/bubbletea)
key names (single letters, `up`, `down`, `enter`, `esc`, `pgup`, `ctrl+d`, …);
pressing any listed key triggers the action. A partial table overrides only the
keys it names — every other key keeps its default. See the
[interface configuration]({{< relref "configuration/interface.md" >}}) page for
the full list of keys and their defaults.

## Shell

Press `ctrl+b s` to open an interactive shell in the current session's worktree. The shell runs as a child process with `GRAITH_WORKTREE` set to the worktree path. When you exit the shell, the terminal is reset (alternate screen buffer cleared, mouse tracking disabled, cursor shown) and you return to the agent session.
