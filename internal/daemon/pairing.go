package daemon

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

// This file implements the cryptographic primitives for device pairing and
// proof-of-possession (design §B.2). Client tokens are stored only as a keyed
// HMAC (never in the clear); proof-of-possession verifies that the connecting
// device holds the ed25519 private key it registered at pairing time, so a
// leaked bearer token alone is insufficient.

// randomHex returns n cryptographically-random bytes hex-encoded.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("random bytes: %w", err)
	}

	return hex.EncodeToString(b), nil
}

// hmacToken computes hex(HMAC-SHA256(key, token)). key is the per-daemon
// pairing HMAC key (State.PairingHMACKey); token is the client token.
func hmacToken(key, token string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(token))

	return hex.EncodeToString(m.Sum(nil))
}

// verifyPoP verifies a proof-of-possession: that sigB64 is a valid ed25519
// signature, by the private key matching pubKeyB64, over the channel-bound
// signing input for (nonce, spki) — see protocol.PoPSigningInput. pubKeyB64 and
// sigB64 are base64 (std encoding); nonce is the raw challenge string and spki
// the daemon's own TLS SPKI pin, which binds the proof to this TLS channel and
// defeats a MITM relaying the handshake (issue #886). Any empty input
// (including an empty spki — fail closed if the pin is somehow unset), decode
// failure, or size mismatch returns false. Replay safety depends on the caller
// issuing a fresh, single-use, connection-bound nonce (handler wiring, Task 6).
func verifyPoP(pubKeyB64, nonce, spki, sigB64 string) bool {
	if pubKeyB64 == "" || nonce == "" || spki == "" || sigB64 == "" {
		return false
	}

	pub, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}

	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}

	return ed25519.Verify(ed25519.PublicKey(pub), protocol.PoPSigningInput(nonce, spki), sig)
}

// validEd25519PubKey reports whether s is a base64 std-encoded ed25519 public
// key of the correct size.
func validEd25519PubKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)

	return err == nil && len(b) == ed25519.PublicKeySize
}

// --- pairing state operations (design §B.2) ---

const (
	maxPendingPairings = 16
	pendingPairingTTL  = 10 * time.Minute
	// defaultPairRate applies when [remote].pair_request_rate is unset/invalid.
	defaultPairCount = 5
	defaultPairPer   = time.Minute
)

// pendingPairing is an unapproved device pairing request awaiting local human
// approval. It lives only in memory — pending requests do not survive a daemon
// restart (a device simply re-requests).
type pendingPairing struct {
	RequestID   string
	DeviceLabel string
	PubKey      string
	Identity    TailnetIdentity
	RequestedAt time.Time
}

// pairApproval is delivered to a blocked pair_request connection when its
// pending request is approved locally, carrying the minted credentials so the
// device receives its client token over the connection it is already holding
// open (design §B.2 step 3).
type pairApproval struct {
	DeviceID string
	Token    string
	Profile  string
	TLSPin   string
}

// unregisterPairWaiter removes a pair_request waiter (on timeout/disconnect).
func (sm *SessionManager) unregisterPairWaiter(requestID string) {
	sm.mu.Lock()
	delete(sm.pairWaiters, requestID)
	sm.mu.Unlock()
}

// rebuildDeviceTokenIndex rebuilds the client-token → device-ID reverse lookup
// from persisted state. Mirrors rebuildTokenIndex for session tokens.
func (sm *SessionManager) rebuildDeviceTokenIndex() {
	sm.deviceTokenIndex = make(map[string]string, len(sm.state.PairedDevices))
	for id, d := range sm.state.PairedDevices {
		if d.TokenHash != "" {
			sm.deviceTokenIndex[d.TokenHash] = id
		}
	}
}

func (sm *SessionManager) pairRate() config.PairRate {
	if r, err := config.ParsePairRequestRate(sm.cfg.Remote.PairRequestRate); err == nil {
		return r
	}

	return config.PairRate{Count: defaultPairCount, Per: defaultPairPer}
}

// expirePendingLocked drops pending pairings older than pendingPairingTTL.
// Must be called with sm.mu held.
func (sm *SessionManager) expirePendingLocked(now time.Time) {
	for rid, p := range sm.pendingPairings {
		if now.Sub(p.RequestedAt) > pendingPairingTTL {
			delete(sm.pendingPairings, rid)
		}
	}
}

// AddPendingPairing records a device pairing request, subject to a per-daemon
// rate limit and a cap on outstanding requests. now is passed for
// deterministic testing. It does not persist state — pending pairings are
// in-memory only.
// The returned channel is the waiter for this request: it is registered under
// the same lock that creates the pending entry, so an approval can never race
// ahead of the waiter and drop the delivery. The caller reads it (with its own
// timeout / disconnect handling) and must unregisterPairWaiter when done.
func (sm *SessionManager) AddPendingPairing(label, pubKey string, id TailnetIdentity, now time.Time) (string, chan pairApproval, error) {
	if !validEd25519PubKey(pubKey) {
		return "", nil, fmt.Errorf("invalid device public key")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.expirePendingLocked(now)

	rate := sm.pairRate()

	cutoff := now.Add(-rate.Per)
	kept := sm.pairReqTimes[:0]

	for _, t := range sm.pairReqTimes {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}

	sm.pairReqTimes = kept

	if len(sm.pairReqTimes) >= rate.Count {
		return "", nil, fmt.Errorf("pair request rate limit exceeded (%d per %s)", rate.Count, rate.Per)
	}

	if len(sm.pendingPairings) >= maxPendingPairings {
		return "", nil, fmt.Errorf("too many pending pairing requests")
	}

	rid, err := randomHex(8)
	if err != nil {
		return "", nil, err
	}

	sm.pendingPairings[rid] = &pendingPairing{
		RequestID:   rid,
		DeviceLabel: label,
		PubKey:      pubKey,
		Identity:    id,
		RequestedAt: now,
	}
	sm.pairReqTimes = append(sm.pairReqTimes, now)

	// Register the waiter atomically with the pending entry (closes the
	// approve-before-waiter race).
	waiter := make(chan pairApproval, 1)
	sm.pairWaiters[rid] = waiter

	return rid, waiter, nil
}

// ApprovePairing approves a pending pairing, minting and persisting a new paired
// device. readOnly marks a device paired while require_pairing=false (maps to
// roleRemoteGuest). It returns the device ID and the one-time client token (only
// returned here; only its HMAC is stored). now is passed for testability.
func (sm *SessionManager) ApprovePairing(requestID string, readOnly bool, now time.Time) (deviceID, clientToken string, err error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Enforce the pending TTL here too, not only opportunistically on add, so an
	// expired request can never be approved.
	sm.expirePendingLocked(now)

	p, ok := sm.pendingPairings[requestID]
	if !ok {
		return "", "", fmt.Errorf("no pending pairing with id %q (unknown or expired)", requestID)
	}

	key, err := sm.state.EnsurePairingHMACKey()
	if err != nil {
		return "", "", err
	}

	deviceID, err = randomHex(8)
	if err != nil {
		return "", "", err
	}

	clientToken, err = generateToken()
	if err != nil {
		return "", "", err
	}

	hash := hmacToken(key, clientToken)

	sm.state.PairedDevices[deviceID] = &PairedDevice{
		ID:          deviceID,
		Label:       p.DeviceLabel,
		PubKey:      p.PubKey,
		TailnetUser: p.Identity.User,
		TailnetNode: p.Identity.Node,
		TokenHash:   hash,
		ReadOnly:    readOnly,
		CreatedAt:   now,
	}
	sm.deviceTokenIndex[hash] = deviceID
	delete(sm.pendingPairings, requestID)

	if err := sm.saveState(); err != nil {
		// Roll back all in-memory mutations so state stays consistent — including
		// restoring the pending request, so a transient save failure doesn't force
		// the device to start a fresh pairing.
		delete(sm.state.PairedDevices, deviceID)
		delete(sm.deviceTokenIndex, hash)
		sm.pendingPairings[requestID] = p

		return "", "", err
	}

	// Hand the credentials to a blocked pair_request connection, if one is
	// waiting, so the device receives its token over its open connection. The
	// channel is buffered (cap 1), so this never blocks under the lock.
	if ch, ok := sm.pairWaiters[requestID]; ok {
		ch <- pairApproval{DeviceID: deviceID, Token: clientToken, Profile: sm.paths.Profile, TLSPin: sm.remoteTLSPin}

		delete(sm.pairWaiters, requestID)
	}

	return deviceID, clientToken, nil
}

// DeviceForToken resolves a client token to its paired device, or nil. Must be
// called under at least RLock.
func (sm *SessionManager) DeviceForToken(token string) *PairedDevice {
	if token == "" || sm.state.PairingHMACKey == "" {
		return nil
	}

	id, ok := sm.deviceTokenIndex[hmacToken(sm.state.PairingHMACKey, token)]
	if !ok {
		return nil
	}

	return sm.state.PairedDevices[id]
}

// RegisterDeviceConn records a live connection for a device so revocation can
// force-close it. Called once per connection after successful authentication.
func (sm *SessionManager) RegisterDeviceConn(deviceID string, conn net.Conn) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.connsByDevice[deviceID] = append(sm.connsByDevice[deviceID], conn)
}

// UnregisterDeviceConn removes a connection from a device's live set (on
// connection close).
func (sm *SessionManager) UnregisterDeviceConn(deviceID string, conn net.Conn) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	conns := sm.connsByDevice[deviceID]
	kept := conns[:0]

	for _, c := range conns {
		if c != conn {
			kept = append(kept, c)
		}
	}

	// Nil out the trailing slots so the backing array doesn't retain references
	// to closed connections.
	for i := len(kept); i < len(conns); i++ {
		conns[i] = nil
	}

	if len(kept) == 0 {
		delete(sm.connsByDevice, deviceID)
	} else {
		sm.connsByDevice[deviceID] = kept
	}
}

// RevokeDevice removes a paired device and force-closes all of its live
// connections, so a revoked (e.g. lost/stolen) device loses control
// immediately rather than at next-connection (design §B.5). It returns the
// number of connections closed.
func (sm *SessionManager) RevokeDevice(deviceID string) (int, error) {
	sm.mu.Lock()

	d, ok := sm.state.PairedDevices[deviceID]
	if !ok {
		sm.mu.Unlock()
		return 0, fmt.Errorf("no paired device with id %q", deviceID)
	}

	delete(sm.state.PairedDevices, deviceID)
	delete(sm.deviceTokenIndex, d.TokenHash)

	conns := sm.connsByDevice[deviceID]
	delete(sm.connsByDevice, deviceID)

	err := sm.saveState()
	if err != nil && sm.log != nil {
		// The device is revoked in memory and its connections are being closed,
		// but persistence failed — a restart would reload it. Surface loudly; the
		// error is also returned so `gr pair revoke` reports failure to the human.
		sm.log.Error("pairing revocation persisted only in memory; retry revoke after a restart", "device", deviceID, "err", err)
	}

	sm.mu.Unlock()

	// Close connections outside the lock: closing wakes each connection's read
	// loop, whose deferred cleanup calls back into the SessionManager
	// (ClearAttachedClient etc.) and would otherwise deadlock on sm.mu.
	for _, c := range conns {
		_ = c.Close()
	}

	return len(conns), err
}

// ListPairings returns snapshots of the pending pairing requests and the
// persisted paired devices. Must be called without holding sm.mu.
func (sm *SessionManager) ListPairings() ([]pendingPairing, []PairedDevice) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	pending := make([]pendingPairing, 0, len(sm.pendingPairings))
	for _, p := range sm.pendingPairings {
		pending = append(pending, *p)
	}

	paired := make([]PairedDevice, 0, len(sm.state.PairedDevices))
	for _, d := range sm.state.PairedDevices {
		paired = append(paired, *d)
	}

	return pending, paired
}
