import Testing
import Foundation
import GraithProtocol
import GraithRemoteKit
@testable import GraithSessionKit

// The shared session/feature layer (#1131). These tests exercise the parts both
// apps now bind to: the per-host `HostConnection` state machine, the
// `FleetModel` tree/grouping helpers and single-attach coordination, and the
// shared model conveniences.

@Suite("SessionInfo conveniences")
struct SessionInfoConvenienceTests {
    @Test func statusHelpers() {
        #expect(makeSession(id: "1", name: "braw", status: "running").isRunning)
        #expect(makeSession(id: "2", name: "bide", status: "stopped").isStopped)
        #expect(makeSession(id: "3", name: "dreich", status: "errored").isErrored)
    }

    @Test func shortBranchTrimsPrefix() {
        #expect(makeSession(id: "abc", name: "canny").shortBranch == "canny-abc")
    }

    @Test func repoEntryRecentDefaultsFalse() throws {
        let absent = try JSONDecoder().decode(RepoEntry.self, from: Data("{\"path\":\"/tmp/glen\",\"name\":\"glen\"}".utf8))
        #expect(absent.isRecent == false)
        let present = try JSONDecoder().decode(RepoEntry.self, from: Data("{\"path\":\"/tmp/ben\",\"name\":\"ben\",\"recent\":true}".utf8))
        #expect(present.isRecent)
    }

    @Test func clientErrorMessagesAreNonEmpty() {
        let cases: [GraithClientError] = [
            .notPaired, .authenticationFailed("x"), .tlsPinMismatch,
            .tailnetUnreachable, .daemon("boom"), .disconnected("gone"), .decoding("bad"),
        ]
        for c in cases { #expect(!c.userMessage.isEmpty) }
        #expect(GraithClientError.daemon("boom").userMessage == "boom")
    }
}

@Suite("HostConnection")
@MainActor
struct HostConnectionTests {
    private func host(_ id: String = "ben") -> Host {
        Host(id: id, label: "Ben Nevis", kind: .remote, magicDNSName: "ben.tail", isPaired: true)
    }

    @Test func connectLoadsSessions() async {
        let client = MockHostClient(sessions: [makeSession(id: "s1", name: "braw")])
        let conn = HostConnection(entry: host(), client: client)
        await conn.connect()
        #expect(conn.state == .connected)
        #expect(conn.sessions.map(\.id) == ["s1"])
    }

    @Test func failedConnectSurfacesError() async {
        let client = MockHostClient(failConnect: .tailnetUnreachable)
        let conn = HostConnection(entry: host("thrawn"), client: client)
        await conn.connect()
        if case .failed = conn.state {} else { Issue.record("expected .failed, got \(conn.state)") }
        #expect(conn.lastError != nil)
    }
}

@Suite("FleetModel tree + grouping")
@MainActor
struct FleetTreeTests {
    @Test func rootsAndChildren() {
        let fleet = makeEmptyFleet()
        let sessions = [
            makeSession(id: "ben", name: "ben"),
            makeSession(id: "brae", name: "brae", parentID: "ben"),
            makeSession(id: "skelf", name: "skelf", parentID: "brae"),
            makeSession(id: "loch", name: "loch"),
        ]
        #expect(Set(fleet.roots(in: sessions).map(\.id)) == ["ben", "loch"])
        #expect(fleet.children(of: "ben", in: sessions).map(\.id) == ["brae"])
        #expect(fleet.descendantCount(of: "ben", in: sessions) == 2)
        #expect(fleet.descendantCount(of: "loch", in: sessions) == 0)
    }

    @Test func toggleCollapsed() {
        let fleet = makeEmptyFleet()
        #expect(!fleet.collapsedSessions.contains("ben"))
        fleet.toggleCollapsed("ben")
        #expect(fleet.collapsedSessions.contains("ben"))
        fleet.toggleCollapsed("ben")
        #expect(!fleet.collapsedSessions.contains("ben"))
    }

    @Test func emptyRegistryHasNoRemoteHosts() {
        let fleet = makeEmptyFleet()
        #expect(!fleet.hasRemoteHosts)
        #expect(fleet.sessions.isEmpty)
    }
}

@Suite("FleetModel single-attach")
@MainActor
struct FleetAttachTests {
    final class Owner {}

    @Test func claimReleaseAndSteal() {
        let fleet = makeEmptyFleet()
        let a = Owner()
        let b = Owner()

        fleet.claimAttach("s1", owner: a)
        #expect(!fleet.isAttachedElsewhere("s1", owner: a))
        #expect(fleet.isAttachedElsewhere("s1", owner: b))

        // A second claim by b is a no-op while a holds it.
        fleet.claimAttach("s1", owner: b)
        #expect(fleet.isAttachedElsewhere("s1", owner: b))

        // b releasing someone else's claim does nothing.
        fleet.releaseAttach("s1", owner: b)
        #expect(fleet.isAttachedElsewhere("s1", owner: b))

        // Force-claim is an explicit takeover.
        fleet.forceClaimAttach("s1", owner: b)
        #expect(!fleet.isAttachedElsewhere("s1", owner: b))
        #expect(fleet.isAttachedElsewhere("s1", owner: a))

        fleet.releaseAttach("s1", owner: b)
        #expect(!fleet.isAttachedElsewhere("s1", owner: a))
    }

    @Test func closedOwnerFreesSession() {
        let fleet = makeEmptyFleet()
        let other = Owner()
        do {
            let transient = Owner()
            fleet.claimAttach("s1", owner: transient)
            #expect(fleet.isAttachedElsewhere("s1", owner: other))
        }
        // The transient owner is gone; the weak ref reads nil ⇒ available.
        #expect(!fleet.isAttachedElsewhere("s1", owner: other))
    }
}
