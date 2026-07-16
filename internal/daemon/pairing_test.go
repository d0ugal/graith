package daemon

import (
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

func newPairingSM(t *testing.T) *SessionManager {
	t.Helper()

	cfg := config.Default()
	cfg.Remote.Enabled = true
	cfg.Remote.RequirePairing = true
	cfg.Remote.AllowTailnetUsers = []string{"speir@example.com"}
	paths := config.Paths{StateFile: filepath.Join(t.TempDir(), "state.json")}

	sm := NewSessionManager(cfg, paths, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// A real remote listener sets this at startup; proof-of-possession binds to
	// it (issue #886), so the handler-level tests need a non-empty pin to sign
	// against.
	sm.remoteTLSPin = "bide-spki-pin"

	return sm
}

// testPubKey returns a fresh base64 ed25519 public key for pairing fixtures.
func testPubKey(t *testing.T) string {
	t.Helper()

	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	return base64.StdEncoding.EncodeToString(pub)
}

func TestHMACTokenDeterministicAndKeyed(t *testing.T) {
	h1 := hmacToken("bide-key", "braw-token")
	h2 := hmacToken("bide-key", "braw-token")

	if h1 != h2 {
		t.Fatalf("hmacToken not deterministic: %q != %q", h1, h2)
	}

	if h1 == "" {
		t.Fatal("hmacToken returned empty")
	}

	// Different key → different hash.
	if hmacToken("thrawn-key", "braw-token") == h1 {
		t.Error("hmacToken should differ for a different key")
	}

	// Different token → different hash.
	if hmacToken("bide-key", "dreich-token") == h1 {
		t.Error("hmacToken should differ for a different token")
	}
}

func TestVerifyPoP(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	pubB64 := base64.StdEncoding.EncodeToString(pub)
	nonce := "haar-nonce-1234"
	spki := "bide-spki-pin"
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, protocol.PoPSigningInput(nonce, spki)))

	if !verifyPoP(pubB64, nonce, spki, sig) {
		t.Error("verifyPoP = false for a valid signature")
	}

	// Wrong nonce (signature is over a different message).
	if verifyPoP(pubB64, "different-nonce", spki, sig) {
		t.Error("verifyPoP = true for a signature over a different nonce")
	}

	// Wrong channel binding (a MITM presents a different cert → different SPKI):
	// the proof must not verify against the daemon's own pin (issue #886).
	if verifyPoP(pubB64, nonce, "thrawn-mitm-pin", sig) {
		t.Error("verifyPoP = true for a signature bound to a different SPKI")
	}

	// A signature bound to the nonce alone (no channel binding) must be rejected
	// — this is exactly the pre-#886 signature a relay could forward.
	unbound := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(nonce)))
	if verifyPoP(pubB64, nonce, spki, unbound) {
		t.Error("verifyPoP = true for an unbound (nonce-only) signature")
	}

	// Signature from a different key.
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	otherSig := base64.StdEncoding.EncodeToString(ed25519.Sign(otherPriv, protocol.PoPSigningInput(nonce, spki)))

	if verifyPoP(pubB64, nonce, spki, otherSig) {
		t.Error("verifyPoP = true for a signature from a different key")
	}

	// Malformed / empty inputs fail closed.
	if verifyPoP("not-base64!!", nonce, spki, sig) || verifyPoP(pubB64, nonce, spki, "not-base64!!") {
		t.Error("verifyPoP must fail closed on malformed base64")
	}

	if verifyPoP("", nonce, spki, sig) || verifyPoP(pubB64, "", spki, sig) ||
		verifyPoP(pubB64, nonce, "", sig) || verifyPoP(pubB64, nonce, spki, "") {
		t.Error("verifyPoP must fail closed on empty inputs")
	}

	// A valid-base64 but wrong-size public key must fail closed.
	if verifyPoP(base64.StdEncoding.EncodeToString([]byte("short")), nonce, spki, sig) {
		t.Error("verifyPoP must reject a wrong-size public key")
	}
}

func TestRandomHexLengthAndUniqueness(t *testing.T) {
	a, err := randomHex(16)
	if err != nil {
		t.Fatal(err)
	}

	if len(a) != 32 { // 16 bytes → 32 hex chars
		t.Errorf("randomHex(16) length = %d, want 32", len(a))
	}

	b, _ := randomHex(16)
	if a == b {
		t.Error("randomHex produced identical values (should be random)")
	}
}

func TestAddPendingPairingRateLimit(t *testing.T) {
	sm := newPairingSM(t)
	sm.cfg.Remote.PairRequestRate = "2/min"
	pub := testPubKey(t)
	id := TailnetIdentity{User: "speir@example.com", Node: "ben"}
	now := time.Now()

	if _, _, err := sm.AddPendingPairing("bairn", pub, id, now); err != nil {
		t.Fatalf("1st request: %v", err)
	}

	if _, _, err := sm.AddPendingPairing("skelf", pub, id, now); err != nil {
		t.Fatalf("2nd request: %v", err)
	}

	if _, _, err := sm.AddPendingPairing("whin", pub, id, now); err == nil {
		t.Error("3rd request should be rate-limited")
	}

	// After the window passes, requests are allowed again.
	if _, _, err := sm.AddPendingPairing("whin", pub, id, now.Add(2*time.Minute)); err != nil {
		t.Errorf("request after window: %v", err)
	}
}

func TestAddPendingPairingRejectsInvalidPubKey(t *testing.T) {
	sm := newPairingSM(t)

	if _, _, err := sm.AddPendingPairing("dreich", "not-a-key", TailnetIdentity{}, time.Now()); err == nil {
		t.Error("expected invalid public key to be rejected")
	}
}

func TestAddPendingPairingCap(t *testing.T) {
	sm := newPairingSM(t)
	sm.cfg.Remote.PairRequestRate = "1000/min" // don't hit the rate limit
	pub := testPubKey(t)
	now := time.Now()

	for i := 0; i < config.RemoteMaxPendingPairingsDefault; i++ {
		if _, _, err := sm.AddPendingPairing("bairn", pub, TailnetIdentity{}, now); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}

	if _, _, err := sm.AddPendingPairing("bairn", pub, TailnetIdentity{}, now); err == nil {
		t.Error("expected pending-cap to reject the extra request")
	}
}

// TestAddPendingPairingConfigurableLimits verifies that both the pending-cap
// and the fallback rate honour config values stricter than the old hardcoded
// limits (16 pending, 5/min fallback): after two allowed requests, the third
// must be rejected.
func TestAddPendingPairingConfigurableLimits(t *testing.T) {
	tests := []struct {
		name  string
		setup func(r *config.RemoteConfig)
	}{
		{
			name: "pending cap of 2 (below old 16)",
			setup: func(r *config.RemoteConfig) {
				r.PairRequestRate = "1000/min" // don't hit the rate limit
				r.MaxPendingPairings = 2
			},
		},
		{
			name: "fallback rate of 2/min (below old 5/min)",
			setup: func(r *config.RemoteConfig) {
				// pair_request_rate unset, so the configured fallback applies.
				r.PairFallbackCount = 2
				r.PairFallbackWindow = "1m"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := newPairingSM(t)
			tt.setup(&sm.cfg.Remote)

			pub := testPubKey(t)
			now := time.Now()

			for i := 0; i < 2; i++ {
				if _, _, err := sm.AddPendingPairing("bairn", pub, TailnetIdentity{}, now); err != nil {
					t.Fatalf("request %d: %v", i, err)
				}
			}

			if _, _, err := sm.AddPendingPairing("bairn", pub, TailnetIdentity{}, now); err == nil {
				t.Error("expected the configured limit to reject the 3rd request")
			}
		})
	}
}

func TestExpirePendingPairingConfigurableTTL(t *testing.T) {
	sm := newPairingSM(t)
	sm.cfg.Remote.PairRequestRate = "1000/min"
	sm.cfg.Remote.PendingPairingTTL = "2m" // shorter than the default 10m
	pub := testPubKey(t)
	base := time.Now()

	rid, _, err := sm.AddPendingPairing("bairn", pub, TailnetIdentity{}, base)
	if err != nil {
		t.Fatal(err)
	}

	// Still alive just before the configured TTL.
	if _, _, err := sm.ApprovePairing(rid, false, base.Add(90*time.Second)); err != nil {
		t.Fatalf("pending should survive until the configured TTL: %v", err)
	}

	rid2, _, err := sm.AddPendingPairing("skelf", pub, TailnetIdentity{}, base.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	// Past the configured 2m TTL (well under the old 10m default), an approval
	// must fail because the pending request has expired.
	if _, _, err := sm.ApprovePairing(rid2, false, base.Add(5*time.Minute)); err == nil {
		t.Error("expected ApprovePairing to reject a request expired under the configured 2m TTL")
	}
}

func TestPendingPairingTTLIsImmutableAcrossReload(t *testing.T) {
	base := time.Now()

	tests := []struct {
		name        string
		initialTTL  string
		reloadedTTL string
		approveAt   time.Duration
		wantApprove bool
	}{
		{
			name:        "shortening does not expire old request early",
			initialTTL:  "10m",
			reloadedTTL: "2m",
			approveAt:   3 * time.Minute,
			wantApprove: true,
		},
		{
			name:        "lengthening does not extend old request",
			initialTTL:  "2m",
			reloadedTTL: "10m",
			approveAt:   3 * time.Minute,
			wantApprove: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := newPairingSM(t)
			sm.cfg.Remote.PairRequestRate = "1000/min"
			sm.cfg.Remote.PendingPairingTTL = tt.initialTTL

			rid, waiter, err := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{}, base)
			if err != nil {
				t.Fatal(err)
			}

			initialDuration, err := time.ParseDuration(tt.initialTTL)
			if err != nil {
				t.Fatal(err)
			}
			if want := base.Add(initialDuration); !waiter.expiresAt.Equal(want) {
				t.Fatalf("waiter expiry = %v, want %v", waiter.expiresAt, want)
			}

			reloaded := *sm.Config()
			reloaded.Remote.PendingPairingTTL = tt.reloadedTTL
			sm.applyConfig(&reloaded)

			deviceID, token, err := sm.ApprovePairing(rid, false, base.Add(tt.approveAt))
			if !tt.wantApprove {
				if err == nil {
					t.Fatal("approval succeeded after the request's original deadline")
				}
				if _, paired := sm.ListPairings(); len(paired) != 0 {
					t.Fatal("expired request persisted a paired device")
				}
				return
			}

			if err != nil {
				t.Fatalf("approval before the original deadline: %v", err)
			}

			select {
			case delivered := <-waiter.approval:
				if delivered.DeviceID != deviceID || delivered.Token != token || token == "" {
					t.Fatalf("delivered credentials = %+v, want device=%q token=%q", delivered, deviceID, token)
				}
			default:
				t.Fatal("approved device was persisted without delivering its one-time token")
			}
		})
	}
}

func TestApprovePairingRejectsMissingWaiter(t *testing.T) {
	sm := newPairingSM(t)
	now := time.Now()
	rid, _, err := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{}, now)
	if err != nil {
		t.Fatal(err)
	}

	sm.unregisterPairWaiter(rid)

	if _, _, err := sm.ApprovePairing(rid, false, now); err == nil {
		t.Fatal("approval without a live requester should fail")
	}

	if pending, paired := sm.ListPairings(); len(pending) != 0 || len(paired) != 0 {
		t.Fatalf("pairings after requester disconnect = pending:%d paired:%d, want both zero", len(pending), len(paired))
	}
}

func TestApprovePairingRejectsDisconnectedWaiterBeforeCleanup(t *testing.T) {
	sm := newPairingSM(t)
	now := time.Now()
	disconnected := make(chan struct{})
	rid, _, err := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{}, now, disconnected)
	if err != nil {
		t.Fatal(err)
	}

	// Model connDone closing before the waiter goroutine gets scheduled to call
	// unregisterPairWaiter. Approval must observe the connection signal itself,
	// rather than relying on cleanup timing.
	close(disconnected)

	if _, _, err := sm.ApprovePairing(rid, false, now); err == nil {
		t.Fatal("approval after requester disconnect should fail")
	}

	if pending, paired := sm.ListPairings(); len(pending) != 0 || len(paired) != 0 {
		t.Fatalf("pairings after requester disconnect = pending:%d paired:%d, want both zero", len(pending), len(paired))
	}
}

func TestExpirePendingPairing(t *testing.T) {
	sm := newPairingSM(t)
	sm.cfg.Remote.PairRequestRate = "1000/min"
	pub := testPubKey(t)
	base := time.Now()

	rid, _, err := sm.AddPendingPairing("bairn", pub, TailnetIdentity{}, base)
	if err != nil {
		t.Fatal(err)
	}

	// A later request past the TTL should expire the earlier pending one.
	if _, _, err := sm.AddPendingPairing("skelf", pub, TailnetIdentity{}, base.Add(config.RemotePendingPairingTTLDefault+time.Minute)); err != nil {
		t.Fatal(err)
	}

	pending, _ := sm.ListPairings()
	for _, p := range pending {
		if p.RequestID == rid {
			t.Error("expired pending pairing should have been dropped")
		}
	}
}

func TestApprovePairingAndResolveToken(t *testing.T) {
	sm := newPairingSM(t)
	pub := testPubKey(t)
	id := TailnetIdentity{User: "speir@example.com", Node: "ben"}
	now := time.Now()

	rid, _, err := sm.AddPendingPairing("bairn", pub, id, now)
	if err != nil {
		t.Fatal(err)
	}

	deviceID, token, err := sm.ApprovePairing(rid, false, now)
	if err != nil {
		t.Fatal(err)
	}

	if deviceID == "" || token == "" {
		t.Fatal("ApprovePairing returned empty device ID or token")
	}

	// The token resolves to the device; only its HMAC is stored (not the token).
	d := sm.DeviceForToken(token)
	if d == nil || d.ID != deviceID {
		t.Fatal("DeviceForToken did not resolve the freshly paired device")
	}

	if d.TokenHash == token || d.TokenHash == "" {
		t.Error("stored TokenHash must be a hash, never the token itself")
	}

	if d.TailnetUser != "speir@example.com" || d.TailnetNode != "ben" {
		t.Error("device not bound to the pairing WhoIs identity")
	}

	if d.ReadOnly {
		t.Error("device paired with require_pairing=true should not be read-only")
	}

	// A wrong token resolves to nothing.
	if sm.DeviceForToken("fash-wrong-token") != nil {
		t.Error("DeviceForToken must not resolve an unknown token")
	}

	// The pending request is consumed.
	pending, paired := sm.ListPairings()
	if len(pending) != 0 {
		t.Errorf("pending = %d, want 0 after approval", len(pending))
	}

	if len(paired) != 1 {
		t.Errorf("paired = %d, want 1", len(paired))
	}
}

func TestApprovePairingReadOnly(t *testing.T) {
	sm := newPairingSM(t)
	now := time.Now()
	rid, _, _ := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{}, now)

	_, token, err := sm.ApprovePairing(rid, true, now)
	if err != nil {
		t.Fatal(err)
	}

	if d := sm.DeviceForToken(token); d == nil || !d.ReadOnly {
		t.Error("device paired with readOnly=true should be marked ReadOnly (roleRemoteGuest)")
	}
}

func TestRevokeDeviceClosesLiveConnections(t *testing.T) {
	sm := newPairingSM(t)
	now := time.Now()
	rid, _, _ := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{}, now)

	deviceID, token, err := sm.ApprovePairing(rid, false, now)
	if err != nil {
		t.Fatal(err)
	}

	c1, c2 := net.Pipe()
	defer func() { _ = c2.Close() }()

	sm.RegisterDeviceConn(deviceID, c1)

	n, err := sm.RevokeDevice(deviceID)
	if err != nil {
		t.Fatal(err)
	}

	if n != 1 {
		t.Errorf("RevokeDevice closed %d connections, want 1", n)
	}

	// The device and its token are gone.
	if sm.DeviceForToken(token) != nil {
		t.Error("revoked device must no longer resolve by token")
	}

	// The live connection is force-closed: a read returns an error immediately.
	_ = c1.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c1.Read(make([]byte, 1)); err == nil {
		t.Error("revoked connection should be closed (read should error)")
	}
}

func TestDeviceForTokenEmptyToken(t *testing.T) {
	sm := newPairingSM(t)
	// An empty token can never resolve a device — the guard fails closed before
	// any index lookup.
	if sm.DeviceForToken("") != nil {
		t.Error("empty token must resolve to no device")
	}
}

func TestRevokeDeviceUnknown(t *testing.T) {
	sm := newPairingSM(t)
	// Revoking a device that was never paired is an error, not a silent no-op.
	if _, err := sm.RevokeDevice("nae-such-device"); err == nil {
		t.Error("revoking an unknown device must return an error")
	}
}

func TestUnregisterDeviceConn(t *testing.T) {
	sm := newPairingSM(t)
	c1, _ := net.Pipe()
	c2, _ := net.Pipe()

	sm.RegisterDeviceConn("bairn-dev", c1)
	sm.RegisterDeviceConn("bairn-dev", c2)
	sm.UnregisterDeviceConn("bairn-dev", c1)

	sm.mu.RLock()
	got := len(sm.connsByDevice["bairn-dev"])
	sm.mu.RUnlock()

	if got != 1 {
		t.Errorf("connsByDevice after unregister = %d, want 1", got)
	}
}

func TestApprovePairingRejectsExpired(t *testing.T) {
	sm := newPairingSM(t)
	sm.cfg.Remote.PairRequestRate = "1000/min"
	base := time.Now()

	rid, _, err := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{User: "u", Node: "ben"}, base)
	if err != nil {
		t.Fatal(err)
	}

	// Approving after the TTL must fail (the pending is expired and cleaned up).
	if _, _, err := sm.ApprovePairing(rid, false, base.Add(config.RemotePendingPairingTTLDefault+time.Minute)); err == nil {
		t.Error("expected ApprovePairing to reject an expired pending request")
	}

	if pending, _ := sm.ListPairings(); len(pending) != 0 {
		t.Errorf("expired pending should be gone, got %d", len(pending))
	}
}

func TestApprovePairingSaveFailurePreservesPending(t *testing.T) {
	sm := newPairingSM(t)
	now := time.Now()

	rid, _, err := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{User: "u", Node: "ben"}, now)
	if err != nil {
		t.Fatal(err)
	}

	// Force saveState to fail: point StateFile under a regular file so the write
	// hits ENOTDIR.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm.paths.StateFile = filepath.Join(blocker, "state.json")

	if _, _, err := sm.ApprovePairing(rid, false, now); err == nil {
		t.Fatal("expected ApprovePairing to fail when saveState fails")
	}

	// The pending request must be restored so the device need not re-pair, and no
	// half-approved device should linger.
	pending, paired := sm.ListPairings()
	if len(pending) != 1 || pending[0].RequestID != rid {
		t.Errorf("pending not preserved on save failure: %+v", pending)
	}

	if len(paired) != 0 {
		t.Errorf("no device should be persisted on save failure, got %d", len(paired))
	}
}

func TestIdentityMatchesDeviceRejectsEmptyNode(t *testing.T) {
	d := &PairedDevice{TailnetUser: "speir@example.com", TailnetNode: "ben"}

	if identityMatchesDevice(&TailnetIdentity{User: "speir@example.com", Node: "ben"}, d) != true {
		t.Error("matching identity should match")
	}

	// A degenerate zero-value identity must never match.
	if identityMatchesDevice(&TailnetIdentity{}, &PairedDevice{}) {
		t.Error("empty identity must not match an empty device record")
	}

	if identityMatchesDevice(&TailnetIdentity{User: "speir@example.com"}, d) {
		t.Error("identity with empty Node must not match")
	}

	if identityMatchesDevice(&TailnetIdentity{User: "speir@example.com", Node: "ben"}, &PairedDevice{TailnetUser: "speir@example.com"}) {
		t.Error("device record with empty Node must not match")
	}
}
