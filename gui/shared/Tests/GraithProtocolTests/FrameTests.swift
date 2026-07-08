import Foundation
import Testing
@testable import GraithProtocol

struct FrameTests {
    @Test func encodeDecodeRoundTrip() throws {
        let payload = Data("braw".utf8)
        let bytes = try encodeFrame(channel: Channel.control, payload: payload)

        #expect(bytes[0] == Channel.control)
        #expect(Array(bytes[1...4]) == [0, 0, 0, UInt8(payload.count)])

        var dec = FrameDecoder()
        dec.append(bytes)
        let frame = try dec.next()
        #expect(frame?.channel == Channel.control)
        #expect(frame?.payload == payload)
        #expect(try dec.next() == nil)
    }

    @Test func chunkedDelivery() throws {
        let payload = Data((0..<1000).map { UInt8($0 % 256) })
        let bytes = try encodeFrame(channel: Channel.data, payload: payload)

        var dec = FrameDecoder()
        var offset = bytes.startIndex
        while offset < bytes.endIndex {
            let end = min(offset + 7, bytes.endIndex)
            dec.append(Data(bytes[offset..<end]))
            offset = end
        }
        let frame = try dec.next()
        #expect(frame?.payload == payload)
        #expect(frame?.channel == Channel.data)
    }

    @Test func multipleFramesInOneBuffer() throws {
        var buf = Data()
        buf.append(try encodeFrame(channel: Channel.control, payload: Data("canny".utf8)))
        buf.append(try encodeFrame(channel: Channel.data, payload: Data("bide".utf8)))

        var dec = FrameDecoder()
        dec.append(buf)
        #expect(try dec.next()?.payload == Data("canny".utf8))
        #expect(try dec.next()?.payload == Data("bide".utf8))
        #expect(try dec.next() == nil)
    }

    @Test func emptyPayloadFrame() throws {
        let bytes = try encodeFrame(channel: Channel.control, payload: Data())
        #expect(bytes.count == 5)
        var dec = FrameDecoder()
        dec.append(bytes)
        #expect(try dec.next()?.payload.count == 0)
    }

    @Test func oversizePayloadRejectedOnEncode() {
        let big = Data(count: maxPayload + 1)
        #expect(throws: FrameError.payloadTooLarge(maxPayload + 1)) {
            try encodeFrame(channel: Channel.data, payload: big)
        }
    }

    @Test func oversizeLengthRejectedOnDecode() {
        var dec = FrameDecoder()
        var hdr = Data([Channel.data])
        let len = UInt32(maxPayload + 1)
        hdr.append(UInt8((len >> 24) & 0xFF))
        hdr.append(UInt8((len >> 16) & 0xFF))
        hdr.append(UInt8((len >> 8) & 0xFF))
        hdr.append(UInt8(len & 0xFF))
        dec.append(hdr)
        #expect(throws: (any Error).self) { try dec.next() }
    }
}
