import Foundation
import GraithProtocol

/// A daemon the app can connect to — either the local daemon over its Unix
/// socket, or a remote daemon over the tailnet (design §C.4).
///
/// Tokens are modelled here but MUST be stored in the Keychain, not in this
/// Codable blob — `clientToken` is a Keychain *reference key*, not the secret
/// itself. (Wiring the Keychain + the ed25519 `DeviceKeySigner` for remote
/// hosts is shared with the iOS track's `HostRegistry`; see
/// gui/NEEDS-MAC-VALIDATION.md.)
struct Host: Codable, Identifiable, Hashable {
    enum Kind: String, Codable {
        case local   // the daemon on this machine, over its Unix socket
        case remote  // a tailnet daemon, over NWConnection + TLS
    }

    var id: String
    var label: String
    var kind: Kind

    // Local
    var socketPath: String?

    // Remote
    var magicDNSName: String?
    var port: UInt16?
    /// Keychain reference key for this host's paired client token.
    var clientTokenRef: String?
    /// TLS SPKI pin captured at pairing (TOFU).
    var tlsPinSPKI: String?
    /// The daemon profile (the handshake carries it; the daemon rejects a
    /// mismatch — `handler.go:110`).
    var daemonProfile: String

    var lastSeen: Date?

    /// The transport for this host.
    var transport: GraithTransport {
        switch kind {
        case .local:
            return .unix(path: socketPath ?? GraithLocalSocket.defaultPath())
        case .remote:
            return .remote(host: magicDNSName ?? "", port: port ?? 4823, tlsPinSPKI: tlsPinSPKI)
        }
    }

    /// The entry for the local daemon on this machine.
    static func local() -> Host {
        Host(
            id: "local",
            label: "This Mac",
            kind: .local,
            socketPath: GraithLocalSocket.defaultPath(),
            daemonProfile: GraithLocalSocket.profile
        )
    }
}

/// Persisted registry of daemons, with a factory that builds a
/// `GraithProtocolClient` per host (design §C.4: the macOS app gains multi-host
/// for free by reusing the transport-abstract client — `.unix` locally,
/// `.remote`+TLS over the tailnet).
///
/// > This is the macOS-side foundation. The full host-tier sidebar UI, the
/// > pairing flow (`pair_request` → local `gr pair approve` → store token+SPKI),
/// > and the Keychain-backed `DeviceKeySigner` for remote PoP are the remaining
/// > Mac work, and are intended to unify with the iOS track's shared
/// > `HostRegistry` at the Phase 2 merge. Tracked in gui/NEEDS-MAC-VALIDATION.md.
@MainActor
final class HostRegistry: ObservableObject {
    @Published private(set) var hosts: [Host]

    private let storeURL: URL

    init() {
        let dir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("graith-app", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        self.storeURL = dir.appendingPathComponent("hosts.json")

        if let data = try? Data(contentsOf: storeURL),
           let decoded = try? JSONDecoder().decode([Host].self, from: data), !decoded.isEmpty {
            self.hosts = decoded
        } else {
            // Always at least the local daemon.
            self.hosts = [.local()]
        }
    }

    func add(_ host: Host) {
        hosts.removeAll { $0.id == host.id }
        hosts.append(host)
        persist()
    }

    func remove(_ host: Host) {
        guard host.kind != .local else { return } // never drop the local host
        hosts.removeAll { $0.id == host.id }
        persist()
    }

    /// Build a protocol client for a host. `token`/`signer` come from the
    /// Keychain layer for remote hosts (nil for local).
    func makeClient(for host: Host, token: String? = nil, signer: DeviceKeySigner? = nil) -> GraithProtocolClient {
        GraithProtocolClient(
            transport: host.transport,
            profile: host.daemonProfile,
            clientID: "graith-macos",
            token: token,
            signer: signer
        )
    }

    private func persist() {
        guard let data = try? JSONEncoder().encode(hosts) else { return }
        try? data.write(to: storeURL)
    }
}
