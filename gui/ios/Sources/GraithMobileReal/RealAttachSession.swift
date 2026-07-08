import Foundation
import GraithClientAPI
import GraithProtocol

/// The real `TerminalAttachSession`, wrapping the shared `AttachSession`.
///
/// The boundary's `send`/`resize` are non-throwing; the underlying calls throw
/// (the connection may have dropped), so failures are swallowed here — the
/// `output` stream finishing is what signals detach to
/// `TerminalAttachViewModel`, which drives the reattach UX.
public actor RealAttachSession: TerminalAttachSession {
    private let inner: AttachSession

    public init(inner: AttachSession) {
        self.inner = inner
    }

    /// `AttachSession` is `Sendable` and its `output`/`session` are immutable, so
    /// these can be read without hopping onto the actor. `output` finishes on
    /// detach / EOF / kick (confirmed with apple-macos).
    public nonisolated var output: AsyncStream<Data> { inner.output }
    public nonisolated var sessionID: String { inner.session.id }

    public func send(_ data: Data) async {
        try? await inner.send(data)
    }

    public func resize(cols: UInt16, rows: UInt16) async {
        try? await inner.resize(cols: cols, rows: rows)
    }

    public func detach() async {
        await inner.detach()
    }
}
