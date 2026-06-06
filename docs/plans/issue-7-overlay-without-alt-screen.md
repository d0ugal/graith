# Issue #7: Overlay Without Alt Screen

## Problem

The session overlay (ctrl+b w) uses `tea.WithAltScreen()`, causing a visible
flash on open and close. The alt screen clears the terminal before the overlay
renders, then clears again before restoring the main buffer. The scrollback
preview is reconstructed via vt10x but loses all color information.

## Decision: Keep bubbletea, remove alt screen, add daemon-side screen model

**Keep bubbletea.** The key parser (`readAnsiInputs`/`detectOneMsg`) is ~600
lines handling dozens of terminal escape sequence variants including Kitty
keyboard protocol. It's tightly coupled to bubbletea's `Msg` type and cannot be
extracted. The overlay's list model, textinput for filter, and delegate are all
bubbletea components that work well. Replacing bubbletea means reimplementing
the hardest part (input parsing) for minimal gain.

**Remove `tea.WithAltScreen()`.** The flash is caused by the alt screen
enter/exit transitions, not by bubbletea itself. Without alt screen, bubbletea's
standard renderer uses `CursorUp(linesRendered-1)` to reposition, which works
for full-screen rendering if we position the cursor at home before starting.

**Add daemon-side vt10x terminal per session.** This solves the restore problem
that the devil's advocate identified: without alt screen, we lose the terminal's
native save/restore of the main buffer. A daemon-side vt10x terminal maintains
color-accurate screen state that can be used for both the overlay background and
screen restore on dismiss.

### How it was stress-tested

**Devil's advocate** challenged 8 claims. Key findings incorporated:

- *Claim 3 (keyboard input)*: The passthrough loop is byte-level dispatch; the
  overlay needs semantic key parsing. Confirmed we must keep bubbletea, not
  reimplement.
- *Claim 5 (textinput complexity)*: The filter mode uses bubbletea's
  `textinput.Model` for cursor blinking, backspace, char accumulation. Not
  trivially replaceable.
- *Claim 7 (monochrome restore)*: vt10x `Cell().Char` strips all colors.
  Restoring from plain text would show a jarring monochrome flash. This is the
  primary motivation for the daemon-side screen model.
- *Claim 2 (tearing)*: Full-screen redraws without synchronized output can
  cause more tearing than alt screen. Must use `\x1b[?2026h`/`\x1b[?2026l`
  (BSU/ESU) where supported.

**Independent review (Codex, gpt-5.5)** arrived at the same core decision
(keep bubbletea, drop alt screen) and proposed the daemon-side vt10x cache
approach. Key additions:

- Daemon should generate ANSI repaint frames, not just plain text snapshots
- New `attach_screen` protocol message bundles screen snapshot with attach
  response, avoiding a separate round-trip
- Terminal mode tracking (cursor visibility, bracketed paste, app cursor mode)
  must be restored after the overlay
- Raw scrollback replay is rejected because it can contain partial escape
  sequences, bells, OSC commands, and mode changes that cause side effects

## Architecture

### Rendering without alt screen

```
Current flow:
  passthrough exits → terminal restored from raw mode
  bubbletea enters alt screen → BLANK FLASH (screen clears)
  bubbletea renders overlay → visible
  bubbletea exits alt screen → BLANK FLASH (screen clears)
  terminal restores main buffer → original content back

New flow:
  passthrough exits → terminal restored from raw mode
  write \x1b[H (cursor home) → no visible change
  bubbletea renders overlay from row 0 → overlay painted over content
  bubbletea exits → overlay content stays on screen
  client writes screen snapshot → original content restored
  passthrough resumes → live PTY data flowing
```

The overlay's `View()` function already renders a full-screen frame (dimmed
background + centered panel). The only change is that instead of the alt screen
saving/restoring the main buffer, we explicitly restore it using a daemon-side
screen snapshot.

### Daemon-side screen model

Each `pty.Session` maintains a `vt10x.Terminal` that mirrors the agent's visible
terminal state. All PTY output passes through this emulator before being fanned
out to attached clients.

```
Agent process → PTY → readLoop → vt10x.Write(data) → fanOut(data)
                                      ↓
                                 screen model
                                 (cells + colors + modes + cursor)
```

The screen model provides two snapshot types:

1. **ANSI repaint frame**: Full-screen string with SGR sequences preserving
   colors, bold, underline, etc. Used for screen restore after overlay dismiss.

2. **Dimmed text preview**: Plain text with dim foreground styling. Used for the
   overlay background (replaces current `FetchScrollbackPreview`).

### Protocol additions

New control message: `screen_snapshot`

```json
{"type": "screen_snapshot", "payload": {"session_id": "..."}}
```

Response:

```json
{
  "type": "screen_snapshot_response",
  "payload": {
    "session_id": "...",
    "frame": "<ANSI repaint frame>",
    "cursor_x": 42,
    "cursor_y": 10,
    "cursor_visible": true,
    "modes": {
      "bracketed_paste": true,
      "app_cursor": false,
      "mouse_mode": "none"
    }
  }
}
```

Alternative: bundle the snapshot into the `attach` response when reattaching
after overlay dismiss. This avoids an extra round-trip.

### Keyboard input

bubbletea continues to own input during the overlay. The attach loop already
works this way:

1. `RunPassthrough()` exits, restoring terminal from raw mode
2. `RunOverlay()` starts bubbletea, which enters raw mode
3. User interacts with overlay
4. bubbletea exits, restoring terminal

The raw mode transition between passthrough and bubbletea is not visible to the
user (it's a single syscall, no screen output). The flash was caused by the alt
screen clear, not the mode transition.

### Screen restore on dismiss

After the overlay exits:

1. Client requests screen snapshot for the target session from daemon
2. Client writes the ANSI repaint frame to stdout:
   - `\x1b[?2026h` (begin synchronized update)
   - `\x1b[?25l` (hide cursor)
   - `\x1b[H` (cursor home)
   - Frame content (full screen, cell by cell with SGR)
   - `\x1b[0m` (reset SGR)
   - Position cursor at session cursor: `\x1b[{row};{col}H`
   - Restore cursor visibility
   - `\x1b[?2026l` (end synchronized update)
3. Client enters `RunPassthrough()` — live PTY data resumes

The synchronized update (`\x1b[?2026h`/`\x1b[?2026l`) prevents tearing during
the full-screen write. Terminals that don't support it degrade gracefully
(slight flicker but no corruption).

### Filter, list, and delete flows

These are unchanged. The bubbletea model (`overlayModel`) keeps its existing
Update/View logic:

- **List navigation**: j/k/up/down moves cursor, skipping group headers
- **Filter mode**: `/` enters filter, textinput handles keystrokes, esc/enter
  exits
- **Delete confirmation**: `x` shows confirm prompt, `y` confirms, any other
  key cancels
- **Selection**: enter selects session, returns OverlayResult with action

The only View() change is ensuring exactly `height` rows are output, each padded
to terminal width, with no trailing newline after the final row. This is needed
because without alt screen, bubbletea's renderer tracks lines rendered and uses
CursorUp to reposition.

## Risk Assessment

### High risk

**Daemon-side vt10x performance**: Every byte of PTY output goes through the
vt10x emulator. For high-throughput agents (e.g., streaming large code blocks),
this could add latency. Mitigation: benchmark with realistic workloads; the
vt10x emulator is simple C-style state machine logic that should be fast.

**vt10x accuracy**: The `hinshun/vt10x` library may not handle all modern
terminal sequences correctly (e.g., 24-bit color via `\x1b[38;2;r;g;bm`).
vt10x does track FG/BG/Mode per cell but may miss edge cases. Mitigation:
test with real agent output; consider upgrading to a more actively maintained
vt10x fork if needed.

### Medium risk

**bubbletea without alt screen edge cases**: bubbletea's non-alt-screen renderer
hasn't been widely tested for full-screen overlays. The `CursorUp(n)` repositioning
may have edge cases with terminals that handle cursor movement differently.
Mitigation: test on iTerm2, Terminal.app, Ghostty, Kitty, WezTerm.

**Terminal mode restoration**: After the overlay, terminal modes (bracketed
paste, cursor visibility, mouse mode) must match the session's state, not
bubbletea's cleanup state. bubbletea may disable bracketed paste on exit.
Mitigation: restore modes from the screen snapshot data after bubbletea exits.

### Low risk

**Synchronized output support**: Not all terminals support `\x1b[?2026h`.
Unsupported terminals ignore the sequence, so there's no breakage — just
potential tearing on the restore frame. Most modern terminals (Ghostty, Kitty,
WezTerm, iTerm2 3.5+) support it.

**Memory overhead of daemon-side vt10x**: One `vt10x.Terminal` per session.
At 200 cols x 60 rows = 12,000 cells, each a few bytes. Negligible even with
100 sessions.

## Task Breakdown

### Phase 1: Daemon-side screen model (pty/session.go)

1. Add `vt10x.Terminal` field to `pty.Session`
2. Initialize with session's terminal dimensions on session create
3. In `readLoop`, call `vt.Write(data)` before fanning out to attached writers
4. Handle resize: call `vt.Resize(cols, rows)` when PTY is resized
5. Add `ScreenSnapshot()` method that renders current screen state as ANSI frame
6. Add `ScreenPreview()` method that renders dimmed text preview (replaces
   current client-side vt10x rendering)

### Phase 2: ANSI repaint frame renderer

1. Write a function that iterates `vt10x.Cell(x, y)` for all cells
2. For each cell: emit SGR sequence for FG, BG, bold, underline, italic, reverse
3. Handle default colors (don't emit SGR if cell has default FG/BG)
4. Optimize: skip SGR changes when adjacent cells have same attributes
5. Pad each row to terminal width, separate rows with `\r\n`
6. Unit test with known terminal output → expected ANSI frame

### Phase 3: Protocol changes

1. Add `ScreenSnapshotMsg` and `ScreenSnapshotResponseMsg` to protocol/messages.go
2. Handle `screen_snapshot` in daemon/handler.go — call session's
   `ScreenSnapshot()`, send response
3. Add client method `FetchScreenSnapshot()` that requests and receives snapshot
4. Update `FetchScrollbackPreview` to use daemon-side `ScreenPreview()` instead
   of client-side vt10x

### Phase 4: Overlay rendering changes (client/overlay.go)

1. Remove `tea.WithAltScreen()` from `RunOverlay`
2. Before `tea.NewProgram().Run()`, write `\x1b[H` to stdout
3. Ensure `View()` renders exactly `height` rows, each padded to `width`
4. No trailing newline after final row
5. Hide cursor before starting bubbletea: `\x1b[?25l`
6. Optionally seed initial preview synchronously to avoid blank first frame

### Phase 5: Restore flow (cli/attach.go, client/overlay.go)

1. After `RunOverlay()` returns, fetch screen snapshot for target session
2. Write ANSI repaint frame to stdout with synchronized update wrapper
3. Restore terminal modes from snapshot data
4. Enter `RunPassthrough()` on the new connection
5. Consider adding `attach_screen` variant to combine attach + snapshot in one
   round-trip

### Phase 6: Cleanup and edge cases

1. Remove client-side vt10x rendering from `FetchScrollbackPreview` (move to
   daemon-side)
2. Handle overlay when no sessions exist (currently shows "No sessions" message)
3. Handle window resize during overlay (bubbletea sends WindowSizeMsg)
4. Test rapid overlay open/close cycles
5. Test with different terminal emulators (Ghostty, iTerm2, Terminal.app, Kitty)
6. Test with high-throughput agent output during overlay
7. Consider suppressing automatic scrollback replay on overlay reattach

## Migration Strategy

The change is backward-compatible at the protocol level:

1. Phase 1-2 can be deployed first (daemon-side screen model) without changing
   the client overlay. Old clients continue to work.
2. Phase 3 adds new protocol messages but doesn't remove existing ones.
3. Phase 4-5 change only the client-side rendering. The `RunOverlay()` function
   signature stays the same.
4. Phase 6 can remove the deprecated client-side vt10x path.

Each phase can be tested and committed independently.
