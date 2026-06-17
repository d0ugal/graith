import SwiftUI
import AppKit

/// Shared search state between the search bar UI and the terminal view.
/// The terminal view provides text; the search bar performs matching.
@MainActor
final class TerminalSearchState: ObservableObject {
    @Published var isVisible = false
    @Published var query = ""
    @Published var matchCount = 0
    @Published var currentMatch = 0  // 1-based index of the current match

    /// Callback to retrieve visible terminal text lines.
    var getVisibleText: (() -> [String])?
    /// Callback to scroll the terminal viewport by a delta.
    var scrollViewport: ((Int) -> Void)?
    /// Callback to force a view redraw (bypasses terminal dirty state).
    var onSearchChanged: (() -> Void)?

    struct Match {
        let row: Int
        let col: Int
        let length: Int
    }

    private(set) var matches: [Match] = []

    func toggle() {
        isVisible.toggle()
        if !isVisible {
            query = ""
            matches = []
            matchCount = 0
            currentMatch = 0
            onSearchChanged?()
        }
    }

    func show() {
        isVisible = true
    }

    func hide() {
        isVisible = false
        query = ""
        matches = []
        matchCount = 0
        currentMatch = 0
        onSearchChanged?()
    }

    func search() {
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

    func findNext() {
        guard !matches.isEmpty else { return }
        if currentMatch < matchCount {
            currentMatch += 1
        } else {
            currentMatch = 1
        }
        scrollToCurrentMatch()
        onSearchChanged?()
    }

    func findPrevious() {
        guard !matches.isEmpty else { return }
        if currentMatch > 1 {
            currentMatch -= 1
        } else {
            currentMatch = matchCount
        }
        scrollToCurrentMatch()
        onSearchChanged?()
    }

    private func scrollToCurrentMatch() {
        // All matches are within the visible viewport (search covers visible
        // text only), so no scrolling is needed. When search is expanded to
        // cover scrollback, this should scroll to bring the match row into view.
    }
}

/// A compact search bar that sits at the top of the terminal area.
struct TerminalSearchBar: View {
    @ObservedObject var searchState: TerminalSearchState
    @FocusState private var isFocused: Bool

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: "magnifyingglass")
                .foregroundStyle(Theme.overlay0)
                .font(.system(size: 12))

            TextField("Find in terminal...", text: $searchState.query)
                .textFieldStyle(.plain)
                .font(.system(.body, design: .monospaced))
                .foregroundStyle(Theme.text)
                .focused($isFocused)
                .onSubmit {
                    searchState.findNext()
                }
                .onChange(of: searchState.query) { _, _ in
                    searchState.search()
                }

            if searchState.matchCount > 0 {
                Text("\(searchState.currentMatch)/\(searchState.matchCount)")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.subtext0)
                    .frame(minWidth: 40)
            } else if !searchState.query.isEmpty {
                Text("No matches")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
            }

            Button(action: { searchState.findPrevious() }) {
                Image(systemName: "chevron.up")
                    .font(.system(size: 11, weight: .medium))
            }
            .buttonStyle(.plain)
            .foregroundStyle(Theme.subtext0)
            .disabled(searchState.matchCount == 0)

            Button(action: { searchState.findNext() }) {
                Image(systemName: "chevron.down")
                    .font(.system(size: 11, weight: .medium))
            }
            .buttonStyle(.plain)
            .foregroundStyle(Theme.subtext0)
            .disabled(searchState.matchCount == 0)

            Button(action: { searchState.hide() }) {
                Image(systemName: "xmark")
                    .font(.system(size: 11, weight: .medium))
            }
            .buttonStyle(.plain)
            .foregroundStyle(Theme.overlay0)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(Theme.surface0)
        .onExitCommand {
            searchState.hide()
        }
        .onAppear {
            isFocused = true
        }
        .onReceive(NotificationCenter.default.publisher(for: .findNext)) { _ in
            searchState.findNext()
        }
        .onReceive(NotificationCenter.default.publisher(for: .findPrevious)) { _ in
            searchState.findPrevious()
        }
    }
}
