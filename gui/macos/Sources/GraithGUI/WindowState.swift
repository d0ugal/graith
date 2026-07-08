import Foundation
import Combine

/// Per-window UI state: which session is selected, the split layout, and
/// transient sheet flags. This is deliberately *not* on `SessionStore` — the
/// store is app-global (one daemon connection, one session list, shared by
/// every window), whereas selection and split are per-window so each window can
/// look at a different session. That's what makes ⌘⇧N ("New Window") and native
/// window tabbing actually useful rather than N identical clones.
///
/// Menu commands target the key window's `WindowState` via `@FocusedValue`
/// (see `AppCommands.swift`), so the menu bar drives the frontmost window only.
@MainActor
final class WindowState: ObservableObject {
    enum SplitPane {
        case primary, secondary
    }

    @Published var selectedSessionID: String?
    @Published var splitSessionID: String?
    @Published var isSplit: Bool = false
    /// Which pane is focused: .primary (left) or .secondary (right)
    @Published var focusedPane: SplitPane = .primary

    /// Transient UI flags driven by menu commands.
    @Published var showNewSession = false

    /// A one-shot find command, routed to *this* window's focused terminal.
    ///
    /// Find (⌘F/⌘G) used to broadcast over `NotificationCenter`, so every
    /// terminal in every window toggled its search bar at once. Routing through
    /// per-window state (published as a `@FocusedValue`, mutated only for the
    /// key window) confines it to the frontmost window; the focused pane picks
    /// it up. `seq` makes each dispatch distinct so repeated presses of the
    /// same command still fire `onChange`.
    struct FindCommand: Equatable {
        enum Action { case toggle, next, previous }
        let action: Action
        let seq: Int
    }

    @Published private(set) var findCommand: FindCommand?
    private var findSeq = 0

    func dispatchFind(_ action: FindCommand.Action) {
        findSeq += 1
        findCommand = FindCommand(action: action, seq: findSeq)
    }

    func selectedSession(in sessions: [Session]) -> Session? {
        guard let id = selectedSessionID else { return nil }
        return sessions.first { $0.id == id }
    }

    func splitSession(in sessions: [Session]) -> Session? {
        guard let id = splitSessionID else { return nil }
        return sessions.first { $0.id == id }
    }

    func selectSession(_ session: Session) {
        if isSplit && focusedPane == .secondary {
            if session.id != selectedSessionID {
                splitSessionID = session.id
            }
        } else {
            if session.id != splitSessionID || !isSplit {
                selectedSessionID = session.id
            }
        }
    }

    // MARK: - Split Pane

    /// Open the split view. The right pane starts with the next available
    /// session (different from the primary) to avoid single-attach conflicts.
    func splitRight(in sessions: [Session]) {
        guard !isSplit else { return }
        isSplit = true
        let sorted = sessions.sorted { $0.name < $1.name }
        splitSessionID = sorted.first { $0.id != selectedSessionID }?.id
        focusedPane = .secondary
    }

    /// Close the split view. The primary selection is preserved.
    func closeSplit() {
        isSplit = false
        splitSessionID = nil
        focusedPane = .primary
    }

    func toggleSplit(in sessions: [Session]) {
        if isSplit { closeSplit() } else { splitRight(in: sessions) }
    }

    // MARK: - Navigation

    func selectSessionByIndex(_ index: Int, in sessions: [Session]) {
        let flat = sessions.sorted { $0.name < $1.name }
        guard index >= 0 && index < flat.count else { return }
        selectSession(flat[index])
    }

    func selectAdjacentSession(offset: Int, in sessions: [Session]) {
        let flat = sessions.sorted { $0.name < $1.name }
        guard !flat.isEmpty else { return }
        let currentID = isSplit && focusedPane == .secondary ? splitSessionID : selectedSessionID
        guard let currentID, let idx = flat.firstIndex(where: { $0.id == currentID }) else {
            selectSession(flat.first!)
            return
        }
        var newIdx = (idx + offset + flat.count) % flat.count
        let otherID = isSplit ? (focusedPane == .secondary ? selectedSessionID : splitSessionID) : nil
        if let otherID, flat[newIdx].id == otherID {
            newIdx = (newIdx + offset + flat.count) % flat.count
        }
        selectSession(flat[newIdx])
    }

    // MARK: - Lifecycle sync

    /// Drop selections that point at sessions no longer present (e.g. deleted
    /// in another window). Called when the store's session list changes.
    func prune(against sessions: [Session]) {
        let ids = Set(sessions.map(\.id))
        if let id = selectedSessionID, !ids.contains(id) { selectedSessionID = nil }
        if let id = splitSessionID, !ids.contains(id) { splitSessionID = nil }
    }
}
