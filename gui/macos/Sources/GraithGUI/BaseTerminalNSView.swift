import AppKit
import GraithTerminalCore
import GraithProtocol
import SwiftUI
import Metal
import CGhosttyVT

/// Base terminal NSView. Renders a session by attaching to the daemon over the
/// shared `GraithProtocolClient` (local Unix socket) — channel 0x01 output is
/// fed into `GhosttyTerminalState`, keystrokes/resize go back over the same
/// attach connection. There is **no** local PTY and **no** `gr` subprocess
/// (the old `forkpty()` + `execve("gr attach")` transport is gone).
class BaseTerminalNSView: NSView, NSTextInputClient {
    private(set) var terminalState: GhosttyTerminalState?

    private let client: GraithProtocolClient
    let sessionID: String
    private var attachSession: AttachSession?
    private var attachTask: Task<Void, Never>?
    private var isAttached = false

    private var displayTimer: Timer?
    var needsTerminalRedraw = true

    var cellWidth: CGFloat = 0
    var cellHeight: CGFloat = 0
    var cellDescent: CGFloat = 0
    private(set) var gridCols: UInt16 = 80
    private(set) var gridRows: UInt16 = 24

    private var markedText: NSMutableAttributedString = NSMutableAttributedString()
    private var markedRange_: NSRange = NSRange(location: NSNotFound, length: 0)
    private var selectedRange_: NSRange = NSRange(location: 0, length: 0)

    private var isSelecting = false
    private var hoveredURL: String?
    private var urlCursorSet = false
    private var fontSizeObserver: NSObjectProtocol?
    private var cleanedUp = false

    /// Called when the attach ends (detach, session stop, or takeover kick).
    var onProcessExit: ((Int32?) -> Void)?
    var searchState: TerminalSearchState?

    init(sessionID: String, client: GraithProtocolClient, fontSize: CGFloat = Theme.defaultFontSize) {
        self.sessionID = sessionID
        self.client = client

        super.init(frame: .zero)

        setupRendering(fontSize: fontSize)

        wantsLayer = true
        layer?.backgroundColor = Theme.terminalBg.cgColor

        fontSizeObserver = NotificationCenter.default.addObserver(
            forName: .terminalFontSizeChanged, object: nil, queue: .main
        ) { [weak self] notification in
            guard let self, let newSize = notification.object as? CGFloat else { return }
            self.fontSizeChanged(newSize)
        }
    }

    func configureSearch(_ state: TerminalSearchState) {
        self.searchState = state
        state.getVisibleText = { [weak self] in
            let text = self?.terminalState?.getVisibleText() ?? []
            self?.needsTerminalRedraw = true
            return text
        }
        state.scrollViewport = { [weak self] delta in
            self?.terminalState?.scrollViewport(delta: delta)
            self?.needsTerminalRedraw = true
        }
        state.onSearchChanged = { [weak self] in
            self?.handleDirtyFrame()
        }
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override var acceptsFirstResponder: Bool { true }
    override var canBecomeKeyView: Bool { true }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        if window != nil && terminalState == nil {
            startTerminal()
        }
    }

    override func removeFromSuperview() {
        cleanup()
        super.removeFromSuperview()
    }

    deinit {
        cleanup()
    }

    private func cleanup() {
        guard !cleanedUp else { return }
        cleanedUp = true

        if urlCursorSet {
            NSCursor.pop()
            urlCursorSet = false
        }

        if let observer = fontSizeObserver {
            NotificationCenter.default.removeObserver(observer)
            fontSizeObserver = nil
        }
        displayTimer?.invalidate()
        displayTimer = nil

        isAttached = false
        attachTask?.cancel()
        attachTask = nil
        if let attachSession {
            Task { await attachSession.close() }
        }
        attachSession = nil

        terminalState?.freeSelection()
        terminalState = nil
    }

    // MARK: - Subclass Hooks

    func setupRendering(fontSize: CGFloat) {}
    func updateRendering(fontSize: CGFloat) {}
    func handleDirtyFrame() {}
    func onFrameResized() {}

    // MARK: - Font

    private func fontSizeChanged(_ size: CGFloat) {
        updateRendering(fontSize: size)

        let (cols, rows) = calculateGridSize()
        gridCols = cols
        gridRows = rows
        terminalState?.resize(
            cols: cols, rows: rows,
            cellWidth: UInt32(cellWidth), cellHeight: UInt32(cellHeight)
        )
        sendResize(cols: cols, rows: rows)
        updateMouseEncoderSize()
        needsTerminalRedraw = true
    }

    // MARK: - Grid

    func calculateGridSize() -> (cols: UInt16, rows: UInt16) {
        let cols = max(1, UInt16(floor(bounds.width / cellWidth)))
        let rows = max(1, UInt16(floor(bounds.height / cellHeight)))
        return (cols, rows)
    }

    // MARK: - Terminal Lifecycle

    private func startTerminal() {
        let (cols, rows) = calculateGridSize()
        gridCols = cols
        gridRows = rows

        terminalState = GhosttyTerminalState(cols: cols, rows: rows)
        terminalState?.setColors(
            fg: (0xcd, 0xd6, 0xf4),
            bg: (0x1e, 0x1e, 0x2e),
            cursor: (0xf5, 0xe0, 0xdc)
        )

        terminalState?.initSelection()
        beginAttach(cols: cols, rows: rows)
        startDisplayTimer()
        updateMouseEncoderSize()
    }

    /// Open an attach connection to the daemon and stream its PTY output into
    /// the terminal. Runs the read loop off the main actor and hops back to the
    /// main thread to feed `GhosttyTerminalState` and request redraws.
    private func beginAttach(cols: UInt16, rows: UInt16) {
        let client = self.client
        let sessionID = self.sessionID
        attachTask = Task { [weak self] in
            do {
                let attach = try await client.attach(sessionID: sessionID, cols: cols, rows: rows)
                await MainActor.run {
                    guard let self, !self.cleanedUp else { return }
                    self.attachSession = attach
                    self.isAttached = true
                }

                // Stream channel 0x01 output (begins with the scrollback tail).
                for await chunk in attach.output {
                    let hasBell = chunk.contains(0x07)
                    await MainActor.run {
                        guard let self else { return }
                        self.terminalState?.write(chunk)
                        self.needsTerminalRedraw = true
                        if hasBell, !NSApp.isActive || self.window?.isKeyWindow == false {
                            NSApp.requestUserAttention(.informationalRequest)
                        }
                    }
                }

                // Output finished: detached, session stopped, or takeover kick.
                await MainActor.run {
                    guard let self else { return }
                    self.isAttached = false
                    self.onProcessExit?(nil)
                }
            } catch {
                await MainActor.run {
                    self?.isAttached = false
                    self?.onProcessExit?(nil)
                }
            }
        }
    }

    private func startDisplayTimer() {
        displayTimer = Timer.scheduledTimer(withTimeInterval: 1.0 / 60.0, repeats: true) { [weak self] _ in
            guard let self, self.needsTerminalRedraw else { return }
            self.needsTerminalRedraw = false
            let dirty = self.terminalState?.updateRenderState() ?? GHOSTTY_RENDER_STATE_DIRTY_FALSE
            if dirty != GHOSTTY_RENDER_STATE_DIRTY_FALSE {
                self.handleDirtyFrame()
            }
        }
    }

    // MARK: - Resize

    override func setFrameSize(_ newSize: NSSize) {
        super.setFrameSize(newSize)
        onFrameResized()
        let (cols, rows) = calculateGridSize()
        if cols != gridCols || rows != gridRows {
            gridCols = cols
            gridRows = rows
            terminalState?.resize(
                cols: cols, rows: rows,
                cellWidth: UInt32(cellWidth), cellHeight: UInt32(cellHeight)
            )
            sendResize(cols: cols, rows: rows)
            updateMouseEncoderSize()
            needsTerminalRedraw = true
        }
    }

    /// Send a resize control message to the daemon (design C.3 — the daemon
    /// resizes the real PTY; there is no local `TIOCSWINSZ`).
    private func sendResize(cols: UInt16, rows: UInt16) {
        guard isAttached, let attachSession else { return }
        Task { try? await attachSession.resize(cols: cols, rows: rows) }
    }

    private func updateMouseEncoderSize() {
        terminalState?.setMouseEncoderSize(
            screenWidth: UInt32(bounds.width),
            screenHeight: UInt32(bounds.height),
            cellWidth: UInt32(cellWidth),
            cellHeight: UInt32(cellHeight)
        )
    }

    // MARK: - Keyboard Input (IME-aware)

    override func keyDown(with event: NSEvent) {
        guard isAttached else { return }

        if event.modifierFlags.contains(.command) {
            if event.charactersIgnoringModifiers == "c" {
                copySelection()
                return
            }
            if event.charactersIgnoringModifiers == "v" {
                pasteFromClipboard()
                return
            }
            if event.charactersIgnoringModifiers == "a" {
                selectAllViewport()
                return
            }
        }

        terminalState?.clearSelection()
        inputContext?.handleEvent(event)
    }

    override func keyUp(with event: NSEvent) {
        guard isAttached else { return }

        let key = macOSKeyCodeToGhostty(event.keyCode)
        let mods = nsModsToGhostty(event.modifierFlags)

        if let encoded = terminalState?.encodeKey(
            action: GHOSTTY_KEY_ACTION_RELEASE, key: key, mods: mods, text: nil
        ) {
            sendInput(encoded)
        }
    }

    override func flagsChanged(with event: NSEvent) {}

    // MARK: - NSTextInputClient (IME support)

    func insertText(_ string: Any, replacementRange: NSRange) {
        guard isAttached else { return }
        markedText = NSMutableAttributedString()
        markedRange_ = NSRange(location: NSNotFound, length: 0)

        let str: String
        if let s = string as? String {
            str = s
        } else if let s = string as? NSAttributedString {
            str = s.string
        } else {
            return
        }

        if str.utf8.count == 1, let byte = str.utf8.first, byte < 0x20 || byte == 0x7f {
            if let event = NSApp.currentEvent, event.type == .keyDown {
                let key = macOSKeyCodeToGhostty(event.keyCode)
                let mods = nsModsToGhostty(event.modifierFlags)
                if let encoded = terminalState?.encodeKey(
                    action: GHOSTTY_KEY_ACTION_PRESS, key: key, mods: mods, text: nil
                ) {
                    sendInput(encoded)
                }
            }
        } else if let event = NSApp.currentEvent, event.type == .keyDown {
            let key = macOSKeyCodeToGhostty(event.keyCode)
            let mods = nsModsToGhostty(event.modifierFlags)
            if let encoded = terminalState?.encodeKey(
                action: GHOSTTY_KEY_ACTION_PRESS, key: key, mods: mods, text: str
            ) {
                sendInput(encoded)
            }
        } else {
            if let data = str.data(using: .utf8) {
                sendInput(data)
            }
        }
    }

    func setMarkedText(_ string: Any, selectedRange: NSRange, replacementRange: NSRange) {
        if let s = string as? NSAttributedString {
            markedText = NSMutableAttributedString(attributedString: s)
        } else if let s = string as? String {
            markedText = NSMutableAttributedString(string: s)
        }
        markedRange_ = NSRange(location: 0, length: markedText.length)
        selectedRange_ = selectedRange
        needsTerminalRedraw = true
    }

    func unmarkText() {
        markedText = NSMutableAttributedString()
        markedRange_ = NSRange(location: NSNotFound, length: 0)
    }

    func selectedRange() -> NSRange { selectedRange_ }
    func markedRange() -> NSRange { markedRange_ }
    func hasMarkedText() -> Bool { markedRange_.location != NSNotFound }
    func attributedSubstring(forProposedRange range: NSRange, actualRange: NSRangePointer?) -> NSAttributedString? { nil }

    func validAttributesForMarkedText() -> [NSAttributedString.Key] {
        [.font, .foregroundColor, .backgroundColor]
    }

    func firstRect(forCharacterRange range: NSRange, actualRange: NSRangePointer?) -> NSRect {
        guard let cursor = terminalState?.getCursorInfo() else {
            return window?.convertToScreen(CGRect(origin: .zero, size: CGSize(width: cellWidth, height: cellHeight))) ?? .zero
        }
        let x = CGFloat(cursor.x) * cellWidth
        let y = bounds.height - CGFloat(cursor.y + 1) * cellHeight
        let viewRect = CGRect(x: x, y: y, width: cellWidth, height: cellHeight)
        let windowRect = convert(viewRect, to: nil)
        return window?.convertToScreen(windowRect) ?? viewRect
    }

    func characterIndex(for point: NSPoint) -> Int { 0 }

    override func doCommand(by selector: Selector) {
        guard isAttached, let event = NSApp.currentEvent, event.type == .keyDown else { return }

        let key = macOSKeyCodeToGhostty(event.keyCode)
        guard key != GHOSTTY_KEY_UNIDENTIFIED else { return }

        let mods = nsModsToGhostty(event.modifierFlags)
        if let encoded = terminalState?.encodeKey(
            action: GHOSTTY_KEY_ACTION_PRESS, key: key, mods: mods, text: event.characters
        ) {
            sendInput(encoded)
        }
    }

    // MARK: - Copy/Paste

    // Standard responder-chain actions so the menu bar's Edit menu (and any
    // ⌘C/⌘V/⌘A key equivalents it owns) routes to the focused terminal. The
    // keyDown interception above is kept as a fallback for when no menu owns
    // the shortcut.
    @objc func copy(_ sender: Any?) { copySelection() }
    @objc func paste(_ sender: Any?) { pasteFromClipboard() }
    @objc override func selectAll(_ sender: Any?) { selectAllViewport() }

    /// Clear the visible screen (⌘K). Sends Ctrl-L, which shells and most agent
    /// TUIs interpret as "clear/redraw" — the closest portable clear over a
    /// remote PTY where we don't own the scrollback buffer directly.
    @objc func clearTerminal(_ sender: Any?) {
        sendInput(Data([0x0c]))
    }

    @objc func validateMenuItem(_ item: NSMenuItem) -> Bool {
        switch item.action {
        case #selector(copy(_:)):
            return !(terminalState?.getSelectedText()?.isEmpty ?? true)
        case #selector(paste(_:)):
            return NSPasteboard.general.string(forType: .string) != nil
        default:
            return true
        }
    }

    private func copySelection() {
        guard let text = terminalState?.getSelectedText(), !text.isEmpty else { return }
        let pb = NSPasteboard.general
        pb.clearContents()
        pb.setString(text, forType: .string)
    }

    private func pasteFromClipboard() {
        guard let text = NSPasteboard.general.string(forType: .string) else { return }
        let output: String
        if terminalState?.isBracketedPasteEnabled() == true {
            output = "\u{1b}[200~\(text)\u{1b}[201~"
        } else {
            output = text
        }
        if let data = output.data(using: .utf8) {
            sendInput(data)
        }
    }

    private func selectAllViewport() {
        if terminalState?.gridRefAtViewport(x: 0, y: 0) != nil {
            terminalState?.selectionPress(
                col: 0, row: 0,
                surfaceX: 0, surfaceY: 0,
                timeNs: currentTimeNs()
            )
            terminalState?.selectionDrag(
                col: gridCols - 1, row: UInt32(gridRows - 1),
                surfaceX: Double(bounds.width), surfaceY: Double(bounds.height),
                columns: UInt32(gridCols), cellWidth: UInt32(cellWidth),
                screenHeight: UInt32(bounds.height)
            )
            needsTerminalRedraw = true
        }
    }

    /// Send raw input bytes (keystrokes, encoded keys, pasted text) to the
    /// session over the attach connection's data channel.
    func sendInput(_ data: Data) {
        guard isAttached, let attachSession else { return }
        Task { try? await attachSession.send(data) }
    }

    func nsModsToGhostty(_ flags: NSEvent.ModifierFlags) -> GhosttyMods {
        var mods: GhosttyMods = 0
        if flags.contains(.shift)   { mods |= UInt16(GHOSTTY_MODS_SHIFT) }
        if flags.contains(.control) { mods |= UInt16(GHOSTTY_MODS_CTRL) }
        if flags.contains(.option)  { mods |= UInt16(GHOSTTY_MODS_ALT) }
        if flags.contains(.command) { mods |= UInt16(GHOSTTY_MODS_SUPER) }
        if flags.contains(.capsLock) { mods |= UInt16(GHOSTTY_MODS_CAPS_LOCK) }
        return mods
    }

    func cellAt(_ point: NSPoint) -> (col: UInt16, row: UInt32) {
        let col = max(0, min(Int(gridCols) - 1, Int(point.x / cellWidth)))
        let row = max(0, min(Int(gridRows) - 1, Int((bounds.height - point.y) / cellHeight)))
        return (UInt16(col), UInt32(row))
    }

    private static let machTimebaseInfo: mach_timebase_info_data_t = {
        var info = mach_timebase_info_data_t()
        mach_timebase_info(&info)
        return info
    }()

    func currentTimeNs() -> UInt64 {
        let info = Self.machTimebaseInfo
        return mach_absolute_time() * UInt64(info.numer) / UInt64(info.denom)
    }

    // MARK: - Scrollback

    override func scrollWheel(with event: NSEvent) {
        if terminalState?.isMouseTrackingActive() == true {
            handleMouseScroll(event)
            return
        }

        let delta = event.scrollingDeltaY
        if event.hasPreciseScrollingDeltas {
            let rows = Int(-delta / cellHeight)
            if rows != 0 {
                terminalState?.scrollViewport(delta: rows)
                needsTerminalRedraw = true
            }
        } else {
            let rows = Int(-delta * 3)
            if rows != 0 {
                terminalState?.scrollViewport(delta: rows)
                needsTerminalRedraw = true
            }
        }
    }

    private func handleMouseScroll(_ event: NSEvent) {
        let delta = event.scrollingDeltaY
        let button: GhosttyMouseButton = delta < 0 ? GHOSTTY_MOUSE_BUTTON_FIVE : GHOSTTY_MOUSE_BUTTON_FOUR
        let mods = nsModsToGhostty(event.modifierFlags)
        let loc = convert(event.locationInWindow, from: nil)

        let ticks = max(1, abs(Int(event.hasPreciseScrollingDeltas ? delta / cellHeight : delta)))
        for _ in 0..<ticks {
            if let encoded = terminalState?.encodeMouse(
                action: GHOSTTY_MOUSE_ACTION_PRESS,
                button: button, mods: mods,
                x: Float(loc.x), y: Float(bounds.height - loc.y)
            ) {
                sendInput(encoded)
            }
        }
    }

    // MARK: - Mouse Events

    override func mouseDown(with event: NSEvent) {
        window?.makeFirstResponder(self)
        let loc = convert(event.locationInWindow, from: nil)

        if event.modifierFlags.contains(.command) {
            let (col, row) = cellAt(loc)
            if let url = terminalState?.hyperlinkAtViewport(col: col, row: row),
               let nsURL = URL(string: url),
               let scheme = nsURL.scheme?.lowercased(),
               scheme == "http" || scheme == "https" || scheme == "mailto" {
                NSWorkspace.shared.open(nsURL)
                return
            }
        }

        if terminalState?.isMouseTrackingActive() == true && !event.modifierFlags.contains(.option) {
            forwardMouse(event, action: GHOSTTY_MOUSE_ACTION_PRESS, button: GHOSTTY_MOUSE_BUTTON_LEFT)
            return
        }

        let (col, row) = cellAt(loc)
        isSelecting = true
        terminalState?.selectionPress(
            col: col, row: row,
            surfaceX: Double(loc.x), surfaceY: Double(bounds.height - loc.y),
            timeNs: currentTimeNs()
        )
        needsTerminalRedraw = true
    }

    override func mouseUp(with event: NSEvent) {
        if terminalState?.isMouseTrackingActive() == true && !event.modifierFlags.contains(.option) {
            forwardMouse(event, action: GHOSTTY_MOUSE_ACTION_RELEASE, button: GHOSTTY_MOUSE_BUTTON_LEFT)
            return
        }

        if isSelecting {
            let loc = convert(event.locationInWindow, from: nil)
            let (col, row) = cellAt(loc)
            terminalState?.selectionRelease(col: col, row: row)
            isSelecting = false
            needsTerminalRedraw = true
        }
    }

    override func mouseDragged(with event: NSEvent) {
        if terminalState?.isMouseTrackingActive() == true && !event.modifierFlags.contains(.option) {
            forwardMouse(event, action: GHOSTTY_MOUSE_ACTION_MOTION, button: GHOSTTY_MOUSE_BUTTON_LEFT)
            return
        }

        if isSelecting {
            let loc = convert(event.locationInWindow, from: nil)
            let (col, row) = cellAt(loc)
            terminalState?.selectionDrag(
                col: col, row: row,
                surfaceX: Double(loc.x), surfaceY: Double(bounds.height - loc.y),
                columns: UInt32(gridCols), cellWidth: UInt32(cellWidth),
                screenHeight: UInt32(bounds.height)
            )
            needsTerminalRedraw = true
        }
    }

    override func mouseMoved(with event: NSEvent) {
        let loc = convert(event.locationInWindow, from: nil)

        if event.modifierFlags.contains(.command) {
            let (col, row) = cellAt(loc)
            let url = terminalState?.hyperlinkAtViewport(col: col, row: row)
            if url != hoveredURL {
                hoveredURL = url
                if url != nil {
                    if !urlCursorSet {
                        NSCursor.pointingHand.push()
                        urlCursorSet = true
                    }
                } else {
                    if urlCursorSet {
                        NSCursor.pop()
                        urlCursorSet = false
                    }
                }
            }
        } else {
            if urlCursorSet {
                NSCursor.pop()
                urlCursorSet = false
            }
            hoveredURL = nil
        }

        if terminalState?.isMouseTrackingActive() == true {
            forwardMouse(event, action: GHOSTTY_MOUSE_ACTION_MOTION, button: nil)
        }
    }

    override func rightMouseDown(with event: NSEvent) {
        guard terminalState?.isMouseTrackingActive() == true else {
            super.rightMouseDown(with: event)
            return
        }
        forwardMouse(event, action: GHOSTTY_MOUSE_ACTION_PRESS, button: GHOSTTY_MOUSE_BUTTON_RIGHT)
    }

    override func rightMouseUp(with event: NSEvent) {
        guard terminalState?.isMouseTrackingActive() == true else {
            super.rightMouseUp(with: event)
            return
        }
        forwardMouse(event, action: GHOSTTY_MOUSE_ACTION_RELEASE, button: GHOSTTY_MOUSE_BUTTON_RIGHT)
    }

    override func otherMouseDown(with event: NSEvent) {
        guard terminalState?.isMouseTrackingActive() == true else { return }
        forwardMouse(event, action: GHOSTTY_MOUSE_ACTION_PRESS, button: GHOSTTY_MOUSE_BUTTON_MIDDLE)
    }

    override func otherMouseUp(with event: NSEvent) {
        guard terminalState?.isMouseTrackingActive() == true else { return }
        forwardMouse(event, action: GHOSTTY_MOUSE_ACTION_RELEASE, button: GHOSTTY_MOUSE_BUTTON_MIDDLE)
    }

    private func forwardMouse(_ event: NSEvent, action: GhosttyMouseAction, button: GhosttyMouseButton?) {
        let loc = convert(event.locationInWindow, from: nil)
        let mods = nsModsToGhostty(event.modifierFlags)

        if let encoded = terminalState?.encodeMouse(
            action: action, button: button, mods: mods,
            x: Float(loc.x), y: Float(bounds.height - loc.y)
        ) {
            sendInput(encoded)
        }
    }

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        for area in trackingAreas { removeTrackingArea(area) }
        let area = NSTrackingArea(
            rect: bounds,
            options: [.mouseMoved, .activeInKeyWindow, .inVisibleRect],
            owner: self
        )
        addTrackingArea(area)
    }
}

// MARK: - Shared SwiftUI Container

struct TerminalContainer: View {
    let session: Session
    /// Which pane this terminal occupies in its window. Find (⌘F/⌘G) only acts
    /// on the window's focused pane, so we compare against `window.focusedPane`.
    var pane: WindowState.SplitPane = .primary
    @EnvironmentObject var store: SessionStore
    @EnvironmentObject var window: WindowState
    @State private var attachID = UUID()
    @State private var isDetached = false
    @State private var exitCode: Int32?
    @StateObject private var searchState = TerminalSearchState()

    var body: some View {
        Group {
            if store.isAttachedElsewhere(session.id, owner: window) {
                // The daemon allows one attach per session; another window
                // already owns this one. Show a placeholder rather than kicking
                // it (which would ping-pong). "Open Here" is an explicit human
                // takeover per the design's single-attach rule.
                SessionBusyElsewhere(sessionName: session.name) {
                    store.forceClaimAttach(session.id, owner: window)
                }
            } else {
                terminalStack
            }
        }
        .onAppear { store.claimAttach(session.id, owner: window) }
        .onDisappear { store.releaseAttach(session.id, owner: window) }
    }

    private var terminalStack: some View {
        VStack(spacing: 0) {
            if searchState.isVisible {
                TerminalSearchBar(searchState: searchState)
            }

            ZStack {
                terminalPane
                    .id(attachID)

                if isDetached {
                    detachedOverlay
                }
            }
        }
        .onChange(of: store.renderer) { _, _ in
            isDetached = false
            exitCode = nil
            attachID = UUID()
        }
        // Find is routed to this window's focused pane only (see AppCommands /
        // WindowState.dispatchFind), replacing the old global broadcast.
        .onChange(of: window.findCommand) { _, command in
            guard let command, window.focusedPane == pane else { return }
            switch command.action {
            case .toggle: searchState.toggle()
            case .next: searchState.findNext()
            case .previous: searchState.findPrevious()
            }
        }
    }

    @ViewBuilder
    private var terminalPane: some View {
        // Attach over the client for the daemon this session lives on (local
        // Unix socket, or a paired remote over the tailnet). The transport is
        // abstract, so the terminal drives a remote session identically.
        if let client = store.client(for: session.id) {
            switch store.renderer {
            case .ghosttyCoreText:
                GhosttyTerminalPane(
                    sessionID: session.id,
                    client: client,
                    fontSize: store.fontSize,
                    onExit: handleExit,
                    searchState: searchState
                )
            case .ghosttyMetal:
                if MTLCreateSystemDefaultDevice() != nil {
                    MetalTerminalPane(
                        sessionID: session.id,
                        client: client,
                        fontSize: store.fontSize,
                        onExit: handleExit,
                        searchState: searchState
                    )
                } else {
                    GhosttyTerminalPane(
                        sessionID: session.id,
                        client: client,
                        fontSize: store.fontSize,
                        onExit: handleExit,
                        searchState: searchState
                    )
                    .onAppear {
                        store.renderer = .ghosttyCoreText
                    }
                }
            }
        } else {
            HostDisconnected(sessionName: session.name)
        }
    }

    private func handleExit(_ code: Int32?) {
        exitCode = code
        isDetached = true
    }

    private var detachedOverlay: some View {
        VStack(spacing: 12) {
            Image(systemName: exitCode == 0 ? "checkmark.circle" : "arrow.uturn.left.circle")
                .font(.system(size: 28))
                .foregroundStyle(exitCode == 0 ? Theme.green : Theme.yellow)

            Text(exitCode == 0 ? "Session detached" : exitCode != nil ? "Process exited (\(exitCode!))" : "Detached")
                .font(.system(.body, design: .monospaced))
                .foregroundStyle(Theme.text)

            Button(action: reattach) {
                HStack(spacing: 6) {
                    Image(systemName: "arrow.clockwise")
                    Text("Reattach")
                }
                .font(.system(.body, design: .monospaced))
                .foregroundStyle(Theme.crust)
                .padding(.horizontal, 16)
                .padding(.vertical, 8)
                .background(Theme.green)
                .clipShape(RoundedRectangle(cornerRadius: 6))
            }
            .buttonStyle(.plain)
            .keyboardShortcut(.return)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.base.opacity(0.85))
    }

    private func reattach() {
        isDetached = false
        exitCode = nil
        attachID = UUID()
    }
}

/// Shown when a session's single attach is held by another window. Offers an
/// explicit takeover instead of silently stealing the attach.
struct SessionBusyElsewhere: View {
    let sessionName: String
    let onTakeover: () -> Void

    var body: some View {
        VStack(spacing: 16) {
            Image(systemName: "macwindow.on.rectangle")
                .font(.system(size: 28))
                .foregroundStyle(Theme.yellow)

            VStack(spacing: 4) {
                Text("Open in another window")
                    .font(.system(.body, design: .monospaced))
                    .foregroundStyle(Theme.text)
                Text("“\(sessionName)” is attached elsewhere")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
            }

            Button(action: onTakeover) {
                HStack(spacing: 6) {
                    Image(systemName: "arrow.right.circle")
                    Text("Open Here")
                }
                .font(.system(.body, design: .monospaced))
                .foregroundStyle(Theme.crust)
                .padding(.horizontal, 16)
                .padding(.vertical, 8)
                .background(Theme.green)
                .clipShape(RoundedRectangle(cornerRadius: 6))
            }
            .buttonStyle(.plain)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.base)
    }
}

/// Shown when a session's host is not connected (a remote daemon dropped off
/// the tailnet, or was removed). The list refreshes on its own; this is a
/// placeholder rather than an error.
struct HostDisconnected: View {
    let sessionName: String

    var body: some View {
        VStack(spacing: 16) {
            Image(systemName: "wifi.slash")
                .font(.system(size: 28))
                .foregroundStyle(Theme.overlay0)

            VStack(spacing: 4) {
                Text("Host not connected")
                    .font(.system(.body, design: .monospaced))
                    .foregroundStyle(Theme.text)
                Text("Can't reach the daemon for “\(sessionName)”")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.base)
    }
}
