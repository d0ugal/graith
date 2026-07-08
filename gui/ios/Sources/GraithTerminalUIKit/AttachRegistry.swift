import Foundation

/// Enforces graith's **single-attach-per-session** rule on the client side so
/// an iPad split-view / multi-window scene can't attach the same session in two
/// panes at once (design "Security & Constraints": the two-pane UI must not show
/// the same session twice — it would fight itself over the one attach, and each
/// attach kicks the other).
///
/// Attach identity is the composite `host/session` (session IDs are per-daemon).
@MainActor
public final class AttachRegistry: ObservableObject {
    public static let shared = AttachRegistry()

    /// Currently-attached `host/session` keys.
    @Published public private(set) var attached: Set<String> = []

    public init() {}

    private func key(host: String, session: String) -> String { "\(host)/\(session)" }

    /// Whether this session is already attached in another pane/window.
    public func isAttachedElsewhere(host: String, session: String) -> Bool {
        attached.contains(key(host: host, session: session))
    }

    /// Try to claim the single attach slot for a session. Returns false if it is
    /// already claimed (the caller must refuse to open a second attach).
    @discardableResult
    public func claim(host: String, session: String) -> Bool {
        let k = key(host: host, session: session)
        if attached.contains(k) { return false }
        attached.insert(k)
        return true
    }

    /// Release the attach slot (on detach / view teardown).
    public func release(host: String, session: String) {
        attached.remove(key(host: host, session: session))
    }
}
