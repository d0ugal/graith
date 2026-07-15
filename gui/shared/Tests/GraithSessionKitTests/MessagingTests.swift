import Testing
import Foundation
import GraithProtocol
import GraithRemoteKit
@testable import GraithSessionKit

// Exercises the inter-agent messaging surface added to the shared layer
// (issue #898): FleetModel/HostConnection route send/conversation/ack to the
// owning host, report success/failure, and clear errors on success. Since both
// GUIs bind to FleetModel, covering it here covers both platforms.

@Suite("FleetModel — messaging")
@MainActor
struct MessagingTests {
    private func sampleSessions() -> [SessionInfo] {
        [
            makeSession(id: "braw0001", name: "braw", status: "running", agentStatus: "active"),
            makeSession(id: "canny002", name: "canny", status: "running", agentStatus: "ready"),
        ]
    }

    @Test func sendRoutesToOwningConnectionAndReportsSuccess() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let target = fleet.sessions.first { $0.id == "braw0001" }!

        let ok = await fleet.sendMessage(to: target, body: "  wire up the brig  ")
        #expect(ok)
        // The mock stores the message; the trimmed body round-trips.
        let stored = await mock.inbox["braw0001"]
        #expect(stored?.count == 1)
        #expect(stored?.first?.body == "wire up the brig")
        #expect(fleet.connections.first?.lastError == nil)
    }

    @Test func sendRejectsBlankBodyWithoutHittingTheClient() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let target = fleet.sessions.first { $0.id == "braw0001" }!

        let ok = await fleet.sendMessage(to: target, body: "   \n  ")
        #expect(!ok)
        let stored = await mock.inbox["braw0001"]
        #expect(stored == nil) // never reached the client
    }

    @Test func sendSurfacesDaemonError() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        await mock.setFailSend(.daemon("session not found"))
        let target = fleet.sessions.first { $0.id == "braw0001" }!

        let ok = await fleet.sendMessage(to: target, body: "haar")
        #expect(!ok)
        #expect(fleet.connections.first?.lastError == "session not found")
    }

    @Test func conversationReturnsSeededMessagesOldestFirst() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        await mock.seedConversation("braw0001", [
            ConversationMessage(id: "m1", seq: 1, stream: "inbox:braw0001", senderID: "canny",
                                senderName: "canny", body: "blether one", createdAt: "2026-07-14T00:00:00Z"),
            ConversationMessage(id: "m2", seq: 2, stream: "inbox:braw0001", senderID: "graith:system",
                                senderName: "pr-watch", body: "CI passed",
                                createdAt: "2026-07-14T00:01:00Z", system: true),
        ])
        let target = fleet.sessions.first { $0.id == "braw0001" }!

        let messages = await fleet.conversation(for: target)
        #expect(messages.map(\.id) == ["m1", "m2"])
        #expect(messages.last?.system == true)
        #expect(fleet.connections.first?.lastError == nil)
    }

    @Test func conversationRespectsLimit() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        await mock.seedConversation("braw0001", (1...5).map {
            ConversationMessage(id: "m\($0)", seq: Int64($0), stream: "inbox:braw0001",
                                senderID: "canny", body: "line \($0)", createdAt: "t\($0)")
        })
        let target = fleet.sessions.first { $0.id == "braw0001" }!

        let messages = await fleet.conversation(for: target, limit: 2)
        #expect(messages.map(\.id) == ["m4", "m5"]) // most-recent suffix
    }

    @Test func conversationSurfacesDaemonError() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        await mock.setFailConversation(.daemon("not authorized"))
        let target = fleet.sessions.first { $0.id == "braw0001" }!

        let messages = await fleet.conversation(for: target)
        #expect(messages.isEmpty)
        #expect(fleet.connections.first?.lastError == "not authorized")
    }

    @Test func ackInboxRoutesToOwningConnectionAndReportsSuccess() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let target = fleet.sessions.first { $0.id == "canny002" }!

        let ok = await fleet.ackInbox(for: target)
        #expect(ok)
        let acked = await mock.acked
        #expect(acked == ["canny002"])
    }

    @Test func ackInboxReportsFailure() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        await mock.setFailAck(.disconnected("link dropped"))
        let target = fleet.sessions.first { $0.id == "canny002" }!

        let ok = await fleet.ackInbox(for: target)
        #expect(!ok) // the UI needs this to tell the user rather than looking successful
        #expect(fleet.connections.first?.lastError == "Connection dropped: link dropped")
    }
}
