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
                                            if repo.recent ?? false {
                                                Label(repo.name, systemImage: "clock")
                                            } else {
                                                Text(repo.name)
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
    private func loadRepos() async {
        loadingRepos = true
        repos = await store.fetchRepos(hostID: selectedHostID)
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
