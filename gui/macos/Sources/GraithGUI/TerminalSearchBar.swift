import SwiftUI
import GraithTerminalCore

/// A compact search bar that sits at the top of the terminal area. The search
/// state itself lives in `GraithTerminalCore.TerminalSearchState` (shared).
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
        // Find Next/Previous are routed to the focused terminal via WindowState
        // (see TerminalContainer), not a global NotificationCenter broadcast.
    }
}
