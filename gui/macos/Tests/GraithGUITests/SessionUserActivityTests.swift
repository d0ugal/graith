import XCTest
import GraithProtocol
@testable import GraithGUI

final class SessionUserActivityTests: XCTestCase {
    func testConfigurationClearsWebpageURLAndReplacesMetadata() throws {
        let session = try JSONDecoder().decode(SessionInfo.self, from: Data("""
        {"id":"braw","name":"Braw","repo_path":"/croft","repo_name":"croft",
         "worktree_path":"/bothy","branch":"b","base_branch":"main","agent":"codex",
         "status":"running","created_at":"now"}
        """.utf8))
        let activity = NSUserActivity(activityType: SessionUserActivity.activityType)
        activity.webpageURL = URL(string: "https://example.invalid/auld")
        activity.userInfo = ["auld": "stale"]

        SessionUserActivity.configure(activity, for: session)

        XCTAssertNil(activity.webpageURL, "custom URL schemes are invalid for NSUserActivity.webpageURL")
        XCTAssertEqual(activity.title, "Braw")
        XCTAssertEqual(activity.targetContentIdentifier, "graith://local/braw")
        XCTAssertEqual(activity.userInfo?["sessionID"] as? String, "braw")
        XCTAssertEqual(activity.userInfo?["sessionName"] as? String, "Braw")
        XCTAssertEqual(activity.userInfo?["repoName"] as? String, "croft")
        XCTAssertEqual(activity.userInfo?[SessionUserActivity.sessionURLKey] as? String, "graith://local/braw")
        XCTAssertNil(activity.userInfo?["auld"])
        XCTAssertTrue(activity.isEligibleForHandoff)
    }
}
