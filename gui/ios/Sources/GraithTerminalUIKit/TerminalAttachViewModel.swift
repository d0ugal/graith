import Foundation
import Combine
import GraithClientAPI

/// Drives a live interactive attach (Task 20), independent of UIKit so it can be
/// unit-tested off-device:
///
///   - opens the attach connection via the host client,
///   - streams channel-0x01 output into the shared VT core (`feedOutput`),
///   - encodes key strokes / text and sends them back on channel 0x01,
///   - sends `resize` control messages when the grid changes,
///   - surfaces detach (data-channel EOF) and supports reattach,
///   - honours the single-attach guard via `AttachRegistry`.
///
/// The UIKit `BaseTerminalUIView` owns one of these and forwards input to it.
@MainActor
public final class TerminalAttachViewModel: ObservableObject {
    public enum Phase: Equatable, Sendable {
        case idle
        case connecting
        case attached
        /// The data channel ended (session stopped, or attach kicked elsewhere).
        case detached(String)
        case failed(String)
        /// Blocked because the session is already attached in another pane.
        case attachedElsewhere
    }

    @Published public private(set) var phase: Phase = .idle
    /// Set true while the app is backgrounded so the UI can show a paused state.
    @Published public private(set) var backgrounded = false

    public let hostID: String
    public let sessionID: String
    public let core: TerminalCoreDriving

    private let client: any GraithHostClient
    private let registry: AttachRegistry
    private var session: (any TerminalAttachSession)?
    private var readTask: Task<Void, Never>?
    private var claimed = false

    /// Current grid geometry, so a resize only fires on an actual change.
    private var cols: UInt16 = 0
    private var rows: UInt16 = 0

    public init(
        hostID: String,
        sessionID: String,
        core: TerminalCoreDriving,
        client: any GraithHostClient,
        registry: AttachRegistry
    ) {
        self.hostID = hostID
        self.sessionID = sessionID
        self.core = core
        self.client = client
        self.registry = registry
    }

    // MARK: - Attach lifecycle

    /// Open the attach. Refuses (→ `.attachedElsewhere`) if the single-attach
    /// slot for this session is already claimed by another pane/window.
    public func attach() async {
        guard phase != .connecting, phase != .attached else { return }
        guard registry.claim(host: hostID, session: sessionID) else {
            phase = .attachedElsewhere
            return
        }
        claimed = true
        phase = .connecting
        do {
            let session = try await client.attach(sessionID: sessionID)
            self.session = session
            phase = .attached
            startReading(from: session)
        } catch {
            releaseClaim()
            phase = .failed(HostErrorText.describe(error))
        }
    }

    private func startReading(from session: any TerminalAttachSession) {
        readTask?.cancel()
        readTask = Task { [weak self] in
            guard let self else { return }
            let stream = await session.output
            for await chunk in stream {
                self.core.feedOutput(chunk)
            }
            // Stream finished ⇒ the daemon closed the data channel.
            if self.phase == .attached {
                self.phase = .detached("Session detached")
            }
            self.releaseClaim()
        }
    }

    /// Re-open the attach after a detach (or on returning to foreground).
    public func reattach() async {
        readTask?.cancel()
        session = nil
        phase = .idle
        await attach()
    }

    public func detach() async {
        readTask?.cancel()
        readTask = nil
        await session?.detach()
        session = nil
        releaseClaim()
        if case .attachedElsewhere = phase {} else { phase = .idle }
    }

    private func releaseClaim() {
        if claimed {
            registry.release(host: hostID, session: sessionID)
            claimed = false
        }
    }

    // MARK: - Input

    /// Encode + send a logical key stroke.
    public func send(key stroke: TerminalKeyStroke) {
        guard let data = core.encode(stroke) else { return }
        sendRaw(data)
    }

    /// Send already-committed text (IME commit, on-screen row characters).
    public func send(text: String) {
        guard !text.isEmpty else { return }
        send(key: TerminalKeyStroke(key: .character(text)))
    }

    /// Send a paste, applying bracketed-paste framing when the terminal wants it.
    public func paste(_ text: String) {
        guard !text.isEmpty else { return }
        if core.isBracketedPasteEnabled {
            sendRaw(Data("\u{1B}[200~".utf8))
            sendRaw(Data(text.utf8))
            sendRaw(Data("\u{1B}[201~".utf8))
        } else {
            sendRaw(Data(text.utf8))
        }
    }

    /// Send raw channel-0x01 bytes.
    public func sendRaw(_ data: Data) {
        guard let session else { return }
        Task { await session.send(data) }
    }

    // MARK: - Resize

    /// Update the grid and, on an actual change, send a `resize` control message
    /// so the remote PTY matches (design §C.3 — no local TIOCSWINSZ).
    public func resize(cols: UInt16, rows: UInt16, cellWidth: UInt32, cellHeight: UInt32) {
        guard cols > 0, rows > 0 else { return }
        core.resize(cols: cols, rows: rows, cellWidth: cellWidth, cellHeight: cellHeight)
        guard cols != self.cols || rows != self.rows else { return }
        self.cols = cols
        self.rows = rows
        if let session {
            Task { await session.resize(cols: cols, rows: rows) }
        }
    }

    // MARK: - Backgrounding (iOS suspends sockets when backgrounded)

    public func applicationDidEnterBackground() {
        backgrounded = true
    }

    /// On foreground, if the socket dropped while suspended, reattach.
    public func applicationWillEnterForeground() async {
        backgrounded = false
        switch phase {
        case .detached, .failed, .idle:
            await reattach()
        default:
            break
        }
    }
}

/// Small helper so this file needn't import `GraithMobileKit`.
enum HostErrorText {
    static func describe(_ error: Error) -> String {
        if let e = error as? GraithClientError {
            switch e {
            case .notPaired: return "Not paired."
            case .authenticationFailed(let r): return "Auth failed: \(r)"
            case .tlsPinMismatch: return "TLS key changed."
            case .tailnetUnreachable: return "Tailnet unreachable."
            case .daemon(let m): return m
            case .disconnected(let m): return "Disconnected: \(m)"
            case .decoding(let m): return "Bad reply: \(m)"
            }
        }
        return error.localizedDescription
    }
}
