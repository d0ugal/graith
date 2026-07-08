import Foundation
import Security

/// A thin wrapper over the Keychain (`SecItem*`) for the secrets the app holds:
/// the device ed25519 private key and per-host client tokens.
///
/// All items are stored as generic passwords, scoped to this device only
/// (`kSecAttrAccessibleWhenUnlockedThisDeviceOnly`) and **not** synced to iCloud
/// Keychain (`kSecAttrSynchronizable = false`) — a client token minted for one
/// physical device must not silently appear on another (design §B.2: tokens are
/// per-device and identity-bound).
public struct KeychainStore: Sendable {
    /// The Keychain service string namespacing all graith items.
    public let service: String

    public init(service: String = "com.graith.mobile") {
        self.service = service
    }

    public enum KeychainError: Error, Sendable {
        case unexpectedStatus(OSStatus)
        case dataConversion
    }

    // MARK: - Read

    /// Return the raw data stored under `account`, or nil if absent.
    public func data(for account: String) throws -> Data? {
        var query = baseQuery(account: account)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne

        var item: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &item)
        switch status {
        case errSecSuccess:
            guard let data = item as? Data else { throw KeychainError.dataConversion }
            return data
        case errSecItemNotFound:
            return nil
        default:
            throw KeychainError.unexpectedStatus(status)
        }
    }

    public func string(for account: String) throws -> String? {
        guard let data = try data(for: account) else { return nil }
        guard let s = String(data: data, encoding: .utf8) else { throw KeychainError.dataConversion }
        return s
    }

    // MARK: - Write

    /// Store `data` under `account`, replacing any existing value.
    public func set(_ data: Data, for account: String) throws {
        // Try update first; add if missing. Avoids errSecDuplicateItem.
        let update: [String: Any] = [kSecValueData as String: data]
        let status = SecItemUpdate(baseQuery(account: account) as CFDictionary, update as CFDictionary)
        switch status {
        case errSecSuccess:
            return
        case errSecItemNotFound:
            var add = baseQuery(account: account)
            add[kSecValueData as String] = data
            add[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
            let addStatus = SecItemAdd(add as CFDictionary, nil)
            guard addStatus == errSecSuccess else { throw KeychainError.unexpectedStatus(addStatus) }
        default:
            throw KeychainError.unexpectedStatus(status)
        }
    }

    public func set(_ string: String, for account: String) throws {
        guard let data = string.data(using: .utf8) else { throw KeychainError.dataConversion }
        try set(data, for: account)
    }

    // MARK: - Delete

    /// Remove the item under `account`. No-op if absent.
    public func remove(_ account: String) throws {
        let status = SecItemDelete(baseQuery(account: account) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw KeychainError.unexpectedStatus(status)
        }
    }

    // MARK: - Helpers

    private func baseQuery(account: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecAttrSynchronizable as String: false,
        ]
    }
}
