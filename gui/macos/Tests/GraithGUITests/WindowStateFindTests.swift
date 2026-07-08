import XCTest
@testable import GraithGUI

/// Covers the fix that routes Find (⌘F/⌘G) through per-window state instead of
/// a global `NotificationCenter` broadcast. The command must produce a distinct,
/// observable signal each time so repeated presses re-fire, and it must carry
/// the right action.
@MainActor
final class WindowStateFindTests: XCTestCase {
    func testDispatchFindSetsActionAndDistinctSeq() {
        let window = WindowState()
        XCTAssertNil(window.findCommand)

        window.dispatchFind(.toggle)
        let first = window.findCommand
        XCTAssertEqual(first?.action, .toggle)

        // A second identical action must still change the value (distinct seq),
        // so SwiftUI's onChange fires on every keypress.
        window.dispatchFind(.toggle)
        let second = window.findCommand
        XCTAssertEqual(second?.action, .toggle)
        XCTAssertNotEqual(first, second, "repeated Find must produce a fresh signal")
    }

    func testDispatchFindActionsAreDistinguished() {
        let window = WindowState()
        window.dispatchFind(.next)
        XCTAssertEqual(window.findCommand?.action, .next)
        window.dispatchFind(.previous)
        XCTAssertEqual(window.findCommand?.action, .previous)
    }

    /// Two windows are independent: dispatching Find in one leaves the other's
    /// command untouched — the essence of "route to the key window only".
    func testFindIsPerWindow() {
        let keyWindow = WindowState()
        let otherWindow = WindowState()

        keyWindow.dispatchFind(.toggle)

        XCTAssertNotNil(keyWindow.findCommand)
        XCTAssertNil(otherWindow.findCommand, "Find must not leak to other windows")
    }
}
