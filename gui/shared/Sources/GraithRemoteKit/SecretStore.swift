import Foundation

/// Abstracts secret storage so ``HostRegistry`` / ``DeviceIdentity`` can be unit
/// tested with an in-memory store instead of the real Keychain (which needs a
/// signed, entitled app to work). ``KeychainStore`` is the production conformer.
///
/// This is the shared (cross-platform) counterpart of the iOS track's
/// `GraithMobileKit.SecretStore`; the macOS app consumes it via `GraithRemoteKit`
/// so both platforms share one implementation of device identity + pairing.
public protocol SecretStore: Sendable {
    func data(for account: String) throws -> Data?
    func set(_ data: Data, for account: String) throws
    func remove(_ account: String) throws
}

public extension SecretStore {
    func string(for account: String) throws -> String? {
        guard let data = try data(for: account) else { return nil }
        return String(data: data, encoding: .utf8)
    }

    func set(_ string: String, for account: String) throws {
        try set(Data(string.utf8), for: account)
    }
}

/// An in-memory ``SecretStore`` for tests and unsigned dev builds (where the
/// Keychain is unavailable). Not persistent — the process losing it is the
/// point: an ad-hoc-signed `swift run` build gets an ephemeral device identity
/// rather than trapping on a Keychain access it can't make.
public final class InMemorySecretStore: SecretStore, @unchecked Sendable {
    private var items: [String: Data] = [:]
    private let lock = NSLock()

    public init() {}

    public func data(for account: String) throws -> Data? {
        lock.lock(); defer { lock.unlock() }
        return items[account]
    }

    public func set(_ data: Data, for account: String) throws {
        lock.lock(); defer { lock.unlock() }
        items[account] = data
    }

    public func remove(_ account: String) throws {
        lock.lock(); defer { lock.unlock() }
        items.removeValue(forKey: account)
    }
}
