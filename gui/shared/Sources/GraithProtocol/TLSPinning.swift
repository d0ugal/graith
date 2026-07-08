import CryptoKit
import Foundation
import Security

/// SPKI (Subject Public Key Info) certificate pinning for the remote (tailnet)
/// transport.
///
/// The pin is `base64(SHA256(DER-encoded SubjectPublicKeyInfo))` of the leaf
/// certificate's public key — pinning the **key**, so it survives certificate
/// renewal (a Let's Encrypt leaf re-issued for the same tsnet key keeps the
/// same pin), per design §A.3.
///
/// > Warning: **Unvalidated.** This path has two open dependencies that a Mac
/// > with Xcode must close (tracked in `gui/NEEDS-MAC-VALIDATION.md`):
/// > 1. It has not been run against a live TLS endpoint here.
/// > 2. The exact SPKI formula must be reconciled with the daemon's TLS task
/// >    (design Task 7) once it lands, so both sides hash identical bytes.
/// >
/// > The local Unix-socket transport (the macOS v1 path) does **not** use TLS,
/// > so this does not gate the macOS deliverable.
enum TLSPinning {
    /// Returns true iff the leaf certificate of `trust` has an SPKI pin equal
    /// to `expectedBase64`. Fails closed (returns false) on any extraction
    /// error.
    static func leafMatchesSPKI(_ trust: SecTrust, expectedBase64: String) -> Bool {
        guard let got = leafSPKIBase64(trust) else { return false }
        return constantTimeEqual(got, expectedBase64)
    }

    /// The base64 SHA-256 SPKI pin of the leaf certificate of `trust`, or nil
    /// on any extraction failure. Used by the pairing lane to capture the pin of
    /// the cert actually presented (TOFU) so it can be bound to the
    /// daemon-reported pin before being stored.
    static func leafSPKIBase64(_ trust: SecTrust) -> String? {
        guard let leaf = leafCertificate(of: trust),
              let key = SecCertificateCopyKey(leaf) else {
            return nil
        }
        return spkiPinBase64(forPublicKey: key)
    }

    private static func leafCertificate(of trust: SecTrust) -> SecCertificate? {
        if #available(macOS 12.0, iOS 15.0, *) {
            let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate]
            return chain?.first
        } else {
            guard SecTrustGetCertificateCount(trust) > 0 else { return nil }
            return SecTrustGetCertificateAtIndex(trust, 0)
        }
    }

    /// base64(SHA256(DER-encoded SubjectPublicKeyInfo)) for a public key, or nil
    /// on any extraction failure.
    ///
    /// `SecKeyCopyExternalRepresentation` returns the raw key (PKCS#1 modulus
    /// for RSA, the EC point for EC), *not* the SPKI wrapper. We reconstruct
    /// the SPKI DER by prepending the fixed ASN.1 algorithm-identifier header
    /// for the key's type+size — the standard iOS SPKI-pinning technique. This
    /// must hash the identical bytes to the daemon's `x509.MarshalPKIXPublicKey`
    /// (see internal/daemon/tls.go — the cert is EC P-256), which the
    /// `TLSPinningTests` known-answer test verifies.
    ///
    /// Internal (not private) so the KAT test can drive it with a reconstructed
    /// `SecKey` without needing a live certificate/TLS endpoint.
    static func spkiPinBase64(forPublicKey key: SecKey) -> String? {
        guard let attrs = SecKeyCopyAttributes(key) as? [CFString: Any],
              let keyType = attrs[kSecAttrKeyType] as? String,
              let keySize = attrs[kSecAttrKeySizeInBits] as? Int,
              let header = asn1Header(keyType: keyType, keySize: keySize),
              let keyData = SecKeyCopyExternalRepresentation(key, nil) as Data? else {
            return nil
        }
        var spki = Data(header)
        spki.append(keyData)
        return Data(SHA256.hash(data: spki)).base64EncodedString()
    }

    /// Fixed ASN.1 SubjectPublicKeyInfo headers for the key types graith's
    /// daemon may present (EC P-256 for the self-signed cert; RSA-2048/4096 if
    /// the tailnet HTTPS cert uses RSA).
    private static func asn1Header(keyType: String, keySize: Int) -> [UInt8]? {
        let ec = kSecAttrKeyTypeECSECPrimeRandom as String
        let rsa = kSecAttrKeyTypeRSA as String
        if keyType == ec, keySize == 256 {
            return [0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2A, 0x86, 0x48,
                    0xCE, 0x3D, 0x02, 0x01, 0x06, 0x08, 0x2A, 0x86, 0x48,
                    0xCE, 0x3D, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00]
        }
        if keyType == rsa, keySize == 2048 {
            return [0x30, 0x82, 0x01, 0x22, 0x30, 0x0D, 0x06, 0x09, 0x2A,
                    0x86, 0x48, 0x86, 0xF7, 0x0D, 0x01, 0x01, 0x01, 0x05,
                    0x00, 0x03, 0x82, 0x01, 0x0F, 0x00]
        }
        if keyType == rsa, keySize == 4096 {
            return [0x30, 0x82, 0x02, 0x22, 0x30, 0x0D, 0x06, 0x09, 0x2A,
                    0x86, 0x48, 0x86, 0xF7, 0x0D, 0x01, 0x01, 0x01, 0x05,
                    0x00, 0x03, 0x82, 0x02, 0x0F, 0x00]
        }
        return nil
    }

    private static func constantTimeEqual(_ a: String, _ b: String) -> Bool {
        let ab = Array(a.utf8), bb = Array(b.utf8)
        guard ab.count == bb.count else { return false }
        var diff: UInt8 = 0
        for i in 0..<ab.count { diff |= ab[i] ^ bb[i] }
        return diff == 0
    }
}
