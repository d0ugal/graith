import Foundation
@testable import GraithProtocol

/// A minimal in-process stand-in for the daemon side of the framed protocol,
/// driven manually by tests over an ``InMemoryByteStream``. It speaks exactly
/// what `internal/daemon/handler.go` speaks: 5-byte frames, channel 0x00
/// control envelopes, channel 0x01 data.
actor MockDaemon {
    private let stream: InMemoryByteStream
    private var decoder = FrameDecoder()
    private var pendingControl: [ControlEnvelope] = []
    private var pendingData: [Data] = []

    init(stream: InMemoryByteStream) {
        self.stream = stream
    }

    /// Block until the next control envelope arrives from the client.
    func readControl() async throws -> ControlEnvelope {
        while pendingControl.isEmpty {
            try await pump()
        }
        return pendingControl.removeFirst()
    }

    /// Block until the next data (channel 0x01) frame arrives.
    func readData() async throws -> Data {
        while pendingData.isEmpty {
            try await pump()
        }
        return pendingData.removeFirst()
    }

    func writeControl(_ type: String, _ payload: some Encodable) async throws {
        let env = try encodeControl(type, payload)
        try await writeFrame(channel: Channel.control, payload: env)
    }

    /// Write a control envelope that carries no payload wrapper — used for
    /// replies whose payload is any encodable value.
    func writeData(_ data: Data) async throws {
        try await writeFrame(channel: Channel.data, payload: data)
    }

    private func writeFrame(channel: UInt8, payload: Data) async throws {
        let frame = try encodeFrame(channel: channel, payload: payload)
        try await stream.send(frame)
    }

    private func pump() async throws {
        let chunk = try await stream.receive(maxLength: 64 * 1024)
        if chunk.isEmpty { throw FrameError.closed }
        decoder.append(chunk)
        while let frame = try decoder.next() {
            switch frame.channel {
            case Channel.control:
                pendingControl.append(try decodeControl(frame.payload))
            case Channel.data:
                pendingData.append(frame.payload)
            default:
                break
            }
        }
    }
}
