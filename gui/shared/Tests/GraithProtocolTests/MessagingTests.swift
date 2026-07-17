import Foundation
import Testing
@testable import GraithProtocol

/// Wire-shape + round-trip coverage for the inter-agent messaging RPCs the GUI
/// gained (issue #898): `msg_pub` (send), `msg_conversation` (inbox view), and
/// `msg_ack`. Each asserts the request type/payload the daemon (handler.go)
/// expects and decodes the reply the way the client does.
struct MessagingTests {
    /// `sendMessage` publishes to `inbox:<id>` and returns the `msg_published`
    /// message the daemon echoes back.
    @Test func sendMessageAddressesInboxAndReturnsPublished() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            _ = try await daemon.readControl() // handshake
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            let req = try await daemon.readControl()
            #expect(req.type == "msg_pub")
            let pub = try decodePayload(req, as: MsgPubMsg.self)
            #expect(pub.stream == "inbox:braw")
            #expect(pub.body == "wire up the bonnie feature")
            #expect(pub.senderName == "human")
            #expect(pub.noReply == nil)
            try await daemon.writeControl("msg_published", ConversationMessage(
                id: "msg_01", seq: 7, stream: "inbox:braw", senderID: "",
                senderName: "human", body: "wire up the bonnie feature",
                createdAt: "2026-07-14T00:00:00Z"))
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        let published = try await client.sendMessage(
            toSessionID: "braw", body: "wire up the bonnie feature", senderName: "human")
        #expect(published.id == "msg_01")
        #expect(published.seq == 7)
        #expect(published.body == "wire up the bonnie feature")

        _ = await server.result
        await client.close()
    }

    /// `conversation` sends `{session_id, limit?}` and decodes the
    /// `msg_conversation_list` reply into the message array.
    @Test func conversationReturnsMessages() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            _ = try await daemon.readControl() // handshake
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            let req = try await daemon.readControl()
            #expect(req.type == "msg_conversation")
            let conv = try decodePayload(req, as: MsgConversationMsg.self)
            #expect(conv.sessionID == "braw")
            #expect(conv.limit == 50)
            try await daemon.writeControl("msg_conversation_list", MsgConversationListMsg(messages: [
                ConversationMessage(id: "m1", seq: 1, stream: "inbox:braw", senderID: "canny",
                                    senderName: "canny", body: "blether about the brig",
                                    createdAt: "2026-07-14T00:00:00Z"),
                ConversationMessage(id: "m2", seq: 2, stream: "inbox:braw", senderID: "graith:system",
                                    senderName: "pr-watch", body: "CI passed",
                                    createdAt: "2026-07-14T00:01:00Z", system: true),
            ]))
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        let messages = try await client.conversation(sessionID: "braw", limit: 50)
        #expect(messages.map(\.id) == ["m1", "m2"])
        #expect(messages[1].system == true)

        _ = await server.result
        await client.close()
    }

    /// A zero `limit` is omitted from the request (the daemon reads "no limit").
    @Test func conversationOmitsZeroLimit() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            _ = try await daemon.readControl()
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            let req = try await daemon.readControl()
            #expect(req.type == "msg_conversation")
            #expect(try decodePayload(req, as: MsgConversationMsg.self).limit == nil)
            try await daemon.writeControl("msg_conversation_list", MsgConversationListMsg(messages: []))
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        _ = try await client.conversation(sessionID: "canny")
        _ = await server.result
        await client.close()
    }

    /// `ackInbox` sends `msg_ack` for `inbox:<id>` with the session as the
    /// subscriber, completing on the daemon's `msg_acked`.
    @Test func ackInboxAddressesInboxStream() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            _ = try await daemon.readControl()
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            let req = try await daemon.readControl()
            #expect(req.type == "msg_ack")
            let ack = try decodePayload(req, as: MsgAckMsg.self)
            #expect(ack.stream == "inbox:braw")
            #expect(ack.subscriber == "braw")
            try await daemon.writeControl("msg_acked", EmptyMsg())
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        try await client.ackInbox(sessionID: "braw")
        _ = await server.result
        await client.close()
    }

    /// A daemon `error` reply to a send surfaces as a thrown `ControlError`
    /// rather than a bogus message.
    @Test func sendMessageErrorReplyThrows() async throws {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)

        let server = Task {
            _ = try await daemon.readControl()
            try await daemon.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
            _ = try await daemon.readControl() // msg_pub
            try await daemon.writeControl("error", ErrorMsg(message: "session not found"))
        }

        let stream = clientStream
        let client = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in stream }
        )
        await #expect(throws: ControlError.self) {
            _ = try await client.sendMessage(toSessionID: "thrawn", body: "haar")
        }
        _ = await server.result
        await client.close()
    }

    /// The `ConversationMessage` wire shape decodes the daemon's snake_case keys
    /// and treats omitted optional fields as nil (partial payloads decode).
    @Test func conversationMessageDecodesWireShape() throws {
        let json = """
        {"id":"msg_ab","seq":3,"stream":"inbox:braw","sender_id":"canny",\
        "sender_name":"canny","body":"speir about the loch","created_at":"2026-07-14T00:00:00Z",\
        "no_reply":true,"system":true}
        """
        let msg = try JSONDecoder().decode(ConversationMessage.self, from: Data(json.utf8))
        #expect(msg.id == "msg_ab")
        #expect(msg.seq == 3)
        #expect(msg.senderID == "canny")
        #expect(msg.system == true)
        #expect(msg.threadID == nil)
        #expect(msg.replyTo == nil)
        #expect(msg.noReply == true)

        // Minimal payload (only the required fields) still decodes.
        let minimal = """
        {"id":"m","seq":1,"stream":"inbox:x","sender_id":"y","body":"b","created_at":"t"}
        """
        let m = try JSONDecoder().decode(ConversationMessage.self, from: Data(minimal.utf8))
        #expect(m.senderName == nil)
        #expect(m.system == nil)
        #expect(m.noReply == nil)
    }

    @Test func msgPubNoReplyRoundTrips() throws {
        let original = MsgPubMsg(
            stream: "updates", body: "morning briefing complete", noReply: true)
        let data = try JSONEncoder().encode(original)
        let json = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(json["no_reply"] as? Bool == true)

        let decoded = try JSONDecoder().decode(MsgPubMsg.self, from: data)
        #expect(decoded.noReply == true)
        #expect(decoded.quiet == nil)
        #expect(decoded.replyTo == nil)
    }
}
