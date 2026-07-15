import SwiftUI
import GraithSessionKit
import GraithDesign

// Scenario UI (#903) for the mobile app, mirroring the desktop surface through
// the same shared `FleetModel`:
//   - `ScenarioSidebarSection` groups each scenario's member sessions at the top
//     of the sidebar so a fleet reads as a unit.
//   - `ScenariosView` is the full list + status sheet with the human-authorized
//     stop / resume / delete lifecycle actions.
//
// `start` / `add` / `task-done` are orchestrator-session-scoped on the daemon,
// so they are intentionally CLI-only and absent here.

// MARK: - Sidebar grouping

/// A List `Section` per running scenario, its member sessions listed together
/// with role + task-done state. Selecting a row drives `model.selection`.
struct ScenarioSidebarSection: View {
    @ObservedObject var model: FleetModel

    var body: some View {
        ForEach(model.hostedScenarios) { scenario in
            Section {
                ForEach(scenario.scenario.sessions) { member in
                    ScenarioMemberRow(member: member)
                        .tag(SessionRef(hostID: scenario.host.id, sessionID: member.sessionID))
                }
            } header: {
                HStack {
                    Image(systemName: "square.stack.3d.up.fill").foregroundStyle(.purple)
                    Text(scenario.scenario.name)
                    ScenarioStatusPill(status: scenario.scenario.status)
                    Spacer()
                    Text("\(scenario.scenario.sessions.count)")
                        .foregroundStyle(.secondary).font(.caption)
                }
            }
        }
    }
}

/// A member-session row inside a scenario section.
private struct ScenarioMemberRow: View {
    let member: ScenarioSessionInfo

    var body: some View {
        HStack(spacing: 8) {
            Circle().fill(scenarioStatusColor(member.status)).frame(width: 8, height: 8)
            VStack(alignment: .leading, spacing: 1) {
                HStack(spacing: 4) {
                    Text(member.name).font(.callout)
                    if member.shared == true {
                        Text("shared").font(.caption2).foregroundStyle(.secondary)
                    }
                }
                if let role = member.role, !role.isEmpty {
                    Text(role).font(.caption2).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            Spacer()
            if member.taskDone == true {
                Image(systemName: "checkmark.circle.fill").foregroundStyle(.green).font(.caption)
            }
        }
    }
}

// MARK: - Full sheet

/// The Scenarios list + status sheet.
struct ScenariosView: View {
    @ObservedObject var model: FleetModel
    @Environment(\.dismiss) private var dismiss

    @State private var deleteTarget: HostedScenario?

    var body: some View {
        NavigationStack {
            Group {
                if model.hostedScenarios.isEmpty {
                    GraithEmptyState(
                        systemImage: "square.stack.3d.up",
                        title: "No running scenarios",
                        subtitle: "Start one from the CLI with gr scenario start.",
                        actionTitle: nil,
                        action: nil
                    )
                } else {
                    List {
                        ForEach(model.hostedScenarios) { scenario in
                            ScenarioCard(scenario: scenario, model: model,
                                         onDelete: { deleteTarget = scenario })
                        }
                    }
                }
            }
            .navigationTitle("Scenarios")
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .confirmationDialog(
                "Delete scenario \u{201c}\(deleteTarget?.scenario.name ?? "")\u{201d}?",
                isPresented: Binding(get: { deleteTarget != nil }, set: { if !$0 { deleteTarget = nil } }),
                titleVisibility: .visible
            ) {
                Button("Delete", role: .destructive) {
                    if let s = deleteTarget { model.deleteScenario(s) }
                    deleteTarget = nil
                }
                Button("Cancel", role: .cancel) { deleteTarget = nil }
            } message: {
                Text("This stops and removes every session in the scenario, along with their worktrees.")
            }
        }
    }
}

/// One scenario in the sheet: header + goal + members + lifecycle actions.
private struct ScenarioCard: View {
    let scenario: HostedScenario
    @ObservedObject var model: FleetModel
    let onDelete: () -> Void

    private var record: ScenarioRecord { scenario.scenario }

    var body: some View {
        Section {
            if !record.goal.isEmpty {
                Text(record.goal).font(.caption).foregroundStyle(.secondary)
            }
            ForEach(record.sessions) { member in
                HStack(spacing: 8) {
                    Circle().fill(scenarioStatusColor(member.status)).frame(width: 8, height: 8)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(member.name).font(.callout)
                        if let role = member.role, !role.isEmpty {
                            Text(role).font(.caption2).foregroundStyle(.secondary)
                        }
                        if let task = member.task, !task.isEmpty {
                            Text(task).font(.caption2).foregroundStyle(.secondary).lineLimit(1)
                        }
                    }
                    Spacer()
                    if member.taskDone == true {
                        Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                    }
                }
            }
            HStack {
                Button { model.stopScenario(scenario) } label: {
                    Label("Stop", systemImage: "stop")
                }
                .buttonStyle(.bordered)
                Button { model.resumeScenario(scenario) } label: {
                    Label("Resume", systemImage: "play")
                }
                .buttonStyle(.bordered)
                Spacer()
                Button(role: .destructive) { onDelete() } label: {
                    Label("Delete", systemImage: "trash")
                }
                .buttonStyle(.bordered)
            }
            .font(.caption)
        } header: {
            HStack {
                Text(record.name)
                ScenarioStatusPill(status: record.status)
            }
        }
    }
}

// MARK: - Shared bits

/// A small status pill for a scenario (running / partial / stopped).
struct ScenarioStatusPill: View {
    let status: String

    var body: some View {
        Text(status)
            .font(.caption2)
            .padding(.horizontal, 6)
            .padding(.vertical, 1)
            .background(scenarioStatusColor(status).opacity(0.25))
            .foregroundStyle(scenarioStatusColor(status))
            .clipShape(Capsule())
    }
}

/// Map a session/scenario status string to a colour.
func scenarioStatusColor(_ status: String?) -> Color {
    switch status {
    case "running": return .green
    case "errored": return .red
    case "stopped": return .gray
    case "creating": return .blue
    case "partial": return .yellow
    default: return .gray
    }
}
