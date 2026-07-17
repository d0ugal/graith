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

	"github.com/d0ugal/graith/internal/atomicfile"
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

func TestLoadOrCreateRemoteTLSReissuesHostnameWithStablePin(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "remote.crt")
	keyPath := filepath.Join(dir, "remote.key")
	now := time.Now()

	_, pin1, err := loadOrCreateRemoteTLS(certPath, keyPath, "ben", now)
	if err != nil {
		t.Fatal(err)
	}

	cert2, pin2, err := loadOrCreateRemoteTLS(certPath, keyPath, "canny", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	if pin2 != pin1 {
		t.Errorf("hostname reissue changed SPKI pin: %q != %q", pin2, pin1)
	}

	leaf, err := x509.ParseCertificate(cert2.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "canny" {
		t.Errorf("reissued DNSNames = %v, want [canny]", leaf.DNSNames)
	}
}

// failingTLSWrite installs a writeTLSFile seam that fails for a chosen file
// (matched by path suffix), simulating an interrupted publication at that step.
// It restores the real writer on cleanup, so these tests must not run parallel.
func failingTLSWrite(t *testing.T, failSuffix string) {
	t.Helper()

	orig := writeTLSFile

	t.Cleanup(func() { writeTLSFile = orig })

	writeTLSFile = func(path string, data []byte) error {
		if failSuffix != "" && filepath.Base(path) == failSuffix {
			return errors.New("injected tls write failure")
		}

		return orig(path, data)
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

func TestLoadOrCreateRemoteTLSFailedReissueKeepsPriorPairLoadable(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "remote.crt")
	keyPath := filepath.Join(dir, "remote.key")
	now := time.Now()

	cert1, pin1, err := loadOrCreateRemoteTLS(certPath, keyPath, "ben", now)
	if err != nil {
		t.Fatal(err)
	}

	priorCert, _ := os.ReadFile(certPath)
	priorKey, _ := os.ReadFile(keyPath)

	// Force the reissue (hostname change) certificate write to fail. Remote
	// service must not be stranded: the prior valid pair is returned, and the
	// on-disk material is untouched.
	failingTLSWrite(t, "remote.crt")

	cert2, pin2, err := loadOrCreateRemoteTLS(certPath, keyPath, "canny", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("failed reissue should not strand the listener: %v", err)
	}

	if pin2 != pin1 {
		t.Errorf("pin changed on failed reissue: %q != %q", pin2, pin1)
	}

	if len(cert2.Certificate) == 0 {
		t.Fatal("failed reissue returned an empty certificate")
	}

	// The old SAN is still served (reissue did not persist), proving the prior
	// generation was preserved rather than half-replaced.
	leaf, err := x509.ParseCertificate(cert2.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "ben" {
		t.Errorf("failed reissue served DNSNames %v, want the prior [ben]", leaf.DNSNames)
	}

	// On-disk material is byte-for-byte the prior generation.
	if gotCert, _ := os.ReadFile(certPath); string(gotCert) != string(priorCert) {
		t.Error("failed reissue mutated the on-disk certificate")
	}

	if gotKey, _ := os.ReadFile(keyPath); string(gotKey) != string(priorKey) {
		t.Error("failed reissue mutated the on-disk key")
	}

	_ = cert1

	// With writes restored, the next start completes the reissue and the pin is
	// still stable (same key), proving recovery.
	writeTLSFile = func(path string, data []byte) error { return atomicfile.Write(path, data, 0o600) }

	cert3, pin3, err := loadOrCreateRemoteTLS(certPath, keyPath, "canny", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("reissue retry after restore failed: %v", err)
	}

	if pin3 != pin1 {
		t.Errorf("pin changed after successful reissue: %q != %q", pin3, pin1)
	}

	leaf3, _ := x509.ParseCertificate(cert3.Certificate[0])
	if len(leaf3.DNSNames) != 1 || leaf3.DNSNames[0] != "canny" {
		t.Errorf("reissue retry served DNSNames %v, want [canny]", leaf3.DNSNames)
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
