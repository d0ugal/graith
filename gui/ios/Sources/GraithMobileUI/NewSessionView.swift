import SwiftUI
import GraithClientAPI

/// Remote session creation. There is no local cwd on a phone, so the repo comes
/// from a `repo_list`-backed picker (design §C.4), scoped to a chosen host.
struct NewSessionView: View {
    @ObservedObject var model: AppModel
    @Environment(\.dismiss) private var dismiss

    @State private var hostID: String = ""
    @State private var name: String = ""
    @State private var agent: String = "claude"
    @State private var prompt: String = ""
    @State private var repos: [RepoEntry] = []
    @State private var selectedRepo: String = ""
    @State private var loadingRepos = false
    @State private var submitting = false
    @State private var error: String?

    private let agents = ["claude", "codex", "cursor", "opencode"]

    var body: some View {
        NavigationStack {
            Form {
                Section("Host") {
                    Picker("Daemon", selection: $hostID) {
                        ForEach(model.connections) { conn in
                            Text(conn.entry.label).tag(conn.id)
                        }
                    }
                }
                Section("Repository") {
                    if loadingRepos {
                        ProgressView()
                    } else if repos.isEmpty {
                        Text("No repositories reported by this daemon.")
                            .foregroundStyle(.secondary)
                    } else {
                        Picker("Repo", selection: $selectedRepo) {
                            ForEach(repos) { repo in
                                HStack {
                                    Text(repo.name)
                                    if repo.recent { Text("recent").font(.caption2).foregroundStyle(.secondary) }
                                }.tag(repo.path)
                            }
                        }
                    }
                }
                Section("Session") {
                    TextField("Name", text: $name)
                        .textFieldStyleCompat()
                    Picker("Agent", selection: $agent) {
                        ForEach(agents, id: \.self) { Text($0).tag($0) }
                    }
                    TextField("Prompt (optional)", text: $prompt, axis: .vertical)
                        .lineLimit(3, reservesSpace: true)
                }
                if let error {
                    Text(error).foregroundStyle(.red).font(.footnote)
                }
            }
            .navigationTitle("New Session")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Create") { Task { await create() } }
                        .disabled(!canCreate || submitting)
                }
            }
            .task {
                if hostID.isEmpty { hostID = model.connections.first?.id ?? "" }
                await loadRepos()
            }
            .onChange(of: hostID) { _ in Task { await loadRepos() } }
        }
    }

    private var canCreate: Bool {
        !hostID.isEmpty && !selectedRepo.isEmpty && !name.trimmingCharacters(in: .whitespaces).isEmpty
    }

    private func loadRepos() async {
        guard let conn = model.connections.first(where: { $0.id == hostID }) else { return }
        loadingRepos = true
        defer { loadingRepos = false }
        repos = await conn.repoList()
        selectedRepo = Self.resolveSelection(repos: repos, current: selectedRepo)
    }

    /// Choose which repo the picker should have selected after a (re)load. Keep
    /// the current selection only if this host still offers it (an unchanged
    /// reload); otherwise fall back to a recent repo, then the first. Without
    /// this, switching hosts leaves the selection pointing at a path the new
    /// host doesn't list, so the picker shows nothing selected and Create stays
    /// disabled (#896).
    static func resolveSelection(repos: [RepoEntry], current: String) -> String {
        if repos.contains(where: { $0.path == current }) { return current }
        return repos.first(where: { $0.recent })?.path ?? repos.first?.path ?? ""
    }

    private func create() async {
        guard let conn = model.connections.first(where: { $0.id == hostID }) else { return }
        submitting = true
        defer { submitting = false }
        let req = CreateRequest(
            name: name.trimmingCharacters(in: .whitespaces),
            agent: agent,
            repoPath: selectedRepo,
            prompt: prompt.isEmpty ? nil : prompt
        )
        if await conn.create(req) {
            dismiss()
        } else {
            error = conn.lastError ?? "Create failed."
        }
    }
}

extension View {
    /// `.textFieldStyle(.roundedBorder)` is unavailable on macOS the same way;
    /// keep it plain and cross-platform.
    func textFieldStyleCompat() -> some View { self }
}
