import Foundation
import Security
import Testing
@testable import GraithProtocol

struct TLSPinningTests {
    // Known-answer test guarding the SPKI-pin formula against drift.
    //
    // The daemon's remote TLS cert is EC P-256 (internal/daemon/tls.go:
    // `ecdsa.GenerateKey(elliptic.P256(), …)`) and it pins
    // `base64(SHA256(x509.MarshalPKIXPublicKey(pub)))`. The Swift side
    // reconstructs the same SubjectPublicKeyInfo DER by prepending a fixed
    // ASN.1 header to the raw EC point from `SecKeyCopyExternalRepresentation`.
    //
    // These fixtures were produced offline with OpenSSL from a throwaway
    // P-256 key, so any change to the header (or to the reconstruction) that
    // diverges from `MarshalPKIXPublicKey` fails this test:
    //
    //   openssl ecparam -name prime256v1 -genkey -noout -out k.pem
    //   openssl ec -in k.pem -pubout -outform DER | tail -c 65 | base64      # rawPoint
    //   openssl ec -in k.pem -pubout -outform DER \
    //     | openssl dgst -sha256 -binary | base64                            # expectedPin
    @Test
    func knownAnswerECP256SPKIPin() throws {
        // Raw uncompressed EC point (0x04 || X || Y), 65 bytes, base64.
        let rawPoint = try #require(Data(base64Encoded:
            "BOdpeS48YFU8+AGHQoGdMcUt8xBEyebyuvzBUVPBKaaKQx4nW43fNTRry7yuHIvgfJjE9zmhbH9fDcl2H9924dQ="))
        let expectedPin = "67dbAyUwRJ7W5XB61p3uH4QdkGGqQE24VE+4bR7/iy0="

        let attrs: [CFString: Any] = [
            kSecAttrKeyType: kSecAttrKeyTypeECSECPrimeRandom,
            kSecAttrKeyClass: kSecAttrKeyClassPublic,
            kSecAttrKeySizeInBits: 256,
        ]
        var error: Unmanaged<CFError>?
        let key = try #require(
            SecKeyCreateWithData(rawPoint as CFData, attrs as CFDictionary, &error),
            "failed to build SecKey: \(String(describing: error?.takeRetainedValue()))"
        )

        #expect(TLSPinning.spkiPinBase64(forPublicKey: key) == expectedPin)
    }
}
