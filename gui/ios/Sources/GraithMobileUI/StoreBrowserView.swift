import SwiftUI
import GraithSessionKit
import GraithDesign

/// A read-only browser for the git-backed document store (`gr store`, #902).
/// Picks a host, lists every document it knows about (per-repo stores plus the
/// shared store) grouped by store, and drills into a document's body. Reads
/// route through the shared `HostConnection.storeList` / `storeGet`.
struct StoreBrowserView: View {
    @ObservedObject var model: FleetModel
    @Environment(\.dismiss) private var dismiss

    @State private var hostID: String = ""
    @State private var entries: [StoreEntryInfo] = []
    @State private var loading = false
    @State private var error: String?
    /// Bumped per list load so a slow earlier fetch (e.g. from a host we've
    /// since switched away from) can't overwrite a newer one.
    @State private var listGeneration = 0

    private var connection: HostConnection? {
        model.connections.first { $0.id == hostID } ?? model.connections.first
    }

    private var grouped: [(repo: String, entries: [StoreEntryInfo])] {
        Dictionary(grouping: entries, by: \.repo)
            .map { (repo: $0.key, entries: $0.value.sorted { $0.key < $1.key }) }
            .sorted { $0.repo < $1.repo }
    }

    var body: some View {
        NavigationStack {
            // A single List so the host picker stays visible in every state —
            // otherwise, with multiple hosts, an empty/errored first host would
            // hide the only control that can switch to a working one.
            List {
                if model.connections.count > 1 {
                    Section("Host") {
                        Picker("Daemon", selection: $hostID) {
                            ForEach(model.connections) { conn in
                                Text(conn.entry.label).tag(conn.id)
                            }
                        }
                        .onChange(of: hostID) { _ in Task { await loadList() } }
                    }
                }

                content
            }
            .navigationTitle("Document Store")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .task {
                if hostID.isEmpty { hostID = model.connections.first?.id ?? "" }
                await loadList()
            }
        }
    }

    @ViewBuilder
    private var content: some View {
        if loading {
            HStack { ProgressView(); Text("Loading…").foregroundStyle(.secondary) }
        } else if let error {
            Section {
                Text(error).foregroundStyle(.red).font(.footnote)
                Button("Retry") { Task { await loadList() } }
            }
        } else if entries.isEmpty {
            ContentUnavailableCompat(title: "No documents",
                                     systemImage: "tray",
                                     description: "The store has no documents yet.")
        } else {
            ForEach(grouped, id: \.repo) { group in
                Section(group.repo) {
                    ForEach(group.entries) { entry in
                        if let conn = connection {
                            NavigationLink {
                                StoreDocumentView(connection: conn, entry: entry)
                            } label: {
                                Text(entry.key)
                                    .font(GraithDesign.mono(.body))
                            }
                        }
                    }
                }
            }
        }
    }

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
        guard gen == listGeneration else { return } // superseded by a newer load
        entries = loaded
        if loaded.isEmpty {
            // Distinguish a genuinely empty store from a connection problem.
            if conn.state != .connected {
                error = "Host not connected."
            } else if let err = conn.lastError {
                error = err
            }
        }
        loading = false
    }
}

/// The body viewer for a single store document, fetched on appear.
struct StoreDocumentView: View {
    @ObservedObject var connection: HostConnection
    let entry: StoreEntryInfo

    @State private var body_ = ""
    @State private var loading = true
    @State private var error: String?

    var body: some View {
        Group {
            if loading {
                ProgressView()
            } else if let error {
                ContentUnavailableCompat(title: "Couldn't load document",
                                         systemImage: "exclamationmark.triangle",
                                         description: error)
            } else {
                ScrollView([.vertical, .horizontal]) {
                    Text(body_.isEmpty ? " " : body_)
                        .font(GraithDesign.mono(.footnote))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding()
                }
            }
        }
        .navigationTitle(entry.key)
        .task { await load() }
    }

    private func load() async {
        loading = true
        error = nil
        do {
            // The daemon treats repo == "shared" as the shared store, so the
            // entry's repo round-trips directly.
            let doc = try await connection.storeGet(repo: entry.repo, shared: false, key: entry.key)
            body_ = doc.body
        } catch {
            self.error = error.localizedDescription
        }
        loading = false
    }
}
