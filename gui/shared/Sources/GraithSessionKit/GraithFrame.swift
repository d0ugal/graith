import Foundation

/// The graith wire frame: a 1-byte channel + big-endian uint32 length + payload,
/// mirroring `internal/protocol/frame.go` exactly. Channels: `0x00` control
/// (JSON envelope), `0x01` raw PTY data, `0x02` MCP. `MaxPayload` is 4 MiB.
///
/// This is a reference codec so the framing can be validated here and reused by
/// the transport; the macOS `GraithProtocolClient` may adopt it or keep its own
/// `FrameReader`/`FrameWriter` — either way both must produce identical bytes.
public struct GraithFrame: Equatable, Sendable {
    public static let channelControl: UInt8 = 0x00
    public static let channelData: UInt8 = 0x01
    public static let channelMCP: UInt8 = 0x02
    public static let maxPayload = 4 * 1024 * 1024
    public static let headerSize = 5

    public let channel: UInt8
    public let payload: Data

    public init(channel: UInt8, payload: Data) {
        self.channel = channel
        self.payload = payload
    }

    /// Encode to `[channel][BE uint32 length][payload]`.
    public func encoded() -> Data {
        precondition(payload.count <= Self.maxPayload, "payload exceeds MaxPayload")
        var out = Data(capacity: Self.headerSize + payload.count)
        out.append(channel)
        let len = UInt32(payload.count)
        out.append(UInt8((len >> 24) & 0xFF))
        out.append(UInt8((len >> 16) & 0xFF))
        out.append(UInt8((len >> 8) & 0xFF))
        out.append(UInt8(len & 0xFF))
        out.append(payload)
        return out
    }

    /// Decode a single frame from the front of `buffer`, consuming its bytes.
    /// Returns nil if `buffer` does not yet hold a complete frame (leaving it
    /// untouched) so a caller can accumulate more bytes and retry.
    public static func decode(from buffer: inout Data) -> GraithFrame? {
        guard buffer.count >= headerSize else { return nil }
        // Index into the Data safely regardless of slice start index.
        let bytes = [UInt8](buffer)
        let channel = bytes[0]
        let length = Int(bytes[1]) << 24 | Int(bytes[2]) << 16 | Int(bytes[3]) << 8 | Int(bytes[4])
        guard length <= maxPayload else { return nil }
        guard bytes.count >= headerSize + length else { return nil }
        let payload = Data(bytes[headerSize..<(headerSize + length)])
        buffer = Data(bytes[(headerSize + length)...])
        return GraithFrame(channel: channel, payload: payload)
    }
}
