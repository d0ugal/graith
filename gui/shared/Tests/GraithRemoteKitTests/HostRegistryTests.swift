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
