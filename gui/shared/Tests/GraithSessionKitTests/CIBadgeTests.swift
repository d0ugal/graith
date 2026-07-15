import Testing
import Foundation
import GraithProtocol
@testable import GraithSessionKit

// Exercises the shared CI-badge presentation logic (#1173): the style bucket
// mapping, the merged/closed visibility rule, and the passed/total progress
// count text that both GUIs render. Decoded from wire JSON so the CodingKeys
// (passed/total) the badges depend on are exercised too.

@Suite("CIBadge — shared presentation logic (#1173)")
struct CIBadgeTests {
    private func ci(_ json: String) throws -> CIInfo {
        try JSONDecoder().decode(CIInfo.self, from: Data(json.utf8))
    }

    private func pr(state: String) throws -> PRInfo {
        try JSONDecoder().decode(PRInfo.self, from: Data("{\"number\":7,\"state\":\"\(state)\"}".utf8))
    }

    // MARK: - Style buckets

    @Test func styleFromState() throws {
        #expect(ciBadgeStyle(for: try ci("{\"state\":\"passing\"}")) == .passing)
        #expect(ciBadgeStyle(for: try ci("{\"state\":\"failing\"}")) == .failing)
        #expect(ciBadgeStyle(for: try ci("{\"state\":\"pending\"}")) == .pending)
    }

    @Test func unknownStateIsPending() throws {
        // A daemon state we don't recognise falls back to the neutral bucket
        // rather than blanking the badge.
        #expect(ciBadgeStyle(for: try ci("{\"state\":\"queued\"}")) == .pending)
    }

    // MARK: - Visibility

    @Test func shownForOpenDraftAndNoPR() throws {
        #expect(shouldShowCI(pr: try pr(state: "open"), ci: try ci("{\"state\":\"passing\"}")))
        #expect(shouldShowCI(pr: try pr(state: "draft"), ci: try ci("{\"state\":\"failing\"}")))
        #expect(shouldShowCI(pr: nil, ci: try ci("{\"state\":\"pending\"}")))
    }

    @Test func hiddenForTerminalPRStates() throws {
        // The daemon keeps the last-known CI after a PR merges/closes; the badge
        // must not render that stale value (#773).
        #expect(!shouldShowCI(pr: try pr(state: "merged"), ci: try ci("{\"state\":\"failing\"}")))
        #expect(!shouldShowCI(pr: try pr(state: "closed"), ci: try ci("{\"state\":\"passing\"}")))
    }

    @Test func hiddenWhenAbsent() throws {
        #expect(!shouldShowCI(pr: try pr(state: "open"), ci: nil))
        #expect(!shouldShowCI(pr: nil, ci: nil))
    }

    // MARK: - Progress count text

    @Test func progressTextFromCounts() throws {
        let c = try ci("{\"state\":\"pending\",\"passed\":16,\"total\":22}")
        #expect(c.progressText == "16/22")
    }

    @Test func progressTextNilWhenNoCount() throws {
        // Total omitted (nil) — mirrors an old daemon or a badge set before the
        // first check resolves.
        #expect(try ci("{\"state\":\"pending\"}").progressText == nil)
        // A present-but-zero total is also "no count" (fall back to the glyph).
        #expect(try ci("{\"state\":\"pending\",\"total\":0}").progressText == nil)
    }

    @Test func progressTextDefaultsPassedToZero() throws {
        // total present, passed omitted → "0/N" (nothing green yet).
        #expect(try ci("{\"state\":\"pending\",\"total\":22}").progressText == "0/22")
    }

    // MARK: - Badge count label (what the row actually shows)

    @Test func pendingShowsCount() throws {
        #expect(try ci("{\"state\":\"pending\",\"passed\":16,\"total\":22}").badgeCountText == "16/22")
    }

    @Test func failingShowsCount() throws {
        #expect(try ci("{\"state\":\"failing\",\"passed\":19,\"total\":22}").badgeCountText == "19/22")
    }

    @Test func passingShowsNoCount() throws {
        // Passing keeps the bare ✓ — a "22/22" adds no progress info once done.
        #expect(try ci("{\"state\":\"passing\",\"passed\":22,\"total\":22}").badgeCountText == nil)
    }

    @Test func countlessBadgeFallsBackToGlyph() throws {
        // No count available → nil, so the view renders the glyph alone.
        #expect(try ci("{\"state\":\"pending\"}").badgeCountText == nil)
        #expect(try ci("{\"state\":\"failing\"}").badgeCountText == nil)
    }
}
