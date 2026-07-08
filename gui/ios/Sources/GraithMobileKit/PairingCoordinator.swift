import Foundation
import Combine
import GraithClientAPI

/// Drives the one-time device-pairing flow (design §B.2) for the UI:
///
///   1. The user enters a MagicDNS host + label and taps Pair.
///   2. We open a pre-auth connection and send `pair_request` with the device
///      label + ed25519 public key (via `GraithPairing`).
///   3. The **local human** approves out-of-band with `gr pair approve <id>`.
///   4. The daemon returns a `PairResponse` once: client token + profile + TLS
///      SPKI pin. We persist the token to the Keychain, record the entry, and
///      store the daemon-assigned device ID against our key.
///   5. The SPKI fingerprint is surfaced so the user can confirm it matches
///      what `gr pair` printed locally (TOFU).
@MainActor
public final class PairingCoordinator: ObservableObject {
    public enum Phase: Equatable, Sendable {
        case idle
        /// Sending `pair_request` and waiting for the local human to approve.
        case awaitingApproval
        /// The daemon returned a token, but nothing is persisted yet: the user
        /// must confirm the SPKI fingerprint matches `gr pair`'s local output
        /// before we trust it (design §B.2 TOFU confirmation).
        case awaitingConfirmation(HostEntry)
        case paired(HostEntry)
        case failed(String)
    }

    @Published public private(set) var phase: Phase = .idle
    /// The human-readable SPKI fingerprint to confirm against `gr pair` output.
    @Published public private(set) var spkiFingerprint: String?

    private let pairing: GraithPairing
    private let identity: DeviceIdentity
    private let registry: HostRegistry

    // Pairing material held in memory between the daemon's response and the
    // user's fingerprint confirmation. Nothing here is written to the Keychain
    // or marked paired until `confirmPairing()`. (PairResponse isn't Equatable,
    // so it lives here rather than in the Equatable `Phase`.)
    private var pendingResponse: PairResponse?
    private var pendingHostID: String?
    private var pendingEntry: HostEntry?

    public init(pairing: GraithPairing, identity: DeviceIdentity, registry: HostRegistry) {
        self.pairing = pairing
        self.identity = identity
        self.registry = registry
    }

    /// Kick off pairing with a daemon at `magicDNSName:port`, labelled `label`.
    /// `deviceLabel` is what the local human sees in `gr pair list`.
    public func pair(
        hostID: String = UUID().uuidString,
        label: String,
        magicDNSName: String,
        port: UInt16 = 4823,
        profile: String = "",
        deviceLabel: String
    ) async {
        phase = .awaitingApproval
        spkiFingerprint = nil

        // Record the entry up-front (unpaired) so it appears in the sidebar
        // while approval is pending.
        var entry = HostEntry(id: hostID, label: label, magicDNSName: magicDNSName, port: port)
        registry.upsert(entry)

        do {
            let transport = GraithTransport.remote(host: magicDNSName, port: port, tlsPinSPKI: nil)
            let response = try await pairing.requestPairing(
                transport: transport,
                deviceLabel: deviceLabel,
                profile: profile,
                signer: identity
            )

            // Do NOT persist trust yet. The token/pin/device-ID are held in
            // memory and only written to the Keychain + registry once the user
            // confirms the fingerprint (confirmPairing) — otherwise a MITM whose
            // cert the user never verified would be trusted the moment the
            // daemon replied. The device ID is per-host (see confirmPairing /
            // HostScopedSigner); the ed25519 key stays global.
            entry.daemonProfile = response.daemonProfile
            entry.tlsPinSPKI = response.tlsPinSPKI
            entry.deviceID = response.deviceID

            pendingResponse = response
            pendingHostID = hostID
            pendingEntry = entry
            spkiFingerprint = Self.formatFingerprint(response.tlsPinSPKI)
            phase = .awaitingConfirmation(entry)
        } catch let error as GraithClientError {
            phase = .failed(Self.describe(error))
        } catch {
            phase = .failed(error.localizedDescription)
        }
    }

    /// The user confirmed the SPKI fingerprint matches `gr pair`'s local output.
    /// This is the first point anything is written to the Keychain or marked
    /// paired — persist the token/pin/device-ID now.
    public func confirmPairing() {
        guard let hostID = pendingHostID, let response = pendingResponse,
              var entry = pendingEntry else { return }
        do {
            try registry.completePairing(hostID: hostID, response: response)
            entry.isPaired = true
            phase = .paired(entry)
        } catch let error as GraithClientError {
            phase = .failed(Self.describe(error))
        } catch {
            phase = .failed(error.localizedDescription)
        }
        clearPending()
    }

    /// The user rejected the fingerprint: discard the token without ever writing
    /// it, and drop the placeholder host entry. (The daemon already issued the
    /// token; it can be cleared there with `gr pair revoke` — this device simply
    /// never trusts it.)
    public func rejectPairing() {
        if let hostID = pendingHostID { registry.remove(hostID: hostID) }
        clearPending()
        spkiFingerprint = nil
        phase = .idle
    }

    public func reset() {
        clearPending()
        phase = .idle
        spkiFingerprint = nil
    }

    private func clearPending() {
        pendingResponse = nil
        pendingHostID = nil
        pendingEntry = nil
    }

    // MARK: - Helpers

    /// Present a base64 SPKI pin as colon-separated hex byte pairs for eyeballing.
    static func formatFingerprint(_ base64: String) -> String {
        guard let data = Data(base64Encoded: base64) else { return base64 }
        return data.map { String(format: "%02X", $0) }.joined(separator: ":")
    }

    public static func describe(_ error: GraithClientError) -> String {
        switch error {
        case .notPaired: return "This device is not paired yet."
        case .authenticationFailed(let r): return "Authentication failed: \(r)"
        case .tlsPinMismatch: return "The daemon's TLS key changed. Re-pair to trust it."
        case .tailnetUnreachable: return "The daemon isn't reachable on the tailnet."
        case .daemon(let m): return m
        case .disconnected(let m): return "Disconnected: \(m)"
        case .decoding(let m): return "Bad reply from daemon: \(m)"
        }
    }
}
