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
approvals            = "a"       # open the pending-approvals prompt
restart_session      = "r"       # restart (resume) the current session
```

Every key above is read from config. The prefix key accepts values like
`ctrl+b`, `ctrl+x`, or a single character; the rest are single characters pressed
after the prefix (or, for `delete_session`/`resume_session`/`search`, inside the
session picker). graith handles both raw control bytes and Kitty keyboard
protocol sequences, so it works in terminals like Ghostty that use the extended
protocol.

If two prefix commands are bound to the same key, graith prints a warning at
load time and starts anyway (only the first command in the passthrough order
would fire).

### Overlay keys

The full-screen terminal overlays (dashboard, approval prompt, message viewer,
scroll pager) read their keys from `[keybindings.overlay]`. Each value is a
space-separated list of [Bubble Tea](https://github.com/charmbracelet/bubbletea)
key names; pressing any listed key triggers the action. A partial table
overrides only the keys it names, and a value you set **replaces** (it does not
extend) the built-in aliases for that action — so list every key you want,
including any you still rely on. For example, the shipped `cancel` default keeps
`ctrl+c`; overriding it with `cancel = "q esc"` would drop Ctrl-C from these
overlays.

```toml
[keybindings.overlay]
# Shared navigation.
up        = "k up"
down      = "j down"
page_up   = "pgup ctrl+u ctrl+b"
page_down = "pgdown space ctrl+d ctrl+f"
top       = "g home"
bottom    = "G end"
confirm   = "enter y"                  # confirm a prompt (e.g. delete/stop)
cancel    = "q esc ctrl+c"             # close the overlay / cancel
# Dashboard actions.
dashboard_attach = "enter a"
dashboard_stop   = "s"
dashboard_delete = "x d"
dashboard_resume = "r"
# Approval prompt actions.
approval_allow     = "y enter"
approval_deny      = "n x"
approval_allow_all = "a"
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

In the session picker (`ctrl+b w`), each of these keys jumps directly to the corresponding session — the 1st key selects session 1, the 2nd key session 2, and so on.

## Input

```toml
[input]
drag_arrow_keys      = false  # translate a left-click hold-and-drag into arrow-key presses
drag_arrow_threshold = 2      # cells of drag movement per emitted arrow-key press (values < 1 use the default)
```

`drag_arrow_keys` lets you press-and-hold the left mouse button and drag up/down/left/right to emit discrete arrow-key presses to the focused pane — handy on touch/mobile terminals. It is off by default because it repurposes left-drag, which terminals otherwise use for text selection. Mouse-wheel scrolling always passes through unchanged. It only takes effect when the focused app has SGR mouse reporting enabled (e.g. a TUI tracking the mouse); graith translates those reports, it does not enable mouse tracking itself.

## Terminal & TUI presentation

```toml
[terminal]
refresh_interval = "2s"  # how often the picker/dashboard/status bar re-poll for state
summary_width    = 40    # max visible width of a `gr status` summary in the picker
```

The `[terminal]` block collects the interactive client's presentation preferences that were previously fixed.

**`refresh_interval`** is the cadence at which the session picker (`ctrl+b w`), the `gr dashboard`, and an attached status bar re-poll the daemon for fresh session state. A shorter interval feels more live at the cost of more polling; a non-positive value falls back to the default (a zero cadence would busy-loop).

**`summary_width`** is the widest a `gr status` summary may render in the picker before it is truncated with an ellipsis. It is measured in terminal display cells, not bytes or runes, so wide CJK/emoji, combining characters, and ANSI styling are truncated without splitting a character or escape sequence.

The fallback terminal geometry (used when graith can't read the real terminal size, e.g. piped output) and the per-session scrollback cap are session-lifecycle settings — see [`[lifecycle]`]({{< relref "/docs/configuration/sessions.md" >}}) (`default_cols`, `default_rows`, `max_log_bytes`). The client's not-a-TTY fallback geometry follows the same `[lifecycle]` defaults, so there is a single source of truth.

Only genuine preferences are configurable here. Layout invariants — the picker's column-width arithmetic, wrap widths, the minimum name column, and the GUI's 60 fps redraw rate — must match the render logic and stay as fixed constants in the code.
