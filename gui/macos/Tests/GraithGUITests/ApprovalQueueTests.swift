import XCTest
import GraithProtocol
@testable import GraithGUI

/// Covers the aggregated approvals queue (#1130): per-host storage, host-ordered
/// merge, optimistic removal on respond, and the composite `host:request`
/// notification keys that keep two daemons' identical request ids distinct.
final class ApprovalQueueTests: XCTestCase {
    // Build fixtures via the wire JSON keys — `ApprovalInfo`'s memberwise init is
    // internal to GraithProtocol, so decoding also exercises its CodingKeys.
    private func approval(_ requestID: String, session: String = "braw") throws -> ApprovalInfo {
        let json = """
        {"request_id":"\(requestID)","session_id":"s-\(requestID)","session_name":"\(session)",
         "tool_name":"Bash","tool_input":"ls","agent":"claude","repo_name":"croft",
         "requested_at":"now"}
        """
        return try JSONDecoder().decode(ApprovalInfo.self, from: Data(json.utf8))
    }

    func testMergedFollowsHostOrder() throws {
        var q = ApprovalQueue()
        q.set([try approval("a1")], host: "ben")
        q.set([try approval("b1")], host: "brae")

        // Order is caller-supplied (registry order), not dictionary order.
        let benFirst = q.merged(order: ["ben", "brae"]).map(\.requestID)
        XCTAssertEqual(benFirst, ["a1", "b1"])
        let braeFirst = q.merged(order: ["brae", "ben"]).map(\.requestID)
        XCTAssertEqual(braeFirst, ["b1", "a1"])
    }

    func testMergedOmitsHostsNotInOrder() throws {
        var q = ApprovalQueue()
        q.set([try approval("a1")], host: "ben")
        q.set([try approval("z1")], host: "dreich") // host dropped from order

        XCTAssertEqual(q.merged(order: ["ben"]).map(\.requestID), ["a1"])
    }

    func testClearForgetsHost() throws {
        var q = ApprovalQueue()
        q.set([try approval("a1")], host: "ben")
        q.clear(host: "ben")
        XCTAssertTrue(q.merged(order: ["ben"]).isEmpty)
    }

    func testRemoveDropsOnlyTheOneRequest() throws {
        var q = ApprovalQueue()
        q.set([try approval("a1"), try approval("a2")], host: "ben")
        q.remove(requestID: "a1", host: "ben")
        XCTAssertEqual(q.merged(order: ["ben"]).map(\.requestID), ["a2"])
    }

    func testRemoveIsScopedToHost() throws {
        // Two hosts minting the same daemon-local request id: removing on one
        // host must not touch the other's identically-named request.
        var q = ApprovalQueue()
        q.set([try approval("dup")], host: "ben")
        q.set([try approval("dup")], host: "brae")

        q.remove(requestID: "dup", host: "ben")
        XCTAssertTrue(q.merged(order: ["ben"]).isEmpty)
        XCTAssertEqual(q.merged(order: ["brae"]).map(\.requestID), ["dup"])
    }

    func testKeysAreHostScopedComposites() throws {
        var q = ApprovalQueue()
        q.set([try approval("dup")], host: "ben")
        q.set([try approval("dup")], host: "brae")

        // A bare request id would collapse these to one; the composite keeps both.
        XCTAssertEqual(q.keys(order: ["ben", "brae"]), ["ben:dup", "brae:dup"])
    }

    func testApplySnapshotSuppressesInFlightRequest() throws {
        // The human answered "a1" (optimistically removed, now in-flight). A
        // stream snapshot that still lists a1 must not resurrect it — but must
        // still surface the genuinely-new a2.
        var q = ApprovalQueue()
        let snapshot = [try approval("a1"), try approval("a2")]
        q.applySnapshot(snapshot, host: "ben", suppressing: ["ben:a1"])
        XCTAssertEqual(q.merged(order: ["ben"]).map(\.requestID), ["a2"])
    }

    func testApplySnapshotSuppressionIsHostScoped() throws {
        // Suppressing ben:dup must not hide brae's own dup.
        var q = ApprovalQueue()
        q.applySnapshot([try approval("dup")], host: "brae", suppressing: ["ben:dup"])
        XCTAssertEqual(q.merged(order: ["brae"]).map(\.requestID), ["dup"])
    }

    func testAddRestoresARolledBackRequest() throws {
        // A failed respond rolls the row back via add().
        var q = ApprovalQueue()
        q.set([try approval("a2")], host: "ben")
        q.add(try approval("a1"), host: "ben")
        XCTAssertEqual(Set(q.merged(order: ["ben"]).map(\.requestID)), ["a1", "a2"])
    }

    func testAddIsIdempotent() throws {
        var q = ApprovalQueue()
        q.set([try approval("a1")], host: "ben")
        q.add(try approval("a1"), host: "ben") // already present — no duplicate
        XCTAssertEqual(q.merged(order: ["ben"]).map(\.requestID), ["a1"])
    }
}
