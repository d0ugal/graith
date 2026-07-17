package daemon

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"slices"
	"time"

	"github.com/d0ugal/graith/internal/atomicfile"
)

// writeTLSFile persists one file of the remote TLS generation. It is a package
// var wrapping the crash-safe atomicfile primitive so a reader always observes a
// complete file (never a truncated one), and so tests can inject a write/rename/
// fsync failure at a chosen generation step (issue #1327). The 0o600 perm keeps
// the private key owner-only.
var writeTLSFile = func(path string, data []byte) error {
	return atomicfile.Write(path, data, 0o600)
}

// This file provides the TLS primitives for the interface-mode remote listener
// (design §A.3): a persistent self-signed certificate and an SPKI pin. The pin
// is derived from the public key, so it is STABLE across certificate renewal
// (same key → same pin) — clients pin the SPKI, never the leaf certificate,
// which would break on rotation. tsnet mode uses tailnet HTTPS certs instead
// and computes its pin the same way from the served leaf's public key.

// computeSPKIPin returns the base64 SHA-256 of the DER-encoded
// SubjectPublicKeyInfo for pub. This is the value clients pin (TOFU) and the
// daemon returns in pair_response.
func computeSPKIPin(pub crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("marshal public key: %w", err)
	}

	sum := sha256.Sum256(der)

	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

// selfSignedCert builds a self-signed certificate for the given key, valid from
// now for ~10 years, advertising hostname (if non-empty) as a SAN. now is passed
// for deterministic tests.
func selfSignedCert(key *ecdsa.PrivateKey, hostname string, now time.Time) ([]byte, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("serial: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "graith-remote"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if hostname != "" {
		tmpl.DNSNames = []string{hostname}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	return der, nil
}

// loadOrCreateRemoteTLS loads the interface-mode TLS certificate and key from
// certPath/keyPath, generating and persisting a fresh self-signed pair (keyed on
// a new ECDSA P-256 key) when there is no usable prior generation. It returns
// the TLS certificate and its SPKI pin. Regenerating the certificate from the
// same persisted key yields the same pin. now is passed for deterministic tests.
//
// Crash safety (issue #1327): the certificate and key are treated as one
// recoverable generation. Every file is published with atomicfile (temp + fsync
// + rename), so a reader observes a complete file, never a truncated one. A
// reissue rewrites only the certificate (the private key — and therefore the
// SPKI pin — is preserved), so whichever certificate is on disk always pairs
// with the persisted key. When the on-disk pair is missing, unreadable, or a
// half-written/mismatched generation, a fresh pair is regenerated on this start
// rather than stranding the remote listener in tls.X509KeyPair.
func loadOrCreateRemoteTLS(certPath, keyPath, hostname string, now time.Time) (tls.Certificate, string, error) {
	certPEM, cerr := os.ReadFile(certPath)
	keyPEM, kerr := os.ReadFile(keyPath)

	if cerr == nil && kerr == nil {
		if cert, perr := tls.X509KeyPair(certPEM, keyPEM); perr == nil {
			// A complete prior generation exists. Reissue in place (SAN/expiry)
			// if needed, preserving it until the replacement is durable.
			return maybeReissueRemoteTLS(cert, keyPEM, certPath, hostname, now)
		} else {
			// Both files present but they do not form a usable pair: an
			// interrupted publication predating atomic writes, or externally
			// corrupted material. There is no good pair to preserve, so fall
			// through and regenerate a fresh generation on this start.
			slog.Warn("remote TLS material on disk is not a usable pair; regenerating", "err", perr, "cert", certPath, "key", keyPath)
		}
	}

	return generateRemoteTLS(certPath, keyPath, hostname, now)
}

// maybeReissueRemoteTLS reissues the certificate from the persisted private key
// when the hostname SAN changed or the leaf has expired, preserving the stable
// SPKI pin. The key is never rewritten, so the certificate is published as a
// single atomic replace: readers always see a complete certificate that pairs
// with the unchanged key. If that write fails, the prior (still loadable) pair
// is returned rather than an error, so a failed reissue never strands the remote
// listener — the reissue simply retries on the next start (issue #1327).
func maybeReissueRemoteTLS(cert tls.Certificate, keyPEM []byte, certPath, hostname string, now time.Time) (tls.Certificate, string, error) {
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("parse tls cert: %w", err)
	}

	wantDNS := []string(nil)
	if hostname != "" {
		wantDNS = []string{hostname}
	}

	if slices.Equal(leaf.DNSNames, wantDNS) && !now.Before(leaf.NotBefore) && now.Before(leaf.NotAfter) {
		pin, perr := computeSPKIPin(leaf.PublicKey)

		return cert, pin, perr
	}

	key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return tls.Certificate{}, "", fmt.Errorf("reload tls certificate: persisted private key is %T, want ECDSA", cert.PrivateKey)
	}

	der, err := selfSignedCert(key, hostname, now)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("reissue tls certificate: %w", err)
	}

	newCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	if werr := writeTLSFile(certPath, newCertPEM); werr != nil {
		// The replacement is not durable. Keep serving the prior valid pair so
		// remote service is not stranded; the reissue retries next start. The
		// pin is unchanged (same key), so it is safe to report either way.
		pin, perr := computeSPKIPin(leaf.PublicKey)
		if perr != nil {
			return tls.Certificate{}, "", perr
		}

		slog.Warn("remote TLS reissue write failed; keeping prior certificate", "err", werr, "cert", certPath)

		return cert, pin, nil
	}

	newCert, err := tls.X509KeyPair(newCertPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("load reissued tls keypair: %w", err)
	}

	newLeaf, err := x509.ParseCertificate(newCert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("parse reissued tls cert: %w", err)
	}

	pin, err := computeSPKIPin(newLeaf.PublicKey)
	if err != nil {
		return tls.Certificate{}, "", err
	}

	return newCert, pin, nil
}

// generateRemoteTLS mints a fresh ECDSA P-256 key and self-signed certificate
// and publishes both crash-safely. The private key is written first, then the
// certificate: if publication is interrupted between the two atomic renames, the
// next start sees a certificate-less key, treats the generation as incomplete
// (X509KeyPair or the missing-cert read fails), and regenerates — never loading
// a mismatched pair (issue #1327). Because this path is reached only when no
// usable prior generation exists, overwriting any leftover file is safe.
func generateRemoteTLS(certPath, keyPath, hostname string, now time.Time) (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("generate key: %w", err)
	}

	der, err := selfSignedCert(key, hostname, now)
	if err != nil {
		return tls.Certificate{}, "", err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("marshal key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := writeTLSFile(keyPath, keyPEM); err != nil {
		return tls.Certificate{}, "", fmt.Errorf("write key: %w", err)
	}

	if err := writeTLSFile(certPath, certPEM); err != nil {
		return tls.Certificate{}, "", fmt.Errorf("write cert: %w", err)
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("load generated keypair: %w", err)
	}

	pin, err := computeSPKIPin(&key.PublicKey)
	if err != nil {
		return tls.Certificate{}, "", err
	}

	return cert, pin, nil
}

// remoteTLSConfig returns a server TLS config for the interface-mode listener.
func remoteTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
}
