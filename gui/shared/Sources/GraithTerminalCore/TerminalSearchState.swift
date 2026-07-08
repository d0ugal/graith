import Combine
import Foundation

/// Shared search state between a search-bar UI and the terminal view.
///
/// Platform-neutral: the SwiftUI search bar lives in each app, but the matching
/// logic and published state are shared. The terminal view provides text (via
/// ``getVisibleText``); this performs matching.
@MainActor
public final class TerminalSearchState: ObservableObject {
    @Published public var isVisible = false
    @Published public var query = ""
    @Published public var matchCount = 0
    @Published public var currentMatch = 0  // 1-based index of the current match

    /// Callback to retrieve visible terminal text lines.
    public var getVisibleText: (() -> [String])?
    /// Callback to scroll the terminal viewport by a delta.
    public var scrollViewport: ((Int) -> Void)?
    /// Callback to force a view redraw (bypasses terminal dirty state).
    public var onSearchChanged: (() -> Void)?

    public struct Match {
        public let row: Int
        public let col: Int
        public let length: Int
    }

    public private(set) var matches: [Match] = []

    public init() {}

    public func toggle() {
        isVisible.toggle()
        if !isVisible {
            query = ""
            matches = []
            matchCount = 0
            currentMatch = 0
            onSearchChanged?()
        }
    }

    public func show() {
        isVisible = true
    }

    public func hide() {
        isVisible = false
        query = ""
        matches = []
        matchCount = 0
        currentMatch = 0
        onSearchChanged?()
    }

    public func search() {
        guard !query.isEmpty, let getVisibleText else {
            matches = []
            matchCount = 0
            currentMatch = 0
            return
        }

        let lines = getVisibleText()
        let needle = query.lowercased()
        var found: [Match] = []

        for (rowIndex, line) in lines.enumerated() {
            let lower = line.lowercased()
            var searchStart = lower.startIndex
            while let range = lower.range(of: needle, range: searchStart..<lower.endIndex) {
                let col = lower.distance(from: lower.startIndex, to: range.lowerBound)
                found.append(Match(row: rowIndex, col: col, length: query.count))
                searchStart = range.upperBound
            }
        }

        matches = found
        matchCount = found.count
        currentMatch = found.isEmpty ? 0 : 1
        onSearchChanged?()
    }

    public func findNext() {
        guard !matches.isEmpty else { return }
        if currentMatch < matchCount {
            currentMatch += 1
        } else {
            currentMatch = 1
        }
        onSearchChanged?()
    }

    public func findPrevious() {
        guard !matches.isEmpty else { return }
        if currentMatch > 1 {
            currentMatch -= 1
        } else {
            currentMatch = matchCount
        }
        onSearchChanged?()
    }
}
