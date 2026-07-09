#if canImport(UIKit)
import UIKit
import GraithClientAPI

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

    public init(viewModel: TerminalAttachViewModel, renderer: TerminalRenderer) {
        self.viewModel = viewModel
        self.renderer = renderer
        super.init(frame: .zero)
        layer.addSublayer(renderer.layer)
        backgroundColor = .black
        isMultipleTouchEnabled = true
        accessory.delegate = self
        setupGestures()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

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
        let link = CADisplayLink(target: self, selector: #selector(tick))
        link.preferredFramesPerSecond = 60
        link.add(to: .main, forMode: .common)
        displayLink = link
    }

    private func stopDisplayLink() {
        displayLink?.invalidate()
        displayLink = nil
    }

    @objc private func tick() {
        renderer.renderIfNeeded()
    }

    // MARK: - Layout + resize

    public override func layoutSubviews() {
        super.layoutSubviews()
        let scale = window?.screen.scale ?? UIScreen.main.scale
        renderer.layout(bounds: bounds, scale: scale)
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

        let pan = UIPanGestureRecognizer(target: self, action: #selector(handleScroll))
        pan.minimumNumberOfTouches = 1
        pan.maximumNumberOfTouches = 1
        addGestureRecognizer(pan)

        let longPress = UILongPressGestureRecognizer(target: self, action: #selector(handleSelection))
        longPress.minimumPressDuration = 0.3
        addGestureRecognizer(longPress)
    }

    @objc private func handleTap() {
        if !isFirstResponder { _ = becomeFirstResponder() }
    }

    private var scrollAccumulator: CGFloat = 0

    @objc private func handleScroll(_ gesture: UIPanGestureRecognizer) {
        let cell = renderer.cellSize
        guard cell.height > 0 else { return }
        switch gesture.state {
        case .began:
            scrollAccumulator = 0
        case .changed:
            let translation = gesture.translation(in: self).y
            scrollAccumulator += translation
            gesture.setTranslation(.zero, in: self)
            let rowDelta = Int(scrollAccumulator / cell.height)
            if rowDelta != 0 {
                // Drag down => scroll up into scrollback (negative viewport delta).
                viewModel.core.scrollViewport(byRows: -rowDelta)
                scrollAccumulator -= CGFloat(rowDelta) * cell.height
                renderer.setNeedsRender()
            }
        default:
            break
        }
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
