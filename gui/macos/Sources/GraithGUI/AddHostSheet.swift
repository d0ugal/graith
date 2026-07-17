import SwiftUI
import GraithProtocol
import GraithRemoteKit

/// The "Add Host" pairing sheet: drives ``PairingCoordinator`` through the
/// one-time device pairing (design §B.2).
///
///   1. The user enters the daemon's MagicDNS name + a label and taps Pair.
///   2. The app sends `pair_request` and waits — the local human approves
///      out-of-band with `gr pair approve <id>` on the host.
///   3. The daemon returns a token + TLS pin; the user confirms the SPKI
///      fingerprint matches what `gr pair` printed (TOFU), then it is trusted.
struct AddHostSheet: View {
    @EnvironmentObject var pairing: PairingCoordinator
    @Environment(\.dismiss) private var dismiss

    @State private var label = ""
    @State private var magicDNSName = ""
    @State private var port = String(GraithTransport.defaultRemotePort)
    @State private var deviceLabel = ProcessInfo.processInfo.hostName
    @State private var profile = ""

    var body: some View {
        VStack(spacing: 0) {
            header

            Divider().background(Theme.surface0)

            Group {
                switch pairing.phase {
                case .idle:
                    formView
                case .awaitingApproval:
                    awaitingApprovalView
                case .awaitingConfirmation:
                    confirmationView
                case .paired:
                    pairedView
                case let .failed(message):
                    failedView(message)
                }
            }
            .padding(20)

            Spacer(minLength: 0)
        }
        .frame(width: 480, height: 520)
        .background(Theme.mantle)
        .onDisappear { pairing.reset() }
    }

    private var header: some View {
        HStack {
            Text("Add Host")
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
    }

    // MARK: - Form

    private var formView: some View {
        VStack(alignment: .leading, spacing: 16) {
            FormField(label: "Label") {
                pairingTextField("graith-ben", text: $label)
            }
            FormField(label: "Tailscale host (MagicDNS)") {
                pairingTextField("graith-ben.tailXXXX.ts.net", text: $magicDNSName)
            }
            FormField(label: "Port") {
                pairingTextField(String(GraithTransport.defaultRemotePort), text: $port)
            }
            FormField(label: "This device's name (shown in gr pair list)") {
                pairingTextField("my-mac", text: $deviceLabel)
            }
            FormField(label: "Daemon profile (optional)") {
                pairingTextField("default", text: $profile)
            }

            Spacer()

            HStack {
                Spacer()
                Button(action: startPairing) {
                    Text("Pair")
                        .foregroundStyle(canPair ? Theme.base : Theme.overlay0)
                        .padding(.horizontal, 16)
                        .padding(.vertical, 8)
                        .background(canPair ? Theme.green : Theme.surface0)
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                }
                .buttonStyle(.plain)
                .keyboardShortcut(.return)
                .disabled(!canPair)
            }
        }
    }

    private var canPair: Bool {
        !magicDNSName.isEmpty && !label.isEmpty && !deviceLabel.isEmpty
    }

    private func startPairing() {
        let portNumber = UInt16(port) ?? GraithTransport.defaultRemotePort
        Task {
            await pairing.pair(
                label: label,
                magicDNSName: magicDNSName,
                port: portNumber,
                profile: profile,
                deviceLabel: deviceLabel
            )
        }
    }

    // MARK: - Phase views

    private var awaitingApprovalView: some View {
        VStack(spacing: 16) {
            ProgressView()
                .controlSize(.large)
            Text("Waiting for approval")
                .font(.system(.title3, design: .monospaced))
                .foregroundStyle(Theme.text)
            VStack(spacing: 6) {
                Text("On \(magicDNSName), run:")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
                Text("gr pair approve <request-id>")
                    .font(.system(.body, design: .monospaced))
                    .foregroundStyle(Theme.green)
                    .padding(8)
                    .background(Theme.crust)
                    .clipShape(RoundedRectangle(cornerRadius: 6))
                Text("List pending requests with `gr pair list`.")
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(Theme.overlay0)
            }
            Button("Cancel") { pairing.reset(); dismiss() }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.subtext0)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var confirmationView: some View {
        VStack(spacing: 16) {
            Image(systemName: "lock.shield")
                .font(.system(size: 32))
                .foregroundStyle(Theme.yellow)
            Text("Confirm the fingerprint")
                .font(.system(.title3, design: .monospaced))
                .foregroundStyle(Theme.text)
            Text("Check this matches the SPKI pin `gr pair approve` printed on the host.")
                .font(.system(.caption, design: .monospaced))
                .foregroundStyle(Theme.overlay0)
                .multilineTextAlignment(.center)

            ScrollView {
                Text(pairing.spkiFingerprint ?? "—")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Theme.text)
                    .textSelection(.enabled)
                    .padding(10)
            }
            .frame(maxHeight: 120)
            .background(Theme.crust)
            .clipShape(RoundedRectangle(cornerRadius: 6))

            HStack(spacing: 12) {
                Button("Reject") { Task { await pairing.rejectPairing() } }
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.red)
                    .padding(.horizontal, 16).padding(.vertical, 8)
                    .background(Theme.surface0)
                    .clipShape(RoundedRectangle(cornerRadius: 6))
                Button("Confirm & Trust") { Task { await pairing.confirmPairing() } }
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.base)
                    .padding(.horizontal, 16).padding(.vertical, 8)
                    .background(Theme.green)
                    .clipShape(RoundedRectangle(cornerRadius: 6))
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var pairedView: some View {
        VStack(spacing: 16) {
            Image(systemName: "checkmark.circle")
                .font(.system(size: 32))
                .foregroundStyle(Theme.green)
            Text("Paired")
                .font(.system(.title3, design: .monospaced))
                .foregroundStyle(Theme.text)
            Text("This host's sessions now appear in the sidebar.")
                .font(.system(.caption, design: .monospaced))
                .foregroundStyle(Theme.overlay0)
            Button("Done") { pairing.reset(); dismiss() }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.base)
                .padding(.horizontal, 16).padding(.vertical, 8)
                .background(Theme.green)
                .clipShape(RoundedRectangle(cornerRadius: 6))
                .keyboardShortcut(.return)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func failedView(_ message: String) -> some View {
        VStack(spacing: 16) {
            Image(systemName: "exclamationmark.triangle.fill")
                .font(.system(size: 32))
                .foregroundStyle(Theme.red)
            Text("Pairing failed")
                .font(.system(.title3, design: .monospaced))
                .foregroundStyle(Theme.text)
            Text(message)
                .font(.system(.caption, design: .monospaced))
                .foregroundStyle(Theme.red)
                .multilineTextAlignment(.center)
            HStack(spacing: 12) {
                Button("Close") { pairing.reset(); dismiss() }
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.subtext0)
                Button("Try Again") { pairing.reset() }
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.base)
                    .padding(.horizontal, 16).padding(.vertical, 8)
                    .background(Theme.green)
                    .clipShape(RoundedRectangle(cornerRadius: 6))
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    // MARK: - Helpers

    private func pairingTextField(_ placeholder: String, text: Binding<String>) -> some View {
        TextField(placeholder, text: text)
            .textFieldStyle(.plain)
            .font(.system(.body, design: .monospaced))
            .padding(8)
            .background(Theme.crust)
            .clipShape(RoundedRectangle(cornerRadius: 6))
    }
}
