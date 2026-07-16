import SwiftUI
import GraithRemoteKit
import GraithSessionKit

// Scenario UI (#903): multi-session orchestration surfaced in the desktop app.
//
// Two entry points share one data source (`store.hostedScenarios`, polled with
// the session list):
//   - `ScenarioSidebarSection` groups each scenario's member sessions together
//     at the top of the sidebar, so a fleet reads as a unit rather than being
//     scattered across repo groups.
//   - `ScenariosSheet` is the full list + per-session status view, with the
//     human-authorized stop / resume / delete lifecycle actions.
//
// `start` / `add` / `task-done` are intentionally absent — the daemon scopes
// them to the scenario's orchestrator *session*, which the human GUI is not.

// MARK: - Sidebar grouping

/// The collapsible "SCENARIOS" block at the top of the sidebar: one section per
/// running scenario, its member sessions listed together with role + task-done
/// state. Tapping a member selects it in the main pane.
struct ScenarioSidebarSection: View {
    @EnvironmentObject var store: SessionStore

    var body: some View {
        if !store.hostedScenarios.isEmpty {
            VStack(alignment: .leading, spacing: 0) {
                Text("SCENARIOS")
                    .font(.system(.caption2, design: .monospaced))
                    .fontWeight(.semibold)
                    .foregroundStyle(Theme.mauve)
                    .padding(.horizontal, 16)
                    .padding(.top, 12)
                    .padding(.bottom, 4)

                ForEach(store.hostedScenarios) { scenario in
                    ScenarioSidebarBlock(scenario: scenario)
                }
            }
        }
    }
}

/// One scenario in the sidebar: a header (name, status, member count) with a
/// lifecycle context menu, over its member session rows.
private struct ScenarioSidebarBlock: View {
    let scenario: HostedScenario
    @EnvironmentObject var store: SessionStore
    @EnvironmentObject var window: WindowState
    @State private var showDeleteConfirm = false

    private var record: ScenarioRecord { scenario.scenario }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                Image(systemName: "square.stack.3d.up.fill")
                    .font(.system(size: 9))
                    .foregroundStyle(Theme.mauve)
                Text(record.name)
                    .font(.system(.caption, design: .monospaced))
                    .fontWeight(.semibold)
                    .foregroundStyle(Theme.subtext1)
                    .lineLimit(1)
                ScenarioStatusPill(status: record.status)
                Spacer()
                Text("\(record.sessions.count)")
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 4)
            .contentShape(Rectangle())
            .contextMenu {
                ScenarioActionMenu(scenario: scenario, onDelete: { showDeleteConfirm = true })
            }

            ForEach(record.sessions) { member in
                ScenarioMemberRow(member: member, onSelect: { select(member) })
            }
        }
        .confirmationDialog(
            "Delete scenario \u{201c}\(record.name)\u{201d}?",
            isPresented: $showDeleteConfirm,
            titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive) { store.deleteScenario(scenario) }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("This stops and removes every session in the scenario, along with their worktrees.")
        }
    }

    /// Resolve the member's live session from its **owning host's** connection —
    /// session IDs are unique only per daemon, so the merged `store.sessions`
    /// (de-duplicated by bare id) could resolve a colliding id to the wrong host.
    /// (WindowState still stores a bare `selectedSessionID`; full host-qualified
    /// selection end-to-end on macOS is a pre-existing limitation shared by the
    /// repo-grouped rows, tracked separately.)
    private func select(_ member: ScenarioSessionInfo) {
        guard let conn = store.connections.first(where: { $0.id == scenario.host.id }),
              let session = conn.sessions.first(where: { $0.id == member.sessionID }) else { return }
        window.selectSession(session)
    }
}

/// A member-session row inside a scenario block: status dot, name, role, and a
/// task-done checkmark.
private struct ScenarioMemberRow: View {
    let member: ScenarioSessionInfo
    let onSelect: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            Circle()
                .fill(ScenariosView.statusColor(member.status))
                .frame(width: 6, height: 6)
                .padding(.leading, 8)
            VStack(alignment: .leading, spacing: 1) {
                HStack(spacing: 4) {
                    Text(member.name)
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(Theme.subtext0)
                        .lineLimit(1)
                    if member.shared == true {
                        Text("shared")
                            .font(.system(size: 8, design: .monospaced))
                            .foregroundStyle(Theme.overlay0)
                            .padding(.horizontal, 3)
                            .padding(.vertical, 1)
                            .background(Theme.surface0)
                            .clipShape(RoundedRectangle(cornerRadius: 3))
                    }
                }
                if let role = member.role, !role.isEmpty {
                    Text(role)
                        .font(.system(size: 9, design: .monospaced))
                        .foregroundStyle(Theme.overlay0)
                        .lineLimit(1)
                }
            }
            Spacer()
            if member.todoTotal > 0 {
                HStack(spacing: 3) {
                    if member.isTodoComplete {
                        Image(systemName: "checkmark.circle.fill")
                            .font(.system(size: 9))
                            .foregroundStyle(Theme.green)
                    }
                    Text("\(member.todoDone)/\(member.todoTotal)")
                        .font(.system(size: 9, design: .monospaced))
                        .foregroundStyle(Theme.overlay0)
                }
                .help("Todo progress: \(member.todoDone) of \(member.todoTotal) done")
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 3)
        .contentShape(Rectangle())
        .onTapGesture { onSelect() }
    }
}

// MARK: - Full sheet

/// The Scenarios list + status sheet: every running scenario, its goal, and its
/// member sessions with role / task / done, plus lifecycle actions.
struct ScenariosSheet: View {
    @EnvironmentObject var store: SessionStore
    @Environment(\.dismiss) private var dismiss

    @State private var deleteTarget: HostedScenario?

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("Scenarios")
                    .font(.system(.title3, design: .monospaced))
                    .fontWeight(.semibold)
                    .foregroundStyle(Theme.text)
                Spacer()
                Button("Done") { dismiss() }
                    .keyboardShortcut(.defaultAction)
            }
            .padding(20)

            Divider().background(Theme.surface0)

            if store.hostedScenarios.isEmpty {
                Spacer()
                VStack(spacing: 8) {
                    Image(systemName: "square.stack.3d.up")
                        .font(.system(size: 28))
                        .foregroundStyle(Theme.overlay0)
                    Text("No running scenarios")
                        .font(.system(.body, design: .monospaced))
                        .foregroundStyle(Theme.overlay0)
                    Text("Start one from the CLI with gr scenario start.")
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(Theme.overlay0)
                }
                Spacer()
            } else {
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        ForEach(store.hostedScenarios) { scenario in
                            ScenarioCard(scenario: scenario, onDelete: { deleteTarget = scenario })
                            Divider().background(Theme.surface0)
                        }
                    }
                }
            }
        }
        .frame(width: 520, height: 460)
        .background(Theme.mantle)
        .confirmationDialog(
            "Delete scenario \u{201c}\(deleteTarget?.scenario.name ?? "")\u{201d}?",
            isPresented: Binding(get: { deleteTarget != nil }, set: { if !$0 { deleteTarget = nil } }),
            titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive) {
                if let s = deleteTarget { store.deleteScenario(s) }
                deleteTarget = nil
            }
            Button("Cancel", role: .cancel) { deleteTarget = nil }
        } message: {
            Text("This stops and removes every session in the scenario, along with their worktrees.")
        }
    }
}

/// One scenario in the sheet: header + goal + member table + actions.
private struct ScenarioCard: View {
    let scenario: HostedScenario
    let onDelete: () -> Void
    @EnvironmentObject var store: SessionStore

    private var record: ScenarioRecord { scenario.scenario }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Text(record.name)
                    .font(.system(.headline, design: .monospaced))
                    .foregroundStyle(Theme.text)
                ScenarioStatusPill(status: record.status)
                Spacer()
                Button("Stop") { store.stopScenario(scenario) }
                    .buttonStyle(.plain)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.yellow)
                Button("Resume") { store.resumeScenario(scenario) }
                    .buttonStyle(.plain)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.blue)
                Button("Delete", role: .destructive) { onDelete() }
                    .buttonStyle(.plain)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.red)
            }

            if !record.goal.isEmpty {
                Text(record.goal)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.subtext0)
            }

            ForEach(record.sessions) { member in
                HStack(spacing: 8) {
                    Circle()
                        .fill(ScenariosView.statusColor(member.status))
                        .frame(width: 6, height: 6)
                    Text(member.name)
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(Theme.subtext1)
                        .frame(width: 90, alignment: .leading)
                    Text(member.role ?? "")
                        .font(.system(.caption2, design: .monospaced))
                        .foregroundStyle(Theme.overlay0)
                        .frame(width: 120, alignment: .leading)
                        .lineLimit(1)
                    Text(member.task ?? "")
                        .font(.system(.caption2, design: .monospaced))
                        .foregroundStyle(Theme.overlay0)
                        .lineLimit(1)
                    Spacer()
                    if member.todoTotal > 0 {
                        HStack(spacing: 3) {
                            if member.isTodoComplete {
                                Image(systemName: "checkmark.circle.fill")
                                    .font(.system(size: 10))
                                    .foregroundStyle(Theme.green)
                            }
                            Text("\(member.todoDone)/\(member.todoTotal)")
                                .font(.system(.caption2, design: .monospaced))
                                .foregroundStyle(Theme.overlay0)
                        }
                    }
                }
            }
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 12)
    }
}

// MARK: - Shared bits

/// A small status pill for a scenario (running / partial / stopped).
struct ScenarioStatusPill: View {
    let status: String

    var body: some View {
        Text(status)
            .font(.system(size: 9, design: .monospaced))
            .foregroundStyle(Theme.crust)
            .padding(.horizontal, 5)
            .padding(.vertical, 1)
            .background(ScenariosView.statusColor(status))
            .clipShape(Capsule())
    }
}

/// The stop / resume / delete context-menu items for a scenario. Delete is
/// routed through `onDelete` so the caller can require confirmation before the
/// destructive (hard) delete — the menu item must not delete on a stray click.
struct ScenarioActionMenu: View {
    let scenario: HostedScenario
    let onDelete: () -> Void
    @EnvironmentObject var store: SessionStore

    var body: some View {
        Button("Stop All") { store.stopScenario(scenario) }
        Button("Resume All") { store.resumeScenario(scenario) }
        Divider()
        Button("Delete Scenario…", role: .destructive) { onDelete() }
    }
}

/// Namespace for scenario view helpers.
enum ScenariosView {
    /// Map a session/scenario status string to a Catppuccin swatch.
    static func statusColor(_ status: String?) -> Color {
        switch status {
        case "running": return Theme.green
        case "errored": return Theme.red
        case "stopped": return Theme.overlay0
        case "creating": return Theme.blue
        case "partial": return Theme.yellow
        default: return Theme.overlay0
        }
    }
}
