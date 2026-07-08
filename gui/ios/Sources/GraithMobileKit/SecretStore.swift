import Foundation

/// Abstracts secret storage so `HostRegistry` / `DeviceIdentity` can be unit
/// tested with an in-memory store instead of the real Keychain (which needs a
/// signed, entitled app to work). `KeychainStore` is the production conformer.
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

extension KeychainStore: SecretStore {}
