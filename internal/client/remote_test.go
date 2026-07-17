package client

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
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

// newPairClient builds a client over conn with a real DataDir so the receipt
// protocol can durably persist the paired host before acknowledging (issue
// #1299). It returns the client and its remote-hosts path.
func newPairClient(t *testing.T, conn net.Conn) (*Client, string) {
	t.Helper()

	dir := t.TempDir()

	return &Client{
		conn:   conn,
		reader: protocol.NewFrameReader(conn),
		writer: protocol.NewFrameWriter(conn),
		paths:  config.Paths{DataDir: dir},
	}, RemoteHostsPath(dir)
}

// fakePairDaemon plays the daemon side of the #1299 receipt handshake over a
// net.Pipe: it reads pair_request, sends pair_response (with the given pin,
// request, and device), reads pair_ack, then — at the moment it has the ack in
// hand — invokes atAck (letting a test assert the client had already persisted
// the credential, since the client persists BEFORE sending pair_ack), and
// finally sends the commit frame the caller supplies. If sendCommit is false it
// closes without a pair_committed, modelling a lost confirmation.
func fakePairDaemon(t *testing.T, conn net.Conn, pin, reqID, devID, token string, commit protocol.Envelope, sendCommit bool, atAck func()) {
	t.Helper()

	go func() {
		defer func() { _ = conn.Close() }()

		w := protocol.NewFrameWriter(conn)
		r := protocol.NewFrameReader(conn)

		// Read pair_request.
		if _, err := r.ReadFrame(); err != nil {
			return
		}

		resp, _ := protocol.EncodeControl("pair_response", protocol.PairResponseMsg{
			RequestID: reqID, DeviceID: devID, ClientToken: token, TLSPinSPKI: pin,
		})
		if err := w.WriteFrame(protocol.ChannelControl, resp); err != nil {
			return
		}

		// Read pair_ack. The client persists the credential before sending it, so
		// by the time we have the ack the file must already exist (issue #1299).
		if _, err := r.ReadFrame(); err != nil {
			return
		}

		if atAck != nil {
			atAck()
		}

		if !sendCommit {
			return // model a lost pair_committed: close without confirming
		}

		out, _ := protocol.EncodeControl(commit.Type, commit.Payload)
		_ = w.WriteFrame(protocol.ChannelControl, out)
	}()
}

// TestCompletePairingPersistsBeforeAck: the client must durably store the
// credential BEFORE it sends pair_ack, so a crash between the ack and the
// caller's later Save cannot lose the client credential (issue #1299).
func TestCompletePairingPersistsBeforeAck(t *testing.T) {
	clientConn, daemonConn := net.Pipe()

	c, hostsPath := newPairClient(t, clientConn)

	// The daemon checks the file the instant it reads pair_ack — which the client
	// only sends after persisting.
	persistedAtAck := make(chan bool, 1)
	commitPayload, _ := protocol.EncodeControl("pair_committed", protocol.PairCommittedMsg{RequestID: "req-braw", DeviceID: "dev-canny"})
	commitEnv, _ := protocol.DecodeControl(commitPayload)
	fakePairDaemon(t, daemonConn, "loch-pin", "req-braw", "dev-canny", "tok-dreich", commitEnv, true, func() {
		_, statErr := os.Stat(hostsPath)
		persistedAtAck <- statErr == nil
	})

	rh, err := c.completePairing("ben.ts.net", 4823, "bairn", "cHVia2V5", "loch-pin")
	if err != nil {
		t.Fatalf("completePairing should succeed on a matched commit: %v", err)
	}

	if rh == nil || rh.Token != "tok-dreich" || rh.TLSPin != "loch-pin" {
		t.Fatalf("unexpected RemoteHost: %+v", rh)
	}

	if !<-persistedAtAck {
		t.Fatal("remote-hosts file was not persisted before pair_ack was sent")
	}

	// The stored host resolves after pairing.
	store, err := LoadRemoteHostStore(hostsPath)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := store.Get("ben.ts.net"); !ok {
		t.Fatal("paired host not durably stored")
	}
}

// TestCompletePairingRejectsMismatchedCommit: a pair_committed for a different
// request/device must not complete the pairing (issue #1299). The already-stored
// credential is intentionally retained (the daemon may have committed the real
// device), but the mismatch is surfaced as an error.
func TestCompletePairingRejectsMismatchedCommit(t *testing.T) {
	clientConn, daemonConn := net.Pipe()

	c, hostsPath := newPairClient(t, clientConn)

	// Commit frame names a different device than the pair_response delivered.
	commitPayload, _ := protocol.EncodeControl("pair_committed", protocol.PairCommittedMsg{RequestID: "req-braw", DeviceID: "dev-THRAWN"})
	commitEnv, _ := protocol.DecodeControl(commitPayload)
	fakePairDaemon(t, daemonConn, "loch-pin", "req-braw", "dev-canny", "tok-dreich", commitEnv, true, nil)

	if _, err := c.completePairing("ben.ts.net", 4823, "bairn", "cHVia2V5", "loch-pin"); err == nil {
		t.Fatal("completePairing must reject a pair_committed that does not match the delivered credential")
	}

	// The credential persisted before the ack is not discarded on a mismatch.
	store, err := LoadRemoteHostStore(hostsPath)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := store.Get("ben.ts.net"); !ok {
		t.Fatal("stored credential should be retained on a commit mismatch")
	}
}

// TestCompletePairingLostCommitRetainsCredential: if the connection drops after
// the client stored the credential and sent pair_ack but before pair_committed
// arrives, the commit status is unknown — surface an error, but do NOT discard
// the durable credential (the daemon may already have committed) (issue #1299).
func TestCompletePairingLostCommitRetainsCredential(t *testing.T) {
	clientConn, daemonConn := net.Pipe()

	c, hostsPath := newPairClient(t, clientConn)

	// sendCommit=false: the daemon reads pair_ack then closes without confirming.
	fakePairDaemon(t, daemonConn, "loch-pin", "req-braw", "dev-canny", "tok-dreich", protocol.Envelope{}, false, nil)

	_, err := c.completePairing("ben.ts.net", 4823, "bairn", "cHVia2V5", "loch-pin")
	if err == nil {
		t.Fatal("a lost pair_committed must surface a commit-status-unknown error")
	}

	// The credential persisted before the ack must be retained.
	store, err := LoadRemoteHostStore(hostsPath)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := store.Get("ben.ts.net"); !ok {
		t.Fatal("stored credential should be retained when the commit confirmation is lost")
	}
}

// TestPersistPairedHostRollsBackOnSaveFailure guards the transactional pre-ack
// persist (issue #1299): if the durable write fails after already landing new
// bytes on disk (a post-rename fsync error), the prior host/token/device-key and
// any other hosts must be restored exactly, and the on-disk JSON left unchanged.
func TestPersistPairedHostRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()

	// Seed an existing paired host, a device key, and a second host.
	seed, err := LoadRemoteHostStore(RemoteHostsPath(dir))
	if err != nil {
		t.Fatal(err)
	}

	seed.DeviceKey = "device-key-x"
	seed.Put(&RemoteHost{Host: "ben", Port: 1, Token: "tok-1", TLSPin: "pin-1", Profile: "prof-1"})
	seed.Put(&RemoteHost{Host: "other", Port: 2, Token: "tok-other", TLSPin: "pin-other", Profile: "prof-other"})

	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(RemoteHostsPath(dir))
	if err != nil {
		t.Fatal(err)
	}

	// Fail the first write AFTER landing its bytes (models a post-rename fsync
	// error); let the rollback write succeed.
	orig := remoteHostsWrite
	defer func() { remoteHostsWrite = orig }()

	calls := 0
	remoteHostsWrite = func(path string, data []byte, perm os.FileMode) error {
		calls++
		_ = os.WriteFile(path, data, perm) // the data lands even though we error

		if calls == 1 {
			return errors.New("simulated post-rename fsync failure")
		}

		return nil
	}

	c := &Client{paths: config.Paths{DataDir: dir}}
	if err := c.persistPairedHost(&RemoteHost{Host: "ben", Port: 9, Token: "tok-2", TLSPin: "pin-2", Profile: "prof-2"}); err == nil {
		t.Fatal("persistPairedHost should surface the write failure")
	}

	// On-disk JSON restored byte-for-byte to the prior state.
	after, err := os.ReadFile(RemoteHostsPath(dir))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(before, after) {
		t.Fatalf("store not restored:\n before=%s\n after=%s", before, after)
	}

	// The prior credential, device key, and other host all survive.
	reloaded, err := LoadRemoteHostStore(RemoteHostsPath(dir))
	if err != nil {
		t.Fatal(err)
	}

	if h, ok := reloaded.Get("ben"); !ok || h.Token != "tok-1" || h.TLSPin != "pin-1" || h.Profile != "prof-1" {
		t.Fatalf("prior host not restored: %+v", h)
	}

	if reloaded.DeviceKey != "device-key-x" {
		t.Errorf("device key lost: %q", reloaded.DeviceKey)
	}

	if _, ok := reloaded.Get("other"); !ok {
		t.Error("unrelated host was dropped")
	}
}

// TestCompletePairingPostStoreFailuresAreCommitUnknown table-tests every feasible
// failure AFTER the credential is stored + acked: each must retain the stored
// credential and surface a commit-status-unknown error, never a plain failure
// that would invite the user to blindly re-pair (issue #1299).
func TestCompletePairingPostStoreFailuresAreCommitUnknown(t *testing.T) {
	errPayload, _ := protocol.EncodeControl("error", protocol.ErrorMsg{Message: "state save failed"})
	errEnv, _ := protocol.DecodeControl(errPayload)
	mismatchPayload, _ := protocol.EncodeControl("pair_committed", protocol.PairCommittedMsg{RequestID: "req-braw", DeviceID: "dev-OTHER"})
	mismatchEnv, _ := protocol.DecodeControl(mismatchPayload)
	weirdPayload, _ := protocol.EncodeControl("handshake_ok", struct{}{})
	weirdEnv, _ := protocol.DecodeControl(weirdPayload)
	// A pair_committed whose body is a JSON array cannot decode into
	// PairCommittedMsg, exercising the malformed-committed post-store path.
	malformedEnv := protocol.Envelope{Type: "pair_committed", Payload: json.RawMessage("[1,2,3]")}

	cases := []struct {
		name   string
		commit protocol.Envelope
		send   bool
	}{
		{"daemon-error-after-ack", errEnv, true},
		{"mismatched-commit", mismatchEnv, true},
		{"malformed-committed", malformedEnv, true},
		{"unexpected-frame", weirdEnv, true},
		{"dropped-read", protocol.Envelope{}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, daemonConn := net.Pipe()
			c, hostsPath := newPairClient(t, clientConn)
			fakePairDaemon(t, daemonConn, "loch-pin", "req-braw", "dev-canny", "tok-dreich", tc.commit, tc.send, nil)

			_, err := c.completePairing("ben.ts.net", 4823, "bairn", "cHVia2V5", "loch-pin")
			if err == nil {
				t.Fatal("a post-store failure must surface an error")
			}

			if !strings.Contains(err.Error(), "commit status unknown") {
				t.Fatalf("expected commit-status-unknown wording, got %v", err)
			}

			store, lerr := LoadRemoteHostStore(hostsPath)
			if lerr != nil {
				t.Fatal(lerr)
			}

			if _, ok := store.Get("ben.ts.net"); !ok {
				t.Fatal("the stored credential must be retained on a post-store failure")
			}
		})
	}
}

// TestCompletePairingLegacyDaemonResponse guards new-client → old-daemon pairing
// (issue #1299 cross-version): a pre-receipt daemon commits during `gr pair
// approve` and sends a pair_response WITHOUT request_id and NO pair_committed.
// The client must store the credential and complete without acking, not discard
// the one-time token.
func TestCompletePairingLegacyDaemonResponse(t *testing.T) {
	clientConn, daemonConn := net.Pipe()
	c, hostsPath := newPairClient(t, clientConn)

	ackSeen := make(chan bool, 1)

	go func() {
		defer func() { _ = daemonConn.Close() }()

		w := protocol.NewFrameWriter(daemonConn)
		r := protocol.NewFrameReader(daemonConn)

		if _, err := r.ReadFrame(); err != nil { // pair_request
			return
		}

		// Legacy response: no request_id, no follow-up pair_committed.
		resp, _ := protocol.EncodeControl("pair_response", protocol.PairResponseMsg{
			DeviceID: "dev-legacy", ClientToken: "tok-legacy", TLSPinSPKI: "loch-pin",
		})
		_ = w.WriteFrame(protocol.ChannelControl, resp)

		// If the client (wrongly) sends a pair_ack, record it; otherwise EOF.
		_, err := r.ReadFrame()
		ackSeen <- err == nil
	}()

	rh, err := c.completePairing("ben.ts.net", 4823, "bairn", "cHVia2V5", "loch-pin")
	if err != nil {
		t.Fatalf("legacy pairing should complete: %v", err)
	}

	if rh == nil || rh.Token != "tok-legacy" {
		t.Fatalf("unexpected host: %+v", rh)
	}

	// The credential is durably stored.
	store, _ := LoadRemoteHostStore(hostsPath)
	if _, ok := store.Get("ben.ts.net"); !ok {
		t.Fatal("legacy credential not stored")
	}

	// Close our side so the daemon's ReadFrame returns; assert no pair_ack was sent.
	_ = clientConn.Close()

	if <-ackSeen {
		t.Fatal("client must not send pair_ack to a legacy daemon")
	}
}

// stalledTLSListener accepts TCP connections but never speaks TLS, modelling a
// peer that completes the TCP handshake and then hangs. Accepted connections are
// held open (and closed on cleanup) so the client's TLS handshake blocks waiting
// for a ServerHello that never comes.
func stalledTLSListener(t *testing.T) (host string, port int) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu    sync.Mutex
		conns []net.Conn
	)

	done := make(chan struct{})

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}

			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		_ = ln.Close()
		close(done)

		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	})

	addr := ln.Addr().(*net.TCPAddr)

	return "127.0.0.1", addr.Port
}

func withShortRemoteHandshake(t *testing.T) {
	t.Helper()

	origHS, origDial := remoteHandshakeTimeout, remoteDialTimeout

	t.Cleanup(func() { remoteHandshakeTimeout, remoteDialTimeout = origHS, origDial })

	remoteHandshakeTimeout = 200 * time.Millisecond
	remoteDialTimeout = 2 * time.Second
}

// TestConnectRemoteBoundsTLSHandshakeAgainstStalledPeer proves the paired-lane
// TLS handshake deadline is installed BEFORE conn.Handshake(): a peer that
// accepts TCP but never completes TLS must fail within the handshake budget, not
// hang forever (issue #1242).
func TestConnectRemoteBoundsTLSHandshakeAgainstStalledPeer(t *testing.T) {
	withShortRemoteHandshake(t)

	host, port := stalledTLSListener(t)

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, pin := makeCertAndPin(t, key)

	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	rh := &RemoteHost{Host: host, Port: port, TLSPin: pin, Token: "braw"}

	start := time.Now()

	done := make(chan error, 1)

	go func() {
		_, err := ConnectRemote(config.Paths{}, rh, priv, 80, 24)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a TLS handshake error against a stalled peer")
		}

		if !strings.Contains(err.Error(), "tls handshake") {
			t.Errorf("err = %v, want a tls handshake failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("ConnectRemote hung past the handshake budget (%v elapsed) — deadline not installed before TLS handshake", time.Since(start))
	}
}

// TestPairRemoteBoundsTLSHandshakeAgainstStalledPeer proves the first-contact
// pairing lane installs the same TLS handshake deadline before conn.Handshake().
func TestPairRemoteBoundsTLSHandshakeAgainstStalledPeer(t *testing.T) {
	withShortRemoteHandshake(t)

	host, port := stalledTLSListener(t)

	done := make(chan error, 1)

	go func() {
		_, err := PairRemote(config.Paths{}, host, port, "", "canny", "pubkey")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a TLS handshake error against a stalled peer")
		}

		if !strings.Contains(err.Error(), "tls handshake") {
			t.Errorf("err = %v, want a tls handshake failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PairRemote hung past the handshake budget — deadline not installed before TLS handshake")
	}
}
