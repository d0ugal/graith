import SwiftUI
import GraithClientAPI
import GraithMobileKit
#if os(iOS)
import UIKit
#endif

/// The add-host + pairing flow (design §B.2). The user enters a MagicDNS host
/// and a label; the app sends `pair_request`; the local human approves with
/// `gr pair approve`; on success the SPKI fingerprint is shown for TOFU
/// confirmation against `gr pair`'s local output.
struct PairingView: View {
    @ObservedObject var model: AppModel
    @Environment(\.dismiss) private var dismiss

    @State private var label = ""
    @State private var magicDNSName = ""
    @State private var port = "4823"
    @State private var deviceLabel = PairingView.defaultDeviceLabel

    // Observe the shared pairing coordinator.
    @ObservedObject private var pairing: PairingCoordinator

    init(model: AppModel) {
        self.model = model
        self.pairing = model.pairing
    }

    var body: some View {
        NavigationStack {
            Form {
                switch pairing.phase {
                case .idle, .failed:
                    inputSection
                    if case .failed(let msg) = pairing.phase {
                        Text(msg).foregroundStyle(.red).font(.footnote)
                    }
                case .awaitingApproval:
                    awaitingSection
                case .awaitingConfirmation(let entry):
                    confirmationSection(entry)
                case .paired(let entry):
                    pairedSection(entry)
                }
            }
            .navigationTitle("Add Host")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { pairing.reset(); dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    switch pairing.phase {
                    case .paired:
                        Button("Done") { pairing.reset(); dismiss() }
                    case .idle, .failed:
                        Button("Pair") { Task { await startPairing() } }
                            .disabled(!canPair || isBusy)
                    case .awaitingApproval, .awaitingConfirmation:
                        // Confirm / reject is an inline decision, not a toolbar action.
                        EmptyView()
                    }
                }
            }
        }
    }

    // MARK: - Sections

    private var inputSection: some View {
        Group {
            Section("Daemon") {
                TextField("Label (e.g. laptop)", text: $label)
                TextField("MagicDNS name (graith-x.ts.net)", text: $magicDNSName)
                    .textInputAutocapitalizationCompat()
                TextField("Port", text: $port)
            }
            Section("This device") {
                TextField("Device label", text: $deviceLabel)
            }
            Section {
                Text("Approve this device on the daemon host with `gr pair approve <id>`.")
                    .font(.footnote).foregroundStyle(.secondary)
            }
        }
    }

    private var awaitingSection: some View {
        Section {
            HStack(spacing: 12) {
                ProgressView()
                VStack(alignment: .leading) {
                    Text("Waiting for approval…").font(.headline)
                    Text("On the daemon host, run `gr pair list` then `gr pair approve <id>`.")
                        .font(.footnote).foregroundStyle(.secondary)
                }
            }
        }
    }

    // Shown once the daemon replies, BEFORE anything is persisted: the user
    // eyeballs the SPKI fingerprint against `gr pair`'s local output and either
    // trusts it (persists the token/pin) or rejects it (discards, nothing
    // written). This is the TOFU gate — see PairingCoordinator.confirmPairing.
    private func confirmationSection(_ entry: HostEntry) -> some View {
        Section("Confirm the daemon's key") {
            Text("\(entry.label) replied. Before trusting it, check this TLS key fingerprint matches what `gr pair` printed on the daemon host.")
                .font(.footnote).foregroundStyle(.secondary)
            if let fp = pairing.spkiFingerprint {
                VStack(alignment: .leading, spacing: 4) {
                    Text("TLS key fingerprint").font(.caption).foregroundStyle(.secondary)
                    Text(fp)
                        .font(.system(.caption2, design: .monospaced))
                        .textSelection(.enabled)
                }
            }
            HStack {
                Button(role: .destructive) { pairing.rejectPairing() } label: {
                    Text("Doesn't match")
                }
                Spacer()
                Button { pairing.confirmPairing() } label: {
                    Text("Confirm & trust")
                }
                .buttonStyle(.borderedProminent)
            }
        }
    }

    private func pairedSection(_ entry: HostEntry) -> some View {
        Section("Paired") {
            Label("\(entry.label) is paired", systemImage: "checkmark.seal.fill")
                .foregroundStyle(.green)
            if let fp = pairing.spkiFingerprint {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Trusted TLS key fingerprint").font(.caption).foregroundStyle(.secondary)
                    Text(fp)
                        .font(.system(.caption2, design: .monospaced))
                        .textSelection(.enabled)
                }
            }
        }
        .task { await model.didPair() }
    }

    // MARK: - Actions

    private var canPair: Bool {
        !label.isEmpty && !magicDNSName.isEmpty && UInt16(port) != nil
    }

    private var isBusy: Bool {
        if case .awaitingApproval = pairing.phase { return true }
        return false
    }

    private func startPairing() async {
        guard let portNum = UInt16(port) else { return }
        await pairing.pair(
            label: label,
            magicDNSName: magicDNSName,
            port: portNum,
            deviceLabel: deviceLabel
        )
    }

    static var defaultDeviceLabel: String {
        #if os(iOS)
        return UIDevice.current.name
        #else
        return Host.current().localizedName ?? "graith device"
        #endif
    }
}

extension View {
    /// Disable autocapitalization for host-name entry, cross-platform.
    @ViewBuilder
    func textInputAutocapitalizationCompat() -> some View {
        #if os(iOS)
        self.textInputAutocapitalization(.never).autocorrectionDisabled()
        #else
        self.autocorrectionDisabled()
        #endif
    }
}
