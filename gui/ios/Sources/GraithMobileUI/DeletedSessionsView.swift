import SwiftUI
import GraithSessionKit
import GraithDesign

/// Recently-deleted (soft-deleted) sessions, with per-row Restore and permanent
/// Delete (purge) actions (#1148). Soft-deleted sessions live outside the polled
/// live list, so this fetches on appear and re-fetches after each action.
struct DeletedSessionsView: View {
    @ObservedObject var model: FleetModel
    @Environment(\.dismiss) private var dismiss

    @State private var rows: [HostedSession] = []
    @State private var loading = true
    @State private var purgeTarget: HostedSession?

    var body: some View {
        NavigationStack {
            content
                .navigationTitle("Recently Deleted")
                .toolbar {
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Done") { dismiss() }
                    }
                }
                .task { await reload() }
                .confirmationDialog(
                    "Permanently delete \u{201c}\(purgeTarget?.session.name ?? "")\u{201d}?",
                    isPresented: Binding(get: { purgeTarget != nil }, set: { if !$0 { purgeTarget = nil } }),
                    titleVisibility: .visible
                ) {
                    Button("Delete Permanently", role: .destructive) {
                        if let row = purgeTarget { purge(row) }
                        purgeTarget = nil
                    }
                    Button("Cancel", role: .cancel) { purgeTarget = nil }
                } message: {
                    Text("This removes the worktree, branch, and history immediately. This cannot be undone.")
                }
        }
        .preferredColorScheme(.dark)
        .tint(GraithDesign.accent)
    }

    @ViewBuilder
    private var content: some View {
        if loading {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if rows.isEmpty {
            GraithEmptyState(
                systemImage: "trash",
                title: "Nothing deleted",
                subtitle: "Soft-deleted sessions appear here until their recovery window expires."
            )
        } else {
            List {
                ForEach(rows) { row in
                    HStack {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(row.session.name).font(.body)
                            Text(row.session.repoName).font(.caption).foregroundStyle(.secondary)
                        }
                        Spacer()
                        Button("Restore") { restore(row) }
                            .buttonStyle(.borderless)
                    }
                    .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                        Button(role: .destructive) { purgeTarget = row } label: {
                            Label("Delete", systemImage: "trash.slash")
                        }
                    }
                }
            }
            .scrollContentBackground(.hidden)
            .background(GraithDesign.background)
        }
    }

    private func reload() async {
        loading = true
        rows = await model.deletedSessions()
        loading = false
    }

    private func restore(_ row: HostedSession) {
        model.restore(row.session, hostID: row.host.id)
        Task { await reloadAfterMutation() }
    }

    private func purge(_ row: HostedSession) {
        model.purge(row.session, hostID: row.host.id)
        Task { await reloadAfterMutation() }
    }

    /// The mutation fires a detached Task on the connection; give the daemon a
    /// brief beat to apply it before re-listing so the row drops out.
    private func reloadAfterMutation() async {
        try? await Task.sleep(nanoseconds: 250_000_000)
        await reload()
    }
}
