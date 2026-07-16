import Foundation
import Testing
@testable import GraithProtocol

/// Guards the centralized remote-port default (#1235). The value is the single
/// source of truth the Swift clients share; it must stay in lockstep with the
/// Go `config.DefaultRemotePort` (4823).
struct TransportDefaultsTests {
    @Test func defaultRemotePortValue() {
        #expect(GraithTransport.defaultRemotePort == 4823)
    }
}
