import Foundation
import Combine
import GraithProtocol

/// The pairing seam: opens a pre-auth (token-less) connection to a daemon and
/// runs the `pair_request` exchange, blocking until the local human approves.
/// Abstracted so ``PairingCoordinator`` can be driven by a mock in tests
/// (the real conformer needs a live daemon over the tailnet).
public protocol GraithPairing: Sendable {
    /// Send `pair_request` to the daemon at `transport` and await the
    /// `pair_response`. Resolves once the local human runs `gr pair approve`,
    /// or throws if the daemon rejects. The client cryptographically binds the
    /// TLS pin to the presented certificate before returning (TOFU binding).
    func requestPairing(
        transport: GraithTransport,
        deviceLabel: String,
        profile: String,
        signer: DeviceKeySigner
    ) async throws -> PairResponseMsg
}

/// The production ``GraithPairing``: builds a token-less client and calls
/// `pairRequest`. A token-less connection skips proof-of-possession and may only
/// send `pair_request` (a brand-new device has no daemon record yet).
public struct RealPairing: GraithPairing {
    private let clientID: String

    public init(clientID: String = "graith-macos") {
        self.clientID = clientID
    }

    public func requestPairing(
        transport: GraithTransport,
        deviceLabel: String,
        profile: String,
        signer: DeviceKeySigner
    ) async throws -> PairResponseMsg {
        let client = GraithProtocolClient(
            transport: transport,
            profile: profile,
            clientID: clientID,
            token: nil,
            signer: signer
        )
        do {
            let resp = try await client.pairRequest(deviceLabel: deviceLabel)
            await client.close()
            return resp
        } catch {
            await client.close()
            throw error
        }
    }
}

/// Drives the one-time device-pairing flow for the UI:
///
///   1. The user enters a MagicDNS host + label and taps Pair.
///   2. We open a pre-auth connection and send `pair_request` with the device
///      label + ed25519 public key (via ``GraithPairing``).
///   3. The **local human** approves out-of-band with `gr pair approve <id>`.
///   4. The daemon returns a `PairResponseMsg` once: client token + profile +
///      TLS SPKI pin. Nothing is persisted yet.
///   5. The SPKI fingerprint is surfaced so the user can confirm it matches
///      what `gr pair` printed locally (TOFU). Only on confirmation do we
///      persist the token to the store and mark the host paired.
@MainActor
public final class PairingCoordinator: ObservableObject {
    public enum Phase: Equatable, Sendable {
        case idle
        /// Sending `pair_request` and waiting for the local human to approve.
        case awaitingApproval
        /// The daemon returned a token, but nothing is persisted yet: the user
        /// must confirm the SPKI fingerprint matches `gr pair`'s local output
        /// before we trust it (TOFU confirmation).
        case awaitingConfirmation(Host)
        case paired(Host)
        case failed(String)
    }

    @Published public private(set) var phase: Phase = .idle
    /// The human-readable SPKI fingerprint to confirm against `gr pair` output.
    @Published public private(set) var spkiFingerprint: String?

    private let pairing: GraithPairing
    private let identity: DeviceIdentity
    private let registry: HostRegistry

    // Pairing material held in memory between the daemon's response and the
    // user's fingerprint confirmation. Nothing here is written to the store or
    // marked paired until `confirmPairing()`.
    private var pendingResponse: PairResponseMsg?
    private var pendingHostID: String?
    private var pendingHost: Host?
    /// Bumped whenever a pairing attempt is started, cancelled, or reset. A
    /// `pair()` call captures its generation and ignores its own (possibly
    /// minutes-late) response once the generation has moved on — so cancelling
    /// the sheet while awaiting approval can't later resurrect the flow and
    /// confirm a pairing the user walked away from.
    private var generation = 0

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
        port: UInt16 = GraithTransport.defaultRemotePort,
        profile: String = "",
        deviceLabel: String
    ) async {
        generation &+= 1
        let myGen = generation
        phase = .awaitingApproval
        spkiFingerprint = nil

        // Record the entry up-front (unpaired) so it appears in the sidebar
        // while approval is pending. `pendingHostID` is set *before* the upsert
        // so `reset()` can always drop the placeholder if the user cancels.
        var host = Host(
            id: hostID, label: label, kind: .remote,
            magicDNSName: magicDNSName, port: port, daemonProfile: profile
        )
        pendingHostID = hostID
        registry.upsert(host)

        do {
            let transport = GraithTransport.remote(host: magicDNSName, port: port, tlsPinSPKI: nil)
            let response = try await pairing.requestPairing(
                transport: transport,
                deviceLabel: deviceLabel,
                profile: profile,
                signer: identity
            )

            // If the attempt was cancelled/superseded while we awaited the local
            // human's approval, discard the result: drop the placeholder and do
            // not touch the (now unrelated) coordinator state.
            guard myGen == generation else {
                registry.remove(hostID: hostID)
                return
            }

            // Do NOT persist trust yet. The token/pin/device-ID are held in
            // memory and only written to the store + registry once the user
            // confirms the fingerprint (confirmPairing).
            host.daemonProfile = response.daemonProfile
            host.tlsPinSPKI = response.tlsPinSPKI
            host.deviceID = response.deviceID

            pendingResponse = response
            pendingHost = host
            spkiFingerprint = Self.formatFingerprint(response.tlsPinSPKI)
            phase = .awaitingConfirmation(host)
        } catch let error as ControlError {
            registry.remove(hostID: hostID)
            if myGen == generation { phase = .failed(Self.describe(error)) }
        } catch {
            registry.remove(hostID: hostID)
            if myGen == generation { phase = .failed(error.localizedDescription) }
        }
    }

    /// The user confirmed the SPKI fingerprint matches `gr pair`'s local output.
    /// This is the first point anything is written to the store or marked
    /// paired — persist the token/pin/device-ID now.
    public func confirmPairing() {
        guard let hostID = pendingHostID, let response = pendingResponse,
              var host = pendingHost else { return }
        do {
            try registry.completePairing(hostID: hostID, response: response)
            // Record the daemon-assigned device ID as the global fallback too.
            try? identity.setDeviceID(response.deviceID)
            host.isPaired = true
            phase = .paired(host)
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

    /// Abandon the current attempt (Cancel / sheet dismissed). Invalidates any
    /// in-flight `pair()` (via `generation`) and drops the unpaired placeholder
    /// host so a cancelled pairing leaves no trace. A completed (`.paired`)
    /// attempt is left alone — its host is legitimately trusted.
    public func reset() {
        generation &+= 1
        if case .paired = phase {
            // Trusted host — keep it.
        } else if let hostID = pendingHostID {
            registry.remove(hostID: hostID)
        }
        clearPending()
        phase = .idle
        spkiFingerprint = nil
    }

    private func clearPending() {
        pendingResponse = nil
        pendingHostID = nil
        pendingHost = nil
    }

    // MARK: - Helpers

    /// Present a base64 SPKI pin as colon-separated hex byte pairs for eyeballing.
    public static func formatFingerprint(_ base64: String) -> String {
        guard let data = Data(base64Encoded: base64) else { return base64 }
        return data.map { String(format: "%02X", $0) }.joined(separator: ":")
    }

    public static func describe(_ error: ControlError) -> String {
        switch error {
        case .malformed(let m): return m
        case .daemon(let m): return m
        case .handshakeRejected(let m): return "Handshake rejected: \(m)"
        case .unexpectedReply(let t): return "Unexpected reply from daemon: \(t)"
        case .tlsPinMismatch(let m): return m
        }
    }
}
