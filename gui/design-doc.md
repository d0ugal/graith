---
title: "graith macOS GUI Application"
authors: ["Dougal Matthews"]
created: "2026-06-17"
status: "Draft"
reviewers: []
informed: []
---

# graith macOS GUI Application

A native macOS GUI for graith that provides a graphical terminal multiplexer
experience, using libghostty-vt for terminal emulation and Metal/CoreText for
rendering.

# Background

graith is a terminal multiplexer for AI coding agent sessions. It manages
multiple agents running in isolated git worktrees, each in its own PTY session.
Today, all interaction happens through the CLI — users attach to sessions via
`gr attach` in their existing terminal emulator.

This works well for experienced terminal users but has limitations:

- **No native window management** — users rely on their terminal emulator's
  tab/split features, which don't integrate with graith's session model.
- **Session discovery** — the overlay picker (ctrl+b w) provides session
  switching, but a persistent sidebar with live status would be more
  discoverable.
- **Split panes** — viewing two agent sessions side-by-side requires manual
  terminal splits that don't preserve graith context.
- **macOS integration** — keyboard shortcuts, dock badges, and native UI
  elements aren't available through a terminal.

The ghostty project provides `libghostty-vt`, a C library that implements
terminal state management (VT parsing, grid state, key encoding, mouse
encoding, selection) without rendering opinions. This makes it possible to
build a native terminal view that leverages ghostty's battle-tested terminal
emulation while providing custom rendering.

# Problem

Users who manage multiple AI agent sessions need a more integrated experience
than what a CLI terminal multiplexer can provide. The specific gaps are:

1. **Context switching cost** — moving between sessions requires remembering
   names and typing commands or using the overlay picker.
2. **Visibility** — no persistent view of all sessions, their status, and
   which need attention.
3. **Comparison** — viewing two sessions side-by-side requires manual setup
   that doesn't survive terminal restarts.

# Goals

- Provide a native macOS application with a session sidebar and terminal panes.
- Support side-by-side split views for comparing two sessions simultaneously.
- Integrate with the graith daemon for session management via `gr` CLI
  (list/create/stop/delete via JSON, terminal I/O via forked `gr attach` PTY
  per visible pane).
- Support both CoreText (software) and Metal (GPU) rendering backends.
- Handle keyboard input correctly through macOS NSTextInputClient (IME-aware).
- Support terminal features: scrollback (local libghostty-vt buffer, max
  10,000 lines), text selection, copy/paste, URL detection, mouse tracking,
  search.

## Non-Goals

- **Cross-platform GUI** — this is macOS-only, using AppKit and Metal. Linux
  and Windows users continue using the CLI.
- **Replacing the CLI** — the GUI is an alternative interface, not a
  replacement. The CLI remains the primary interface.
- **Full terminal emulator** — the GUI attaches to graith sessions via
  `gr attach`. It does not implement a general-purpose terminal emulator.
- **Custom theming** — the initial version uses a fixed Catppuccin Mocha
  (dark) theme. The app does not follow macOS system light/dark mode — it is
  always dark. Configurable themes are deferred.
- **Tabs** — the sidebar provides session navigation. A tab bar is deferred.
- **App Store distribution** — v1 targets developer machines only. App
  Sandbox is incompatible with the fork/exec attach model (see Deployment).

## V1 Acceptance Criteria

- [ ] Sidebar shows hierarchical session tree with parent/child relationships
- [ ] Session creation via New Session sheet (`gr new`)
- [ ] Attach to session by clicking in sidebar
- [ ] Side-by-side split with two different sessions (same-session split guard — see Known Issues)
- [ ] IME input works correctly (tested: US English, Japanese)
- [ ] Both CoreText and Metal renderers produce correct output
- [ ] Metal fallback to CoreText when GPU unavailable (currently crashes — see Error Handling)
- [ ] Detached session shows overlay with reattach button
- [ ] Daemon error shows in sidebar (daemon auto-starts via `gr` — see Daemon Lifecycle)
- [ ] Copy/paste, text selection, URL click-to-open work
- [ ] In-terminal search (Cmd+F) works (viewport text only — see Scrollback)
- [ ] Keyboard shortcuts: Cmd+N (new), Cmd+] (next), Cmd+[ (prev), Cmd+D (split), Cmd+Shift+D (close split), Cmd+1-9 (jump)
- [ ] Bell triggers dock bounce when app is not focused
- [ ] No crashes on session stop/delete while attached

# Proposals

## Proposal 0: Do Nothing

Continue using the CLI exclusively. Users manage sessions through `gr`
commands and their terminal emulator's native features.

**Consequences:**
- Users continue context-switching between terminal tabs/windows manually.
- No persistent session overview — must run `gr list` or use the overlay.
- Split-pane viewing requires terminal emulator support and manual setup.
- No native macOS integration (dock badges, keyboard shortcuts).

## Proposal 1: Native macOS App with libghostty-vt

Build a SwiftUI application that wraps libghostty-vt for terminal emulation.

### Architecture

**Session management and CLI subprocess layer:**

```
SessionStore ──poll (2s)──> gr list --json ──> daemon (graithd)
     │                                              │
     │ actions                                      │
     ├──> gr new/stop/delete ──────────────────────>│
     │                                              │
ContentView / SessionSidebar (hierarchical tree)    │
     │                                              │
TerminalContainer (x N panes)                       │
     └── forkpty() → gr attach <session> ──────────>│
              ↕ raw PTY I/O                         │
         BaseTerminalNSView                         │
```

**Rendering stack:**

```
+---------------------------+
|       GraithApp           |
|  (SwiftUI App lifecycle)  |
+---------------------------+
           |
+---------------------------+
|      ContentView          |
| +-------+  +------------+|
| |Session |  |Terminal    ||
| |Sidebar |  |Area       ||
| |(tree)  |  |(split-able)||
| +-------+  +------------+|
+---------------------------+
           |
+---------------------------+
|    TerminalContainer      |
| (SwiftUI: renderer switch,|
|  search bar, detach       |
|  overlay, reattach)       |
+---------------------------+
           |
+---------------------------+
|   BaseTerminalNSView      |
|  (NSView + NSTextInput)   |
|  PTY, keyboard, mouse,    |
|  selection, scrollback    |
|  Contains:                |
|   GhosttyTerminalState    |
+---------------------------+
     |                |
+----------+  +-------------+
|CoreText  |  |Metal        |
|NSView    |  |NSView       |
|(draw)    |  |(MTKView)    |
+----------+  +-------------+
```

`GhosttyTerminalState` lives inside `BaseTerminalNSView` and is used by
both renderer subclasses — it is not a separate layer below them.

**Component descriptions:**

- **GraithApp** — SwiftUI app entry point, menu bar setup, keyboard shortcut
  routing via NotificationCenter.
- **ContentView** — Main layout: resizable sidebar + terminal area with
  optional side-by-side split panes (vertical divider).
- **SessionStore** — Observable object managing session list, selection state,
  renderer choice, and font size. Polls `gr list --json` every 2 seconds for
  session updates. Future: migrate to FSEvents watch on daemon state file or
  a dedicated daemon notification channel.
- **SessionSidebar** — Hierarchical tree of sessions grouped by repo, showing
  parent/child relationships (via `parent_id`). Status indicators, agent type
  badges, "needs attention" fleet summary bar, and context menu actions
  (stop, resume, restart, delete, copy name).
- **TerminalContainer** — SwiftUI view that wraps the NSView terminal. Handles
  renderer switching (CoreText/Metal), search bar toggle, detached overlay with
  reattach button, and `attachID` invalidation on session changes.
- **BaseTerminalNSView** — NSView subclass conforming to NSTextInputClient.
  Contains all shared terminal logic: PTY lifecycle (fork/exec `gr attach`),
  keyboard input (IME-aware via NSTextInputClient), mouse event handling
  (selection, tracking, URL detection), scrollback, copy/paste. Owns a
  `GhosttyTerminalState` instance. Subclasses override four hooks for rendering.
- **GhosttyTerminalNSView** — CoreText rendering subclass. Uses `draw(_:)` to
  render cells directly via CTLineDraw. Software rendering, no GPU dependency.
- **MetalTerminalNSView** — Metal rendering subclass. Builds vertex buffers
  from cell data and renders via MTKView. GPU-accelerated.
- **GhosttyTerminalState** — Swift wrapper around the libghostty-vt C API.
  Manages terminal, render state, key encoder, mouse encoder, and selection
  gesture objects. Handles memory lifecycle of all ghostty objects.
- **TerminalSearchBar** — In-terminal search UI (Cmd+F), backed by
  `TerminalSearchState`.
- **FleetSummaryBar** — Compact bar showing session counts by status.
- **NewSessionSheet** — Session creation dialog (name, repo, agent, prompt).

**Key input flow:**

```
NSEvent (keyDown)
    |
    v
inputContext.handleEvent(event)  -- NSTextInputClient
    |                    |
    v                    v
insertText()        doCommand(by:)
(printable text)    (control keys: enter, arrows, etc.)
    |                    |
    v                    v
encodeKey(key, mods, text: "a")   encodeKey(key, mods, text: nil)
    |                              |
    v                              v
ghostty_key_encoder_encode  -->  escape sequence bytes
    |
    v
writeToPTY(data)  -->  ptyFD  -->  gr attach  -->  daemon
```

The NSTextInputClient protocol routes printable characters through
`insertText` (with the actual text) and non-printable keys through
`doCommand` (with nil text, letting the key encoder derive the sequence from
the logical key code). The ghostty key encoder handles legacy mode, kitty
keyboard protocol, and modifyOtherKeys automatically based on terminal state.

**PTY output (read) flow:**

```
daemon  -->  gr attach  -->  PTY master fd
    |
    v
DispatchSource.makeReadSource(fileDescriptor: ptyFD)  [background queue]
    |
    v
readFromPTY()  -->  read() raw bytes into Data
    |
    v
DispatchQueue.main.async {                             [main thread]
    GhosttyTerminalState.write(data)  -->  VT parse, update grid
    needsTerminalRedraw = true
}
    |
    v  (separate, 60Hz Timer on main thread)
startDisplayTimer: if needsTerminalRedraw {
    updateRenderState()  -->  handleDirtyFrame()  -->  renderer redraws
}
```

**Resize flow:**

```
NSView frame change (layout)
    |
    v
onFrameResized()  -->  calculate new gridCols/gridRows from frame + cell size
    |
    v
GhosttyTerminalState.resize(cols, rows)
    |
    v
var size = winsize(ws_row: rows, ws_col: cols, ...)
ioctl(ptyFD, TIOCSWINSZ, &size)  -->  gr attach  -->  daemon
```

**Detach/reattach flow:**

```
gr attach child process exits (user detaches or session stops)
    |
    v
onProcessExit callback  -->  TerminalContainer
    |
    v
Show detached overlay with "Reattach" button
    |
    v
User clicks Reattach  -->  increment attachID  -->  new TerminalContainer
    |
    v
New forkpty() + execve("gr", "attach", sessionName)
```

**Rendering backends:**

| Backend | Rendering | GPU | Use case |
|---------|-----------|-----|----------|
| CoreText | `draw(_:)` + CTLineDraw | No | Fallback, simplicity |
| Metal | MTKView + vertex buffers | Yes | Performance, large grids |

Both backends share identical input, selection, scrollback, and PTY logic
via `BaseTerminalNSView`. The only differences are:
- How cell metrics are computed (font measurement vs renderer metrics)
- How dirty frames trigger redraws (setNeedsDisplay vs MTKView.needsDisplay)
- The actual pixel rendering

**Pros:**
- Native macOS experience with standard keyboard shortcuts and dock integration.
- Two rendering backends allow performance vs. simplicity tradeoff.
- libghostty-vt provides battle-tested VT emulation without reimplementing it.
- BaseTerminalNSView eliminates code duplication between backends (~700 lines
  shared, each backend adds ~100-170 lines).
- PTY-based attachment (`gr attach`) reuses the existing daemon protocol —
  the GUI doesn't need its own daemon communication layer.

**Cons:**
- macOS only — no cross-platform reach.
- Static linking of libghostty-vt ties us to a specific ghostty version.
  Updates require rebuilding the library.
- PTY-based attachment adds a process per session (the `gr attach` child
  process), vs. a direct socket connection to the daemon.
- Two rendering backends to maintain, even with shared base class.

### Daemon Constraints

**Single-attach semantics:** graith allows only one attached client per session.
A new attach kicks the previous client. This means:

- The GUI and a CLI terminal cannot attach to the same session simultaneously.
- Side-by-side split must use two different sessions — same-session split
  would create two `gr attach` processes fighting for one session. The GUI
  should prevent same-session split or warn the user.
- When the GUI attaches to a session, any CLI user attached to that session
  is detached, and vice versa.

**Daemon lifecycle:** The `gr` CLI auto-starts the daemon via `EnsureDaemon`
when the socket is unreachable. This means the GUI's first `gr list --json`
poll (and every `gr new`, `gr attach` subprocess) will start the daemon if
it isn't running. The genuine failure modes are: `gr` binary not found,
daemon failed to start within timeout, handshake/version mismatch, socket
permission errors, or config corruption. `SessionStore.error` captures these
and the sidebar displays the error text.

### Concurrency Model

All `GhosttyTerminalState` mutation happens on the main thread:

- **Main thread** — SwiftUI views, SessionStore polling, user interaction,
  all `GhosttyTerminalState` reads and writes (VT parsing, key encoding,
  resize, selection, viewport scroll).
- **PTY read source** — `DispatchSource` on the PTY file descriptor fires on
  a background queue (`DispatchQueue.global(qos: .userInteractive)`). Reads
  raw bytes into a `Data` buffer, then dispatches to `DispatchQueue.main.async`
  where `GhosttyTerminalState.write()` parses VT sequences and updates grid
  state. The background queue only does the `read()` syscall.
- **Display timer** — A 60Hz `Timer` on the main thread polls
  `GhosttyTerminalState.updateRenderState()`. If dirty, it calls the
  renderer's `handleDirtyFrame()`. This coalesces multiple VT writes between
  timer ticks into one redraw.
- **PTY writes** — `writeToPTY()` writes synchronously from the main thread
  (key events originate on main thread via NSTextInputClient). If the PTY
  buffer is full, it busy-waits with `usleep(1000)` — a large paste while the
  agent isn't reading could briefly stall the UI.

There is no explicit locking. Thread safety relies on:
1. GhosttyTerminalState is only mutated from the main thread — PTY read
   callbacks dispatch to `DispatchQueue.main.async` before writing to it
2. The 60Hz display timer coalesces redraws — multiple VT writes between
   timer ticks produce one redraw
3. The background read queue only performs the `read()` syscall and copies
   bytes into a `Data` value; it never touches terminal state directly

### Scrollback

Scrollback is local to the libghostty-vt terminal state (configured at
`max_scrollback: 10000` lines in `GhosttyTerminalState`). This is separate
from the daemon's append-only scrollback file. On reattach, the local
scrollback resets, but `gr attach` replays the daemon's last 300 scrollback
lines as initial data, so the terminal starts with recent context. Full
daemon scrollback beyond that tail is not surfaced in the GUI.

In-terminal search (Cmd+F) searches only the currently visible viewport
text via `getVisibleText()`, not the full scrollback buffer. Scrollback
search is deferred.

### Security

The GUI runs without App Sandbox (`--disable-sandbox`) because it needs to
fork/exec `gr attach` child processes. This means:

- **Full user privileges** — the GUI process and all child processes run with
  the user's full permissions. No sandbox containment.
- **Command execution surface** — `grPath` and session names flow to
  `execve`. Session names are validated by the daemon (alphanumeric first
  character, then alphanumerics/dots/underscores/hyphens, max 128 chars, no
  `..`) so injection risk is low, but the GUI should validate session names
  in `NewSessionSheet` before passing to `gr new` for better UX (currently
  only checks non-empty).
- **Environment variables** — both management subprocesses (`gr list`,
  `gr new`) and attach PTY children start from the GUI process's full
  environment (`ProcessInfo.processInfo.environment`), then add/override
  specific variables. Management subprocesses add `GR_AGENT_MODE=1`; attach
  children add `TERM=xterm-256color` and `COLORTERM=truecolor`. Both augment
  `PATH` with Homebrew paths. If the GUI is launched from a shell with API
  tokens or secrets in its environment, those are inherited by `gr`
  subprocesses. Consider filtering the environment to a known-safe set for
  distribution builds.
- **URL opening** — Cmd+click on OSC 8 hyperlinks in terminal output opens
  links via `NSWorkspace.shared.open()`. Only `http`, `https`, and `mailto`
  schemes are opened automatically; all other schemes are silently ignored
  (no confirmation dialog exists yet).
- **Sandboxed sessions** — the `Session` model includes a `sandboxed` field
  from the daemon. The GUI does not currently display sandbox status but
  should show it in the sidebar as a visual indicator.
- **Codesigning** — for distribution, the binary must be signed with a
  Developer ID certificate and notarized. The hardened runtime entitlement
  (`com.apple.security.get-task-allow`) is currently debug-only.

### Error Handling & Recovery

| Failure | Current behavior | Target behavior |
|---------|-----------------|-----------------|
| Daemon fails to start | `SessionStore.error` set, sidebar shows error text | Show specific error banner (binary not found, timeout, config) |
| `gr attach` fork fails | Silent: frees args/env, returns — pane stays blank, no error shown | Show error in terminal pane, log to console, offer retry |
| `gr attach` child exits | `onProcessExit` fires | Show detached overlay with "Reattach" / "Choose another session" |
| Session deleted while attached | Child exits, reattach fails | Show "Session deleted" in overlay, auto-select next session |
| Metal device unavailable | `fatalError` | Fall back to CoreText renderer automatically |
| `gr list` JSON decode fails | Error logged, stale data shown | Show stale data with "last updated" timestamp, retry on next poll |
| `gr new` fails | Error from CLI | Show error in NewSessionSheet, keep sheet open for retry |

### Accessibility

v1 accessibility targets (not yet implemented — relies on default SwiftUI
semantics until explicit accessibility code is added):

- **Sidebar** — VoiceOver labels for session name, status, agent type. Session
  tree structure exposed via `NSAccessibility` group roles.
- **Keyboard navigation** — Cmd+1-9 for session jumping, Cmd+]/[ for
  next/prev. Tab should move focus between sidebar and terminal pane.
- **Terminal content** — inherently difficult for VoiceOver. The terminal grid
  is not semantically accessible (it's a character grid, not structured text).
  This is a known limitation shared by all terminal emulators. The `getVisibleText`
  method provides raw text for copy/paste but not semantic navigation.
- **High contrast** — not supported in v1 (fixed Catppuccin Mocha theme).
  Deferred to configurable themes.
- **Reduce Motion** — cursor blink and attention animations should respect
  `NSWorkspace.shared.accessibilityDisplayShouldReduceMotion`.

### Testing Strategy

**Unit tests (high priority):**
- `KeyMapping`: verify all macOS virtual keyCodes map to correct ghostty keys
- `GhosttyTerminalState`: VT write, resize, key encoding, selection
- `Session` model: JSON decoding from `gr list --json` output, including
  schema evolution (unknown fields ignored by Codable)
- `NewSessionSheet`: argument construction validation

**Integration tests (medium priority):**
- Spawn real daemon + create session + attach via PTY + verify output
- Resize flow: verify `TIOCSWINSZ` ioctl propagates correctly
- Detach/reattach cycle: process exit → overlay → reattach → new PTY

**Manual test matrix (pre-release):**
- [ ] US English keyboard: all printable chars, arrows, function keys, modifiers
- [ ] Japanese IME: compose, commit, cancel
- [ ] Text selection: click-drag, double-click word, triple-click line
- [ ] Copy/paste: Cmd+C terminal selection, Cmd+V into terminal
- [ ] URL click: http/https links open in browser
- [ ] Split view: two different sessions, resize divider, close split
- [ ] Metal renderer: normal output, scrollback, resize
- [ ] CoreText renderer: same checks as Metal
- [ ] Font size: Cmd+/Cmd+- changes font, new sessions inherit size
- [ ] Bell: dock bounce when app unfocused
- [ ] Session lifecycle: create, stop, resume, restart, delete from sidebar

**CI:** Add `swift test` target to the GUI package. Currently no test target
exists in `Package.swift` — needs a test target with `@testable import GraithGUI`.

### Compatibility & Versioning

| Dependency | Version | Notes |
|-----------|---------|-------|
| macOS | 14.0+ (Apple Silicon only) | Set in Package.swift `.macOS(.v14)`. Intel Macs require a universal libghostty-vt rebuild. |
| Swift tools | 5.9+ | Package.swift `swift-tools-version: 5.9` |
| libghostty-vt | **TODO: pin commit SHA** | Pre-built `gui/Libraries/libghostty-vt.a`, arm64 only. No rebuild script yet. |
| graith CLI (`gr`) | **TODO: minimum version** | GUI depends on `gr list --json` schema, `gr attach`, `gr new` flags |

**Schema evolution:** The GUI's `Session` model uses `Codable` with default
values for optional fields. New fields added to `gr list --json` are silently
ignored. Breaking changes (field removal/rename) would require a GUI update.
The GUI should log a warning if it encounters an unrecognized `status` value.

**libghostty-vt rebuild:** The static library is currently pre-built with no
documented provenance. To rebuild:
1. Clone ghostty, check out the pinned commit
2. Build libghostty-vt for arm64-apple-macos14
3. Copy headers to `gui/Sources/CGhosttyVT/include/`
4. Copy `libghostty-vt.a` to `gui/Libraries/`

This process needs to be scripted and the commit SHA pinned in the repo.

### Deployment & Distribution

**Current state:** Developer-only. `swift build --disable-sandbox` produces
an executable, not an `.app` bundle. SPM executables aren't `.app` bundles,
so macOS treats the process as a background app (no dock icon). The app
works around this by calling `NSApplication.shared.setActivationPolicy(.regular)`
on launch to force regular app behavior with a dock icon.

**v1 distribution plan (developer machines):**
1. **Build script** — produce a proper `.app` bundle with `Info.plist`, app
   icon, bundle identifier (`com.graith.gui` or similar)
2. **`gr` discovery** — the GUI finds `gr` via PATH search with Homebrew
   fallbacks (`/opt/homebrew/bin/gr`, `/usr/local/bin/gr`). For bundled
   distribution, embed `gr` in the app bundle's `Contents/MacOS/` or
   `Contents/Helpers/`.
3. **Code signing** — sign with Developer ID certificate for distribution
   outside the App Store. Use hardened runtime.
4. **Notarization** — submit to Apple's notary service for Gatekeeper
   approval. Required for users who haven't disabled Gatekeeper.
5. **Updates** — manual download for v1. Consider Sparkle framework for
   auto-updates in v2.

**App Sandbox is not feasible** for the current architecture because
`forkpty()` + `execve()` is blocked by the sandbox. Alternatives:
- XPC service for process management (complex, deferred)
- `NSTask` with specific entitlements (limited, may not support PTY)
- App Sandbox with `com.apple.security.temporary-exception.mach-lookup` (fragile)

None of these are worth the complexity for v1.

# Consensus

Proposal 1 is the chosen approach. The PTY-based attachment model is pragmatic
— it reuses the existing client/daemon protocol without changes, and the
process-per-session overhead is negligible for the expected session count
(tens, not thousands).

# Other Notes

## References

- [ghostty](https://github.com/ghostty-org/ghostty) — Terminal emulator
  whose libghostty-vt library provides VT parsing and key encoding.
- [graith](https://github.com/d0ugal/graith) — The terminal multiplexer
  this GUI wraps.
- [NSTextInputClient](https://developer.apple.com/documentation/appkit/nstextinputclient) —
  Apple's protocol for IME-aware text input in custom views.

## Implementation Notes

### File structure

```
gui/
  Package.swift                     Swift package manifest
  Libraries/libghostty-vt.a        Static library (pre-built, arm64)
  Sources/
    CGhosttyVT/                    C module map for libghostty-vt headers
    GraithGUI/
      GraithApp.swift              App entry point, menus
      ContentView.swift            Main layout, split panes
      BaseTerminalNSView.swift     Shared terminal NSView base class
      GhosttyTerminalView.swift    CoreText rendering (GhosttyTerminalNSView)
                                   + SwiftUI wrapper (GhosttyTerminalPane)
      MetalTerminalView.swift      Metal rendering (MetalTerminalNSView)
                                   + SwiftUI wrapper (MetalTerminalPane)
      MetalTerminalRenderer.swift  Metal pipeline, font atlas, shaders
      GhosttyTerminal.swift        libghostty-vt Swift wrapper
                                   (GhosttyTerminalState)
      KeyMapping.swift             macOS keyCode -> ghostty key mapping
      SessionStore.swift           Session list management, polling
      SessionSidebar.swift         Hierarchical sidebar UI (RepoSection,
                                   SessionTreeNode, FleetSummaryBar)
      NewSessionSheet.swift        New session creation dialog
      TerminalSearchBar.swift      In-terminal search UI
                                   (TerminalSearchState)
      Session.swift                Session data model (Codable)
      SplitDivider.swift           Resizable split divider
      Theme.swift                  Color and font constants (Catppuccin Mocha)
```

### Build

```bash
cd gui && swift build --disable-sandbox
swift run --disable-sandbox GraithGUI
```

The `--disable-sandbox` flag is required because the app forks child processes
(`gr attach`) which App Sandbox prevents.

### Known issues and remaining work

1. **Keyboard input** — Use-after-free in key encoder is fixed (C string
   pointer kept valid during encode), along with doCommand C0/PUA text
   filtering and insertText control char keyCode fix. Verified via code
   review; full manual testing per the test matrix above is pending.
2. **Font atlas rebuild** — The Metal renderer recreates the entire
   MetalTerminalRenderer on font size change. A more efficient approach would
   rebuild only the font atlas.
3. **Session polling** — SessionStore polls `gr list --json` every 2 seconds.
   See Compatibility section for migration plan to fs-watch.
4. **Same-session split guard** — `splitRight()` initializes the secondary
   pane with the same session as the primary, which triggers single-attach
   conflict (daemon kicks the first attach). Need to either block same-session
   split or auto-select a different session for the secondary pane. V1
   acceptance criterion depends on this.
5. **Search scroll bug** — `TerminalSearchState.scrollToCurrentMatch` passes
   the match row index as a scroll delta, but `scrollViewport` expects a
   relative delta. Result: scrolls by N rows instead of to the match. No
   visual match highlighting in the renderer yet either.
