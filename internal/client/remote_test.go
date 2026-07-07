package client

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"testing"
	"time"
)

// makeCertAndPin builds a self-signed cert and its SPKI pin (matching the
// daemon's computeSPKIPin) for the given key.
func makeCertAndPin(t *testing.T, key *ecdsa.PrivateKey) ([]byte, string) {
	t.Helper()

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "graith-remote"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(spki)

	return der, base64.StdEncoding.EncodeToString(sum[:])
}

func TestSPKIPinVerifier(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, pin := makeCertAndPin(t, key)

	// Correct pin accepts.
	if err := spkiPinVerifier(pin)([][]byte{der}, nil); err != nil {
		t.Errorf("correct pin rejected: %v", err)
	}

	// Wrong pin rejects.
	if err := spkiPinVerifier("d3JvbmctcGlu")([][]byte{der}, nil); err == nil {
		t.Error("wrong pin should be rejected")
	}

	// A cert from a different key (same pin) rejects — proves it pins the key.
	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherDER, _ := makeCertAndPin(t, otherKey)

	if err := spkiPinVerifier(pin)([][]byte{otherDER}, nil); err == nil {
		t.Error("a different key's cert should fail the pin")
	}

	// No certificate rejects (fail closed).
	if err := spkiPinVerifier(pin)(nil, nil); err == nil {
		t.Error("empty cert chain should be rejected")
	}
}
