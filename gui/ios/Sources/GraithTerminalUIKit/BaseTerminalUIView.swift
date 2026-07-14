#if canImport(UIKit)
import UIKit
import GraithSessionKit

/// The iOS interactive terminal surface (Task 20). A `UIView` that:
///
///   - hosts the shared Metal render layer (via the `TerminalRenderer` seam),
///   - accepts hardware-keyboard input through `pressesBegan/Ended` and
///     `UIKeyInput`, and IME (marked-text) input through `UITextInput`,
///   - shows the on-screen key accessory row for esc/ctrl/alt/arrows/tab,
///   - drives selection and scroll via gestures,
///   - computes cols/rows from its bounds + cell metrics and sends `resize`
///     control messages (no local `TIOCSWINSZ` — the PTY is remote),
///
/// forwarding everything to a `TerminalAttachViewModel`. This is a rewrite of
/// gui-poc's `BaseTerminalNSView`, not a port: `UITextInput` and
/// `NSTextInputClient` are different protocols.
///
/// NOTE: this file is UIKit-only and cannot be compiled without the iOS SDK;
/// it is validated in Xcode (see NEEDS-IOS-VALIDATION.md).
public final class BaseTerminalUIView: UIView {
    private let viewModel: TerminalAttachViewModel
    private let renderer: TerminalRenderer
    private let accessory = KeyboardAccessoryView()

    private var displayLink: CADisplayLink?

    // IME marked-text backing (accessed from the UITextInput conformance in
    // BaseTerminalUIView+TextInput.swift — must be internal, not fileprivate,
    // to be visible across files in the module).
    // Backing store for the IME composition range. Named markedRange (not
    // markedTextRange) to avoid colliding with UITextInput's required
    // markedTextRange: UITextRange? — that is a computed getter in the
    // conformance below.
    var markedRange: IndexedRange?
    var committedText = ""
    var markedText = ""
    public lazy var tokenizer: UITextInputTokenizer = UITextInputStringTokenizer(textInput: self)
    public weak var inputDelegate: UITextInputDelegate?

    // MARK: - Scroll (issue #984)

    /// Pure physics/state for two-finger scrollback scrolling: momentum, bounce,
    /// and the indicator thumb. UIKit glue below feeds it gestures + frame ticks.
    private var scroll = TerminalScrollController()
    /// Last touch point of a scroll gesture, reused as the surface position for
    /// forwarded mouse-wheel events (TUI apps) and momentum frames.
    private var lastScrollPoint: CGPoint = .zero
    /// Thin scroll-position indicator drawn on the trailing edge.
    private let scrollIndicator = UIView()
    private let scrollIndicatorInset: CGFloat = 4
    private var indicatorHideWork: DispatchWorkItem?
    /// Chevron shown when the viewport is scrolled up off the live bottom.
    private let jumpToBottomButton = UIButton(type: .system)
    private var jumpButtonVisible = false

    public init(viewModel: TerminalAttachViewModel, renderer: TerminalRenderer) {
        self.viewModel = viewModel
        self.renderer = renderer
        super.init(frame: .zero)
        layer.addSublayer(renderer.layer)
        backgroundColor = .black
        isMultipleTouchEnabled = true
        accessory.delegate = self
        setupScrollUI()
        setupGestures()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

    deinit {
        // The weak-proxy display link (see startDisplayLink) breaks the run-loop
        // → link → view retain cycle, so this can run; tear the link down here as
        // a backstop in case the integrator never calls `stop()`.
        displayLink?.invalidate()
    }

    // MARK: - First responder / accessory

    public override var canBecomeFirstResponder: Bool { true }
    public override var inputAccessoryView: UIView? { accessory }

    // Toggle the *software keyboard* without resigning first responder: a
    // zero-height inputView collapses the keyboard while the accessory row (and
    // its show/hide button) stay on screen; nil restores the default keyboard.
    private let hiddenKeyboardStub = UIView(frame: .zero)
    private(set) var softKeyboardHidden = false
    public override var inputView: UIView? {
        softKeyboardHidden ? hiddenKeyboardStub : nil
    }

    @discardableResult
    public override func becomeFirstResponder() -> Bool {
        let became = super.becomeFirstResponder()
        if became { startDisplayLink() }
        return became
    }

    // MARK: - Lifecycle

    public func start() async {
        startDisplayLink()
        await viewModel.attach()
        _ = becomeFirstResponder()
    }

    public func stop() async {
        stopDisplayLink()
        await viewModel.detach()
    }

    private func startDisplayLink() {
        guard displayLink == nil else { return }
        // Target the weak proxy, not `self`: CADisplayLink retains its target and
        // the run loop retains the link, so a direct target would be a permanent
        // retain cycle that also prevents `deinit` from ever running. The proxy
        // holds the view weakly, so the view can deallocate and its `deinit`
        // tears the link down even if the integrator never calls `stop()`.
        let link = CADisplayLink(target: DisplayLinkProxy(self), selector: #selector(DisplayLinkProxy.tick))
        link.preferredFramesPerSecond = 60
        link.add(to: .main, forMode: .common)
        displayLink = link
    }

    private func stopDisplayLink() {
        displayLink?.invalidate()
        displayLink = nil
    }

    @objc fileprivate func tick() {
        if scroll.isSettling, let link = displayLink {
            // CADisplayLink's frame interval; clamp so a stall doesn't fling.
            let dt = CGFloat(min(0.05, max(0, link.targetTimestamp - link.timestamp)))
            stepScrollPhysics(dt: dt)
        }
        refreshJumpButton()
        renderer.renderIfNeeded()
    }

    // MARK: - Layout + resize

    public override func layoutSubviews() {
        super.layoutSubviews()
        let scale = window?.screen.scale ?? UIScreen.main.scale
        renderer.layout(bounds: bounds, scale: scale)
        // Re-apply any live overscroll transform after the renderer resets its
        // layer frame to `bounds`, so a resize mid-bounce doesn't snap.
        applyOverscrollTransform()
        layoutJumpToBottomButton()
        updateScrollIndicator()
        sendResizeFromBounds()
    }

    private func sendResizeFromBounds() {
        let cell = renderer.cellSize
        guard cell.width > 0, cell.height > 0 else { return }
        let cols = UInt16(max(1, Int(bounds.width / cell.width)))
        let rows = UInt16(max(1, Int(bounds.height / cell.height)))
        let scale = window?.screen.scale ?? UIScreen.main.scale
        viewModel.resize(
            cols: cols, rows: rows,
            cellWidth: UInt32(cell.width * scale),
            cellHeight: UInt32(cell.height * scale)
        )
        renderer.setNeedsRender()
    }

    // MARK: - Hardware key events

    public override func pressesBegan(_ presses: Set<UIPress>, with event: UIPressesEvent?) {
        var handled = false
        for press in presses {
            guard let key = press.key else { continue }
            if let stroke = UIKeyMapping.stroke(for: key, action: .press) {
                viewModel.send(key: stroke)
                handled = true
            }
        }
        if !handled { super.pressesBegan(presses, with: event) }
    }

    public override func pressesEnded(_ presses: Set<UIPress>, with event: UIPressesEvent?) {
        super.pressesEnded(presses, with: event)
    }

    // MARK: - Gestures (selection + scroll)

    private func setupGestures() {
        let tap = UITapGestureRecognizer(target: self, action: #selector(handleTap))
        addGestureRecognizer(tap)

        // Two-finger drag scrolls the scrollback (issue #984). Single-finger is
        // reserved for tap-to-focus and long-press selection, matching the iOS
        // convention where one finger interacts with content and two scroll it.
        let pan = UIPanGestureRecognizer(target: self, action: #selector(handleScroll))
        pan.minimumNumberOfTouches = 2
        pan.maximumNumberOfTouches = 2
        addGestureRecognizer(pan)

        let longPress = UILongPressGestureRecognizer(target: self, action: #selector(handleSelection))
        longPress.minimumPressDuration = 0.3
        addGestureRecognizer(longPress)
    }

    @objc private func handleTap() {
        if !isFirstResponder { _ = becomeFirstResponder() }
    }

    // MARK: - Scroll gesture + physics (issue #984)

    @objc private func handleScroll(_ gesture: UIPanGestureRecognizer) {
        let cell = renderer.cellSize
        guard cell.height > 0 else { return }
        lastScrollPoint = gesture.location(in: self)
        switch gesture.state {
        case .began:
            scroll.cellHeight = cell.height
            scroll.beginDrag()
            showScrollIndicator()
        case .changed:
            let dy = gesture.translation(in: self).y
            gesture.setTranslation(.zero, in: self)
            let rows = scroll.drag(translationDelta: dy)
            // One scrollbar read per frame (it is flagged expensive): reuse it for
            // boundary detection and the indicator.
            let metrics = viewModel.core.scrollMetrics()
            if rows != 0 {
                scroll.absorbOverscroll(rows: applyScroll(rows, at: lastScrollPoint, metrics: metrics))
            }
            applyOverscrollTransform()
            updateScrollIndicator(metrics: metrics)
        case .ended, .cancelled, .failed:
            scroll.endDrag(velocityY: gesture.velocity(in: self).y)
            // Momentum / spring continues on the display-link `tick`; if it
            // settled immediately, fade the indicator out.
            if !scroll.isSettling { scheduleIndicatorHide() }
        default:
            break
        }
    }

    /// Advance momentum/spring one frame: apply any row delta (or forward it as
    /// mouse-wheel to a TUI), feed back boundary overscroll, and update the
    /// visual bounce + indicator.
    private func stepScrollPhysics(dt: CGFloat) {
        let rows = scroll.tick(dt: dt)
        // Skip the (expensive) scrollbar read entirely while purely springing:
        // the bounce moves no rows, so there is no boundary to detect and the
        // indicator's thumb position hasn't changed.
        if rows != 0 {
            let metrics = viewModel.core.scrollMetrics()
            scroll.absorbOverscroll(rows: applyScroll(rows, at: lastScrollPoint, metrics: metrics))
            updateScrollIndicator(metrics: metrics)
        }
        applyOverscrollTransform()
        if !scroll.isSettling { scheduleIndicatorHide() }
    }

    /// Apply a viewport row delta. For a mouse-tracking program (a TUI like
    /// `claude`, vim, tmux) the scroll is forwarded as wheel events so the
    /// program scrolls its own content; otherwise it moves the local scrollback
    /// viewport. Returns the rows the core refused at a boundary (0 for a TUI,
    /// which manages its own history), for rubber-band overscroll.
    @discardableResult
    private func applyScroll(_ rows: Int, at point: CGPoint, metrics: ScrollMetrics) -> Int {
        guard rows != 0 else { return 0 }
        if viewModel.core.isMouseTrackingActive {
            forwardWheel(rows: rows, at: point)
            return 0
        }
        viewModel.core.scrollViewport(byRows: rows)
        renderer.setNeedsRender()
        // Estimate how many rows the core actually applied from the single
        // metrics read: it clamps the viewport offset into [0, total-len], so
        // anything past that is refused and becomes rubber-band overscroll. This
        // avoids a second per-frame scrollbar read (the contract flags it as
        // expensive at an arbitrary pin).
        let maxOffset = max(0, metrics.total - metrics.len)
        let applied = min(maxOffset, max(0, metrics.offset + rows)) - metrics.offset
        return rows - applied
    }

    /// Forward a row delta to a mouse-tracking program as wheel events.
    private func forwardWheel(rows: Int, at point: CGPoint) {
        let cell = renderer.cellSize
        guard cell.width > 0, cell.height > 0 else { return }
        let scale = window?.screen.scale ?? UIScreen.main.scale
        let chunks = viewModel.core.encodeScrollWheel(
            ticks: rows,
            surfaceX: Double(point.x * scale), surfaceY: Double(point.y * scale),
            screenWidth: UInt32(bounds.width * scale), screenHeight: UInt32(bounds.height * scale),
            cellWidth: UInt32(cell.width * scale), cellHeight: UInt32(cell.height * scale))
        for chunk in chunks { viewModel.sendRaw(chunk) }
    }

    private func applyOverscrollTransform() {
        let ty = scroll.contentTranslation(viewportHeight: bounds.height)
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        renderer.layer.transform = CATransform3DMakeTranslation(0, ty, 0)
        CATransaction.commit()
    }

    // MARK: - Scroll indicator + jump-to-bottom

    private func setupScrollUI() {
        scrollIndicator.backgroundColor = UIColor.white.withAlphaComponent(0.35)
        scrollIndicator.layer.cornerRadius = 1.5
        scrollIndicator.alpha = 0
        scrollIndicator.isUserInteractionEnabled = false
        addSubview(scrollIndicator)

        let config = UIImage.SymbolConfiguration(pointSize: 20, weight: .semibold)
        jumpToBottomButton.setImage(UIImage(systemName: "chevron.down", withConfiguration: config), for: .normal)
        jumpToBottomButton.tintColor = .white
        jumpToBottomButton.backgroundColor = UIColor.black.withAlphaComponent(0.55)
        jumpToBottomButton.layer.cornerRadius = 20
        jumpToBottomButton.alpha = 0
        jumpToBottomButton.isUserInteractionEnabled = false
        jumpToBottomButton.addTarget(self, action: #selector(handleJumpToBottom), for: .touchUpInside)
        addSubview(jumpToBottomButton)
    }

    private func layoutJumpToBottomButton() {
        let size: CGFloat = 40
        let margin: CGFloat = 16
        jumpToBottomButton.frame = CGRect(
            x: bounds.width - size - margin,
            y: bounds.height - size - margin,
            width: size, height: size)
    }

    private func showScrollIndicator() {
        // A mouse-tracking program (a TUI) scrolls its own content; graith's local
        // scrollback indicator would be misleading, so don't show it there.
        guard !viewModel.core.isMouseTrackingActive else { return }
        indicatorHideWork?.cancel()
        indicatorHideWork = nil
        updateScrollIndicator()
        UIView.animate(withDuration: 0.1) { self.scrollIndicator.alpha = 1 }
    }

    private func scheduleIndicatorHide() {
        indicatorHideWork?.cancel()
        let work = DispatchWorkItem { [weak self] in
            UIView.animate(withDuration: 0.4) { self?.scrollIndicator.alpha = 0 }
        }
        indicatorHideWork = work
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.0, execute: work)
    }

    private func updateScrollIndicator(metrics: ScrollMetrics? = nil) {
        let track = bounds.height - 2 * scrollIndicatorInset
        // Reuse a caller-supplied read where possible; the scrollbar read is
        // flagged expensive. Never show the local indicator over a TUI.
        guard !viewModel.core.isMouseTrackingActive else {
            scrollIndicator.isHidden = true
            return
        }
        let m = metrics ?? viewModel.core.scrollMetrics()
        guard track > 0,
              let thumb = TerminalScrollController.thumb(metrics: m, trackLength: track) else {
            // Nothing to scroll (empty history, or a TUI's alt-screen).
            scrollIndicator.isHidden = true
            return
        }
        scrollIndicator.isHidden = false
        let width: CGFloat = 3
        scrollIndicator.frame = CGRect(
            x: bounds.width - width - 2,
            y: scrollIndicatorInset + thumb.offset,
            width: width, height: thumb.length)
        scrollIndicator.layer.cornerRadius = width / 2
    }

    /// Show the jump-to-bottom chevron only when scrolled up off the live bottom
    /// and not driving a mouse-tracking program (which owns its own scrollback).
    /// Called every frame but no-ops unless the visible state actually changes.
    private func refreshJumpButton() {
        let shouldShow = !viewModel.core.isViewportAtBottom && !viewModel.core.isMouseTrackingActive
        guard shouldShow != jumpButtonVisible else { return }
        jumpButtonVisible = shouldShow
        jumpToBottomButton.isUserInteractionEnabled = shouldShow
        UIView.animate(withDuration: 0.2) { self.jumpToBottomButton.alpha = shouldShow ? 1 : 0 }
    }

    @objc private func handleJumpToBottom() {
        scroll.stop()
        viewModel.core.scrollToBottom()
        applyOverscrollTransform()
        renderer.setNeedsRender()
        refreshJumpButton()
        updateScrollIndicator()
        scheduleIndicatorHide()
    }

    @objc private func handleSelection(_ gesture: UILongPressGestureRecognizer) {
        let cell = renderer.cellSize
        guard cell.width > 0, cell.height > 0 else { return }
        let point = gesture.location(in: self)
        let scale = window?.screen.scale ?? UIScreen.main.scale
        let col = UInt16(max(0, Int(point.x / cell.width)))
        let row = UInt32(max(0, Int(point.y / cell.height)))
        let ref = ViewportCell(col: col, row: row)
        let surfaceX = Double(point.x * scale)
        let surfaceY = Double(point.y * scale)

        switch gesture.state {
        case .began:
            viewModel.core.beginSelection(at: ref, surfaceX: surfaceX, surfaceY: surfaceY,
                                          timeNs: DispatchTime.now().uptimeNanoseconds)
        case .changed:
            viewModel.core.dragSelection(to: ref, surfaceX: surfaceX, surfaceY: surfaceY,
                                         columns: UInt32(bounds.width / cell.width),
                                         cellWidth: UInt32(cell.width * scale),
                                         screenHeight: UInt32(bounds.height * scale))
        case .ended, .cancelled:
            viewModel.core.endSelection(at: ref)
            // Always present the edit menu on long-press: with a selection it
            // offers Copy (+ Paste); a stationary long-press selects nothing, so
            // it offers Paste alone — the standard iOS long-press-to-paste
            // gesture (canPerformAction filters the items).
            let selected = viewModel.core.selectedText()
            showEditMenu(selectedText: (selected?.isEmpty ?? true) ? nil : selected, at: point)
        default:
            break
        }
        renderer.setNeedsRender()
    }

    private func showEditMenu(selectedText: String?, at point: CGPoint) {
        becomeFirstResponder()
        pendingSelectedText = selectedText
        if #available(iOS 16.0, *) {
            UIMenuController.shared.showMenu(from: self, rect: CGRect(origin: point, size: .zero))
        }
    }

    fileprivate var pendingSelectedText: String?

    // MARK: - Copy / paste

    public override func copy(_ sender: Any?) {
        if let text = viewModel.core.selectedText() ?? pendingSelectedText {
            UIPasteboard.general.string = text
        }
        viewModel.core.clearSelection()
        renderer.setNeedsRender()
    }

    public override func paste(_ sender: Any?) {
        if let text = UIPasteboard.general.string {
            viewModel.paste(text)
        }
    }

    public override func canPerformAction(_ action: Selector, withSender sender: Any?) -> Bool {
        switch action {
        case #selector(copy(_:)):
            return viewModel.core.selectedText() != nil || pendingSelectedText != nil
        case #selector(paste(_:)):
            return UIPasteboard.general.hasStrings
        default:
            return super.canPerformAction(action, withSender: sender)
        }
    }
}

/// Weak forwarder for `CADisplayLink` so the link (and the run loop) don't retain
/// the terminal view. See `BaseTerminalUIView.startDisplayLink`.
private final class DisplayLinkProxy {
    private weak var view: BaseTerminalUIView?
    init(_ view: BaseTerminalUIView) { self.view = view }
    @objc func tick() { view?.tick() }
}

// MARK: - UIKeyInput

extension BaseTerminalUIView: UIKeyInput {
    public var hasText: Bool { true }

    public func insertText(_ text: String) {
        // Enter arrives here as "\n" on some keyboards; normalise to the
        // logical enter key so the encoder applies terminal rules.
        if text == "\n" {
            viewModel.send(key: TerminalKeyStroke(key: .enter, modifiers: accessory.takeStickyModifiers()))
            return
        }
        let mods = accessory.takeStickyModifiers()
        viewModel.send(key: TerminalKeyStroke(key: .character(text), modifiers: mods))
    }

    public func deleteBackward() {
        viewModel.send(key: TerminalKeyStroke(key: .backspace))
    }
}

// The on-screen key-accessory row routes esc/ctrl/arrows/tab etc. through the
// same key-encoder path as the hardware keyboard.
extension BaseTerminalUIView: KeyboardAccessoryDelegate {
    func accessory(_ view: KeyboardAccessoryView, didPress key: TerminalKey, modifiers: TerminalModifiers) {
        viewModel.send(key: TerminalKeyStroke(key: key, modifiers: modifiers))
    }

    func accessory(_ view: KeyboardAccessoryView, stickyModifiersChanged modifiers: TerminalModifiers) {
        // Sticky modifiers live on the accessory (consumed via
        // takeStickyModifiers on the next keystroke); nothing to persist here.
    }

    func accessoryDidRequestDismiss(_ view: KeyboardAccessoryView) {
        // Toggle the software keyboard while staying first responder, so the
        // accessory row (and this button) remain on screen to bring it back.
        softKeyboardHidden.toggle()
        reloadInputViews()
    }
}
#endif
