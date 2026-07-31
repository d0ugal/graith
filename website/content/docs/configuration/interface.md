---
weight: 350
title: "TUI & input"
description: "Keybindings, the Session Navigator, and input handling."
icon: "keyboard"
toc: true
draft: false
---

## Keybindings

When updating from older builds that used the Session Navigator's pre-rename
config keys, migrate them with this release: `keybindings.session_list` is now
`keybindings.session_navigator`, `[overlay]` is now `[session_navigator]`, and
`[keybindings.overlay]` is now `[keybindings.tui]`. The old names are ignored by
the renamed config schema; `gr doctor` reports them with the new names.

```toml
[keybindings]
prefix               = "ctrl+b"  # prefix key
new_session          = "c"       # create a session
fork_session         = "f"       # fork the current session
delete_session       = "x"       # delete a session in the Session Navigator
detach               = "d"       # detach without stopping the agent
session_navigator    = "w"       # open the Session Navigator
next_session         = "n"       # next session
prev_session         = "p"       # previous session
last_session         = "l"       # last (most recently attached) session
resume_session       = "R"       # open the Session Navigator restart menu
rename_session       = ","       # rename the current session
search               = "/"       # filter sessions in the Session Navigator
scroll_mode          = "["       # open a scrollable pager over the session history
shell                = "s"       # open a shell in the worktree
orchestrator_session = "o"       # switch to the orchestrator session
messages             = "m"       # open the message viewer for the current session
restart_session      = "r"       # restart (resume) the current session
```

The prefix key accepts `ctrl+a` through `ctrl+z`, or exactly one printable ASCII
byte. Literal bytes are preserved: `A` is different from `a`, and a single space
is a valid literal prefix. Prefix-command actions are exactly one printable
ASCII byte pressed after the prefix; empty strings, multi-character values,
multi-byte text, and NUL are rejected at config load. Navigator action fields
(`delete_session`, `resume_session`, and `search`) accept one supported Bubble
Tea key name such as `x`, `space`, `ctrl+d`, or `f5`. graith handles both raw
control bytes and Kitty keyboard protocol sequences, so it works in
extended-protocol terminals like Ghostty.

If two prefix commands share a key, or a prefix command uses the same byte as the
prefix itself, graith warns at load time and starts anyway. The warning names the
action that wins in passthrough runtime order.

### TUI keys

The message viewer and scroll pager read navigation and cancel aliases from
`[keybindings.tui]`. The Session Navigator reads only `cancel` from this table;
its navigation keys are fixed, and its action keys are the top-level
`delete_session`, `resume_session`, and `search` bindings. Each TUI-table
value is a space-separated list of
[Bubble Tea](https://github.com/charmbracelet/bubbletea) key names; pressing any
listed key triggers the action. A partial table overrides only the actions it
names. A named action's aliases replace the default aliases for that action.

```toml
[keybindings.tui]
# Shared navigation.
up        = "k up"
down      = "j down"
page_up   = "pgup ctrl+u ctrl+b"
page_down = "pgdown space ctrl+d ctrl+f"
top       = "g home"
bottom    = "G end"
cancel    = "q esc ctrl+c"             # close the TUI / cancel
# Message viewer actions.
message_pin               = "enter"
message_expand_all        = "O"
message_collapse_all      = "C"
message_next_conversation = "l right tab"
message_prev_conversation = "h left shift+tab"
message_topics            = "t"
message_direct            = "d"
```

See [Keybindings]({{< relref "/docs/keybindings.md" >}}) for the complete keybinding reference.

Terminal control sequences such as Kitty keyboard protocol, paste markers, mouse
reports, and viewport-owned pager navigation are not raw remappable bindings.
Mouse-wheel reports expose only the typed gestures documented under `[input]`
below. macOS menu shortcuts use native Command-key equivalents in the app;
daemon config is not pushed into GUI shortcut definitions.

## Session Navigator

```toml
[session_navigator]
shortcut_keys = "1234567890"  # keys that jump straight to the Nth session in the Navigator

[session_navigator.help]
compact_actions     = ["attach", "new", "view", "group", "filter", "help", "quit"]
expanded_actions    = ["move", "top_bottom", "view", "group", "jump", "attach", "new", "star", "fold", "fold_all", "delete", "stop", "restart", "restart_menu", "filter", "help", "quit"]
toggle_keys         = "? f1"
expanded_by_default = false

[session_navigator.selected_detail]
enabled = true
layout = "side_panel"
min_terminal_width = 150
min_terminal_height = 24
max_width = 54
fields = ["summary", "agent", "model", "branch", "mode", "base", "git", "worktree", "pr", "review", "labels", "created", "attached", "changed", "deleted", "purges", "config", "id"]
```

In the Session Navigator (`ctrl+b w`), each key jumps straight to its session — the 1st key selects session 1, and so on.

`[session_navigator.help]` controls the Navigator footer and expanded help
state. The action lists are ordered semantic IDs; labels are rendered from the
active keybindings, so remapping `search` or `delete_session` also updates the
help text. `compact_actions` is the first-line footer. `expanded_actions` is
shown when expanded help is open. `toggle_keys` is a space-separated list of
Bubble Tea key names that toggle expanded help; existing Navigator actions win
on conflict. `expanded_by_default` starts the Navigator with expanded help
open. Set either action list to `[]` to hide all help entries for that state.

Supported action IDs are `attach`, `delete`, `filter`, `fold`, `fold_all`,
`group`, `help`, `jump`, `move`, `new`, `quit`, `restart`, `restart_menu`,
`star`, `stop`, `top_bottom`, and `view`. Unknown action IDs, duplicate action
IDs within one list, and invalid `toggle_keys` names fail config validation.

`[session_navigator.selected_detail]` controls the optional wide-terminal
detail panel for the highlighted session. It never replaces the session tree,
columns, or live terminal preview. When it is shown, default footer metadata
moves into the side panel; rows omitted from `fields` are hidden. The `summary`
field is rendered as a wrapped full-status block at the bottom of the Navigator
when the wide detail panel is active, so long `gr status` text remains visible
without widening the side panel. Set `enabled = false` to hide the wide detail
surface, raise `min_terminal_width`/`min_terminal_height` if you want it only on
larger terminals, adjust `max_width` to cap the side panel at 38 columns or
wider, or trim/reorder `fields` to choose which metadata rows are shown. Set
`fields = []` to keep only the selected-session heading and context line.
Supported field IDs are `summary`, `agent`, `model`, `branch`,
`mode`, `base`, `git`, `worktree`, `cwd`, `pr`, `review`, `labels`, `created`,
`attached`, `changed`, `deleted`, `purges`, `config`, and `id`. The only
supported layout today is `side_panel`; the key exists so future Navigator
customization can grow inside the same Session Navigator namespace.

## Input

```toml
[input]
mouse_wheel_policy   = "off"  # off | respect_terminal_modes | always
drag_arrow_keys      = false  # translate a left-click hold-and-drag into arrow-key presses
drag_arrow_threshold = 2      # cells of drag movement per emitted arrow-key press (values < 1 use the default)

[input.bindings]
mouse_wheel_up = "scroll_mode"
# mouse_wheel_down = "none"
# shift_mouse_wheel_up = "scroll_mode"
# shift_mouse_wheel_down = "none"

[agents.codex.input]
mouse_wheel_policy = "respect_terminal_modes"
```

`mouse_wheel_policy` controls when configured wheel gestures trigger Graith
actions. The global default is `off`, so unknown or custom agents keep current
wheel behavior. Bundled coding agents set
`mouse_wheel_policy = "respect_terminal_modes"` under `[agents.<name>.input]`:
wheel-up opens `scroll_mode` only when Graith's terminal mode snapshot says the
child has not enabled mouse tracking and is not using alternate-screen
alternate-scroll. `always` makes the configured Graith action win even when a
child app might otherwise receive wheel events, so use it only when you accept
that trade-off. Enabling a wheel gesture makes Graith enable mouse reporting for
the attach so it can observe wheel events; depending on the terminal, plain
click-and-drag text selection may require Shift, Option, or another terminal
modifier while attached.

`[input.bindings]` maps semantic gestures to Graith actions. Supported gestures
are `mouse_wheel_up`, `mouse_wheel_down`, `shift_mouse_wheel_up`, and
`shift_mouse_wheel_down`. Supported actions are `scroll_mode` and `none`; use
`none` in `[agents.<name>.input.bindings]` to disable a global binding for a
specific agent. Shift-wheel depends on the terminal reporting the standard SGR
Shift modifier.

Remote attaches do not currently run configured wheel gestures; use the prefix
keybinding for local attach scroll mode until the remote scrollback path is
implemented.

`drag_arrow_keys` lets you press-and-hold the left mouse button and drag to emit
discrete arrow-key presses to the focused pane — handy on touch/mobile
terminals. It's off by default because it repurposes left-drag, which terminals
use for text selection. It only takes effect when the focused app has SGR mouse
reporting enabled (e.g. a TUI tracking the mouse); graith translates those
reports, it doesn't enable mouse tracking itself.

## Terminal & TUI presentation

```toml
[terminal]
refresh_interval = "2s"  # how often the Navigator/status bar/message viewer re-poll
summary_width    = 40    # max visible width of a `gr status` summary in the Navigator
```

The `[terminal]` block holds the interactive client's presentation preferences that were previously fixed.

**`refresh_interval`** is the cadence at which the Session Navigator (`ctrl+b w`), an attached status bar, and the message viewer (`m`) re-poll the daemon for session state. A shorter interval feels more live but polls more; a non-positive value falls back to the default (zero would busy-loop).

**`summary_width`** is the widest a `gr status` summary renders in the Navigator
and human-readable `gr list` output before an ellipsis truncates it. It's a
**display-cell** budget, not bytes or runes: wide characters (CJK, emoji) count
as two cells, zero-width and combining marks as none, and ANSI styling is
ignored. Truncation never splits a multi-byte character, so the summary is
always valid UTF-8. (This differs from `[limits]` byte caps such as
`inbox_preview_bytes`, measured in bytes.)

The fallback terminal geometry (used when graith can't read the real size, e.g. piped output) and the per-session scrollback cap are session-lifecycle settings — see [`[lifecycle]`]({{< relref "/docs/configuration/sessions.md" >}}) (`default_cols`, `default_rows`, `max_log_bytes`). The client's not-a-TTY fallback follows the same `[lifecycle]` defaults, for a single source of truth.

Only genuine preferences are configurable here. Layout invariants — the Navigator's column-width arithmetic, wrap widths, the minimum name column, and the GUI's 60 fps redraw rate — stay as fixed constants matching the render logic.

## iOS terminal gesture physics

The iOS terminal reads its touch-scroll feel from namespaced `UserDefaults`
keys. For the settle-critical scroll keys below, missing or non-finite values
use the shipped default, and finite values outside the accepted range are
clamped.

| `UserDefaults` key | Default | Accepted range |
|--------------------|---------|----------------|
| `graith.gesture.scrollFriction` | `4.5` | `1...60` |
| `graith.gesture.scrollMomentumCutoff` | `24` | `1...10000` points/s |
| `graith.gesture.scrollSpringStiffness` | `220` | `30...400` |
| `graith.gesture.scrollSpringDamping` | `26` | `4...29` |

These ranges keep momentum decaying and keep the overscroll spring stable at
the controller's maximum 50 ms integration step. Settling also has a ten-second
fail-safe, after which the terminal snaps to idle instead of running display-link
physics indefinitely.
