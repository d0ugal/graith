import Testing
import Foundation
import GraithProtocol
import GraithRemoteKit
@testable import GraithSessionKit

// Proves the production composition actually consumes the persisted
// `reachabilityProbeTimeout` preference (#1254): `FleetModel` threads the
// timeout it is constructed with into `TailnetReachability.probe` whenever the
// fleet fails to connect. Previously the configurable timeout had no caller —
// this is the regression that fails on the old behaviour (no probe at all).

@Suite("FleetModel — tailnet reachability probe wiring (#1254)", .timeLimit(.minutes(1)))
@MainActor
struct FleetReachabilityProbeTests {

    /// A scratch UserDefaults suite so a test's writes never touch the shared
    /// standard domain or leak between tests.
    private func scratchDefaults(_ name: String) -> UserDefaults {
        let defaults = UserDefaults(suiteName: name)!
        defaults.removePersistentDomain(forName: name)
        return defaults
    }

    /// Build a fleet with a single remote host whose client fails to connect,
    /// so `refreshReachability` takes the probe fallback path. Returns the
    /// fleet, the reachability (network path pre-marked present), and the box
    /// the injected prober records into.
    private func makeFailingFleet(
        probeTimeout: TimeInterval,
        proberResult: Bool
    ) -> (fleet: FleetModel, reach: TailnetReachability, captured: CapturedProbe) {
        let secrets = InMemorySecretStore()
        let identity = try! DeviceIdentity(keychain: secrets)
        let registry = HostRegistry(
            keychain: secrets,
            storeURL: FileManager.default.temporaryDirectory
                .appendingPathComponent("graith-reach-\(UUID().uuidString)", isDirectory: true)
                .appendingPathComponent("hosts.json")
        )
        registry.upsert(Host(id: "ben", label: "Ben Nevis", kind: .remote,
                             magicDNSName: "graith-ben.ts.net", port: 4823, isPaired: false))
        try! registry.completePairing(hostID: "ben", response: PairResponseMsg(
            deviceID: "dev-ben", clientToken: "tok-ben", daemonProfile: "", tlsPinSPKI: "cGlu"))

        // Client always fails to connect ⇒ no connection reaches `.connected`,
        // so the aggregate observation would say "not on tailnet" without a probe.
        let mock = MockHostClient(failConnect: .daemon("daemon doon"))
        let factory = MockFactory(clients: ["tok-ben": mock])
        let pairing = PairingCoordinator(pairing: StubPairing(), identity: identity, registry: registry)

        let captured = CapturedProbe()
        let reach = TailnetReachability(tcpProber: { host, port, timeout in
            captured.record(host: host, port: port, timeout: timeout)
            return proberResult
        })
        reach.applyNetworkPath(usable: true)   // network path present, tailnet unconfirmed

        let fleet = FleetModel(
            registry: registry, identity: identity, reachability: reach,
            factory: factory, pairing: pairing, subscribeApprovals: false,
            reachabilityProbeTimeout: probeTimeout)
        return (fleet, reach, captured)
    }

    /// The configured (persisted) timeout is threaded into the probe, and a
    /// reachable host proves we're on the tailnet even though every control
    /// connection failed (daemon down ≠ off the tailnet).
    @Test func connectFailureProbesWithPersistedTimeout() async {
        let defaults = scratchDefaults("reach.probe.timeout.braw")
        defaults.set(9.0, forKey: PresentationPreferences.Key.reachabilityProbeTimeout)
        let prefs = PresentationPreferences(userDefaults: defaults)

        let (fleet, reach, captured) = makeFailingFleet(
            probeTimeout: prefs.reachabilityProbeTimeout, proberResult: true)

        await fleet.connectAll()
        #expect(fleet.connections.first?.state != .connected)   // fleet is down
        #expect(captured.timeout == 9.0)                        // persisted timeout, not the 3s default
        #expect(captured.host == "graith-ben.ts.net")
        #expect(captured.port == 4823)
        #expect(reach.state == .onTailnet)                      // reachable host ⇒ on the tailnet
    }

    /// When the probe also fails, the banner state is "network up, tailnet down".
    @Test func connectFailureUnreachableHostReportsNotOnTailnet() async {
        let (fleet, reach, captured) = makeFailingFleet(probeTimeout: 5, proberResult: false)
        await fleet.connectAll()
        #expect(captured.timeout == 5)
        #expect(reach.state == .notOnTailnet)
    }
}

/// Records the arguments the injected prober was called with.
private final class CapturedProbe: @unchecked Sendable {
    private let lock = NSLock()
    private var _host: String?
    private var _port: UInt16?
    private var _timeout: TimeInterval?
    func record(host: String, port: UInt16, timeout: TimeInterval) {
        lock.lock(); defer { lock.unlock() }
        _host = host; _port = port; _timeout = timeout
    }
    var host: String? { lock.lock(); defer { lock.unlock() }; return _host }
    var port: UInt16? { lock.lock(); defer { lock.unlock() }; return _port }
    var timeout: TimeInterval? { lock.lock(); defer { lock.unlock() }; return _timeout }
}
