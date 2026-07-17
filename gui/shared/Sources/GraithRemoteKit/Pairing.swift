import Foundation
import Combine
import GraithProtocol

/// The pairing seam: opens a pre-auth (token-less) connection to a daemon, runs
/// the `pair_request` exchange, and returns a live ``PairingSession`` — the
/// connection stays OPEN across the human's TOFU fingerprint confirmation so the
/// receipt handshake (issue #1299) can commit only after the user confirms.
/// Abstracted so ``PairingCoordinator`` can be driven by a mock in tests (the
/// real conformer needs a live daemon over the tailnet).
public protocol GraithPairing: Sendable {
    /// Send `pair_request` to the daemon at `transport` and await the
    /// `pair_response`. Resolves once the local human runs `gr pair approve`,
    /// or throws if the daemon rejects. The client cryptographically binds the
    /// TLS pin to the presented certificate before returning (TOFU binding).
    /// Nothing is acknowledged or committed yet — the returned session holds the
    /// open connection until ``PairingSession/ackAndAwaitCommit()`` or
    /// ``PairingSession/abandon()``.
    func beginPairing(
        transport: GraithTransport,
        deviceLabel: String,
        profile: String,
        signer: DeviceKeySigner
    ) async throws -> PairingSession
}

/// A live pairing exchange awaiting the receipt handshake (issue #1299). The
/// delivered `pair_response` is available immediately; the caller must durably
/// store the credential and then call ``ackAndAwaitCommit()`` to commit it, or
/// ``abandon()`` to drop it (letting the daemon's uncommitted grant expire).
public protocol PairingSession: Sendable {
    /// The credential the daemon delivered (not yet acknowledged or committed).
    var response: PairResponseMsg { get }
    /// Send `pair_ack` and await `pair_committed`. On any non-confirmation it
    /// throws ``PairingError`` (always commit-unknown once the ack is sent), and
    /// the caller must retain the credential it stored before acking — the daemon
    /// may already be durable.
    func ackAndAwaitCommit() async throws
    /// Close the pairing connection without acknowledging, abandoning the grant.
    func abandon() async
}

/// The production ``GraithPairing``: builds a token-less client and runs the
/// receipt handshake. A token-less connection skips proof-of-possession and may
/// only send `pair_request` (a brand-new device has no daemon record yet).
public struct RealPairing: GraithPairing {
    private let clientID: String

    public init(clientID: String = "graith-macos") {
        self.clientID = clientID
    }

    public func beginPairing(
        transport: GraithTransport,
        deviceLabel: String,
        profile: String,
        signer: DeviceKeySigner
    ) async throws -> PairingSession {
        let client = GraithProtocolClient(
            transport: transport,
            profile: profile,
            clientID: clientID,
            token: nil,
            signer: signer
        )
        do {
            let (response, connection) = try await client.beginPairing(deviceLabel: deviceLabel)
            return RealPairingSession(client: client, connection: connection, response: response)
        } catch {
            await client.close()
            throw error
        }
    }
}

/// ``PairingSession`` over a live ``GraithProtocolClient`` connection.
final class RealPairingSession: PairingSession {
    let response: PairResponseMsg

    private let client: GraithProtocolClient
    private let connection: GraithConnection

    init(client: GraithProtocolClient, connection: GraithConnection, response: PairResponseMsg) {
        self.client = client
        self.connection = connection
        self.response = response
    }

    func ackAndAwaitCommit() async throws {
        // Cross-version: a legacy (pre-receipt) daemon omits request_id — it already
        // committed the device during `gr pair approve` and understands neither
        // pair_ack nor pair_committed. Completing without acking avoids stranding the
        // device (issue #1299). The credential has already been durably stored by
        // the coordinator before this call.
        if response.requestID.isEmpty {
            await connection.close()
            await client.close()
            return
        }

        do {
            try await client.ackPairing(on: connection, response: response)
            await client.close()
        } catch {
            await client.close()
            throw error
        }
    }

    func abandon() async {
        await connection.close()
        await client.close()
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
    /// The live pairing session held open across the confirmation step (issue
    /// #1299): confirm acks + awaits commit on it; reject/reset abandon it.
    private var pendingSession: PairingSession?
    /// True once the credential has been durably persisted client-side (inside
    /// confirmPairing, before the ack). While set, a concurrent reset/dismiss must
    /// NOT delete the credential — the daemon may already have committed, and once
    /// the ack is sent no failure proves otherwise. This closes the
    /// MainActor-reentrancy strand where reset runs during the post-ack
    /// await (issue #1299). generation alone cannot express this.
    private var persisted = false
    /// Generation of a confirm currently running, if any. `confirmPairing` refuses
    /// to start a second time for the same attempt, so a double-tapped "Confirm &
    /// Trust" cannot persist twice, send duplicate pair_acks, or read the same
    /// connection concurrently (issue #1299). Generation-tagged so a stale confirm's
    /// cleanup can never clear a newer attempt's guard.
    private var confirmingGeneration: Int?
    /// True when THIS attempt created a fresh placeholder host row (i.e. the host
    /// id did not already exist). Only such rows may be removed on
    /// reject/reset/failure/supersede — re-pairing an existing paired host must
    /// never drop the prior trusted row (issue #1299).
    private var pendingCreatedPlaceholder = false
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

        // Supersede any prior attempt still holding an open session / unconfirmed
        // placeholder: abandon its connection (never acking) and drop the
        // placeholder ONLY if this-attempt created it and it is not durably
        // persisted. Do this BEFORE clearing pending so the old flags are still
        // available (issue #1299).
        if let oldSession = pendingSession {
            Task { await oldSession.abandon() }
        }

        // Drop the old unpersisted placeholder regardless of whether its id matches
        // this attempt's — a same-id re-attempt must still clear the stale
        // placeholder. Only rows this-attempt-previous created are removed; an
        // existing paired row (or a persisted one) is retained.
        if let oldID = pendingHostID, pendingCreatedPlaceholder, !persisted {
            registry.remove(hostID: oldID)
        }

        clearPending()
        persisted = false

        // Only create a placeholder when the host id is new: re-pairing an existing
        // host must not overwrite its paired row with an unpaired placeholder (which
        // completePairing would then snapshot as the "previous" state).
        let existing = registry.host(id: hostID)
        let createdPlaceholder = existing == nil
        var host = existing ?? Host(
            id: hostID, label: label, kind: .remote,
            magicDNSName: magicDNSName, port: port, daemonProfile: profile
        )
        pendingHostID = hostID
        pendingCreatedPlaceholder = createdPlaceholder
        if createdPlaceholder {
            registry.upsert(host)
        }

        do {
            let transport = GraithTransport.remote(host: magicDNSName, port: port, tlsPinSPKI: nil)
            let session = try await pairing.beginPairing(
                transport: transport,
                deviceLabel: deviceLabel,
                profile: profile,
                signer: identity
            )
            let response = session.response

            // If the attempt was cancelled/superseded while we awaited the local
            // human's approval, discard the result: abandon the open session and do
            // NOT touch the registry — a newer attempt now owns this row, and the
            // supersede path already cleaned up our old placeholder.
            guard myGen == generation else {
                await session.abandon()
                return
            }

            // Do NOT persist trust or acknowledge receipt yet. The token/pin/
            // device-ID are held in memory and the session stays open; only after
            // the user confirms the fingerprint (confirmPairing) do we durably
            // store the credential and send pair_ack (issue #1299).
            host.daemonProfile = response.daemonProfile
            host.tlsPinSPKI = response.tlsPinSPKI
            host.deviceID = response.deviceID

            pendingResponse = response
            pendingHost = host
            pendingSession = session
            spkiFingerprint = Self.formatFingerprint(response.tlsPinSPKI)
            phase = .awaitingConfirmation(host)
        } catch let error as ControlError {
            // Only clean up while this attempt is still current: a superseding
            // pair() now owns the row and did its own placeholder cleanup.
            if myGen == generation {
                if createdPlaceholder { registry.remove(hostID: hostID) }
                clearPending()
                phase = .failed(Self.describe(error))
            }
        } catch {
            if myGen == generation {
                if createdPlaceholder { registry.remove(hostID: hostID) }
                clearPending()
                phase = .failed(error.localizedDescription)
            }
        }
    }

    /// The user confirmed the SPKI fingerprint matches `gr pair`'s local output.
    ///
    /// This is the receipt-protocol commit point (issue #1299): durably persist
    /// the credential FIRST (throwing), then send pair_ack and await
    /// pair_committed. Persistence-before-ack ensures a crash after the ack cannot
    /// leave the daemon paired with no client credential. Rollback is outcome-aware:
    /// only a pre-ack failure (the persist itself) removes the host; any post-ack
    /// failure retains it because the daemon may already be durable.
    public func confirmPairing() async {
        let myGen = generation

        // Enforce single-flight per attempt: a second tap (same generation, still
        // .awaitingConfirmation) must be a no-op, or it would persist twice, send a
        // duplicate pair_ack, and read the session connection concurrently.
        guard confirmingGeneration != myGen else { return }
        confirmingGeneration = myGen
        defer {
            // Generation-tagged so an old confirm's defer can't clear a newer one.
            if confirmingGeneration == myGen { confirmingGeneration = nil }
        }

        let createdPlaceholder = pendingCreatedPlaceholder

        guard let hostID = pendingHostID, let response = pendingResponse,
              var host = pendingHost, let session = pendingSession else { return }

        // 1. Durable persist FIRST (synchronous, no await before this — myGen is
        //    still current). If it throws, completePairing has already restored the
        //    exact prior state, so nothing is durable for this attempt and no ack
        //    was sent — abandon the grant. Only a placeholder THIS attempt created
        //    is removed; a failed re-pair of an existing host keeps the prior row.
        //    deviceID is NOT recorded yet (it must not outlive a failed pre-ack persist).
        //    Every registry and coordinator-state mutation after the first await is
        //    generation-scoped so a stale (superseded) confirm cannot clobber a newer
        //    attempt's row or phase.
        do {
            try registry.completePairing(hostID: hostID, response: response)
        } catch {
            // completePairing already restored the exact prior state. While still
            // current, drop only a placeholder this attempt created (before awaiting
            // abandon, so a new attempt during the await isn't affected); if
            // superseded, touch nothing in the registry.
            if myGen == generation {
                if createdPlaceholder { registry.remove(hostID: hostID) }
                persisted = false
                phase = .failed(error.localizedDescription)
                clearPending()
            }
            await session.abandon()
            return
        }

        // The credential is now durable. From here a concurrent reset/dismiss must
        // never delete it (see `persisted`) — once the ack is sent no outcome proves
        // the daemon did not commit.
        if myGen == generation { persisted = true }

        // 2. Only after durable persistence, acknowledge receipt and await the
        //    daemon's commit confirmation. Everything after this await is scoped to
        //    myGen == generation, since a new pair()/reset() may have advanced the
        //    attempt while the ack was in flight.
        //
        //    Once the ack is sent, NO failure proves the daemon did not commit (an
        //    error reply may be an atomic-write failure that already landed the
        //    device on disk). So every post-ack failure RETAINS the durable
        //    credential — deleting it could strand a paired device (issue #1299) —
        //    records the device ID for the retained credential, and surfaces the
        //    unknown outcome.
        do {
            try await session.ackAndAwaitCommit()
        } catch {
            // Retain the credential (the daemon may be durable). Only record the
            // global device-ID fallback and touch coordinator state while this
            // attempt is still current — a stale continuation must not clobber a
            // newer attempt's identity or phase.
            if myGen == generation {
                try? identity.setDeviceID(response.deviceID)
                phase = .failed("Pairing acknowledged but not confirmed by the daemon; credentials kept. A later connection will settle it. (\(String(describing: error)))")
                clearPending()
            }
            return
        }

        // 3. Committed on both ends. Record the daemon-assigned device ID and mark
        //    paired only while this attempt is still current.
        host.isPaired = true
        if myGen == generation {
            try? identity.setDeviceID(response.deviceID)
            phase = .paired(host)
            clearPending()
        }
    }

    /// The user rejected the fingerprint: abandon the open session (never acking)
    /// and drop a placeholder this attempt created. Reject usually runs pre-persist,
    /// but the phase stays `.awaitingConfirmation` while a confirm is suspended in
    /// its post-ack wait, so it can also race a durable/in-flight commit — hence it
    /// gates placeholder removal on captured `!persisted` (retaining the credential
    /// when the daemon may already have it), exactly like `reset`.
    public func rejectPairing() async {
        generation &+= 1
        // Capture and clear the current state BEFORE awaiting abandon, so a new
        // pair() started during the await is never touched. Reject can also race a
        // confirm suspended in the post-ack wait (phase stays .awaitingConfirmation
        // while committing), so gate placeholder removal on captured !persisted:
        // once the credential is durable it must be retained (the daemon may have
        // committed), exactly like reset. An existing re-pair target row is also
        // never removed.
        let session = pendingSession
        let hostID = pendingHostID
        let createdPlaceholder = pendingCreatedPlaceholder
        let wasPersisted = persisted

        if let hostID, createdPlaceholder, !wasPersisted {
            registry.remove(hostID: hostID)
        }

        persisted = false
        clearPending()
        spkiFingerprint = nil
        phase = .idle

        if let session { await session.abandon() }
    }

    /// Abandon the current attempt (Cancel / sheet dismissed). Invalidates any
    /// in-flight `pair()` (via `generation`) and drops the unpaired placeholder
    /// host so a cancelled pairing leaves no trace. A completed (`.paired`)
    /// attempt is left alone — its host is legitimately trusted.
    public func reset() {
        generation &+= 1
        // Always abandon any open session — a reset never acks.
        if let session = pendingSession {
            Task { await session.abandon() }
        }

        if case .paired = phase {
            // Trusted host — keep it.
        } else if persisted {
            // The credential is already durable (confirm ran and may be mid-commit,
            // or committed-unknown): the daemon may have committed it, so NEVER
            // delete it here — that would recreate the strand.
        } else if let hostID = pendingHostID, pendingCreatedPlaceholder {
            // Only a placeholder this attempt created is dropped; an existing paired
            // row being re-paired is left intact.
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
        pendingSession = nil
        pendingCreatedPlaceholder = false
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
