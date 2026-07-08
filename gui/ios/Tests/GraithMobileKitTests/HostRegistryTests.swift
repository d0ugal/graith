import XCTest
import GraithClientAPI
@testable import GraithMobileKit
import GraithMobileMock

final class HostRegistryTests: XCTestCase {
    @MainActor
    private func makeRegistry(_ secrets: InMemorySecretStore) -> HostRegistry {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-test-\(UUID().uuidString)")
            .appendingPathComponent("hosts.json")
        return HostRegistry(keychain: secrets, storeURL: url)
    }

    @MainActor
    func testUpsertAndRemove() {
        let registry = makeRegistry(InMemorySecretStore())
        let ben = HostEntry(id: "ben", label: "ben", magicDNSName: "graith-ben.ts.net")
        registry.upsert(ben)
        XCTAssertEqual(registry.hosts.count, 1)
        XCTAssertEqual(registry.host(id: "ben")?.label, "ben")

        registry.remove(hostID: "ben")
        XCTAssertTrue(registry.hosts.isEmpty)
    }

    @MainActor
    func testCompletePairingStoresTokenInSecretStore() throws {
        let secrets = InMemorySecretStore()
        let registry = makeRegistry(secrets)
        let brae = HostEntry(id: "brae", label: "brae", magicDNSName: "graith-brae.ts.net")
        registry.upsert(brae)

        let response = PairResponse(deviceID: "dev-bairn", clientToken: "tok-canny",
                                    daemonProfile: "default", tlsPinSPKI: "cGlu")
        try registry.completePairing(hostID: "brae", response: response)

        XCTAssertTrue(registry.host(id: "brae")?.isPaired ?? false)
        XCTAssertEqual(registry.host(id: "brae")?.daemonProfile, "default")

        // Credentials come back with the token from the secret store.
        let creds = registry.credentials(for: registry.host(id: "brae")!, deviceID: "dev-bairn")
        XCTAssertEqual(creds?.clientToken, "tok-canny")
        XCTAssertEqual(creds?.deviceID, "dev-bairn")
    }

    @MainActor
    func testCredentialsNilWhenUnpaired() {
        let registry = makeRegistry(InMemorySecretStore())
        let dreich = HostEntry(id: "dreich", label: "dreich", magicDNSName: "graith-dreich.ts.net")
        registry.upsert(dreich)
        XCTAssertNil(registry.credentials(for: dreich, deviceID: "dev"))
    }

    @MainActor
    func testRemoveWipesToken() throws {
        let secrets = InMemorySecretStore()
        let registry = makeRegistry(secrets)
        let ben = HostEntry(id: "ben", label: "ben", magicDNSName: "graith-ben.ts.net")
        registry.upsert(ben)
        try registry.completePairing(hostID: "ben", response:
            PairResponse(deviceID: "d", clientToken: "tok", daemonProfile: "p", tlsPinSPKI: "s"))

        registry.remove(hostID: "ben")
        XCTAssertNil(try secrets.string(for: "host.ben.clientToken"))
    }

    @MainActor
    func testPersistenceRoundTrip() throws {
        let secrets = InMemorySecretStore()
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("graith-persist-\(UUID().uuidString)")
            .appendingPathComponent("hosts.json")

        let first = HostRegistry(keychain: secrets, storeURL: url)
        first.upsert(HostEntry(id: "bide", label: "bide", magicDNSName: "graith-bide.ts.net", port: 4823))

        // A fresh registry over the same file reloads the entry.
        let second = HostRegistry(keychain: secrets, storeURL: url)
        XCTAssertEqual(second.host(id: "bide")?.magicDNSName, "graith-bide.ts.net")
    }
}
