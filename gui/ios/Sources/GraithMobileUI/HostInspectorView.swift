import SwiftUI
import GraithSessionKit
import GraithDesign

/// Read-only host inspector (#904): the daemon's effective config + diff and a
/// `gr doctor`-equivalent diagnostics panel, in one sheet with a segmented
/// switch. iOS is remote-only, so both are fetched over the control protocol.
/// Findings are derived by the shared `HealthReport`, matching macOS exactly.
public struct HostInspectorView: View {
    @ObservedObject var model: FleetModel
    @Environment(\.dismiss) private var dismiss

    @State private var tab: Tab = .diagnostics
    @State private var hostID: String

    private enum Tab: String, CaseIterable { case diagnostics = "Diagnostics", config = "Config" }

    public init(model: FleetModel) {
        self.model = model
        _hostID = State(initialValue: model.connections.first?.id ?? "local")
    }

    public var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                if model.connections.count > 1 {
                    Picker("Host", selection: $hostID) {
                        ForEach(model.connections) { conn in Text(conn.entry.label).tag(conn.id) }
                    }
                    .pickerStyle(.menu)
                    .padding(.horizontal)
                }
                Picker("", selection: $tab) {
                    ForEach(Tab.allCases, id: \.self) { Text($0.rawValue).tag($0) }
                }
                .pickerStyle(.segmented)
                .padding()

                switch tab {
                case .diagnostics: DiagnosticsPane(model: model, hostID: hostID)
                case .config: ConfigPane(model: model, hostID: hostID)
                }
            }
            .background(GraithDesign.background)
            .navigationTitle("Host")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .preferredColorScheme(.dark)
        .tint(GraithDesign.accent)
    }
}

// MARK: - Diagnostics

private struct DiagnosticsPane: View {
    @ObservedObject var model: FleetModel
    let hostID: String
    @State private var diag: DiagnosticsMsg?
    @State private var error: String?
    @State private var loading = false

    private var findings: [HealthFinding] {
        guard let diag else { return [] }
        return HealthReport.findings(from: diag).sorted { ($0.level, $0.section) < ($1.level, $1.section) }
    }

    var body: some View {
        Group {
            if let error {
                InspectorMessage(systemImage: "exclamationmark.triangle.fill", text: error, tint: GraithDesign.red)
            } else if loading && diag == nil {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let diag {
                List {
                    Section {
                        HStack {
                            Image(systemName: HealthReport.hasFailures(findings) ? "xmark.octagon.fill" : "checkmark.circle.fill")
                                .foregroundStyle(HealthReport.hasFailures(findings) ? GraithDesign.red : GraithDesign.green)
                            Text(HealthReport.hasFailures(findings) ? "Issues found" : "No daemon/session issues")
                            Spacer()
                            Text("\(diag.fleet.total) session(s)").foregroundStyle(GraithDesign.subtext0)
                        }
                        .font(GraithDesign.mono(.footnote))
                    }
                    ForEach(sections, id: \.self) { section in
                        Section(section) {
                            ForEach(findings.filter { $0.section == section }) { InspectorFindingRow(finding: $0) }
                        }
                    }
                    Section {
                        Text("Covers daemon, session and storage checks. Run `gr doctor` for host-level checks (sandbox, config keys, disk).")
                            .font(.caption2)
                            .foregroundStyle(GraithDesign.subtext0)
                    }
                }
            } else {
                Spacer()
            }
        }
        .task(id: hostID) { await load() }
        .refreshable { await load() }
    }

    private var sections: [String] {
        var seen = Set<String>()
        return findings.compactMap { seen.insert($0.section).inserted ? $0.section : nil }
    }

    private func load() async {
        loading = true
        defer { loading = false }
        do { diag = try await model.diagnostics(hostID: hostID); error = nil }
        catch { self.error = FleetModel.describeError(error) }
    }
}

private struct InspectorFindingRow: View {
    let finding: HealthFinding

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: icon).foregroundStyle(tint)
            VStack(alignment: .leading, spacing: 2) {
                Text(finding.message).font(GraithDesign.mono(.caption))
                if let hint = finding.hint {
                    Text("→ \(hint)").font(.caption2).foregroundStyle(GraithDesign.subtext0)
                }
            }
        }
    }

    private var icon: String {
        switch finding.level {
        case .fail: "xmark.circle.fill"
        case .warn: "exclamationmark.triangle.fill"
        case .ok: "checkmark.circle"
        }
    }

    private var tint: Color {
        switch finding.level {
        case .fail: GraithDesign.red
        case .warn: GraithDesign.yellow
        case .ok: GraithDesign.green
        }
    }
}

// MARK: - Config

private struct ConfigPane: View {
    @ObservedObject var model: FleetModel
    let hostID: String
    @State private var mode: Mode = .effective
    @State private var response: ConfigResponseMsg?
    @State private var error: String?
    @State private var loading = false

    private enum Mode: String, CaseIterable { case effective = "Effective", diff = "Diff" }

    var body: some View {
        VStack(spacing: 8) {
            Picker("", selection: $mode) {
                ForEach(Mode.allCases, id: \.self) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented)
            .padding(.horizontal)

            if let response, response.configExists == false {
                Label("No config file — running on built-in defaults.", systemImage: "info.circle")
                    .font(.caption)
                    .foregroundStyle(GraithDesign.subtext0)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal)
            } else if let path = response?.configPath, !path.isEmpty {
                Text(path)
                    .font(GraithDesign.mono(.caption2))
                    .foregroundStyle(GraithDesign.subtext0)
                    .lineLimit(1)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal)
            }

            if let error {
                InspectorMessage(systemImage: "exclamationmark.triangle.fill", text: error, tint: GraithDesign.red)
            } else if loading && response == nil {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let response {
                let text = mode == .effective ? response.effectiveTOML : response.diffFromDefaults
                if mode == .diff && text.isEmpty {
                    InspectorMessage(systemImage: "checkmark.seal",
                                     text: "Configuration matches the built-in defaults.", tint: GraithDesign.green)
                } else {
                    ScrollView([.vertical, .horizontal]) {
                        Text(text.isEmpty ? "(empty)" : text)
                            .font(GraithDesign.mono(.caption))
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(8)
                    }
                }
            } else {
                Spacer()
            }
        }
        .task(id: hostID) { await load() }
        .refreshable { await load() }
    }

    private func load() async {
        loading = true
        defer { loading = false }
        do { response = try await model.config(hostID: hostID); error = nil }
        catch { self.error = FleetModel.describeError(error) }
    }
}

private struct InspectorMessage: View {
    let systemImage: String
    let text: String
    let tint: Color

    var body: some View {
        VStack(spacing: 8) {
            Image(systemName: systemImage).font(.title).foregroundStyle(tint)
            Text(text).font(.footnote).foregroundStyle(GraithDesign.subtext0).multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding()
    }
}
