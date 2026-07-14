import Testing
import Foundation
import GraithProtocol
import GraithRemoteKit
@testable import GraithSessionKit

// Coverage for the small, pure pieces of the shared layer: the error-mapping
// adapter, the single-attach registry, the reference frame codec, the real
// client factory, and the app-level model value types.

@Suite("RealClientError.map")
struct ErrorMappingTests {
    @Test func passesThroughExistingClientError() {
        #expect(RealClientError.map(GraithClientError.notPaired) == .notPaired)
    }

    @Test func mapsControlErrors() {
        #expect(RealClientError.map(ControlError.daemon("invalid token")) == .notPaired)
        #expect(RealClientError.map(ControlError.daemon("not authorized over remote")) == .notPaired)
        #expect(RealClientError.map(ControlError.daemon("rate limited")) == .daemon("rate limited"))
        #expect(RealClientError.map(ControlError.handshakeRejected("bad")) == .authenticationFailed("bad"))
        #expect(RealClientError.map(ControlError.malformed("x")) == .decoding("x"))
        if case .decoding = RealClientError.map(ControlError.unexpectedReply("y")) {} else { Issue.record("expected decoding") }
        #expect(RealClientError.map(ControlError.tlsPinMismatch("m")) == .tlsPinMismatch)
    }

    @Test func mapsTransportErrors() {
        #expect(RealClientError.map(TransportError.notReady("waiting")) == .tailnetUnreachable)
        #expect(RealClientError.map(TransportError.failed("boom")) == .disconnected("boom"))
    }

    @Test func fallsBackToDisconnected() {
        struct Whin: Error {}
        if case .disconnected = RealClientError.map(Whin()) {} else { Issue.record("expected disconnected fallback") }
    }
}

@Suite("AttachRegistry")
@MainActor
struct AttachRegistryTests {
    @Test func claimIsPerHostSession() {
        let reg = AttachRegistry()
        #expect(reg.claim(host: "ben", session: "s1"))
        // Same host+session is already claimed.
        #expect(!reg.claim(host: "ben", session: "s1"))
        #expect(reg.isAttachedElsewhere(host: "ben", session: "s1"))
        // A daemon-local id on a different host is a distinct slot.
        #expect(reg.claim(host: "brae", session: "s1"))
        #expect(!reg.isAttachedElsewhere(host: "ben", session: "s2"))
        reg.release(host: "ben", session: "s1")
        #expect(!reg.isAttachedElsewhere(host: "ben", session: "s1"))
    }
}

@Suite("GraithFrame codec")
struct GraithFrameTests {
    @Test func encodeRoundTrips() {
        let frame = GraithFrame(channel: GraithFrame.channelControl, payload: Data("hullo".utf8))
        var buf = frame.encoded()
        #expect(buf.count == GraithFrame.headerSize + 5)
        #expect(buf.first == GraithFrame.channelControl)
        let decoded = GraithFrame.decode(from: &buf)
        #expect(decoded?.channel == GraithFrame.channelControl)
        #expect(decoded?.payload == Data("hullo".utf8))
        #expect(buf.isEmpty)  // exactly one frame consumed
    }

    @Test func decodeReturnsNilOnPartialBuffer() {
        var buf = Data([GraithFrame.channelData, 0, 0, 0, 8, 0x41])  // header says 8, only 1 byte
        #expect(GraithFrame.decode(from: &buf) == nil)
        #expect(buf.count == 6)  // untouched
    }
}

@Suite("RealHostClientFactory")
struct FactoryTests {
    @Test func buildsDisconnectedClients() async {
        let factory = RealHostClientFactory(clientID: "graith-test")
        let creds = HostCredentials(clientToken: "tok", deviceID: "dev", daemonProfile: "", tlsPinSPKI: "cGlu")
        let signer = try! DeviceIdentity(keychain: InMemorySecretStore())
        let remote = factory.makeClient(
            transport: .remote(host: "ben.tail", port: 4823, tlsPinSPKI: "cGlu"),
            credentials: creds, signer: signer)
        let local = factory.makeLocalClient(transport: .unix(path: "/tmp/graith.sock"), profile: "")
        let remoteConnected = await remote.isConnected
        let localConnected = await local.isConnected
        #expect(!remoteConnected)  // constructed, not dialled
        #expect(!localConnected)
    }
}

@Suite("Model value types")
struct ModelTypeTests {
    @Test func fleetSummaryAndStatusResponse() {
        let fleet = FleetSummary(total: 3, active: 1, approval: 1, ready: 0, errored: 0, stopped: 1)
        #expect(fleet.total == 3)
        let status = StatusResponse(session: makeSession(id: "1", name: "kirk"), unreadCount: 2, fleet: fleet)
        #expect(status.unreadCount == 2)
        #expect(status.fleet.active == 1)
        #expect(status.session.name == "kirk")
    }

    @Test func createRequestAliasIsCreateMsg() {
        let req = CreateRequest(name: "braw", agent: "claude", repoPath: "/tmp/croft", prompt: "go", model: "opus")
        #expect(req.name == "braw")
        #expect(req.prompt == "go")
        #expect(req.model == "opus")
    }
}
