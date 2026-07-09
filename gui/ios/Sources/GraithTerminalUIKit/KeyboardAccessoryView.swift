#if canImport(UIKit)
import UIKit
import GraithClientAPI

/// Delegate for the on-screen key row. `sticky` reports the current sticky
/// modifier set (so the terminal view applies them to the next character), and
/// `didPressSpecial` fires for esc/tab/arrows/etc.
protocol KeyboardAccessoryDelegate: AnyObject {
    func accessory(_ view: KeyboardAccessoryView, didPress key: TerminalKey, modifiers: TerminalModifiers)
    func accessory(_ view: KeyboardAccessoryView, stickyModifiersChanged modifiers: TerminalModifiers)
    /// The user tapped the dismiss-keyboard button; the terminal should resign
    /// first responder (tap the terminal again to bring the keyboard back).
    func accessoryDidRequestDismiss(_ view: KeyboardAccessoryView)
}

/// The input-accessory bar above the soft keyboard (Blink / a-Shell style),
/// supplying keys iOS soft keyboards lack: esc, ctrl, alt, tab, arrows —
/// plus **sticky** ctrl/alt that latch for the next keystroke (design §C.3).
final class KeyboardAccessoryView: UIView {
    weak var delegate: KeyboardAccessoryDelegate?

    /// Currently-latched sticky modifiers (ctrl / alt).
    private(set) var stickyModifiers: TerminalModifiers = [] {
        didSet {
            updateStickyButtons()
            delegate?.accessory(self, stickyModifiersChanged: stickyModifiers)
        }
    }

    private var ctrlButton: UIButton!
    private var altButton: UIButton!
    private var stack: UIStackView!

    // Space-key drag → arrow keys (issue #979). The tracker is the pure state
    // machine; the recognizer feeds it translations and it decides which arrows
    // (if any) to emit. `spaceDragStart` anchors the gesture so translations are
    // measured from the touch-down point regardless of where on the key it began.
    private var spaceTracker = SpaceDragTracker()
    private var spaceDragStart: CGPoint = .zero
    private let arrowHaptics = UIImpactFeedbackGenerator(style: .light)

    init() {
        super.init(frame: CGRect(x: 0, y: 0, width: 0, height: 44))
        autoresizingMask = .flexibleWidth
        backgroundColor = .secondarySystemBackground
        buildButtons()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

    private func buildButtons() {
        ctrlButton = stickyButton(title: "ctrl", modifier: .control)
        altButton = stickyButton(title: "alt", modifier: .option)

        let items: [UIView] = [
            keyButton(title: "esc", key: .escape),
            ctrlButton,
            altButton,
            keyButton(title: "tab", key: .tab),
            spaceKey(),
            keyButton(title: "↑", key: .arrowUp),
            keyButton(title: "↓", key: .arrowDown),
            keyButton(title: "←", key: .arrowLeft),
            keyButton(title: "→", key: .arrowRight),
        ]

        let scroll = UIScrollView()
        scroll.showsHorizontalScrollIndicator = false
        scroll.translatesAutoresizingMaskIntoConstraints = false

        stack = UIStackView(arrangedSubviews: items)
        stack.axis = .horizontal
        stack.spacing = 8
        stack.alignment = .center
        stack.translatesAutoresizingMaskIntoConstraints = false
        stack.isLayoutMarginsRelativeArrangement = true
        stack.layoutMargins = UIEdgeInsets(top: 4, left: 8, bottom: 4, right: 8)

        scroll.addSubview(stack)
        addSubview(scroll)

        // A fixed dismiss-keyboard button pinned to the right, outside the
        // scrolling key row so it's always reachable.
        let dismiss = dismissButton()
        dismiss.translatesAutoresizingMaskIntoConstraints = false
        addSubview(dismiss)

        NSLayoutConstraint.activate([
            dismiss.trailingAnchor.constraint(equalTo: trailingAnchor, constant: -8),
            dismiss.centerYAnchor.constraint(equalTo: centerYAnchor),
            scroll.leadingAnchor.constraint(equalTo: leadingAnchor),
            scroll.trailingAnchor.constraint(equalTo: dismiss.leadingAnchor, constant: -4),
            scroll.topAnchor.constraint(equalTo: topAnchor),
            scroll.bottomAnchor.constraint(equalTo: bottomAnchor),
            stack.leadingAnchor.constraint(equalTo: scroll.contentLayoutGuide.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: scroll.contentLayoutGuide.trailingAnchor),
            stack.topAnchor.constraint(equalTo: scroll.contentLayoutGuide.topAnchor),
            stack.bottomAnchor.constraint(equalTo: scroll.contentLayoutGuide.bottomAnchor),
            stack.heightAnchor.constraint(equalTo: scroll.frameLayoutGuide.heightAnchor),
        ])
    }

    // MARK: - Button factories

    private func keyButton(title: String, key: TerminalKey) -> UIButton {
        let button = makeButton(title: title)
        button.addAction(UIAction { [weak self] _ in
            guard let self else { return }
            let mods = self.stickyModifiers
            self.delegate?.accessory(self, didPress: key, modifiers: mods)
            // One-shot modifiers clear after use.
            self.stickyModifiers = []
        }, for: .touchUpInside)
        return button
    }

    /// A wider "space" key that doubles as an arrow-key trackpad (issue #979):
    /// a plain tap types a space; holding and dragging emits arrow keys in the
    /// drag direction (one per threshold of travel) with light haptic feedback,
    /// and suppresses the space so a drag never also types a character.
    ///
    /// A single `UILongPressGestureRecognizer` with `minimumPressDuration = 0`
    /// handles both: it fires on touch-down (`.began`), reports finger movement
    /// (`.changed`), and its `.ended` tells us whether to send a space. Because
    /// the recognizer owns the touch we don't also wire `.touchUpInside`, so tap
    /// and drag can't both fire.
    private func spaceKey() -> UIView {
        let key = makeButton(title: "␣ space")
        key.contentEdgeInsets = UIEdgeInsets(top: 6, left: 28, bottom: 6, right: 28)
        key.accessibilityLabel = "Space (drag for arrow keys)"

        let drag = UILongPressGestureRecognizer(target: self, action: #selector(handleSpaceDrag))
        drag.minimumPressDuration = 0
        key.addGestureRecognizer(drag)
        return key
    }

    @objc private func handleSpaceDrag(_ gesture: UILongPressGestureRecognizer) {
        guard let view = gesture.view else { return }
        let point = gesture.location(in: view)
        switch gesture.state {
        case .began:
            spaceDragStart = point
            spaceTracker.begin()
            arrowHaptics.prepare()
            view.backgroundColor = .systemBlue
        case .changed:
            let translation = CGPoint(x: point.x - spaceDragStart.x,
                                      y: point.y - spaceDragStart.y)
            for key in spaceTracker.update(translation: translation) {
                delegate?.accessory(self, didPress: key, modifiers: [])
                arrowHaptics.impactOccurred()
                arrowHaptics.prepare()
            }
        case .ended:
            view.backgroundColor = .tertiarySystemBackground
            // A drag that moved an arrow is navigation only — no space. A plain
            // tap (no arrow emitted) types a space, consuming any sticky modifier
            // like the other keys do.
            if !spaceTracker.didEmit {
                let mods = stickyModifiers
                delegate?.accessory(self, didPress: .character(" "), modifiers: mods)
                stickyModifiers = []
            }
        case .cancelled, .failed:
            view.backgroundColor = .tertiarySystemBackground
        default:
            break
        }
    }

    private func stickyButton(title: String, modifier: TerminalModifiers) -> UIButton {
        let button = makeButton(title: title)
        button.addAction(UIAction { [weak self] _ in
            guard let self else { return }
            if self.stickyModifiers.contains(modifier) {
                self.stickyModifiers.remove(modifier)
            } else {
                self.stickyModifiers.insert(modifier)
            }
        }, for: .touchUpInside)
        return button
    }

    private func makeButton(title: String) -> UIButton {
        let button = UIButton(type: .system)
        button.setTitle(title, for: .normal)
        button.titleLabel?.font = .monospacedSystemFont(ofSize: 15, weight: .medium)
        button.backgroundColor = .tertiarySystemBackground
        button.layer.cornerRadius = 6
        button.contentEdgeInsets = UIEdgeInsets(top: 6, left: 12, bottom: 6, right: 12)
        return button
    }

    private func dismissButton() -> UIButton {
        let button = UIButton(type: .system)
        button.setImage(UIImage(systemName: "keyboard.chevron.compact.down"), for: .normal)
        button.tintColor = .label
        button.backgroundColor = .tertiarySystemBackground
        button.layer.cornerRadius = 6
        button.contentEdgeInsets = UIEdgeInsets(top: 6, left: 12, bottom: 6, right: 12)
        button.addAction(UIAction { [weak self] _ in
            guard let self else { return }
            self.delegate?.accessoryDidRequestDismiss(self)
        }, for: .touchUpInside)
        return button
    }

    private func updateStickyButtons() {
        ctrlButton.backgroundColor = stickyModifiers.contains(.control) ? .systemBlue : .tertiarySystemBackground
        ctrlButton.setTitleColor(stickyModifiers.contains(.control) ? .white : .label, for: .normal)
        altButton.backgroundColor = stickyModifiers.contains(.option) ? .systemBlue : .tertiarySystemBackground
        altButton.setTitleColor(stickyModifiers.contains(.option) ? .white : .label, for: .normal)
    }

    /// Consume + clear any latched sticky modifiers (called by the terminal view
    /// after it applies them to a typed character).
    func takeStickyModifiers() -> TerminalModifiers {
        let mods = stickyModifiers
        if !mods.isEmpty { stickyModifiers = [] }
        return mods
    }
}
#endif
