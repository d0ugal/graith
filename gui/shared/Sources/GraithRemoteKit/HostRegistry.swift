import Foundation
import Combine
import GraithProtocol

/// The registry of daemons the app knows about. Persists non-secret host
/// metadata to a JSON file and per-host client tokens to the ``SecretStore``
/// (Keychain). Observable so the SwiftUI shell reacts to add/remove/pair.
///
/// The local host is always present and never removable. Only remote hosts are
/// persisted to disk (the local host is re-seeded from the app at launch, so a
/// changed socket path is never stale).
public enum HostRegistryError: Error, Equatable {
    /// `completePairing` was called for a host that is not in the registry
    /// (its placeholder was removed before the pairing was confirmed).
    case unknownHost(String)
}

@MainActor
public final class HostRegistry: ObservableObject {
    @Published public private(set) var hosts: [Host] = []

    private let keychain: SecretStore
    /// The local daemon entry, or nil on platforms with no local daemon (iOS,
    /// which only ever talks to remote hosts over the tailnet). When present it
    /// is always kept first and never removable.
    private let localHost: Host?
    private let storeURL: URL
    private let encoder = JSONEncoder()
    private let decoder = JSONDecoder()

    /// - Parameters:
    ///   - keychain: backing store for per-host client tokens.
    ///   - localHost: the local daemon entry (its socket path is platform-specific,
    ///     so the app resolves it). Always kept present and never removable.
    ///   - storeURL: JSON file for remote-host metadata. Defaults to
    ///     `<ApplicationSupport>/graith-app/hosts.json`.
    public init(keychain: SecretStore, localHost: Host, storeURL: URL? = nil) {
        self.keychain = keychain
        self.localHost = localHost
        self.storeURL = storeURL ?? HostRegistry.defaultStoreURL()
        load()
    }

    /// Remote-only registry for platforms with no local daemon (iOS). Identical
    /// to the designated init but seeds no local host, so `hosts` is exactly the
    /// persisted set of paired remotes.
    public init(keychain: SecretStore, storeURL: URL? = nil) {
        self.keychain = keychain
        self.localHost = nil
        self.storeURL = storeURL ?? HostRegistry.defaultStoreURL()
        load()
    }

    // MARK: - Query

    public func host(id: String) -> Host? {
        hosts.first { $0.id == id }
    }

    /// Build the credentials to present when connecting to `host`, reading the
    /// client token from the store. Returns nil for a local host (it connects
    /// tokenless over the Unix socket) and for a remote host that isn't fully
    /// paired.
    ///
    /// Security invariant: an authenticated connection must never run in
    /// accept-any-cert (TOFU) mode. A host with a token but an empty
    /// `tlsPinSPKI` would connect with a nil pin, which the transport treats as
    /// first-contact TOFU — so we refuse to vend credentials for it and treat
    /// it as unpaired. (The tokenless pairing lane doesn't go through here, so
    /// it keeps working.)
    public func credentials(for host: Host) -> HostCredentials? {
        guard host.kind == .remote else { return nil }
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

    /// Add or replace a host entry (metadata only; token set separately at
    /// pairing). The local host cannot be replaced through here.
    public func upsert(_ host: Host) {
        guard host.id != localHost?.id else { return }
        if let idx = hosts.firstIndex(where: { $0.id == host.id }) {
            hosts[idx] = host
        } else {
            hosts.append(host)
        }
        persist()
    }

    /// Record the result of a successful pairing: store the token in the
    /// Keychain and mark the entry paired with its profile + TLS pin + device ID.
    ///
    /// The host row is checked *before* the token is written, so a pairing
    /// confirmed after its placeholder was forgotten can't orphan a token in the
    /// Keychain with no host referencing it.
    public func completePairing(hostID: String, response: PairResponseMsg) throws {
        guard let idx = hosts.firstIndex(where: { $0.id == hostID }) else {
            throw HostRegistryError.unknownHost(hostID)
        }
        try keychain.set(response.clientToken, for: Self.tokenAccount(hostID))
        hosts[idx].isPaired = true
        hosts[idx].daemonProfile = response.daemonProfile
        hosts[idx].tlsPinSPKI = response.tlsPinSPKI
        hosts[idx].deviceID = response.deviceID
        persist()
    }

    /// Update the last-seen timestamp for a host (display only).
    public func markSeen(hostID: String, at date: Date) {
        guard let idx = hosts.firstIndex(where: { $0.id == hostID }) else { return }
        hosts[idx].lastSeen = date
        persist()
    }

    /// Remove a host and wipe its client token from the store. The local host is
    /// never removed.
    public func remove(hostID: String) {
        guard hostID != localHost?.id else { return }
        hosts.removeAll { $0.id == hostID }
        try? keychain.remove(Self.tokenAccount(hostID))
        persist()
    }

    // MARK: - Persistence

    private func load() {
        var remotes: [Host] = []
        if let data = try? Data(contentsOf: storeURL),
           let decoded = try? decoder.decode([Host].self, from: data) {
            remotes = decoded.filter { $0.kind == .remote }
        }
        // Local host always first (when present), then persisted remotes.
        hosts = [localHost].compactMap { $0 } + remotes
    }

    private func persist() {
        // Only remote hosts are persisted — the local host is re-seeded at launch.
        let remotes = hosts.filter { $0.kind == .remote }
        do {
            try FileManager.default.createDirectory(
                at: storeURL.deletingLastPathComponent(),
                withIntermediateDirectories: true
            )
            let data = try encoder.encode(remotes)
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
        return base.appendingPathComponent("graith-app", isDirectory: true)
            .appendingPathComponent("hosts.json")
    }
}
