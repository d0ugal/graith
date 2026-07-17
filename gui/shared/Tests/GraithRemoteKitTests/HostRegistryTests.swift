import Foundation
import Testing
@testable import GraithRemoteKit

@MainActor
struct HostRegistryTests {
    private func makeRegistry(store: SecretStore = InMemorySecretStore()) -> (HostRegistry, URL) {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-test-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let local = Host.local(socketPath: "/tmp/bothy/graith.sock")
        return (HostRegistry(keychain: store, localHost: local, storeURL: url), url)
    }

    @Test func seedsLocalHostAndKeepsItFirst() {
        let (registry, _) = makeRegistry()
        #expect(registry.hosts.count == 1)
        #expect(registry.hosts.first?.kind == .local)
        #expect(registry.hosts.first?.id == "local")
    }

    @Test func localHostIsNeverRemovable() {
        let (registry, _) = makeRegistry()
        registry.remove(hostID: "local")
        #expect(registry.hosts.contains { $0.kind == .local })
    }

    @Test func upsertAddsRemoteHost() {
        let (registry, _) = makeRegistry()
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        #expect(registry.host(id: "ben")?.label == "ben")
        #expect(registry.hosts.count == 2)
    }

    @Test func localHostVendsNoCredentials() {
        let (registry, _) = makeRegistry()
        let local = registry.host(id: "local")!
        #expect(registry.credentials(for: local) == nil)
    }

    @Test func completePairingStoresTokenAndVendsCredentials() {
        let store = InMemorySecretStore()
        let (registry, _) = makeRegistry(store: store)
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))

        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw",
                                    daemonProfile: "", tlsPinSPKI: "cGlu")
        try? registry.completePairing(hostID: "ben", response: resp)

        let host = registry.host(id: "ben")!
        #expect(host.isPaired)
        #expect(host.deviceID == "dev-ben")
        #expect(host.tlsPinSPKI == "cGlu")

        let creds = registry.credentials(for: host)
        #expect(creds?.clientToken == "tok-braw")
        #expect(creds?.deviceID == "dev-ben")
    }

    @Test func refusesCredentialsWhenTokenPresentButNoPin() {
        // A token but an empty TLS pin would connect in accept-any (TOFU) mode.
        // The registry must fail closed and treat the host as unpaired.
        let store = InMemorySecretStore()
        let (registry, _) = makeRegistry(store: store)
        registry.upsert(Host(id: "thrawn", label: "thrawn", kind: .remote, magicDNSName: "thrawn.tail.ts.net"))
        try? store.set("tok-only", for: "host.thrawn.clientToken")

        let host = registry.host(id: "thrawn")!
        #expect(host.tlsPinSPKI.isEmpty)
        #expect(registry.credentials(for: host) == nil)
    }

    @Test func completePairingThrowsForUnknownHostWithoutOrphaningToken() {
        // If the placeholder was forgotten before confirmation, completePairing
        // must not write a token that no host references.
        let store = InMemorySecretStore()
        let (registry, _) = makeRegistry(store: store)
        let resp = makePairResponse(deviceID: "dev", clientToken: "tok", tlsPinSPKI: "cGlu")
        #expect(throws: HostRegistryError.self) {
            try registry.completePairing(hostID: "ghost", response: resp)
        }
        #expect((try? store.string(for: "host.ghost.clientToken")) == nil)
    }

    @Test func failedRepairOfExistingHostLeavesPriorStateExactlyIntact() throws {
        // Re-pairing an existing paired host whose durable write then fails must
        // leave the token, pin, device ID, profile, paired flag, and on-disk JSON
        // exactly as they were before the attempt (issue #1299).
        let store = InMemorySecretStore()
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-repair-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent("hosts.json")
        let local = Host.local(socketPath: "/tmp/bothy/graith.sock")
        let registry = HostRegistry(keychain: store, localHost: local, storeURL: url)

        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        let first = makePairResponse(requestID: "req-1", deviceID: "dev-1", clientToken: "tok-1",
                                     daemonProfile: "profile-1", tlsPinSPKI: "cGluMQ==")
        try registry.completePairing(hostID: "ben", response: first)

        let priorJSON = try Data(contentsOf: url)

        // Make the store directory unwritable so the second durable write fails at
        // temp-file creation, before the existing hosts.json is touched.
        try FileManager.default.setAttributes([.posixPermissions: 0o500], ofItemAtPath: dir.path)
        defer { try? FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: dir.path) }

        let repair = makePairResponse(requestID: "req-2", deviceID: "dev-2", clientToken: "tok-2",
                                      daemonProfile: "profile-2", tlsPinSPKI: "cGluMg==")
        #expect(throws: (any Error).self) {
            try registry.completePairing(hostID: "ben", response: repair)
        }

        // In-memory metadata is exactly the prior pairing.
        let host = try #require(registry.host(id: "ben"))
        #expect(host.isPaired == true)
        #expect(host.tlsPinSPKI == "cGluMQ==")
        #expect(host.deviceID == "dev-1")
        #expect(host.daemonProfile == "profile-1")
        // The secret is the prior token, not the failed repair's.
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-1")
        // On-disk JSON is byte-for-byte the prior pairing.
        #expect(try Data(contentsOf: url) == priorJSON)
    }

    @Test func failedExistingHostRepairRestoresSecretWhenSetThrows() throws {
        // A SecretStore.set that applies its side effect then throws must still be
        // rolled back to the exact prior token and metadata (issue #1299).
        let store = ThrowOnceOnSetStore()
        let (registry, _) = makeRegistry(store: store)

        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        let first = makePairResponse(requestID: "req-1", deviceID: "dev-1", clientToken: "tok-1",
                                     daemonProfile: "profile-1", tlsPinSPKI: "cGluMQ==")
        try registry.completePairing(hostID: "ben", response: first)

        store.armSetFailure()
        let repair = makePairResponse(requestID: "req-2", deviceID: "dev-2", clientToken: "tok-2",
                                      daemonProfile: "profile-2", tlsPinSPKI: "cGluMg==")
        #expect(throws: (any Error).self) {
            try registry.completePairing(hostID: "ben", response: repair)
        }

        // The prior token and metadata survive the partial-then-failed set.
        #expect((try? store.string(for: "host.ben.clientToken")) == "tok-1")
        let host = try #require(registry.host(id: "ben"))
        #expect(host.tlsPinSPKI == "cGluMQ==")
        #expect(host.deviceID == "dev-1")
        #expect(host.daemonProfile == "profile-1")
        #expect(host.isPaired == true)
    }

    @Test func removeWipesToken() {
        let store = InMemorySecretStore()
        let (registry, _) = makeRegistry(store: store)
        registry.upsert(Host(id: "ben", label: "ben", kind: .remote, magicDNSName: "ben.tail.ts.net"))
        let resp = makePairResponse(deviceID: "dev-ben", clientToken: "tok-braw", tlsPinSPKI: "cGlu")
        try? registry.completePairing(hostID: "ben", response: resp)

        registry.remove(hostID: "ben")
        #expect(registry.host(id: "ben") == nil)
        #expect((try? store.string(for: "host.ben.clientToken")) == nil)
    }

    @Test func remoteHostsPersistAndReload() {
        let store = InMemorySecretStore()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-test-\(UUID().uuidString)", isDirectory: true)
            .appendingPathComponent("hosts.json")
        let local = Host.local(socketPath: "/tmp/bothy/graith.sock")

        let first = HostRegistry(keychain: store, localHost: local, storeURL: url)
        first.upsert(Host(id: "ben", label: "ben", kind: .remote,
                          magicDNSName: "ben.tail.ts.net", tlsPinSPKI: "cGlu",
                          deviceID: "dev-ben", isPaired: true))

        // A fresh registry over the same file recovers the remote host + local.
        let second = HostRegistry(keychain: store, localHost: local, storeURL: url)
        #expect(second.hosts.contains { $0.id == "local" })
        #expect(second.host(id: "ben")?.magicDNSName == "ben.tail.ts.net")
    }
}
