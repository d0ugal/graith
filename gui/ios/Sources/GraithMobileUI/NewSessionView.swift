import SwiftUI
import GraithSessionKit

/// Remote session creation. There is no local cwd on a phone, so the repo comes
/// from a `repo_list`-backed picker (design §C.4), scoped to a chosen host.
struct NewSessionView: View {
    @ObservedObject var model: FleetModel
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

    // Advanced options (mirror `gr new` flags).
    @State private var base: String = ""
    @State private var yolo = false
    @State private var inPlace = false
    @State private var agentHooks = true

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
                                    if repo.isRecent { Text("recent").font(.caption2).foregroundStyle(.secondary) }
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
                Section("Advanced") {
                    TextField("Base branch (optional)", text: $base)
                        .textFieldStyleCompat()
                        .disabled(inPlace)
                    Toggle("Yolo mode", isOn: $yolo)
                    Toggle("Run in place", isOn: $inPlace)
                    Toggle("Agent hooks", isOn: $agentHooks)
                        .disabled(yolo)
                    if yolo {
                        Text("Yolo mode requires agent hooks, so they stay on.")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                }
                // Base doesn't apply in-place (no branch created); clear the stale
                // value rather than leaving it to fail validation on Create.
                .onChange(of: inPlace) { on in if on { base = "" } }
                // Yolo forces agent hooks on daemon-side; mirror that in the UI.
                .onChange(of: yolo) { on in if on { agentHooks = true } }
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
        let requestedHostID = hostID
        guard let conn = model.connections.first(where: { $0.id == requestedHostID }) else { return }
        loadingRepos = true
        defer { loadingRepos = false }
        let loaded = await conn.repoList()
        // Drop a late response for a host we've since switched away from —
        // otherwise a slow host's repos could overwrite the current host's list
        // (and selection) after the user has moved on.
        guard requestedHostID == hostID else { return }
        repos = loaded
        selectedRepo = RepoPickerLogic.resolveSelection(repos: loaded, current: selectedRepo)
    }

    private func create() async {
        guard let conn = model.connections.first(where: { $0.id == hostID }) else { return }
        // Normalise once so validation and the wire agree that a whitespace-only
        // base is absent (otherwise it slips past the guard and the daemon rejects
        // it after a round-trip).
        let trimmedBase = base.trimmingCharacters(in: .whitespacesAndNewlines)
        // Surface mutually-exclusive options before a daemon round-trip.
        if let invalid = FleetModel.validateCreateOptions(base: trimmedBase, inPlace: inPlace) {
            error = invalid
            return
        }
        submitting = true
        defer { submitting = false }
        let req = CreateRequest(
            name: name.trimmingCharacters(in: .whitespaces),
            agent: agent,
            repoPath: selectedRepo,
            base: trimmedBase.isEmpty ? nil : trimmedBase,
            prompt: prompt.isEmpty ? nil : prompt,
            // Yolo forces agent hooks on daemon-side; send the effective value.
            agentHooks: agentHooks || yolo,
            inPlace: inPlace ? true : nil,
            yolo: yolo ? true : nil
        )
        if await conn.create(req) {
            dismiss()
        } else {
            error = conn.lastError ?? "Create failed."
        }
    }
}

/// Pure repo-picker selection logic, split out of the SwiftUI view so it can be
/// exercised without driving SwiftUI (see GraithMobileSmoke — the CLT toolchain
/// has no XCTest).
public enum RepoPickerLogic {
    /// Choose which repo the picker should have selected after a (re)load. Keep
    /// the current selection only if this host still offers it (an unchanged
    /// reload); otherwise fall back to a recent repo, then the first. Without
    /// this, switching hosts leaves the selection pointing at a path the new
    /// host doesn't list, so the picker shows nothing selected and Create stays
    /// disabled (#896).
    public static func resolveSelection(repos: [RepoEntry], current: String) -> String {
        if repos.contains(where: { $0.path == current }) { return current }
        return repos.first(where: { $0.isRecent })?.path ?? repos.first?.path ?? ""
    }
}

extension View {
    /// `.textFieldStyle(.roundedBorder)` is unavailable on macOS the same way;
    /// keep it plain and cross-platform.
    func textFieldStyleCompat() -> some View { self }
}
