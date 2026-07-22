---
weight: 500
title: "Keybindings"
description: "Keyboard shortcuts for the TUI."
icon: "keyboard"
toc: true
draft: false
---

## Passthrough mode

When attached, your terminal is in raw passthrough mode: all input goes straight to the agent's PTY unless the prefix key intercepts it.

### Prefix key

Default `ctrl+b`, configurable via `keybindings.prefix`.

Press the prefix key to show a help bar at the bottom of the screen (it clears
after the next keypress), then press one of these. Each key is configurable via
the matching `keybindings.*` field (defaults shown):

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
| `r` | Restart/resume the current session | `restart_session` |
| `ctrl+b` | Send a literal prefix byte to the agent | -- |

If two prefix commands share a key, graith starts but warns at load time and
only the first in passthrough order fires — pick distinct keys.

### Literal prefix

Press the prefix key twice (`ctrl+b ctrl+b`) to send a single `ctrl+b` to the agent — for when a program inside the session needs the prefix byte itself.

### Kitty protocol

graith also understands the Kitty keyboard protocol: extended terminals (e.g. Ghostty) send `ESC [ <codepoint> ; 5 u` for ctrl+key combinations, which graith normalizes to raw control bytes for prefix detection, stripping release events.

## Session picker overlay

The overlay is a full-screen TUI showing all sessions. Open it with `ctrl+b w`, or run `gr attach` with no arguments.

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

Cycle with `h`/`l` or arrows:

| View | Description |
|------|-------------|
| All | Every session, grouped by repo, with parent/child trees inside each repo |
| Starred | Starred sessions in a parent/child tree |
| Labels | Sessions grouped by label across all repositories, with a parent/child tree inside each label; a multi-labelled session appears in each matching group |
| Scenarios | Every session grouped by scenario, with a parent/child tree inside each scenario and unassigned sessions in a separate group |
| Deleted | Recently deleted sessions; press `enter` to restore the highlighted session |

### Actions

| Key | Action |
|-----|--------|
| `n` | Create a new session (opens a form with name, repo, agent, and optional comma-separated labels) |
| `x` | Delete session (prompts for confirmation with `y`) |
| `s` | Toggle starred state |
| `r` | Restart session (prompts for confirmation) |
| `R` | Restart all sessions in current view (prompts for confirmation) |
| Space | Fold/unfold children of a parent session |
| `C` | Fold/unfold all parent sessions |
| `/` | Enter filter mode (type to search by name, repo, or label) |
| Esc (in filter) | Clear filter and return to list |

Text search narrows the selected view, so searching while in **Labels** keeps the
cross-repository label grouping. Refresh preserves the selected label group when
the session still belongs to it. An empty Labels view says that there are no
labelled sessions. Trees contain only sessions matched by the selected view and
search: when a parent is absent, its visible child is shown as a root.

The overlay has no stop, resume, rename, or label-edit action — use `gr stop`,
`gr restart`, or `gr update` from the CLI.

### Preview panel

The right side of the overlay shows a live preview of the selected session's
terminal screen. The daemon maintains the screen model, and clients request
snapshots while the session produces output.

### Session display

Each session row shows:

| Column | Content |
|--------|---------|
| Name | Session name (with star indicator if starred) |
| Status | Running, stopped, errored, or agent status (`active`, `ready`, or `error`) |
| Summary | Status text, tool name from hooks, or auto-derived activity |
| Git | Branch name (or "(in-place)"), dirty indicator, unpushed commit count |
| Output | Age of most recent output |

## Message viewer and scroll pager

The message viewer (`ctrl+b m`) and scrollback pager (`ctrl+b [`) share a
configurable navigation vocabulary and add their own action keys.

| Overlay | Keys | Config keys |
|---------|------|-------------|
| Message viewer | `j`/`k` move · `pgdn`/`pgup` scroll · `g`/`G` first/last · `h`/`l` conversation · `enter` pin · `O`/`C` expand/collapse all · `q` close | `overlay.up`/`down`, `overlay.page_down`/`page_up`, `overlay.top`/`bottom`, `overlay.message_prev_conversation`/`message_next_conversation`, `overlay.message_pin`, `overlay.message_expand_all`/`message_collapse_all`, `overlay.cancel` |
| Scroll pager | `g`/`G` top/bottom · `q` quit (up/down/page keys are handled by the pager) | `overlay.top`/`bottom`, `overlay.cancel` |

## Configuring overlay keys

The full-screen overlays read their keys from the `[keybindings.overlay]` config
table. Each value is a space-separated list of [Bubble Tea](https://github.com/charmbracelet/bubbletea)
key names (single letters, `up`, `down`, `enter`, `esc`, `pgup`, `ctrl+d`, …);
any listed key triggers the action. A partial table overrides only the keys it
names — the rest keep their defaults. See
[interface configuration]({{< relref "configuration/interface.md" >}}) for the
full list and defaults.

## Shell

Press `ctrl+b s` to open an interactive shell in the current session's worktree, as a child process with `GRAITH_WORKTREE` set to that path. On exit, the terminal resets (alternate screen buffer cleared, mouse tracking disabled, cursor shown) and you return to the agent session.
