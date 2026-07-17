package daemon

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestComputeSPKIPinStableAcrossCertRenewal(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()

	// Two certificates issued from the same key at different times.
	der1, err := selfSignedCert(key, "ben", now)
	if err != nil {
		t.Fatal(err)
	}

	der2, err := selfSignedCert(key, "ben", now.Add(365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	c1, _ := x509.ParseCertificate(der1)
	c2, _ := x509.ParseCertificate(der2)

	pin1, err := computeSPKIPin(c1.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	pin2, err := computeSPKIPin(c2.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	if pin1 != pin2 {
		t.Errorf("SPKI pin changed across renewal with the same key: %q != %q", pin1, pin2)
	}

	if pin1 == "" {
		t.Error("SPKI pin is empty")
	}

	// A different key must produce a different pin.
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	otherPin, err := computeSPKIPin(&other.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	if otherPin == pin1 {
		t.Error("different keys produced the same SPKI pin")
	}
}

func TestLoadOrCreateRemoteTLS(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "remote.crt")
	keyPath := filepath.Join(dir, "remote.key")
	now := time.Now()

	cert1, pin1, err := loadOrCreateRemoteTLS(certPath, keyPath, "ben", now)
	if err != nil {
		t.Fatal(err)
	}

	if pin1 == "" {
		t.Fatal("first call returned an empty pin")
	}

	if len(cert1.Certificate) == 0 {
		t.Fatal("first call returned an empty certificate")
	}

	// A second call loads the persisted pair and yields the SAME pin.
	cert2, pin2, err := loadOrCreateRemoteTLS(certPath, keyPath, "ben", now)
	if err != nil {
		t.Fatal(err)
	}

	if pin2 != pin1 {
		t.Errorf("pin changed on reload: %q != %q", pin2, pin1)
	}

	// The cert is usable in a TLS config.
	if cfg := remoteTLSConfig(cert2); len(cfg.Certificates) != 1 || cfg.MinVersion != tls.VersionTLS12 {
		t.Error("remoteTLSConfig did not produce a usable server config")
	}
}

func TestLoadOrCreateRemoteTLSInitialWriteFailureRecoversNextStart(t *testing.T) {
	for _, failName := range []string{"remote.key", "remote.crt"} {
		t.Run(failName, func(t *testing.T) {
			dir := t.TempDir()
			certPath := filepath.Join(dir, "remote.crt")
			keyPath := filepath.Join(dir, "remote.key")
			now := time.Now()

			restore := writeTLSFile
			writeTLSFile = func(path string, data []byte) error {
				if filepath.Base(path) == failName {
					return errors.New("injected tls write failure")
				}

				return restore(path, data)
			}

			if _, _, err := loadOrCreateRemoteTLS(certPath, keyPath, "ben", now); err == nil {
				t.Fatalf("expected initial issuance to fail when %s write fails", failName)
			}

			// Writes recover on the next start (the crash is over).
			writeTLSFile = restore

			// A partial initial publication must never load as a mismatched pair
			// on the next start: the missing/half-written generation is discarded
			// and a fresh, complete, loadable pair is minted instead.
			cert, pin, err := loadOrCreateRemoteTLS(certPath, keyPath, "ben", now)
			if err != nil {
				t.Fatalf("recovery start failed after %s write failure: %v", failName, err)
			}

			if pin == "" || len(cert.Certificate) == 0 {
				t.Fatalf("recovery start produced no usable generation")
			}

			// The recovered pair is durable and reloads with a stable pin.
			_, pin2, err := loadOrCreateRemoteTLS(certPath, keyPath, "ben", now)
			if err != nil {
				t.Fatalf("reload after recovery failed: %v", err)
			}

			if pin2 != pin {
				t.Errorf("pin changed after recovery reload: %q != %q", pin2, pin)
			}
		})
	}
}

func TestLoadOrCreateRemoteTLSRegeneratesCorruptPair(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "remote.crt")
	keyPath := filepath.Join(dir, "remote.key")
	now := time.Now()

	if _, _, err := loadOrCreateRemoteTLS(certPath, keyPath, "ben", now); err != nil {
		t.Fatal(err)
	}

	// Simulate a legacy truncated certificate (a pre-atomic-write partial
	// publication) sitting next to a valid key. The daemon must recover by
	// regenerating rather than failing in tls.X509KeyPair on every start.
	if err := os.WriteFile(certPath, []byte("-----BEGIN CERTIFICATE-----\ntruncated"), 0o600); err != nil {
		t.Fatal(err)
	}

	cert, pin, err := loadOrCreateRemoteTLS(certPath, keyPath, "ben", now)
	if err != nil {
		t.Fatalf("expected recovery from a corrupt pair, got: %v", err)
	}

	if pin == "" || len(cert.Certificate) == 0 {
		t.Fatal("recovery produced no usable generation")
	}
}
