import SwiftUI
import GraithProtocol
import GraithSessionKit

/// A read-only browser for the git-backed document store (`gr store`, #902).
/// Lists every document the selected host knows about (per-repo stores plus the
/// shared store), grouped by store, and shows a selected document's body. Reads
/// route through the shared `HostConnection.storeList` / `storeGet`.
struct StoreBrowserSheet: View {
    @EnvironmentObject var store: SessionStore
    @Environment(\.dismiss) private var dismiss

    @State private var hostID: String = ""
    @State private var entries: [StoreEntryInfo] = []
    @State private var loading = true
    @State private var error: String?
    /// Bumped per list load so a slow earlier fetch can't overwrite a newer one.
    @State private var listGeneration = 0

    @State private var selected: StoreEntryInfo?
    @State private var body_ = ""
    @State private var loadingBody = false
    @State private var bodyError: String?
    @State private var bodyGeneration = 0

    private var connection: HostConnection? {
        store.connections.first { $0.id == hostID } ?? store.connections.first
    }

    private var grouped: [(repo: String, entries: [StoreEntryInfo])] {
        Dictionary(grouping: entries, by: \.repo)
            .map { (repo: $0.key, entries: $0.value.sorted { $0.key < $1.key }) }
            .sorted { $0.repo < $1.repo }
    }

    var body: some View {
        VStack(spacing: 0) {
            header

            Divider().background(Theme.surface0)

            if store.connections.count > 1 {
                hostPicker
                Divider().background(Theme.surface0)
            }

            if loading {
                loadingView
            } else if let error {
                PeekError(message: error)
            } else if entries.isEmpty {
                PeekEmpty(systemImage: "tray", message: "No documents in the store.")
            } else {
                HStack(spacing: 0) {
                    documentList
                        .frame(width: 280)
                    Divider().background(Theme.surface0)
                    documentViewer
                        .frame(maxWidth: .infinity)
                }
            }
        }
        .frame(width: 860, height: 600)
        .background(Theme.mantle)
        .task {
            if hostID.isEmpty { hostID = store.connections.first?.id ?? "" }
            await loadList()
        }
    }

    // MARK: - Header + host picker

    private var header: some View {
        PeekHeader(title: "Document Store", subtitle: connection?.entry.label ?? "store",
                   reloadDisabled: loading,
                   reload: { Task { await loadList() } }) { dismiss() }
    }

    private var hostPicker: some View {
        HStack {
            Text("Host")
                .font(.system(.caption, design: .monospaced))
                .foregroundStyle(Theme.overlay0)
            Picker("Host", selection: $hostID) {
                ForEach(store.connections) { conn in
                    Text(conn.entry.label).tag(conn.id)
                }
            }
            .labelsHidden()
            .onChange(of: hostID) { _, _ in
                selected = nil
                Task { await loadList() }
            }
            Spacer()
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 8)
    }

    private var loadingView: some View {
        VStack { ProgressView().controlSize(.large) }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    // MARK: - Document list

    private var documentList: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                ForEach(grouped, id: \.repo) { group in
                    Text(group.repo)
                        .font(.system(.caption2, design: .monospaced))
                        .fontWeight(.semibold)
                        .foregroundStyle(Theme.overlay0)
                        .padding(.horizontal, 12)
                        .padding(.top, 10)
                        .padding(.bottom, 4)

                    ForEach(group.entries) { entry in
                        Button {
                            selected = entry
                            Task { await loadBody(entry) }
                        } label: {
                            Text(entry.key)
                                .font(.system(size: 11, design: .monospaced))
                                .foregroundStyle(selected == entry ? Theme.text : Theme.subtext1)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .padding(.horizontal, 12)
                                .padding(.vertical, 5)
                                .background(selected == entry ? Theme.surface0 : Color.clear)
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
            .padding(.vertical, 4)
        }
        .background(Theme.crust)
    }

    // MARK: - Document viewer

    private var documentViewer: some View {
        Group {
            if loadingBody {
                loadingView
            } else if let bodyError {
                PeekError(message: bodyError)
            } else if selected == nil {
                PeekEmpty(systemImage: "doc.text", message: "Select a document.")
            } else {
                ScrollView([.vertical, .horizontal]) {
                    Text(body_.isEmpty ? " " : body_)
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundStyle(Theme.subtext1)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(12)
                }
                .background(Theme.crust)
            }
        }
    }

    // MARK: - Loading

    private func loadList() async {
        listGeneration += 1
        let gen = listGeneration
        loading = true
        error = nil
        guard let conn = connection else {
            error = "No host connected."
            loading = false
            return
        }
        // repo=nil + shared=false lists every store (all repo stores + shared).
        let loaded = await conn.storeList(repo: nil, shared: false, prefix: nil)
        guard gen == listGeneration else { return }
        entries = loaded
        if loaded.isEmpty {
            // Distinguish a genuinely empty store from a connection problem so
            // a disconnected host doesn't masquerade as "No documents".
            if conn.state != .connected {
                error = "Host not connected."
            } else if let err = conn.lastError {
                error = err
            }
        }
        loading = false
    }

    private func loadBody(_ entry: StoreEntryInfo) async {
        bodyGeneration += 1
        let gen = bodyGeneration
        loadingBody = true
        bodyError = nil
        guard let conn = connection else {
            bodyError = "No host connected."
            loadingBody = false
            return
        }
        do {
            // The daemon treats repo == "shared" as the shared store, so the
            // entry's repo round-trips directly.
            let doc = try await conn.storeGet(repo: entry.repo, shared: false, key: entry.key)
            guard gen == bodyGeneration else { return }
            body_ = doc.body
        } catch {
            guard gen == bodyGeneration else { return }
            bodyError = error.localizedDescription
        }
        loadingBody = false
    }
}
