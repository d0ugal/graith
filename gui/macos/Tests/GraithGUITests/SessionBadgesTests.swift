import XCTest
import GraithProtocol
@testable import GraithGUI

/// Covers the sidebar metadata badges (issue #901): the daemon sends
/// sandboxed/yolo/config_stale/scenario/PR/CI on the wire, and the row must
/// surface them. The pure style-mapping functions and the boolean helpers are
/// tested here (SwiftUI `Color`s are opaque, the style buckets are not).
final class SessionBadgesTests: XCTestCase {
    // Decode a SessionInfo from the wire JSON keys — the memberwise init is
    // internal to GraithProtocol, so building fixtures by JSON also exercises
    // the CodingKeys the badges depend on.
    private func session(_ json: String) throws -> SessionInfo {
        try JSONDecoder().decode(SessionInfo.self, from: Data(json.utf8))
    }

    private let braw = """
    {"id":"braw","name":"braw","repo_path":"/croft","repo_name":"croft",
     "worktree_path":"/bothy","branch":"b","base_branch":"main","agent":"claude",
     "status":"running","created_at":"now"}
    """

    // MARK: - Boolean helpers

    func testModeHelpersDefaultFalseWhenAbsent() throws {
        let s = try session(braw)
        XCTAssertFalse(s.isYolo)
        XCTAssertFalse(s.isSandboxed)
        XCTAssertFalse(s.isScenarioMember)
        XCTAssertFalse(s.isConfigStale)
    }

    func testModeHelpersReflectWireFlags() throws {
        let s = try session("""
        {"id":"canny","name":"canny","repo_path":"/croft","repo_name":"croft",
         "worktree_path":"/bothy","branch":"b","base_branch":"main","agent":"codex",
         "status":"running","created_at":"now","yolo":true,"sandboxed":true,
         "config_stale":true,"scenario_id":"sc-1","scenario_name":"strath"}
        """)
        XCTAssertTrue(s.isYolo)
        XCTAssertTrue(s.isSandboxed)
        XCTAssertTrue(s.isConfigStale)
        XCTAssertTrue(s.isScenarioMember)
        XCTAssertEqual(s.scenarioName, "strath")
    }

    func testScenarioMemberIsFalseForEmptyID() throws {
        // A present-but-empty scenario_id must not count as membership.
        let s = try session("""
        {"id":"haar","name":"haar","repo_path":"/croft","repo_name":"croft",
         "worktree_path":"/bothy","branch":"b","base_branch":"main","agent":"claude",
         "status":"running","created_at":"now","scenario_id":""}
        """)
        XCTAssertFalse(s.isScenarioMember)
    }

    // MARK: - PR badge style

    func testPRBadgeStyleFromState() {
        XCTAssertEqual(prBadgeStyle(for: pr(state: "merged")), .merged)
        XCTAssertEqual(prBadgeStyle(for: pr(state: "closed")), .closed)
        XCTAssertEqual(prBadgeStyle(for: pr(state: "draft")), .draft)
        XCTAssertEqual(prBadgeStyle(for: pr(state: "open")), .open)
    }

    func testPRBadgeConflictingOverridesOpen() {
        // A conflicting open PR is flagged distinctly (merge conflict).
        XCTAssertEqual(prBadgeStyle(for: pr(state: "open", conflicting: true)), .conflicting)
    }

    func testPRBadgeMergedIgnoresConflictFlag() {
        // Merged/closed are terminal — the stale conflict bit must not win.
        XCTAssertEqual(prBadgeStyle(for: pr(state: "merged", conflicting: true)), .merged)
        XCTAssertEqual(prBadgeStyle(for: pr(state: "closed", conflicting: true)), .closed)
    }

    func testPRBadgeDraftConflictDoesNotHideConflict() {
        // Regression: a draft PR with a merge conflict must surface the conflict,
        // not be swallowed by the draft styling (matches the terminal overlay's
        // `#56d ⚠`). A plain draft stays a draft.
        XCTAssertEqual(prBadgeStyle(for: pr(state: "draft", conflicting: true)), .draftConflicting)
        XCTAssertEqual(prBadgeStyle(for: pr(state: "draft")), .draft)
    }

    // MARK: - CI visibility

    func testCIShownForOpenPRAndNoPR() {
        XCTAssertTrue(shouldShowCI(pr: pr(state: "open"), ci: ci(state: "passing")))
        XCTAssertTrue(shouldShowCI(pr: pr(state: "draft"), ci: ci(state: "failing")))
        XCTAssertTrue(shouldShowCI(pr: nil, ci: ci(state: "passing")))
    }

    func testCIHiddenForTerminalPRStates() {
        // The daemon keeps the last-known CI after a PR merges/closes; the badge
        // must not render that stale value beside a merged/closed PR.
        XCTAssertFalse(shouldShowCI(pr: pr(state: "merged"), ci: ci(state: "failing")))
        XCTAssertFalse(shouldShowCI(pr: pr(state: "closed"), ci: ci(state: "passing")))
    }

    func testCIHiddenWhenAbsent() {
        XCTAssertFalse(shouldShowCI(pr: pr(state: "open"), ci: nil))
        XCTAssertFalse(shouldShowCI(pr: nil, ci: nil))
    }

    // MARK: - CI badge style

    func testCIBadgeStyleFromState() {
        XCTAssertEqual(ciBadgeStyle(for: ci(state: "passing")), .passing)
        XCTAssertEqual(ciBadgeStyle(for: ci(state: "failing")), .failing)
        XCTAssertEqual(ciBadgeStyle(for: ci(state: "pending")), .pending)
    }

    func testCIBadgeUnknownStateIsPending() {
        // Anything we don't recognise falls back to the neutral "running" bucket.
        XCTAssertEqual(ciBadgeStyle(for: ci(state: "queued")), .pending)
    }

    // MARK: - Fixtures

    private func pr(state: String, conflicting: Bool = false) -> PRInfo {
        let c = conflicting ? ",\"conflicting\":true" : ""
        return try! JSONDecoder().decode(
            PRInfo.self,
            from: Data("{\"number\":42,\"state\":\"\(state)\"\(c)}".utf8)
        )
    }

    private func ci(state: String) -> CIInfo {
        try! JSONDecoder().decode(
            CIInfo.self,
            from: Data("{\"state\":\"\(state)\"}".utf8)
        )
    }
}
