import AppKit
import SwiftUI
import Metal
import CGhosttyVT

class BaseTerminalNSView: NSView, NSTextInputClient {
    private(set) var terminalState: GhosttyTerminalState?
    private var ptyFD: Int32 = -1
    private var childPID: pid_t = 0
    private var readSource: DispatchSourceRead?
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
    private let writeQueue = DispatchQueue(label: "com.graith.pty-write", qos: .userInteractive)

    var onProcessExit: ((Int32?) -> Void)?
    var searchState: TerminalSearchState?

    let grPath: String
    let sessionName: String

    init(sessionName: String, grPath: String, fontSize: CGFloat = Theme.defaultFontSize) {
        self.sessionName = sessionName
        self.grPath = grPath

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

        let pid = childPID
        childPID = 0
        ptyFD = -1
        if pid > 0 {
            kill(pid, SIGTERM)
        }

        readSource?.cancel()
        readSource = nil
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

        if ptyFD >= 0 {
            var ws = winsize()
            ws.ws_col = cols
            ws.ws_row = rows
            ws.ws_xpixel = UInt16(cellWidth * CGFloat(cols))
            ws.ws_ypixel = UInt16(cellHeight * CGFloat(rows))
            _ = ioctl(ptyFD, TIOCSWINSZ, &ws)
        }

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
        startPTY()
        startDisplayTimer()
        updateMouseEncoderSize()
    }

    private func startPTY() {
        var env = ProcessInfo.processInfo.environment
        let extra = "/opt/homebrew/bin:/usr/local/bin"
        env["PATH"] = extra + ":" + (env["PATH"] ?? "/usr/bin:/bin")
        env["TERM"] = "xterm-256color"
        env["COLORTERM"] = "truecolor"

        let envArray = env.map { "\($0.key)=\($0.value)" }
        let cEnv = envArray.map { strdup($0) } + [nil]

        let args = [grPath, "attach", sessionName]
        let cArgs = args.map { strdup($0) } + [nil]

        var ws = winsize()
        ws.ws_col = gridCols
        ws.ws_row = gridRows
        ws.ws_xpixel = UInt16(cellWidth * CGFloat(gridCols))
        ws.ws_ypixel = UInt16(cellHeight * CGFloat(gridRows))

        var masterFD: Int32 = 0
        let pid = forkpty(&masterFD, nil, nil, &ws)

        if pid == 0 {
            execve(grPath, cArgs, cEnv)
            _exit(127)
        } else if pid > 0 {
            cArgs.forEach { if let p = $0 { free(p) } }
            cEnv.forEach { if let p = $0 { free(p) } }

            ptyFD = masterFD
            childPID = pid

            let flags = fcntl(ptyFD, F_GETFL)
            _ = fcntl(ptyFD, F_SETFL, flags | O_NONBLOCK)

            let capturedPID = childPID
            let capturedFD = ptyFD

            let source = DispatchSource.makeReadSource(fileDescriptor: ptyFD, queue: .global(qos: .userInteractive))
            source.setEventHandler { [weak self] in
                self?.readFromPTY()
            }
            source.setCancelHandler { [weak self] in
                var status: Int32 = 0
                waitpid(capturedPID, &status, 0)
                close(capturedFD)
                let exitCode: Int32? = (status & 0x7f) == 0 ? (status >> 8) & 0xff : nil
                DispatchQueue.main.async { [weak self] in
                    self?.ptyFD = -1
                    self?.childPID = 0
                    self?.onProcessExit?(exitCode)
                }
            }
            source.resume()
            readSource = source
        } else {
            cArgs.forEach { if let p = $0 { free(p) } }
            cEnv.forEach { if let p = $0 { free(p) } }
            onProcessExit?(nil)
        }
    }

    private func readFromPTY() {
        var buffer = [UInt8](repeating: 0, count: 16384)
        while true {
            let n = read(ptyFD, &buffer, buffer.count)
            if n > 0 {
                let hasBell = buffer[0..<n].contains(0x07)

                let data = Data(bytes: buffer, count: n)
                DispatchQueue.main.async { [weak self] in
                    self?.terminalState?.write(data)
                    self?.needsTerminalRedraw = true

                    if hasBell, NSApp.isActive == false || self?.window?.isKeyWindow == false {
                        NSApp.requestUserAttention(.informationalRequest)
                    }
                }
            } else if n == 0 {
                DispatchQueue.main.async { [weak self] in
                    self?.readSource?.cancel()
                }
                return
            } else {
                if errno == EAGAIN || errno == EWOULDBLOCK {
                    return
                }
                DispatchQueue.main.async { [weak self] in
                    self?.readSource?.cancel()
                }
                return
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

            if ptyFD >= 0 {
                var ws = winsize()
                ws.ws_col = cols
                ws.ws_row = rows
                ws.ws_xpixel = UInt16(cellWidth * CGFloat(cols))
                ws.ws_ypixel = UInt16(cellHeight * CGFloat(rows))
                _ = ioctl(ptyFD, TIOCSWINSZ, &ws)
            }

            updateMouseEncoderSize()
            needsTerminalRedraw = true
        }
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
        guard ptyFD >= 0 else { return }

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
                selectAll()
                return
            }
        }

        terminalState?.clearSelection()
        inputContext?.handleEvent(event)
    }

    override func keyUp(with event: NSEvent) {
        guard ptyFD >= 0 else { return }

        let key = macOSKeyCodeToGhostty(event.keyCode)
        let mods = nsModsToGhostty(event.modifierFlags)

        if let encoded = terminalState?.encodeKey(
            action: GHOSTTY_KEY_ACTION_RELEASE, key: key, mods: mods, text: nil
        ) {
            writeToPTY(encoded)
        }
    }

    override func flagsChanged(with event: NSEvent) {}

    // MARK: - NSTextInputClient (IME support)

    func insertText(_ string: Any, replacementRange: NSRange) {
        guard ptyFD >= 0 else { return }
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
                    writeToPTY(encoded)
                }
            }
        } else if let event = NSApp.currentEvent, event.type == .keyDown {
            let key = macOSKeyCodeToGhostty(event.keyCode)
            let mods = nsModsToGhostty(event.modifierFlags)
            if let encoded = terminalState?.encodeKey(
                action: GHOSTTY_KEY_ACTION_PRESS, key: key, mods: mods, text: str
            ) {
                writeToPTY(encoded)
            }
        } else {
            if let data = str.data(using: .utf8) {
                writeToPTY(data)
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
        guard ptyFD >= 0, let event = NSApp.currentEvent, event.type == .keyDown else { return }

        let key = macOSKeyCodeToGhostty(event.keyCode)
        guard key != GHOSTTY_KEY_UNIDENTIFIED else { return }

        let mods = nsModsToGhostty(event.modifierFlags)
        if let encoded = terminalState?.encodeKey(
            action: GHOSTTY_KEY_ACTION_PRESS, key: key, mods: mods, text: event.characters
        ) {
            writeToPTY(encoded)
        }
    }

    // MARK: - Copy/Paste

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
            writeToPTY(data)
        }
    }

    private func selectAll() {
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

    func writeToPTY(_ data: Data) {
        let fd = ptyFD
        guard fd >= 0 else { return }
        writeQueue.async {
            data.withUnsafeBytes { buf in
                guard let ptr = buf.baseAddress else { return }
                var offset = 0
                while offset < buf.count {
                    let n = Darwin.write(fd, ptr + offset, buf.count - offset)
                    if n > 0 {
                        offset += n
                    } else if n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK) {
                        usleep(1000)
                        continue
                    } else {
                        break
                    }
                }
            }
        }
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
                writeToPTY(encoded)
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
            writeToPTY(encoded)
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
    let grPath: String
    @EnvironmentObject var store: SessionStore
    @State private var attachID = UUID()
    @State private var isDetached = false
    @State private var exitCode: Int32?
    @StateObject private var searchState = TerminalSearchState()

    var body: some View {
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
        .onReceive(NotificationCenter.default.publisher(for: .toggleFind)) { _ in
            searchState.toggle()
        }
    }

    @ViewBuilder
    private var terminalPane: some View {
        switch store.renderer {
        case .ghosttyCoreText:
            GhosttyTerminalPane(
                sessionName: session.name,
                grPath: grPath,
                fontSize: store.fontSize,
                onExit: handleExit,
                searchState: searchState
            )
        case .ghosttyMetal:
            if MTLCreateSystemDefaultDevice() != nil {
                MetalTerminalPane(
                    sessionName: session.name,
                    grPath: grPath,
                    fontSize: store.fontSize,
                    onExit: handleExit,
                    searchState: searchState
                )
            } else {
                GhosttyTerminalPane(
                    sessionName: session.name,
                    grPath: grPath,
                    fontSize: store.fontSize,
                    onExit: handleExit,
                    searchState: searchState
                )
                .onAppear {
                    store.renderer = .ghosttyCoreText
                }
            }
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

            Text(exitCode == 0 ? "Session detached" : exitCode != nil ? "Process exited (\(exitCode!))" : "Failed to attach")
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
