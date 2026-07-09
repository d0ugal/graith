import Foundation
import GraithProtocol

/// A daemon the app can connect to — either the local daemon over its Unix
/// socket (macOS), or a remote daemon over the tailnet.
///
/// Non-secret metadata only. The client token is **not** stored here — it lives
/// in the ``SecretStore`` (Keychain), keyed by ``id``. `tlsPinSPKI` /
/// `deviceID` are captured at pairing.
public struct Host: Codable, Identifiable, Hashable, Sendable {
    public enum Kind: String, Codable, Sendable {
        case local   // the daemon on this machine, over its Unix socket
        case remote  // a tailnet daemon, over NWConnection + TLS
    }

    public var id: String
    public var label: String
    public var kind: Kind

    // Local
    public var socketPath: String?

    // Remote
    public var magicDNSName: String?
    public var port: UInt16
    /// The daemon profile, recorded at pairing — the handshake rejects on
    /// mismatch (`handler.go` profile check), so it must be presented back.
    public var daemonProfile: String
    /// SPKI (public-key) pin captured at pairing, for TLS validation (TOFU).
    public var tlsPinSPKI: String
    /// The device ID this daemon assigned us at pairing. Stored **per host**:
    /// the ed25519 key is one global device key, but each daemon issues its own
    /// device ID, and proof-of-possession must present the ID for the host being
    /// connected to (a global ID would let pairing host B clobber host A).
    public var deviceID: String
    /// Whether pairing completed and a client token exists in the store.
    /// Always true for a local host (no pairing needed over the Unix socket).
    public var isPaired: Bool
    public var lastSeen: Date?

    public init(
        id: String,
        label: String,
        kind: Kind,
        socketPath: String? = nil,
        magicDNSName: String? = nil,
        port: UInt16 = 4823,
        daemonProfile: String = "",
        tlsPinSPKI: String = "",
        deviceID: String = "",
        isPaired: Bool = false,
        lastSeen: Date? = nil
    ) {
        self.id = id
        self.label = label
        self.kind = kind
        self.socketPath = socketPath
        self.magicDNSName = magicDNSName
        self.port = port
        self.daemonProfile = daemonProfile
        self.tlsPinSPKI = tlsPinSPKI
        self.deviceID = deviceID
        self.isPaired = isPaired
        self.lastSeen = lastSeen
    }

    private enum CodingKeys: String, CodingKey {
        case id, label, kind, socketPath, magicDNSName, port
        case daemonProfile, tlsPinSPKI, deviceID, isPaired, lastSeen
    }

    // Custom decode so entries persisted before a field existed still load
    // (synthesized Codable throws on a missing key even with a default value,
    // which would otherwise drop every previously-paired host on upgrade).
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        label = try c.decode(String.self, forKey: .label)
        kind = try c.decodeIfPresent(Kind.self, forKey: .kind) ?? .remote
        socketPath = try c.decodeIfPresent(String.self, forKey: .socketPath)
        magicDNSName = try c.decodeIfPresent(String.self, forKey: .magicDNSName)
        port = try c.decodeIfPresent(UInt16.self, forKey: .port) ?? 4823
        daemonProfile = try c.decodeIfPresent(String.self, forKey: .daemonProfile) ?? ""
        tlsPinSPKI = try c.decodeIfPresent(String.self, forKey: .tlsPinSPKI) ?? ""
        deviceID = try c.decodeIfPresent(String.self, forKey: .deviceID) ?? ""
        isPaired = try c.decodeIfPresent(Bool.self, forKey: .isPaired) ?? false
        lastSeen = try c.decodeIfPresent(Date.self, forKey: .lastSeen)
    }

    /// The transport for reaching this daemon.
    public var transport: GraithTransport {
        switch kind {
        case .local:
            return .unix(path: socketPath ?? "")
        case .remote:
            return .remote(
                host: magicDNSName ?? "",
                port: port,
                tlsPinSPKI: tlsPinSPKI.isEmpty ? nil : tlsPinSPKI
            )
        }
    }

    public var isRemote: Bool { kind == .remote }

    /// Build the local-daemon host entry for this machine. The socket path is
    /// platform-specific, so the app resolves it and passes it in.
    public static func local(socketPath: String, profile: String = "") -> Host {
        Host(
            id: "local",
            label: "This Mac",
            kind: .local,
            socketPath: socketPath,
            daemonProfile: profile,
            isPaired: true
        )
    }
}

/// The credentials to present when connecting to a remote daemon. Assembled by
/// ``HostRegistry/credentials(for:)`` from the stored token + host metadata.
/// `nil` (⇒ not paired) when there is no stored token or no TLS pin.
public struct HostCredentials: Sendable, Hashable {
    public var clientToken: String
    public var deviceID: String
    public var daemonProfile: String
    public var tlsPinSPKI: String

    public init(clientToken: String, deviceID: String, daemonProfile: String, tlsPinSPKI: String) {
        self.clientToken = clientToken
        self.deviceID = deviceID
        self.daemonProfile = daemonProfile
        self.tlsPinSPKI = tlsPinSPKI
    }
}
