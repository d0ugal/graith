import Foundation
import GraithSessionKit

/// A mock attach session. Echoes sent bytes back on the output stream (so the
/// UIKit terminal view can be exercised without a daemon) and records resizes.
/// `TerminalAttachSession` refines `Actor`, so `output` is actor-isolated and
/// callers reach it with `await session.output`.
public actor MockAttachSession: TerminalAttachSession {
    public let sessionID: String
    public let output: AsyncStream<Data>

    private let continuation: AsyncStream<Data>.Continuation
    public private(set) var lastResize: (cols: UInt16, rows: UInt16)?
    public private(set) var sentBytes = Data()
    public private(set) var detached = false

    public init(sessionID: String) {
        self.sessionID = sessionID
        var cont: AsyncStream<Data>.Continuation!
        self.output = AsyncStream { cont = $0 }
        self.continuation = cont
        // Prime the stream with a banner so previews render something.
        cont.yield(Data("bonnie terminal — mock attach to \(sessionID)\r\n$ ".utf8))
    }

    public func send(_ data: Data) async {
        sentBytes.append(data)
        continuation.yield(data) // echo, so callers see round-trip bytes
    }

    public func resize(cols: UInt16, rows: UInt16) async {
        lastResize = (cols, rows)
    }

    public func detach() async {
        detached = true
        continuation.finish()
    }
}
