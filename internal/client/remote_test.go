package client

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
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

// fakePoPDaemon plays the daemon side of a proof-of-possession exchange over a
// net.Pipe: it sends the given challenge, reads the client's auth_proof, and
// replies with the supplied envelope type. The verified signature (if valid)
// is reported on the returned channel.
func fakePoPDaemon(t *testing.T, conn net.Conn, nonce, spki string, pub ed25519.PublicKey, reply protocol.Envelope) chan bool {
	t.Helper()

	verified := make(chan bool, 1)

	go func() {
		defer func() { _ = conn.Close() }()

		w := protocol.NewFrameWriter(conn)
		r := protocol.NewFrameReader(conn)

		ch, _ := protocol.EncodeControl("auth_challenge", protocol.AuthChallengeMsg{Nonce: nonce})
		if err := w.WriteFrame(protocol.ChannelControl, ch); err != nil {
			verified <- false
			return
		}

		frame, err := r.ReadFrame()
		if err != nil {
			verified <- false
			return
		}

		env, _ := protocol.DecodeControl(frame.Payload)

		var proof protocol.AuthProofMsg

		_ = protocol.DecodePayload(env, &proof)

		sig, _ := base64.StdEncoding.DecodeString(proof.Signature)

		ok := ed25519.Verify(pub, protocol.PoPSigningInput(nonce, spki), sig)
		verified <- ok

		out, _ := protocol.EncodeControl(reply.Type, reply.Payload)
		_ = w.WriteFrame(protocol.ChannelControl, out)
	}()

	return verified
}

func newPoPClient(conn net.Conn) *Client {
	return &Client{
		conn:   conn,
		reader: protocol.NewFrameReader(conn),
		writer: protocol.NewFrameWriter(conn),
	}
}

func TestCompleteRemotePoPSuccess2(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	clientConn, daemonConn := net.Pipe()

	okPayload, _ := protocol.EncodeControl("auth_ok", struct{}{})
	okEnv, _ := protocol.DecodeControl(okPayload)
	verified := fakePoPDaemon(t, daemonConn, "haar-nonce", "loch-pin", pub, okEnv)

	c := newPoPClient(clientConn)

	if err := c.completeRemotePoP(priv, "loch-pin"); err != nil {
		t.Fatalf("completeRemotePoP should succeed, got %v", err)
	}

	if !<-verified {
		t.Error("daemon failed to verify the client's channel-bound signature")
	}
}

func TestCompleteRemotePoPRejectsEmptySPKI2(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	// Daemon just needs to send a challenge; the client must refuse to sign
	// before answering because there's no pinned channel to bind against.
	go func() {
		w := protocol.NewFrameWriter(daemonConn)
		ch, _ := protocol.EncodeControl("auth_challenge", protocol.AuthChallengeMsg{Nonce: "n"})
		_ = w.WriteFrame(protocol.ChannelControl, ch)
	}()

	c := newPoPClient(clientConn)

	if err := c.completeRemotePoP(priv, ""); err == nil {
		t.Fatal("completeRemotePoP with empty SPKI should refuse (unbound proof)")
	}
}

func TestCompleteRemotePoPRejectsNonChallenge2(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	clientConn, daemonConn := net.Pipe()
	defer func() { _ = daemonConn.Close() }()

	go func() {
		w := protocol.NewFrameWriter(daemonConn)
		// Send something other than auth_challenge first.
		out, _ := protocol.EncodeControl("handshake_ok", struct{}{})
		_ = w.WriteFrame(protocol.ChannelControl, out)
	}()

	c := newPoPClient(clientConn)

	if err := c.completeRemotePoP(priv, "pin"); err == nil {
		t.Fatal("completeRemotePoP should error when the daemon's first message isn't a challenge")
	}
}

func TestCompleteRemotePoPRejectedByDaemon2(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	clientConn, daemonConn := net.Pipe()

	errPayload, _ := protocol.EncodeControl("error", protocol.ErrorMsg{Message: "unknown device"})
	errEnv, _ := protocol.DecodeControl(errPayload)
	verified := fakePoPDaemon(t, daemonConn, "n", "pin", pub, errEnv)

	c := newPoPClient(clientConn)

	err := c.completeRemotePoP(priv, "pin")
	if err == nil {
		t.Fatal("completeRemotePoP should surface the daemon's error reply")
	}

	<-verified
}

func TestCompleteRemotePoPUnexpectedAck2(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	clientConn, daemonConn := net.Pipe()

	weirdPayload, _ := protocol.EncodeControl("handshake_ok", struct{}{})
	weirdEnv, _ := protocol.DecodeControl(weirdPayload)
	verified := fakePoPDaemon(t, daemonConn, "n", "pin", pub, weirdEnv)

	c := newPoPClient(clientConn)

	if err := c.completeRemotePoP(priv, "pin"); err == nil {
		t.Fatal("completeRemotePoP should error on an unexpected ack type")
	}

	<-verified
}

func TestRemoteTLSConfig2(t *testing.T) {
	rh := &RemoteHost{Host: "graith-ben.ts.net", Port: 4823, TLSPin: "some-pin"}

	cfg := remoteTLSConfig(rh)

	if cfg.ServerName != rh.Host {
		t.Errorf("ServerName = %q, want %q", cfg.ServerName, rh.Host)
	}

	if !cfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify must be true (chain validation is replaced by the SPKI pin)")
	}

	if !cfg.SessionTicketsDisabled {
		t.Error("SessionTicketsDisabled must be true so resumption can't bypass the pin")
	}

	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %d, want TLS 1.2", cfg.MinVersion)
	}

	if cfg.VerifyPeerCertificate == nil {
		t.Fatal("VerifyPeerCertificate must be wired to the SPKI pin verifier")
	}
}

func TestSPKIPinVerifierRejectsUnparseableCert2(t *testing.T) {
	// A byte blob that is not a valid DER certificate must be rejected rather
	// than panicking.
	if err := spkiPinVerifier("pin")([][]byte{{0x00, 0x01, 0x02}}, nil); err == nil {
		t.Error("an unparseable certificate should be rejected")
	}
}

// TestRemoteTLSConfigVerifierPinsKey wires the verifier built by
// remoteTLSConfig against a real cert to prove it uses the host's pin.
func TestRemoteTLSConfigVerifierPinsKey2(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, pin := makeCertAndPin(t, key)

	cfg := remoteTLSConfig(&RemoteHost{Host: "h", TLSPin: pin})
	if err := cfg.VerifyPeerCertificate([][]byte{der}, nil); err != nil {
		t.Errorf("verifier built from the host pin rejected the matching cert: %v", err)
	}

	wrong := remoteTLSConfig(&RemoteHost{Host: "h", TLSPin: "not-the-pin"})
	if err := wrong.VerifyPeerCertificate([][]byte{der}, nil); err == nil {
		t.Error("verifier with a mismatched pin should reject the cert")
	}
}
