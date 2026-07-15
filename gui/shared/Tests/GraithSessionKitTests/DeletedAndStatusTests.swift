import Testing
import Foundation
import GraithProtocol
import GraithRemoteKit
@testable import GraithSessionKit

// Exercises the GUI↔CLI parity gaps closed in #1148: restore, purge, and
// set-status, plus the deleted-session listing that backs the Restore surface.
// The mock models the daemon's retention window — `delete` soft-deletes (moves
// into a deleted list), `restore` un-deletes, and `purge` removes permanently.

@Suite("FleetModel — restore / purge / set-status (#1148)")
@MainActor
struct DeletedAndStatusTests {
    private func sampleSessions() -> [SessionInfo] {
        [
            makeSession(id: "braw0001", name: "braw", status: "running", agentStatus: "active", repoName: "croft"),
            makeSession(id: "bide0003", name: "bide", status: "stopped", repoName: "glen"),
        ]
    }

    // MARK: - HostConnection

    @Test func deleteThenRestoreRoundTrips() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let conn = fleet.connections[0]
        let target = conn.sessions.first { $0.id == "braw0001" }!

        // Soft delete moves it out of the live list and into the deleted list.
        await conn.delete(target)
        #expect(conn.sessions.first { $0.id == "braw0001" } == nil)
        var deleted = await conn.deletedSessions()
        #expect(deleted.map(\.id) == ["braw0001"])
        #expect(conn.lastError == nil)

        // Restore brings it back to the live list; the deleted list empties.
        await conn.restore(target)
        #expect(conn.sessions.contains { $0.id == "braw0001" })
        deleted = await conn.deletedSessions()
        #expect(deleted.isEmpty)
        #expect(conn.lastError == nil)
    }

    @Test func purgeRemovesPermanently() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let conn = fleet.connections[0]
        let live = conn.sessions.first { $0.id == "braw0001" }!

        // Purging a live session hard-deletes it — gone from both lists.
        await conn.purge(live)
        #expect(conn.sessions.first { $0.id == "braw0001" } == nil)
        let deleted = await conn.deletedSessions()
        #expect(deleted.isEmpty)
        #expect(conn.lastError == nil)
    }

    @Test func purgeAlsoRemovesASoftDeletedSession() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let conn = fleet.connections[0]
        let target = conn.sessions.first { $0.id == "bide0003" }!

        await conn.delete(target)
        #expect(await conn.deletedSessions().map(\.id) == ["bide0003"])
        await conn.purge(target)
        #expect(await conn.deletedSessions().isEmpty)
    }

    @Test func setStatusForwardsTextAndClearFlag() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let conn = fleet.connections[0]
        let target = conn.sessions.first { $0.id == "braw0001" }!

        await conn.setStatus(target, text: "building the bonnie feature", ttlSeconds: 600)
        var last = await mock.lastSetStatus
        #expect(last?.text == "building the bonnie feature")
        #expect(last?.ttlSeconds == 600)
        #expect(last?.clear == false)

        await conn.setStatus(target, text: "", clear: true)
        last = await mock.lastSetStatus
        #expect(last?.clear == true)
        #expect(conn.lastError == nil)
    }

    @Test func setStatusFailureSurfacesOnConnection() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let conn = fleet.connections[0]
        let target = conn.sessions.first { $0.id == "braw0001" }!
        await mock.setFailSetStatus(.daemon("status rpc broke"))
        await conn.setStatus(target, text: "thrawn attempt")
        #expect(conn.lastError == "status rpc broke")
    }

    @Test func deletedSessionsFailureSurfacesAsHostError() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let conn = fleet.connections[0]
        await mock.setFailList(.daemon("list broke"))
        let deleted = await conn.deletedSessions()
        #expect(deleted.isEmpty)
        #expect(conn.lastError == "list broke")
    }

    // MARK: - FleetModel aggregation

    @Test func fleetDeletedSessionsAggregatesTaggedByHost() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let conn = fleet.connections[0]
        await conn.delete(conn.sessions.first { $0.id == "braw0001" }!)

        let deleted = await fleet.deletedSessions()
        #expect(deleted.count == 1)
        #expect(deleted.first?.host.id == "ben")
        #expect(deleted.first?.session.id == "braw0001")
    }

    @Test func fleetRestoreByHostReturnsSessionToLiveList() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let conn = fleet.connections[0]
        await conn.delete(conn.sessions.first { $0.id == "braw0001" }!)
        let deleted = await fleet.deletedSessions()
        let row = deleted.first!

        // restore awaits the connection op, so the mock has moved it back by the
        // time it returns — no polling needed.
        await fleet.restore(row.session, hostID: row.host.id)
        #expect(await mock.sessions.contains { $0.id == "braw0001" })
    }

    @Test func fleetPurgeByUnknownHostIsANoOp() async {
        let (fleet, _) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let target = fleet.sessions.first { $0.id == "braw0001" }!
        // Unknown host id (thrawn — a stubborn miss): nothing to act on, must not
        // crash or mutate.
        await fleet.purge(target, hostID: "thrawn")
        await fleet.restore(target, hostID: "thrawn")
        #expect(fleet.sessions.contains { $0.id == "braw0001" })
    }

    @Test func fleetSetStatusDelegatesToOwningConnection() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let target = fleet.sessions.first { $0.id == "braw0001" }!
        fleet.setStatus(target, text: "ken this", ttlSeconds: nil, clear: false)
        for _ in 0..<50 {
            if await mock.lastSetStatus != nil { break }
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
        #expect(await mock.lastSetStatus?.text == "ken this")
    }

    @Test func fleetPurgeSessionHardDeletesALiveSession() async {
        let (fleet, mock) = makeFleetWithRemote(sessions: sampleSessions(), subscribeApprovals: false)
        await fleet.connectAll()
        let target = fleet.sessions.first { $0.id == "bide0003" }!
        fleet.purgeSession(target)
        for _ in 0..<50 {
            if !(await mock.sessions.contains(where: { $0.id == "bide0003" })) { break }
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
        #expect(!(await mock.sessions.contains { $0.id == "bide0003" }))
        #expect(await mock.deleted.isEmpty)  // purge, not soft delete
    }
}
