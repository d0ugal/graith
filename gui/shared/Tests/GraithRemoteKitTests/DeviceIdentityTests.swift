import Foundation
import Testing
import CryptoKit
@testable import GraithRemoteKit

/// The device ed25519 identity: key persistence, signing, and per-host scoping.
struct DeviceIdentityTests {
    @Test func keyPersistsAcrossInstancesSharingAStore() throws {
        let store = InMemorySecretStore()
        let first = try DeviceIdentity(keychain: store)
        let pub1 = try first.publicKeyRaw()

        // A second identity over the same store must load the same key, not mint
        // a fresh one — otherwise every launch would need re-pairing.
        let second = try DeviceIdentity(keychain: store)
        let pub2 = try second.publicKeyRaw()

        #expect(pub1 == pub2)
        #expect(pub1.count == 32)
    }

    @Test func signatureVerifiesAgainstPublicKey() throws {
        let identity = try DeviceIdentity(keychain: InMemorySecretStore())
        let nonce = Data("dreich-nonce".utf8)

        let sig = try identity.sign(nonce)
        #expect(sig.count == 64)

        let pub = try Curve25519.Signing.PublicKey(rawRepresentation: identity.publicKeyRaw())
        #expect(pub.isValidSignature(sig, for: nonce))
        // A different message must not verify.
        #expect(!pub.isValidSignature(sig, for: Data("haar".utf8)))
    }

    @Test func deviceIDPersistsAndResets() throws {
        let store = InMemorySecretStore()
        let identity = try DeviceIdentity(keychain: store)
        #expect(identity.deviceID.isEmpty)
        #expect(!identity.isPaired)

        try identity.setDeviceID("dev-ben")
        #expect(identity.deviceID == "dev-ben")
        #expect(identity.isPaired)

        // A fresh instance over the same store recovers the device ID.
        let reloaded = try DeviceIdentity(keychain: store)
        #expect(reloaded.deviceID == "dev-ben")

        try identity.reset()
        #expect(identity.deviceID.isEmpty)
        #expect(!identity.isPaired)
    }

    @Test func hostScopedSignerOverridesIDButReusesKey() throws {
        let base = try DeviceIdentity(keychain: InMemorySecretStore())
        try base.setDeviceID("global-id")

        let scopedA = HostScopedSigner(base: base, deviceID: "dev-A")
        let scopedB = HostScopedSigner(base: base, deviceID: "dev-B")

        // Each host presents its own device ID...
        #expect(scopedA.deviceID == "dev-A")
        #expect(scopedB.deviceID == "dev-B")
        // ...but signs with the same underlying key.
        let nonce = Data("brig".utf8)
        #expect(try scopedA.sign(nonce) == base.sign(nonce))
        #expect(try scopedA.publicKeyRaw() == base.publicKeyRaw())

        // And the proof carries the scoped ID.
        let proof = try scopedA.proof(forNonce: "brig", channelBinding: "brig-spki")
        #expect(proof.deviceID == "dev-A")
    }
}
