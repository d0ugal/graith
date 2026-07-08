import Foundation

/// Channel identifiers for the graith framed protocol.
///
/// Mirrors `internal/protocol/frame.go`. The wire framing is:
/// `[channel:1][len:4 big-endian][payload:len]`.
public enum Channel {
    /// JSON control envelopes (`internal/protocol` `Envelope`).
    public static let control: UInt8 = 0x00
    /// Raw PTY bytes, both directions (terminal output and keystrokes).
    public static let data: UInt8 = 0x01
    /// MCP proxy channel — dynamically assigned by `mcp_connect`. The apps
    /// never open this; it is documented here only for completeness.
    public static let mcp: UInt8 = 0x02
}

/// Maximum payload size for a single frame (4 MiB), matching
/// `protocol.MaxPayload`. Frames larger than this are a protocol error.
public let maxPayload = 4 * 1024 * 1024

/// The fixed header size: 1 channel byte + 4 length bytes.
let frameHeaderSize = 5

/// A single decoded frame: a channel byte and its payload.
public struct Frame: Sendable, Equatable {
    public let channel: UInt8
    public let payload: Data

    public init(channel: UInt8, payload: Data) {
        self.channel = channel
        self.payload = payload
    }
}

/// Errors produced while encoding or decoding frames.
public enum FrameError: Error, Equatable {
    /// Payload exceeded ``maxPayload``.
    case payloadTooLarge(Int)
    /// The peer closed the stream cleanly (EOF) mid-frame or between frames.
    case closed
}

/// Encodes a frame to its wire bytes: `[channel][len BE][payload]`.
///
/// This is the exact inverse of `protocol.FrameWriter.WriteFrame`.
public func encodeFrame(channel: UInt8, payload: Data) throws -> Data {
    guard payload.count <= maxPayload else {
        throw FrameError.payloadTooLarge(payload.count)
    }

    var out = Data(capacity: frameHeaderSize + payload.count)
    out.append(channel)
    let len = UInt32(payload.count)
    out.append(UInt8((len >> 24) & 0xFF))
    out.append(UInt8((len >> 16) & 0xFF))
    out.append(UInt8((len >> 8) & 0xFF))
    out.append(UInt8(len & 0xFF))
    out.append(payload)
    return out
}

/// Incrementally decodes frames from a byte buffer.
///
/// Feed bytes with ``append(_:)`` as they arrive off the wire, then pull whole
/// frames with ``next()``. Partial frames stay buffered until complete. This
/// mirrors the read side of `protocol.FrameReader` but is pull-based so it can
/// sit on top of any chunked byte source (Network.framework, a POSIX socket,
/// or an in-memory pipe for tests).
public struct FrameDecoder {
    private var buffer = Data()

    public init() {}

    /// Append freshly-received bytes to the internal buffer.
    public mutating func append(_ data: Data) {
        buffer.append(data)
    }

    /// Return the next complete frame, or `nil` if not enough bytes have been
    /// buffered yet. Throws ``FrameError/payloadTooLarge(_:)`` if a frame
    /// header declares a length above ``maxPayload``.
    public mutating func next() throws -> Frame? {
        guard buffer.count >= frameHeaderSize else { return nil }

        // Data may not be zero-based after slicing; index via startIndex.
        let base = buffer.startIndex
        let channel = buffer[base]
        let b1 = UInt32(buffer[base + 1])
        let b2 = UInt32(buffer[base + 2])
        let b3 = UInt32(buffer[base + 3])
        let b4 = UInt32(buffer[base + 4])
        let length = Int((b1 << 24) | (b2 << 16) | (b3 << 8) | b4)

        guard length <= maxPayload else {
            throw FrameError.payloadTooLarge(length)
        }

        let total = frameHeaderSize + length
        guard buffer.count >= total else { return nil }

        let payloadStart = base + frameHeaderSize
        let payload = Data(buffer[payloadStart ..< payloadStart + length])

        // Drop the consumed bytes; re-base so indices stay simple.
        buffer.removeSubrange(base ..< base + total)

        return Frame(channel: channel, payload: payload)
    }
}
