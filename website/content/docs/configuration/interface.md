---
weight: 350
title: "TUI & input"
description: "Keybindings, the session picker overlay, and input handling."
icon: "keyboard"
toc: true
draft: false
---

## Keybindings

```toml
[keybindings]
prefix               = "ctrl+b"  # prefix key
new_session          = "c"       # create a session
fork_session         = "f"       # fork the current session
delete_session       = "x"       # delete a session in the picker
detach               = "d"       # detach without stopping the agent
session_list         = "w"       # open the session picker overlay
next_session         = "n"       # next session
prev_session         = "p"       # previous session
last_session         = "l"       # last (most recently attached) session
resume_session       = "R"       # open the restart menu for a session in the picker
rename_session       = ","       # rename the current session
search               = "/"       # filter sessions in the picker
scroll_mode          = "["       # open a scrollable pager over the session history
shell                = "s"       # open a shell in the worktree
orchestrator_session = "o"       # switch to the orchestrator session
messages             = "m"       # open the message viewer for the current session
restart_session      = "r"       # restart (resume) the current session
```

The prefix key accepts values like `ctrl+b`, `ctrl+x`, or a single character; the rest are single characters pressed after the prefix (or, for `delete_session`/`resume_session`/`search`, inside the session picker). graith handles both raw control bytes and Kitty keyboard protocol sequences, so it works in extended-protocol terminals like Ghostty.

If two prefix commands share a key, graith warns at load time and starts anyway (only the first command in passthrough order fires).

### Overlay keys

The full-screen overlays (message viewer and scroll pager) read their keys from `[keybindings.overlay]`. Each value is a space-separated list of [Bubble Tea](https://github.com/charmbracelet/bubbletea) key names; pressing any listed key triggers the action. A partial table overrides only the keys it names.

```toml
[keybindings.overlay]
# Shared navigation.
up        = "k up"
down      = "j down"
page_up   = "pgup ctrl+u ctrl+b"
page_down = "pgdown space ctrl+d ctrl+f"
top       = "g home"
bottom    = "G end"
cancel    = "q esc ctrl+c"             # close the overlay / cancel
# Message viewer actions.
message_pin               = "enter"
message_expand_all        = "O"
message_collapse_all      = "C"
message_next_conversation = "l right tab"
message_prev_conversation = "h left shift+tab"
```

See [Keybindings]({{< relref "/docs/keybindings.md" >}}) for the complete keybinding reference.

## Overlay

```toml
[overlay]
shortcut_keys = "1234567890"  # keys that jump straight to the Nth session in the picker
```

In the session picker (`ctrl+b w`), each key jumps straight to its session — the 1st key selects session 1, and so on.

## Input

```toml
[input]
drag_arrow_keys      = false  # translate a left-click hold-and-drag into arrow-key presses
drag_arrow_threshold = 2      # cells of drag movement per emitted arrow-key press (values < 1 use the default)
```

`drag_arrow_keys` lets you press-and-hold the left mouse button and drag to emit discrete arrow-key presses to the focused pane — handy on touch/mobile terminals. It's off by default because it repurposes left-drag, which terminals use for text selection. Mouse-wheel scrolling always passes through unchanged. It only takes effect when the focused app has SGR mouse reporting enabled (e.g. a TUI tracking the mouse); graith translates those reports, it doesn't enable mouse tracking itself.

## Terminal & TUI presentation

```toml
[terminal]
refresh_interval = "2s"  # how often the picker/status bar/message viewer re-poll
summary_width    = 40    # max visible width of a `gr status` summary in the picker
```

The `[terminal]` block holds the interactive client's presentation preferences that were previously fixed.

**`refresh_interval`** is the cadence at which the session picker (`ctrl+b w`), an attached status bar, and the in-picker message viewer (`m`) re-poll the daemon for session state. A shorter interval feels more live but polls more; a non-positive value falls back to the default (zero would busy-loop).

**`summary_width`** is the widest a `gr status` summary renders in the picker before an ellipsis truncates it. It's a **display-cell** budget, not bytes or runes: wide characters (CJK, emoji) count as two cells, zero-width and combining marks as none, and ANSI styling is ignored. Truncation never splits a multi-byte character, so the summary is always valid UTF-8. (This differs from `[limits]` byte caps such as `inbox_preview_bytes`, measured in bytes.)

The fallback terminal geometry (used when graith can't read the real size, e.g. piped output) and the per-session scrollback cap are session-lifecycle settings — see [`[lifecycle]`]({{< relref "/docs/configuration/sessions.md" >}}) (`default_cols`, `default_rows`, `max_log_bytes`). The client's not-a-TTY fallback follows the same `[lifecycle]` defaults, for a single source of truth.

Only genuine preferences are configurable here. Layout invariants — the picker's column-width arithmetic, wrap widths, the minimum name column, and the GUI's 60 fps redraw rate — stay as fixed constants matching the render logic.
