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
	"math/big"
	"os"
	"slices"
	"time"
)

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
// certPath/keyPath, generating and persisting a fresh self-signed pair (keyed
// on a new ECDSA P-256 key) if either is missing. It returns the TLS
// certificate and its SPKI pin. Regenerating the certificate from the same
// persisted key yields the same pin. now is passed for deterministic tests.
func loadOrCreateRemoteTLS(certPath, keyPath, hostname string, now time.Time) (tls.Certificate, string, error) {
	if certPEM, err := os.ReadFile(certPath); err == nil {
		if keyPEM, kerr := os.ReadFile(keyPath); kerr == nil {
			cert, cerr := tls.X509KeyPair(certPEM, keyPEM)
			if cerr != nil {
				return tls.Certificate{}, "", fmt.Errorf("load tls keypair: %w", cerr)
			}

			leaf, perr := x509.ParseCertificate(cert.Certificate[0])
			if perr != nil {
				return tls.Certificate{}, "", fmt.Errorf("parse tls cert: %w", perr)
			}

			wantDNS := []string(nil)
			if hostname != "" {
				wantDNS = []string{hostname}
			}

			// Hostname is listener-derived TLS state. Reissue the certificate
			// from the persisted private key when it changes (or the leaf has
			// expired), preserving the SPKI pin while applying the new SAN.
			if !slices.Equal(leaf.DNSNames, wantDNS) || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
				key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
				if !ok {
					return tls.Certificate{}, "", fmt.Errorf("reload tls certificate: persisted private key is %T, want ECDSA", cert.PrivateKey)
				}

				der, rerr := selfSignedCert(key, hostname, now)
				if rerr != nil {
					return tls.Certificate{}, "", fmt.Errorf("reissue tls certificate: %w", rerr)
				}

				certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
				if werr := os.WriteFile(certPath, certPEM, 0o600); werr != nil {
					return tls.Certificate{}, "", fmt.Errorf("write reissued cert: %w", werr)
				}

				cert, cerr = tls.X509KeyPair(certPEM, keyPEM)
				if cerr != nil {
					return tls.Certificate{}, "", fmt.Errorf("load reissued tls keypair: %w", cerr)
				}

				leaf, rerr = x509.ParseCertificate(cert.Certificate[0])
				if rerr != nil {
					return tls.Certificate{}, "", fmt.Errorf("parse reissued tls cert: %w", rerr)
				}
			}

			pin, perr := computeSPKIPin(leaf.PublicKey)
			if perr != nil {
				return tls.Certificate{}, "", perr
			}

			return cert, pin, nil
		}
	}

	// Generate a fresh key + self-signed cert and persist both.
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

	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return tls.Certificate{}, "", fmt.Errorf("write cert: %w", err)
	}

	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, "", fmt.Errorf("write key: %w", err)
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
