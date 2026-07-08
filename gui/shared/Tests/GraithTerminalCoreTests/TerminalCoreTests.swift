import Foundation
import Testing
@testable import GraithTerminalCore

struct TerminalCoreTests {
    /// Writing a VT transcript renders into the grid: the written text appears
    /// in the visible rows (exercises libghostty-vt parse + render state).
    @Test func writeRendersToGrid() {
        let term = GhosttyTerminalState(cols: 80, rows: 24)
        term.write("braw bothy\r\n")
        _ = term.updateRenderState()
        let lines = term.getVisibleText()
        #expect(lines.contains { $0.contains("braw bothy") })
    }

    /// Resizing keeps the terminal usable and re-renders prior content.
    @Test func resizePreservesContent() {
        let term = GhosttyTerminalState(cols: 80, rows: 24)
        term.write("canny glen\r\n")
        term.resize(cols: 100, rows: 40, cellWidth: 8, cellHeight: 16)
        _ = term.updateRenderState()
        let lines = term.getVisibleText()
        #expect(lines.contains { $0.contains("canny glen") })
    }

    /// Key encoding produces the expected control bytes: Enter -> CR (0x0D).
    @Test func encodeEnterKey() {
        let term = GhosttyTerminalState(cols: 80, rows: 24)
        let data = term.encodeKey(action: GHOSTTY_KEY_ACTION_PRESS, key: GHOSTTY_KEY_ENTER, mods: 0, text: nil)
        #expect(data == Data([0x0D]))
    }

    /// Ctrl+C encodes to ETX (0x03).
    @Test func encodeCtrlC() {
        let term = GhosttyTerminalState(cols: 80, rows: 24)
        let mods = GhosttyMods(GHOSTTY_MODS_CTRL)
        let data = term.encodeKey(action: GHOSTTY_KEY_ACTION_PRESS, key: GHOSTTY_KEY_C, mods: mods, text: "c")
        #expect(data == Data([0x03]))
    }

    /// Printable text encodes to its UTF-8 bytes.
    @Test func encodePrintable() {
        let term = GhosttyTerminalState(cols: 80, rows: 24)
        let data = term.encodeKey(action: GHOSTTY_KEY_ACTION_PRESS, key: GHOSTTY_KEY_A, mods: 0, text: "a")
        #expect(data == Data("a".utf8))
    }

    /// The search state finds matches in supplied visible text.
    @MainActor
    @Test func searchFindsMatches() {
        let search = TerminalSearchState()
        search.getVisibleText = { ["the dreich haar", "over the loch", "a dreich day"] }
        search.query = "dreich"
        search.search()
        #expect(search.matchCount == 2)
        #expect(search.currentMatch == 1)
        search.findNext()
        #expect(search.currentMatch == 2)
        search.findNext() // wraps
        #expect(search.currentMatch == 1)
    }
}
