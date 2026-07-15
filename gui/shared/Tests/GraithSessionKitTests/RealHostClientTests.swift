import Testing
import Foundation
@testable import GraithProtocol
@testable import GraithSessionKit

// End-to-end coverage for the production `RealHostClient` adapter, driving a
// real `GraithProtocolClient` over an in-memory transport against a scripted
// `MockDaemon`. Covers the control-path methods + error normalisation; the
// event-connection paths (approvalStream/attach) are covered structurally by
// GraithProtocolTests' multi-connection integration tests.

@Suite("RealHostClient — over the framed protocol")
struct RealHostClientTests {
    /// Build a `RealHostClient` whose inner client speaks to `server` over an
    /// in-memory stream. The server closure scripts the daemon side.
    private func make(_ server: @escaping @Sendable (MockDaemon) async throws -> Void)
        -> (client: RealHostClient, server: Task<Void, Error>) {
        let (clientStream, serverStream) = InMemoryByteStream.makePair()
        let daemon = MockDaemon(stream: serverStream)
        let task = Task { try await server(daemon) }
        let inner = GraithProtocolClient(
            transport: .unix(path: "/tmp/graith.sock"),
            profile: "", clientID: "app", token: nil, signer: nil,
            streamFactory: { _ in clientStream }
        )
        return (RealHostClient(inner: inner), task)
    }

    private func handshake(_ d: MockDaemon) async throws {
        let hs = try await d.readControl()
        #expect(hs.type == "handshake")
        try await d.writeControl("handshake_ok", HandshakeOkMsg(version: "1.0", daemonVersion: "dev"))
    }

    @Test func connectThenListSessions() async throws {
        let (client, server) = make { d in
            try await self.handshake(d)
            let req = try await d.readControl()
            #expect(req.type == "list")
            try await d.writeControl("session_list", SessionListMsg(sessions: [
                makeSession(id: "braw0001", name: "braw"),
                makeSession(id: "canny002", name: "canny"),
            ]))
        }
        try await client.connect()
        let connected = await client.isConnected
        #expect(connected)
        let sessions = try await client.listSessions()
        #expect(sessions.map(\.name) == ["braw", "canny"])
        _ = await server.result
        await client.disconnect()
    }

    @Test func statusSynthesizedFromList() async throws {
        let (client, server) = make { d in
            try await self.handshake(d)
            _ = try await d.readControl()
            try await d.writeControl("session_list", SessionListMsg(sessions: [
                makeSession(id: "x", name: "braw", status: "running", agentStatus: "approval"),
            ]))
        }
        try await client.connect()
        let status = try await client.status(sessionID: "x")
        #expect(status.session.name == "braw")
        #expect(status.fleet.total == 1)
        #expect(status.fleet.approval == 1)
        _ = await server.result
        await client.disconnect()
    }

    @Test func createRoundTrips() async throws {
        let (client, server) = make { d in
            try await self.handshake(d)
            let req = try await d.readControl()
            #expect(req.type == "create")
            try await d.writeControl("session", makeSession(id: "new1", name: "bonnie"))
        }
        try await client.connect()
        try await client.create(CreateRequest(name: "bonnie", agent: "claude", repoPath: "/tmp/croft"))
        _ = await server.result
        await client.disconnect()
    }

    @Test func repoListMaps() async throws {
        let (client, server) = make { d in
            try await self.handshake(d)
            _ = try await d.readControl()
            try await d.writeControl("repo_list", RepoListResponseMsg(repos: [
                RepoEntry(path: "/tmp/croft", name: "croft", recent: true),
            ]))
        }
        try await client.connect()
        let repos = try await client.repoList()
        #expect(repos.first?.name == "croft")
        #expect(repos.first?.isRecent == true)
        _ = await server.result
        await client.disconnect()
    }

    @Test func storeListMaps() async throws {
        let (client, server) = make { d in
            try await self.handshake(d)
            let req = try await d.readControl()
            #expect(req.type == "store_list")
            try await d.writeControl("store_list", StoreListResponseMsg(entries: [
                StoreEntryInfo(key: "design/api.md", repo: "croft-abc", updatedAt: "2026-07-15T09:00:00Z"),
                StoreEntryInfo(key: "blether.md", repo: "shared", updatedAt: "2026-07-15T10:00:00Z"),
            ]))
        }
        try await client.connect()
        let entries = try await client.storeList(repo: nil, shared: false, prefix: nil)
        #expect(entries.map(\.key) == ["design/api.md", "blether.md"])
        #expect(entries.last?.repo == "shared")
        _ = await server.result
        await client.disconnect()
    }

    @Test func storeGetMaps() async throws {
        let (client, server) = make { d in
            try await self.handshake(d)
            let req = try await d.readControl()
            #expect(req.type == "store_get")
            try await d.writeControl("store_get",
                                     StoreGetResponseMsg(key: "design/api.md", repo: "croft-abc", body: "still waters"))
        }
        try await client.connect()
        let doc = try await client.storeGet(repo: "croft-abc", shared: false, key: "design/api.md")
        #expect(doc.body == "still waters")
        #expect(doc.repo == "croft-abc")
        _ = await server.result
        await client.disconnect()
    }

    @Test func storeGetMapsDaemonError() async throws {
        let (client, server) = make { d in
            try await self.handshake(d)
            _ = try await d.readControl()
            try await d.writeControl("error", ErrorMsg(message: "unknown store \"thrawn\""))
        }
        try await client.connect()
        await #expect(throws: GraithClientError.self) {
            _ = try await client.storeGet(repo: "thrawn", shared: false, key: "haar.md")
        }
        _ = await server.result
        await client.disconnect()
    }

    @Test func stopMutationRoundTrips() async throws {
        let (client, server) = make { d in
            try await self.handshake(d)
            let req = try await d.readControl()
            #expect(req.type == "stop")
            try await d.writeControl("ok", EmptyMsg())
        }
        try await client.connect()
        try await client.stop(sessionID: "x")
        _ = await server.result
        await client.disconnect()
    }

    @Test func lifecycleMutationsRoundTrip() async throws {
        let expected = ["resume", "restart", "interrupt", "delete", "rename", "star", "unstar"]
        let (client, server) = make { d in
            try await self.handshake(d)
            for want in expected {
                let req = try await d.readControl()
                #expect(req.type == want)
                try await d.writeControl("ok", EmptyMsg())
            }
        }
        try await client.connect()
        try await client.resume(sessionID: "x")
        try await client.restart(sessionID: "x")
        try await client.interrupt(sessionID: "x")
        try await client.delete(sessionID: "x")
        try await client.rename(sessionID: "x", newName: "renamed")
        try await client.star(sessionID: "x")
        try await client.unstar(sessionID: "x")
        _ = await server.result
        await client.disconnect()
    }

    @Test func forkAndMigrateReturnSessions() async throws {
        let (client, server) = make { d in
            try await self.handshake(d)
            let fork = try await d.readControl()
            #expect(fork.type == "fork")
            try await d.writeControl("session", makeSession(id: "f1", name: "forked"))
            let migrate = try await d.readControl()
            #expect(migrate.type == "migrate")
            try await d.writeControl("session", makeSession(id: "x", name: "migrated"))
        }
        try await client.connect()
        try await client.fork(name: "forked", sourceSessionID: "x")
        try await client.migrate(sessionID: "x", agent: "codex", model: "o3")
        _ = await server.result
        await client.disconnect()
    }

    @Test func screenSnapshotMaps() async throws {
        let (client, server) = make { d in
            try await self.handshake(d)
            _ = try await d.readControl()
            try await d.writeControl("screen_snapshot", ScreenSnapshotResponseMsg(
                sessionID: "x", frame: "hullo", cols: 80, rows: 24))
        }
        try await client.connect()
        let snap = try await client.screenSnapshot(sessionID: "x")
        #expect(snap.frame == "hullo")
        #expect(snap.cols == 80)
        _ = await server.result
        await client.disconnect()
    }

    @Test func respondApprovalRoundTrips() async throws {
        let (client, server) = make { d in
            try await self.handshake(d)
            let req = try await d.readControl()
            #expect(req.type == "approval_respond")
            try await d.writeControl("ok", EmptyMsg())
        }
        try await client.connect()
        try await client.respondApproval(requestID: "r1", decision: .allow, reason: nil)
        _ = await server.result
        await client.disconnect()
    }

    @Test func daemonErrorMapsToClientError() async throws {
        let (client, server) = make { d in
            try await self.handshake(d)
            _ = try await d.readControl()
            try await d.writeControl("error", ErrorMsg(message: "no such session"))
        }
        try await client.connect()
        await #expect(throws: GraithClientError.self) {
            _ = try await client.listSessions()
        }
        _ = await server.result
        await client.disconnect()
    }

    @Test func connectFailureMapsError() async throws {
        // Daemon rejects the handshake → connect() surfaces a mapped error and
        // stays disconnected.
        let (client, server) = make { d in
            let hs = try await d.readControl()
            #expect(hs.type == "handshake")
            try await d.writeControl("handshake_err", HandshakeErrMsg(reason: "version mismatch"))
        }
        await #expect(throws: Error.self) { try await client.connect() }
        let connected = await client.isConnected
        #expect(!connected)
        _ = await server.result
    }
}
