import SwiftUI
import GraithProtocol

struct NewSessionSheet: View {
    @EnvironmentObject var store: SessionStore
    @EnvironmentObject var window: WindowState
    @Environment(\.dismiss) private var dismiss

    @AppStorage("defaultAgent") private var defaultAgent = "claude"
    @State private var name = ""
    @State private var repoPath = ""
    @State private var agent = "claude"
    @State private var model = ""
    @State private var prompt = ""
    @State private var isCreating = false
    @State private var error: String?
    @State private var selectedHostID = "local"
    /// Repos the selected host offers (design §C.4). Populated on appear / host
    /// change; the free-text field remains for paths the daemon didn't list.
    @State private var repos: [RepoEntry] = []
    @State private var loadingRepos = false

    let agents = ["claude", "codex", "agy", "opencode"]

    /// Repo names that appear more than once (same basename, different path), so
    /// the picker knows to spell out the full path for those entries.
    private var duplicateNames: Set<String> {
        Dictionary(grouping: repos, by: \.name).filter { $0.value.count > 1 }.keys.reduce(into: Set()) { $0.insert($1) }
    }

    var body: some View {
        VStack(spacing: 0) {
            // Header
            HStack {
                Text("New Session")
                    .font(.system(.title3, design: .monospaced))
                    .fontWeight(.semibold)
                    .foregroundStyle(Theme.text)
                Spacer()
                Button(action: { dismiss() }) {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(Theme.overlay0)
                        .font(.system(size: 18))
                }
                .buttonStyle(.plain)
            }
            .padding(20)

            Divider().background(Theme.surface0)

            // Form
            VStack(alignment: .leading, spacing: 16) {
                if store.hasRemoteHosts {
                    FormField(label: "Host") {
                        HStack(spacing: 8) {
                            ForEach(store.registry.hosts) { host in
                                AgentChip(name: host.label, isSelected: selectedHostID == host.id) {
                                    selectedHostID = host.id
                                }
                            }
                            Spacer()
                        }
                    }
                }

                FormField(label: "Name") {
                    TextField("my-feature", text: $name)
                        .textFieldStyle(.plain)
                        .font(.system(.body, design: .monospaced))
                        .padding(8)
                        .background(Theme.crust)
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                }

                FormField(label: "Repository") {
                    VStack(alignment: .leading, spacing: 6) {
                        HStack(spacing: 8) {
                            TextField("~/Code/project (default: cwd)", text: $repoPath)
                                .textFieldStyle(.plain)
                                .font(.system(.body, design: .monospaced))
                                .padding(8)
                                .background(Theme.crust)
                                .clipShape(RoundedRectangle(cornerRadius: 6))

                            if loadingRepos {
                                ProgressView().controlSize(.small)
                            } else if !repos.isEmpty {
                                Menu {
                                    ForEach(repos) { repo in
                                        Button {
                                            repoPath = repo.path
                                        } label: {
                                            // The daemon dedupes by path, not
                                            // name, so two repos can share a
                                            // basename — show the path to
                                            // disambiguate those, and mark recent
                                            // ones with a clock.
                                            let label = duplicateNames.contains(repo.name)
                                                ? "\(repo.name) — \(repo.path)"
                                                : repo.name
                                            if repo.recent ?? false {
                                                Label(label, systemImage: "clock")
                                            } else {
                                                Text(label)
                                            }
                                        }
                                    }
                                } label: {
                                    Image(systemName: "folder")
                                        .font(.system(size: 13))
                                }
                                .menuStyle(.borderlessButton)
                                .frame(width: 28)
                                .help("Pick a repository the daemon knows about")
                            }
                        }
                    }
                }

                FormField(label: "Agent") {
                    HStack(spacing: 8) {
                        ForEach(agents, id: \.self) { a in
                            AgentChip(name: a, isSelected: agent == a) {
                                agent = a
                            }
                        }
                        Spacer()
                    }
                }

                FormField(label: "Model (optional)") {
                    TextField("e.g. claude-sonnet-4-6", text: $model)
                        .textFieldStyle(.plain)
                        .font(.system(.body, design: .monospaced))
                        .padding(8)
                        .background(Theme.crust)
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                }

                FormField(label: "Prompt (optional)") {
                    TextEditor(text: $prompt)
                        .font(.system(.body, design: .monospaced))
                        .scrollContentBackground(.hidden)
                        .padding(8)
                        .frame(minHeight: 60, maxHeight: 120)
                        .background(Theme.crust)
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                }

                if let error {
                    HStack(spacing: 6) {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .foregroundStyle(Theme.red)
                        Text(error)
                            .font(.system(.caption, design: .monospaced))
                            .foregroundStyle(Theme.red)
                    }
                }
            }
            .padding(20)

            Spacer()

            Divider().background(Theme.surface0)

            // Actions
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                    .keyboardShortcut(.escape)
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.subtext0)
                    .padding(.horizontal, 16)
                    .padding(.vertical, 8)

                Button(action: createSession) {
                    if isCreating {
                        ProgressView()
                            .controlSize(.small)
                            .padding(.horizontal, 8)
                    } else {
                        Text("Create")
                    }
                }
                .keyboardShortcut(.return)
                .buttonStyle(.plain)
                .foregroundStyle(name.isEmpty ? Theme.overlay0 : Theme.base)
                .padding(.horizontal, 16)
                .padding(.vertical, 8)
                .background(name.isEmpty ? Theme.surface0 : Theme.green)
                .clipShape(RoundedRectangle(cornerRadius: 6))
                .disabled(name.isEmpty || isCreating)
            }
            .padding(20)
        }
        .frame(width: 480, height: 520)
        .background(Theme.mantle)
        .onAppear { agent = defaultAgent }
        .task(id: selectedHostID) { await loadRepos() }
    }

    /// Load the selected host's repo list for the picker. Failures leave `repos`
    /// empty, so the form silently falls back to the free-text path field.
    ///
    /// A slow `repo_list` for one host must not overwrite another's menu after a
    /// quick host switch: the shared RPC's reply continuation isn't cancelled by
    /// task cancellation, so we snapshot the requested host and drop a late
    /// response that no longer matches the selection (mirrors the iOS guard).
    private func loadRepos() async {
        let requestedHostID = selectedHostID
        repos = [] // clear stale entries so none can be picked mid-load
        loadingRepos = true
        let loaded = await store.fetchRepos(hostID: requestedHostID)
        guard requestedHostID == selectedHostID else { return }
        repos = loaded
        loadingRepos = false
    }

    func createSession() {
        guard !name.isEmpty else { return }
        isCreating = true
        error = nil

        store.createSession(
            name: name,
            agent: agent,
            repoPath: repoPath,
            model: model,
            prompt: prompt,
            hostID: selectedHostID
        ) { result in
            isCreating = false
            switch result {
            case .success(let session):
                if let session {
                    window.selectSession(session)
                }
                dismiss()
            case .failure(let err):
                error = err.localizedDescription
            }
        }
    }
}

struct FormField<Content: View>: View {
    let label: String
    @ViewBuilder let content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(label.uppercased())
                .font(.system(.caption2, design: .monospaced))
                .fontWeight(.semibold)
                .foregroundStyle(Theme.overlay0)
            content
        }
    }
}

struct AgentChip: View {
    let name: String
    let isSelected: Bool
    let action: () -> Void

    var chipColor: Color {
        switch name {
        case "claude": return Theme.mauve
        case "codex": return Theme.blue
        case "agy": return Theme.peach
        case "opencode": return Theme.teal
        default: return Theme.subtext0
        }
    }

    var body: some View {
        Button(action: action) {
            Text(name)
                .font(.system(.caption, design: .monospaced))
                .foregroundStyle(isSelected ? Theme.crust : chipColor)
                .padding(.horizontal, 10)
                .padding(.vertical, 5)
                .background(isSelected ? chipColor : chipColor.opacity(0.15))
                .clipShape(RoundedRectangle(cornerRadius: 6))
        }
        .buttonStyle(.plain)
    }
}
