import Testing
import Foundation
import Combine
import GraithProtocol
import GraithRemoteKit
@testable import GraithSessionKit

// Exercises the FleetModel + HostConnection paths that only run against a
// connected host: rebuildConnections (remote/paired branch), the aggregations,
// the mutation surface, refresh guarding + error handling, and the observable
// single-attach takeover.

@Suite("FleetModel — connected remote host", .timeLimit(.minutes(1)))
@MainActor
struct FleetConnectedTests {
    private func sampleSessions() -> [SessionInfo] {
        [
            makeSession(id: "braw0001", name: "braw", status: "running", agentStatus: "active", repoName: "croft"),
            makeSession(id: "canny002", name: "canny", status: "running", agentStatus: "approval", repoName: "croft"),
            makeSession(id: "bide0003", name: "bide", status: "stopped", repoName: "glen"),
        ]
    }

    @Test func connectBuildsConnectionAndAggregates() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        #expect(fleet.connections.count == 1)
        #expect(fleet.hasRemoteHosts)
        await fleet.connectAll()
        #expect(fleet.connections.first?.state == .connected)
        #expect(Set(fleet.sessions.map(\.id)) == ["braw0001", "canny002", "bide0003"])
        #expect(fleet.allSessions.count == 3)
        #expect(fleet.allSessions.allSatisfy { $0.host.id == "ben" })
        // Grouped by repo, then by host→repo.
        #expect(fleet.sessionsByRepo.map(\.repo) == ["croft", "glen"])
        #expect(fleet.sessionsByHost.count == 1)
        #expect(fleet.sessionsByHost[0].groups.first?.repo == "croft")
    }

    @Test func disconnectAndReconnect() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        #expect(fleet.connections.first?.state == .connected)
        await fleet.disconnectAll()
        #expect(fleet.connections.first?.state == .idle)
        let live = await mock.isConnected
        #expect(!live)
        await fleet.reconnectAll()
        #expect(fleet.connections.first?.state == .connected)
    }

    @Test func mutationsDelegateToOwningConnection() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let conn = fleet.connections[0]
        let target = conn.sessions.first { $0.id == "braw0001" }!
        // Delete removes it from the mock; refresh then drops it from the list.
        await conn.delete(target)
        #expect(conn.sessions.first { $0.id == "braw0001" } == nil)
        #expect(conn.lastError == nil)
        // The other lifecycle wrappers just round-trip without error.
        let other = conn.sessions.first { $0.id == "canny002" }!
        await conn.stop(other); await conn.resume(other); await conn.restart(other)
        await conn.interrupt(other); await conn.rename(other, to: "renamed"); await conn.toggleStar(other)
        await conn.fork(other, name: "forked")
        #expect(conn.lastError == nil)
    }

    @Test func migrateForwardsModelToClient() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let conn = fleet.connections[0]
        let target = conn.sessions.first { $0.id == "braw0001" }!
        await conn.migrate(target, agent: "codex", model: "o3")
        let m = await mock.lastMigrate
        #expect(m?.agent == "codex")
        #expect(m?.model == "o3")
    }

    @Test func fleetMigrateNormalisesBlankModelToNil() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let target = fleet.sessions.first { $0.id == "braw0001" }!
        fleet.migrateSession(target, agent: "codex", model: "   ")
        // migrateSession fires a detached Task; poll briefly for the delegate.
        for _ in 0..<50 {
            if await mock.lastMigrate != nil { break }
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
        let m = await mock.lastMigrate
        #expect(m?.agent == "codex")
        #expect(m?.model == nil)  // whitespace-only model trimmed to nil
    }

    @Test func createSessionReportsCreated() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(),
                                                repos: [RepoEntry(path: "/tmp/croft", name: "croft", recent: true)],
                                                subscribeApprovals: false)
        await fleet.connectAll()
        // The mock's `create` is a no-op, so seed the session it should surface.
        await mock.appendSession(makeSession(id: "new9", name: "bonnie", repoName: "croft"))
        var created: SessionInfo?
        await withCheckedContinuation { cont in
            fleet.createSession(name: "bonnie", agent: "claude", repoPath: "/tmp/croft",
                                model: "", prompt: "", hostID: "ben") { result in
                created = try? result.get()
                cont.resume()
            }
        }
        #expect(created?.name == "bonnie")
    }

    @Test func createSessionForwardsAdvancedOptions() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(),
                                                repos: [RepoEntry(path: "/tmp/croft", name: "croft", recent: true)],
                                                subscribeApprovals: false)
        await fleet.connectAll()
        await withCheckedContinuation { cont in
            fleet.createSession(name: "canny", agent: "claude", repoPath: "/tmp/croft",
                                model: "", prompt: "", base: "  auld-main  ", yolo: false,
                                inPlace: false, agentHooks: false, hostID: "ben") { _ in
                cont.resume()
            }
        }
        let req = await mock.lastCreate
        #expect(req?.base == "auld-main")  // trimmed before it goes on the wire
        #expect(req?.yolo == nil)          // false collapses to nil (omitted)
        #expect(req?.inPlace == nil)       // false collapses to nil (omitted)
        // agentHooks is always sent explicitly (false is meaningful — Go's
        // omitempty can't distinguish absent from false).
        #expect(req?.agentHooks == false)
    }

    @Test func createSessionYoloForcesAgentHooksOn() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(),
                                                repos: [RepoEntry(path: "/tmp/croft", name: "croft", recent: true)],
                                                subscribeApprovals: false)
        await fleet.connectAll()
        // Yolo on + hooks off is a combination the daemon rewrites (agentHooks ||
        // yolo); the client sends the effective value so the wire matches reality.
        await withCheckedContinuation { cont in
            fleet.createSession(name: "bonnie", agent: "claude", repoPath: "/tmp/croft",
                                model: "", prompt: "", yolo: true, agentHooks: false,
                                hostID: "ben") { _ in
                cont.resume()
            }
        }
        let req = await mock.lastCreate
        #expect(req?.yolo == true)
        #expect(req?.agentHooks == true)
    }

    @Test func createSessionRejectsInPlaceWithBase() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(),
                                                repos: [RepoEntry(path: "/tmp/croft", name: "croft", recent: true)],
                                                subscribeApprovals: false)
        await fleet.connectAll()
        var failed = false
        await withCheckedContinuation { cont in
            fleet.createSession(name: "thrawn", agent: "claude", repoPath: "/tmp/croft",
                                model: "", prompt: "", base: "main", inPlace: true,
                                hostID: "ben") { result in
                if case .failure = result { failed = true }
                cont.resume()
            }
        }
        #expect(failed)
        // Rejected before any daemon round-trip.
        let req = await mock.lastCreate
        #expect(req == nil)
    }

    @Test func validateCreateOptionsGuardsInPlaceBase() {
        #expect(FleetModel.validateCreateOptions(base: "main", inPlace: true) != nil)
        #expect(FleetModel.validateCreateOptions(base: "  ", inPlace: true) == nil)
        #expect(FleetModel.validateCreateOptions(base: "main", inPlace: false) == nil)
        #expect(FleetModel.validateCreateOptions(base: "", inPlace: false) == nil)
    }

    @Test func createSessionUnknownHostFails() async {
        let (fleet, _) = makeFleetWithRemote(subscribeApprovals: false)
        await fleet.connectAll()
        var failed = false
        await withCheckedContinuation { cont in
            fleet.createSession(name: "x", agent: "claude", repoPath: "/tmp", model: "", prompt: "", hostID: "nope") { result in
                if case .failure = result { failed = true }
                cont.resume()
            }
        }
        #expect(failed)
    }

    @Test func listFailureSurfacesAsHostErrorThenClears() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let conn = fleet.connections[0]
        await mock.setFailList(.daemon("list broke"))
        await conn.refresh()
        // Still .connected, but the error surfaces via hostErrors + the footer.
        #expect(conn.state == .connected)
        #expect(fleet.hostErrors["ben"] == "list broke")
        #expect(fleet.error == "list broke")
        // A subsequent good list clears it.
        await mock.setFailList(nil)
        await conn.refresh()
        #expect(fleet.hostErrors.isEmpty)
        #expect(fleet.error == nil)
    }

    @Test func refreshCoalescesOverlappingCallIntoOneFollowUp() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()  // one refresh already ran (list call #1)
        let conn = fleet.connections[0]
        await mock.setGateList(true)
        // First refresh blocks in flight on the gate (isRefreshing == true).
        let first = Task { await conn.refresh() }
        // Wait until it has actually entered listSessions (call #2) so the
        // overlapping refresh below is guaranteed to land while one is in flight
        // — deterministic, no sleep-and-hope.
        for _ in 0..<500 where await mock.listCallCount < 2 {
            try? await Task.sleep(nanoseconds: 2_000_000)
        }
        // If scheduling starved the in-flight refresh so it never entered
        // listSessions, the overlapping `conn.refresh()` below would itself
        // become the gated call and block — the very hang this test guards
        // against. Release + drain and fail cleanly instead of leaning on the
        // suite time-limit to unstick it.
        guard await mock.listCallCount == 2 else {
            await mock.releaseList()
            await first.value
            Issue.record("in-flight refresh did not enter listSessions within the poll window")
            return
        }
        // A refresh while one is in flight does NOT run concurrently: it sets the
        // queued flag and returns immediately (guaranteed non-blocking now that
        // call #2 is confirmed in flight), so the in-flight refresh loops exactly
        // once more when released.
        await conn.refresh()
        await mock.releaseList()
        await first.value
        #expect(conn.state == .connected)
        // The gated call (#2) plus a single coalesced loop-around (#3) — no more
        // (would mean the queue flag stuck on and it kept looping), no fewer
        // (would mean the overlapping refresh was silently dropped).
        #expect(await mock.listCallCount == 3)
    }

    @Test func removeHostTearsDownConnection() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        #expect(fleet.connections.count == 1)
        let host = fleet.connections[0].entry
        await fleet.removeHost(host)
        #expect(fleet.connections.isEmpty)
        #expect(!fleet.hasRemoteHosts)
    }

    @Test func approvalsAggregateAcrossConnection() async {
        let approval = try! JSONDecoder().decode(ApprovalInfo.self, from: Data(
            "{\"request_id\":\"r1\",\"session_id\":\"canny002\",\"session_name\":\"canny\",\"tool_name\":\"Bash\",\"agent\":\"codex\",\"repo_name\":\"croft\",\"requested_at\":\"\"}".utf8))
        let (fleet, _) = makeFleetWithRemote(sessions: sampleSessions(), pending: [approval], subscribeApprovals: true)
        await fleet.connectAll()
        for _ in 0..<60 where fleet.totalPendingApprovals == 0 { try? await Task.sleep(nanoseconds: 5_000_000) }
        #expect(fleet.totalPendingApprovals == 1)
        #expect(fleet.allApprovals.first?.host.id == "ben")
        #expect(fleet.allApprovals.first?.approval.requestID == "r1")
        await fleet.disconnectAll()  // stop the retry loop
    }

    @Test func forceClaimAttachPublishesChange() {
        final class Owner {}
        let (fleet, _) = makeFleetWithRemote(subscribeApprovals: false)
        let a = Owner(); let b = Owner()
        fleet.claimAttach("s1", owner: a)
        var fired = false
        let c = fleet.objectWillChange.sink { fired = true }
        fleet.forceClaimAttach("s1", owner: b)
        #expect(fired)  // @Published attachOwners → takeover is observable
        #expect(fleet.isAttachedElsewhere("s1", owner: a))
        c.cancel()
    }
}
