import SwiftUI
import GraithClientAPI

/// The aggregated approvals queue across all hosts (design §C.6). Subscribes via
/// each `HostConnection`'s approval stream — no attach, no desktop kick. Shows
/// the tool + input and lets the user allow/deny with an optional reason.
struct ApprovalsView: View {
    @ObservedObject var model: AppModel
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            Group {
                if model.allApprovals.isEmpty {
                    ContentUnavailableCompat(
                        title: "No pending approvals",
                        systemImage: "checkmark.shield",
                        description: "Tool calls awaiting your decision will appear here."
                    )
                } else {
                    List {
                        ForEach(model.connections) { conn in
                            if !conn.approvals.isEmpty {
                                Section(conn.entry.label) {
                                    ForEach(conn.approvals) { approval in
                                        ApprovalCard(connection: conn, approval: approval)
                                    }
                                }
                            }
                        }
                    }
                }
            }
            .navigationTitle("Approvals")
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }
}

/// One approval with allow / deny actions.
struct ApprovalCard: View {
    @ObservedObject var connection: HostConnection
    let approval: ApprovalInfo
    @State private var busy = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Image(systemName: "wrench.and.screwdriver")
                Text(approval.toolName).font(.headline)
                Spacer()
                Text(approval.sessionName).font(.caption).foregroundStyle(.secondary)
            }
            if let input = approval.toolInput, !input.isEmpty {
                Text(input)
                    .font(.system(.footnote, design: .monospaced))
                    .padding(8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color.gray.opacity(0.12))
                    .cornerRadius(6)
                    .textSelection(.enabled)
            }
            HStack {
                Text("\(approval.agent) · \(approval.repoName)")
                    .font(.caption2).foregroundStyle(.secondary)
                Spacer()
                Button(role: .destructive) {
                    Task { await respond(.deny) }
                } label: { Text("Deny") }
                    .buttonStyle(.bordered)
                    .disabled(busy)
                Button {
                    Task { await respond(.allow) }
                } label: { Text("Allow") }
                    .buttonStyle(.borderedProminent)
                    .disabled(busy)
            }
        }
        .padding(.vertical, 4)
    }

    private func respond(_ decision: ApprovalDecision) async {
        busy = true
        defer { busy = false }
        await connection.respond(approval, decision: decision)
    }
}
