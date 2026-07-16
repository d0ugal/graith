import Foundation
import Testing
import GraithProtocol
@testable import GraithRemoteKit

/// Guards that the remote-host default port is sourced from the centralized
/// `GraithTransport.defaultRemotePort` constant rather than a duplicated literal
/// (#1235), including the legacy-decode fallback for entries persisted before
/// the field existed.
struct HostDefaultPortTests {
    @Test func remoteHostDefaultsToCentralizedPort() {
        let host = Host(id: "ben", label: "graith-ben", kind: .remote, magicDNSName: "ben.ts.net")
        #expect(host.port == GraithTransport.defaultRemotePort)
    }

    @Test func decodeFallsBackToCentralizedPortWhenMissing() throws {
        // A host record persisted before `port` existed omits the key entirely.
        let json = Data(#"{"id":"brae","label":"graith-brae","kind":"remote"}"#.utf8)
        let host = try JSONDecoder().decode(Host.self, from: json)
        #expect(host.port == GraithTransport.defaultRemotePort)
    }
}
