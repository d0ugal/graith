import XCTest
@testable import GraithGUI

/// Covers the multi-window single-attach coordination fix: two windows must not
/// fight over the daemon's one-attach-per-session rule. Ownership is keyed by
/// the owning window; a second window sees a session as "attached elsewhere"
/// rather than silently kicking the first.
@MainActor
final class AttachOwnershipTests: XCTestCase {
    func testDifferentWindowsDifferentSessionsDoNotFight() {
        let store = SessionStore()
        let winA = WindowState()
        let winB = WindowState()

        store.claimAttach("braw", owner: winA)
        store.claimAttach("canny", owner: winB)

        // Neither window sees the other's session as its own conflict.
        XCTAssertFalse(store.isAttachedElsewhere("braw", owner: winA))
        XCTAssertFalse(store.isAttachedElsewhere("canny", owner: winB))
        // But each does see the other's distinct session as owned elsewhere.
        XCTAssertTrue(store.isAttachedElsewhere("braw", owner: winB))
        XCTAssertTrue(store.isAttachedElsewhere("canny", owner: winA))
    }

    func testSecondWindowOnSameSessionIsBlockedNotStealing() {
        let store = SessionStore()
        let winA = WindowState()
        let winB = WindowState()

        store.claimAttach("thrawn", owner: winA)
        // B tries to claim the same session — A keeps it.
        store.claimAttach("thrawn", owner: winB)

        XCTAssertFalse(store.isAttachedElsewhere("thrawn", owner: winA))
        XCTAssertTrue(store.isAttachedElsewhere("thrawn", owner: winB),
                      "second window must be told the session is busy, not steal it")
    }

    func testForceClaimIsAnExplicitTakeover() {
        let store = SessionStore()
        let winA = WindowState()
        let winB = WindowState()

        store.claimAttach("thrawn", owner: winA)
        store.forceClaimAttach("thrawn", owner: winB)

        XCTAssertTrue(store.isAttachedElsewhere("thrawn", owner: winA))
        XCTAssertFalse(store.isAttachedElsewhere("thrawn", owner: winB))
    }

    func testReleaseFreesTheSession() {
        let store = SessionStore()
        let winA = WindowState()
        let winB = WindowState()

        store.claimAttach("braw", owner: winA)
        store.releaseAttach("braw", owner: winA)

        // Now B can take it freely.
        XCTAssertFalse(store.isAttachedElsewhere("braw", owner: winB))
        store.claimAttach("braw", owner: winB)
        XCTAssertFalse(store.isAttachedElsewhere("braw", owner: winB))
    }

    func testReleaseByNonOwnerIsIgnored() {
        let store = SessionStore()
        let winA = WindowState()
        let winB = WindowState()

        store.claimAttach("braw", owner: winA)
        // B doesn't own it; releasing must not clobber A's ownership.
        store.releaseAttach("braw", owner: winB)

        XCTAssertTrue(store.isAttachedElsewhere("braw", owner: winB))
        XCTAssertFalse(store.isAttachedElsewhere("braw", owner: winA))
    }

    func testClosedWindowOwnerSelfHeals() {
        let store = SessionStore()
        let winB = WindowState()

        // A owns "bide", then goes away without releasing (window closed).
        do {
            let winA = WindowState()
            store.claimAttach("bide", owner: winA)
            XCTAssertTrue(store.isAttachedElsewhere("bide", owner: winB))
        }
        // The weak owner reference is now nil, so the session is available again.
        XCTAssertFalse(store.isAttachedElsewhere("bide", owner: winB))
    }
}
