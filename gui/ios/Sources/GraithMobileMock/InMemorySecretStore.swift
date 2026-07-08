import Foundation
import GraithMobileKit

/// A thread-safe in-memory `SecretStore` for unit tests (no Keychain / no
/// entitlements required).
public final class InMemorySecretStore: SecretStore, @unchecked Sendable {
    private let lock = NSLock()
    private var items: [String: Data] = [:]

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
        items[account] = nil
    }
}
