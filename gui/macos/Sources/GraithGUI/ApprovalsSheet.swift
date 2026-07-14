import SwiftUI
import GraithProtocol
import GraithRemoteKit

/// The aggregated approvals panel: pending tool-call approvals across every
/// connected host, with allow/deny actions (design §C.6). macOS already
/// *subscribed* to the approval stream for the Dock badge + banners; this closes
/// the functional hole where it could not *respond* (#1130).
///
/// Responses route back through ``ApprovalMonitor`` to the owning host's client,
/// so a decision always reaches the right daemon.
struct ApprovalsSheet: View {
    @EnvironmentObject var approvals: ApprovalMonitor
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(spacing: 0) {
            header

            Divider().background(Theme.surface0)

            if let error = approvals.lastError {
                HStack(spacing: 6) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .foregroundStyle(Theme.red)
                        .font(.system(size: 11))
                    Text(error)
                        .font(.system(.caption2, design: .monospaced))
                        .foregroundStyle(Theme.red)
                        .lineLimit(2)
                    Spacer()
                    Button(action: { approvals.lastError = nil }) {
                        Image(systemName: "xmark")
                            .font(.system(size: 9))
                            .foregroundStyle(Theme.overlay0)
                    }
                    .buttonStyle(.plain)
                }
                .padding(.horizontal, 20)
                .padding(.vertical, 8)
                .background(Theme.red.opacity(0.1))
            }

            if approvals.pending.isEmpty {
                emptyState
            } else {
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        ForEach(approvals.grouped, id: \.host.id) { group in
                            hostSection(group.host, approvals: group.approvals)
                        }
                    }
                    .padding(.vertical, 8)
                }
            }
        }
        .frame(width: 520, height: 560)
        .background(Theme.mantle)
    }

    private var header: some View {
        HStack {
            Text("Approvals")
                .font(.system(.title3, design: .monospaced))
                .fontWeight(.semibold)
                .foregroundStyle(Theme.text)
            if !approvals.pending.isEmpty {
                Text("\(approvals.pending.count)")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.crust)
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(Theme.yellow)
                    .clipShape(Capsule())
            }
            Spacer()
            Button(action: { dismiss() }) {
                Image(systemName: "xmark.circle.fill")
                    .foregroundStyle(Theme.overlay0)
                    .font(.system(size: 18))
            }
            .buttonStyle(.plain)
        }
        .padding(20)
    }

    private var emptyState: some View {
        VStack(spacing: 12) {
            Image(systemName: "checkmark.shield")
                .font(.system(size: 40))
                .foregroundStyle(Theme.green)
            Text("No pending approvals")
                .font(.system(.body, design: .monospaced))
                .foregroundStyle(Theme.subtext0)
            Text("Tool calls awaiting your decision appear here.")
                .font(.system(.caption, design: .monospaced))
                .foregroundStyle(Theme.overlay0)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    @ViewBuilder
    private func hostSection(_ host: Host, approvals items: [ApprovalInfo]) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(host.label.uppercased())
                .font(.system(.caption2, design: .monospaced))
                .fontWeight(.semibold)
                .foregroundStyle(Theme.overlay0)
                .padding(.horizontal, 20)
                .padding(.top, 8)

            ForEach(items) { approval in
                ApprovalCard(approval: approval, hostID: host.id)
                    .padding(.horizontal, 16)
            }
        }
    }
}

/// One pending approval: tool + input, the requesting session, and allow/deny.
struct ApprovalCard: View {
    let approval: ApprovalInfo
    let hostID: String
    @EnvironmentObject var approvals: ApprovalMonitor

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                Image(systemName: "wrench.and.screwdriver")
                    .foregroundStyle(Theme.blue)
                    .font(.system(size: 12))
                Text(approval.toolName)
                    .font(.system(.body, design: .monospaced))
                    .fontWeight(.semibold)
                    .foregroundStyle(Theme.text)
                Spacer()
                Text(approval.sessionName)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.subtext0)
                    .lineLimit(1)
            }

            if let input = approval.toolInput, !input.isEmpty {
                ScrollView {
                    Text(input)
                        .font(.system(.footnote, design: .monospaced))
                        .foregroundStyle(Theme.subtext1)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .textSelection(.enabled)
                        .padding(8)
                }
                .frame(maxHeight: 120)
                .background(Theme.crust)
                .clipShape(RoundedRectangle(cornerRadius: 6))
            }

            HStack {
                Text("\(approval.agent) \u{b7} \(approval.repoName)")
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
                    .lineLimit(1)
                Spacer()
                Button(action: { approvals.respond(approval, host: hostID, decision: .deny) }) {
                    Text("Deny")
                        .foregroundStyle(Theme.red)
                        .padding(.horizontal, 14)
                        .padding(.vertical, 6)
                        .background(Theme.surface0)
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                }
                .buttonStyle(.plain)

                Button(action: { approvals.respond(approval, host: hostID, decision: .allow) }) {
                    Text("Allow")
                        .foregroundStyle(Theme.base)
                        .padding(.horizontal, 14)
                        .padding(.vertical, 6)
                        .background(Theme.green)
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                }
                .buttonStyle(.plain)
            }
        }
        .padding(12)
        .background(Theme.surface0.opacity(0.4))
        .clipShape(RoundedRectangle(cornerRadius: 8))
    }
}
