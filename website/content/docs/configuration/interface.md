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
prefix              = "ctrl+b"  # prefix key
new_session         = "c"       # create a session (configurable)
fork_session        = "f"       # fork the current session (configurable)
next_session        = "n"       # next session (configurable)
prev_session        = "p"       # previous session (configurable)
last_session        = "l"       # last (most recently attached) session (configurable)
orchestrator_session = "o"      # switch to orchestrator session (configurable)
detach              = "d"       # detach (reserved, currently hardcoded)
session_list        = "w"       # open the session picker overlay (reserved, currently hardcoded)
shell               = "s"       # open a shell in the worktree (reserved, currently hardcoded)
delete_session      = "x"       # reserved, not currently wired
resume_session      = "R"       # reserved, not currently wired
rename_session      = ","       # reserved, not currently wired
search              = "/"       # reserved, not currently wired
scroll_mode         = "["       # reserved, not currently wired
```

Only `prefix`, `new_session`, `fork_session`, `next_session`, `prev_session`, `last_session`, and `orchestrator_session` are currently read from config. Other keys are present in the config struct but hardcoded in passthrough or not yet wired.

The prefix key accepts values like `ctrl+b`, `ctrl+x`, or a single character. graith handles both raw control bytes and Kitty keyboard protocol sequences, so it works in terminals like Ghostty that use the extended protocol.

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
