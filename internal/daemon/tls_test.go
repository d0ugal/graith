package daemon

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
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
