import XCTest
import GraithProtocol
@testable import GraithGUI

/// Covers the repo picker ordering for the new-session form (#1130, design §C.4):
/// recent repos first, then alphabetical by name, regardless of the daemon's
/// order.
final class RepoOrderingTests: XCTestCase {
    // `RepoEntry`'s memberwise init is internal to GraithProtocol, so build
    // fixtures from the wire JSON.
    private func repos(_ json: String) throws -> [RepoEntry] {
        try JSONDecoder().decode([RepoEntry].self, from: Data(json.utf8))
    }

    func testRecentFirstThenAlphabetical() throws {
        let input = try repos("""
        [{"path":"/code/whin","name":"whin"},
         {"path":"/code/braw","name":"braw"},
         {"path":"/code/croft","name":"croft","recent":true},
         {"path":"/code/bothy","name":"bothy","recent":true}]
        """)

        let ordered = SessionStore.orderedRepos(input).map(\.name)
        // Recent group (bothy, croft) sorts ahead of the rest (braw, whin), each
        // alphabetical within its group.
        XCTAssertEqual(ordered, ["bothy", "croft", "braw", "whin"])
    }

    func testCaseInsensitiveNameSort() throws {
        let input = try repos("""
        [{"path":"/a","name":"Ben"},{"path":"/b","name":"brae"},{"path":"/c","name":"Auld"}]
        """)
        XCTAssertEqual(SessionStore.orderedRepos(input).map(\.name), ["Auld", "Ben", "brae"])
    }

    func testEmptyStaysEmpty() throws {
        XCTAssertTrue(SessionStore.orderedRepos([]).isEmpty)
    }

    func testMissingRecentTreatedAsNotRecent() throws {
        let input = try repos("""
        [{"path":"/a","name":"neep","recent":true},{"path":"/b","name":"kail"}]
        """)
        // `kail` has no `recent` key → not recent → sorts after the recent `neep`.
        XCTAssertEqual(SessionStore.orderedRepos(input).map(\.name), ["neep", "kail"])
    }
}
