import Foundation
import Combine
import GraithClientAPI

/// Non-secret metadata for one paired (or being-paired) daemon. The client
/// token is **not** stored here — it lives in the Keychain, keyed by `id`
/// (design §C.4: `HostRegistry` persisted; tokens in Keychain).
public struct HostEntry: Codable, Identifiable, Hashable, Sendable {
    /// Stable local identifier for this host entry.
    public let id: String
    public var label: String
    /// MagicDNS name, e.g. `graith-laptop.tailXXXX.ts.net`.
    public var magicDNSName: String
    public var port: UInt16
    /// The daemon profile, recorded at pairing — the handshake rejects on
    /// mismatch (`handler.go` profile check), so it must be presented back.
    public var daemonProfile: String
    /// SPKI (public-key) pin captured at pairing, for TLS validation.
    public var tlsPinSPKI: String
    /// The device ID this daemon assigned to us at pairing. Stored **per host**:
    /// the ed25519 key is one global device key, but each daemon issues its own
    /// device ID, and proof-of-possession must present the ID for the host being
    /// connected to (a global ID would let pairing host B clobber host A).
    public var deviceID: String
    /// Whether pairing completed and a client token exists in the Keychain.
    public var isPaired: Bool
    /// ISO-8601 timestamp of the last successful connection (display only).
    public var lastSeen: String?

    public init(
        id: String, label: String, magicDNSName: String, port: UInt16 = 4823,
        daemonProfile: String = "", tlsPinSPKI: String = "", deviceID: String = "",
        isPaired: Bool = false, lastSeen: String? = nil
    ) {
        self.id = id
        self.label = label
        self.magicDNSName = magicDNSName
        self.port = port
        self.daemonProfile = daemonProfile
        self.tlsPinSPKI = tlsPinSPKI
        self.deviceID = deviceID
        self.isPaired = isPaired
        self.lastSeen = lastSeen
    }

    private enum CodingKeys: String, CodingKey {
        case id, label, magicDNSName, port, daemonProfile, tlsPinSPKI, deviceID, isPaired, lastSeen
    }

    // Custom decode so entries persisted before `deviceID` existed still load
    // (synthesized Codable throws on a missing key even with a default value,
    // which would otherwise drop every previously-paired host on upgrade).
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        label = try c.decode(String.self, forKey: .label)
        magicDNSName = try c.decode(String.self, forKey: .magicDNSName)
        port = try c.decode(UInt16.self, forKey: .port)
        daemonProfile = try c.decode(String.self, forKey: .daemonProfile)
        tlsPinSPKI = try c.decode(String.self, forKey: .tlsPinSPKI)
        deviceID = try c.decodeIfPresent(String.self, forKey: .deviceID) ?? ""
        isPaired = try c.decode(Bool.self, forKey: .isPaired)
        lastSeen = try c.decodeIfPresent(String.self, forKey: .lastSeen)
    }

    /// The transport for reaching this daemon. Always remote on iOS.
    public var transport: GraithTransport {
        .remote(host: magicDNSName, port: port, tlsPinSPKI: tlsPinSPKI.isEmpty ? nil : tlsPinSPKI)
    }
}

/// The registry of daemons the app knows about. Persists non-secret entry
/// metadata to a JSON file in Application Support and per-host client tokens to
/// the Keychain. Observable so the SwiftUI shell reacts to add/remove/pair.
@MainActor
public final class HostRegistry: ObservableObject {
    @Published public private(set) var hosts: [HostEntry] = []

    private let keychain: SecretStore
    private let storeURL: URL
    private let encoder = JSONEncoder()
    private let decoder = JSONDecoder()

    /// - Parameters:
    ///   - keychain: backing store for per-host client tokens.
    ///   - storeURL: JSON file for entry metadata. Defaults to
    ///     `<ApplicationSupport>/graith/hosts.json`.
    public init(keychain: SecretStore, storeURL: URL? = nil) {
        self.keychain = keychain
        self.storeURL = storeURL ?? HostRegistry.defaultStoreURL()
        load()
    }

    // MARK: - Query

    public func host(id: String) -> HostEntry? {
        hosts.first { $0.id == id }
    }

    /// Build the credentials to present when connecting to `host`, reading the
    /// client token from the Keychain. Returns nil (⇒ `notPaired`) if there is
    /// no stored token yet.
    ///
    /// Security invariant: an authenticated connection must never run in
    /// accept-any-cert (TOFU) mode. A host with a token but an empty
    /// `tlsPinSPKI` would connect with a nil pin, which the transport treats as
    /// first-contact TOFU — so we refuse to vend credentials for it and treat
    /// it as unpaired. (The tokenless pairing lane doesn't go through here, so
    /// it keeps working.)
    public func credentials(for host: HostEntry) -> HostCredentials? {
        guard let token = try? keychain.string(for: Self.tokenAccount(host.id)), !token.isEmpty else {
            return nil
        }
        guard !host.tlsPinSPKI.isEmpty else {
            NSLog("HostRegistry: refusing credentials for host \(host.id) — token present but no TLS pin (would connect in accept-any mode); re-pair required")
            return nil
        }
        return HostCredentials(
            clientToken: token,
            deviceID: host.deviceID,
            daemonProfile: host.daemonProfile,
            tlsPinSPKI: host.tlsPinSPKI
        )
    }

    // MARK: - Mutations

    /// Add or replace a host entry (metadata only; token set separately at pairing).
    public func upsert(_ entry: HostEntry) {
        if let idx = hosts.firstIndex(where: { $0.id == entry.id }) {
            hosts[idx] = entry
        } else {
            hosts.append(entry)
        }
        persist()
    }

    /// Record the result of a successful pairing: store the token in the
    /// Keychain and mark the entry paired with its profile + TLS pin.
    public func completePairing(hostID: String, response: PairResponse) throws {
        try keychain.set(response.clientToken, for: Self.tokenAccount(hostID))
        guard let idx = hosts.firstIndex(where: { $0.id == hostID }) else { return }
        hosts[idx].isPaired = true
        hosts[idx].daemonProfile = response.daemonProfile
        hosts[idx].tlsPinSPKI = response.tlsPinSPKI
        // Record the daemon-assigned device ID on the entry (per host, not global).
        hosts[idx].deviceID = response.deviceID
        persist()
    }

    /// Update the last-seen timestamp for a host (display only).
    public func markSeen(hostID: String, at iso: String) {
        guard let idx = hosts.firstIndex(where: { $0.id == hostID }) else { return }
        hosts[idx].lastSeen = iso
        persist()
    }

    /// Remove a host and wipe its client token from the Keychain.
    public func remove(hostID: String) {
        hosts.removeAll { $0.id == hostID }
        try? keychain.remove(Self.tokenAccount(hostID))
        persist()
    }

    // MARK: - Persistence

    private func load() {
        guard let data = try? Data(contentsOf: storeURL) else { return }
        if let decoded = try? decoder.decode([HostEntry].self, from: data) {
            hosts = decoded
        }
    }

    private func persist() {
        do {
            try FileManager.default.createDirectory(
                at: storeURL.deletingLastPathComponent(),
                withIntermediateDirectories: true
            )
            let data = try encoder.encode(hosts)
            try data.write(to: storeURL, options: .atomic)
        } catch {
            // Non-fatal: the registry stays in memory for this session.
            NSLog("HostRegistry persist failed: \(error)")
        }
    }

    private static func tokenAccount(_ hostID: String) -> String {
        "host.\(hostID).clientToken"
    }

    private static func defaultStoreURL() -> URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? FileManager.default.temporaryDirectory
        return base.appendingPathComponent("graith", isDirectory: true)
            .appendingPathComponent("hosts.json")
    }
}
