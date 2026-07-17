package daemon

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
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

// pendingPairing is an unapproved device pairing request awaiting local human
// approval. It lives only in memory — pending requests do not survive a daemon
// restart (a device simply re-requests).
type pendingPairing struct {
	RequestID   string
	DeviceLabel string
	PubKey      string
	Identity    TailnetIdentity
	RequestedAt time.Time
	ExpiresAt   time.Time
}

// pairApproval is handed to a blocked pair_request connection's delivery
// goroutine when its pending request is approved locally, carrying the minted
// credentials so the device receives its client token over the connection it is
// already holding open (design §B.2 step 3).
//
// It also carries the receipt handshake channels for the #1299 commit protocol:
// the delivery goroutine delivers pair_response, awaits the client's pair_ack,
// and reports the outcome on receipt; ApprovePairing then durably commits (or
// not) and reports the commit outcome on committed, which the goroutine uses to
// emit pair_committed. Both channels are buffered (cap 1) so neither side blocks.
type pairApproval struct {
	RequestID string
	DeviceID  string
	Token     string
	Profile   string
	TLSPin    string

	// delivered: delivery goroutine → ApprovePairing. nil once pair_response has
	// actually been written to the live requester; non-nil on a write failure. It
	// is never sent if the goroutine exited (timeout/disconnect) before reading the
	// approval — the buffered approval handoff alone does NOT prove delivery, so
	// ApprovePairing must confirm this before staging the local pin (issue #1299).
	delivered chan error
	// receipt: delivery goroutine → ApprovePairing. nil once the live requester
	// acknowledged receipt of pair_response; non-nil on requester disconnect or
	// receipt timeout.
	receipt chan error
	// committed: ApprovePairing → delivery goroutine. nil once the durable device
	// is persisted; non-nil when the transaction was abandoned (no device saved).
	committed chan error
}

// pairWaiter binds the credential delivery channel to the same immutable
// deadline stored on the pending request. A config reload therefore cannot
// retime one side of an in-flight pairing without the other.
type pairWaiter struct {
	approval     chan pairApproval
	expiresAt    time.Time
	disconnected <-chan struct{}
}

// unregisterPairWaiter removes a pair_request waiter (on timeout/disconnect).
func (sm *SessionManager) unregisterPairWaiter(requestID string) {
	sm.mu.Lock()
	delete(sm.pairWaiters, requestID)
	delete(sm.pendingPairings, requestID)
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

	return sm.cfg.Remote.PairFallbackRate()
}

// maxPendingPairings is the outstanding pending-request cap, from config.
func (sm *SessionManager) maxPendingPairings() int {
	return sm.cfg.Remote.MaxPendingPairingsOrDefault()
}

// expirePendingLocked drops pending pairings whose immutable deadline passed.
// Must be called with sm.mu held.
func (sm *SessionManager) expirePendingLocked(now time.Time) {
	for rid, p := range sm.pendingPairings {
		if !now.Before(p.ExpiresAt) {
			delete(sm.pendingPairings, rid)
			delete(sm.pairWaiters, rid)
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
func (sm *SessionManager) AddPendingPairing(label, pubKey string, id TailnetIdentity, now time.Time, disconnected ...<-chan struct{}) (string, *pairWaiter, error) {
	if !validEd25519PubKey(pubKey) {
		return "", nil, errors.New("invalid device public key")
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

	if len(sm.pendingPairings) >= sm.maxPendingPairings() {
		return "", nil, errors.New("too many pending pairing requests")
	}

	rid, err := randomHex(8)
	if err != nil {
		return "", nil, err
	}

	expiresAt := now.Add(sm.cfg.Remote.PendingPairingTTLDuration())
	sm.pendingPairings[rid] = &pendingPairing{
		RequestID:   rid,
		DeviceLabel: label,
		PubKey:      pubKey,
		Identity:    id,
		RequestedAt: now,
		ExpiresAt:   expiresAt,
	}
	sm.pairReqTimes = append(sm.pairReqTimes, now)

	// Register the waiter atomically with the pending entry (closes the
	// approve-before-waiter race).
	var disconnectedCh <-chan struct{}
	if len(disconnected) > 0 {
		disconnectedCh = disconnected[0]
	}

	waiter := &pairWaiter{
		approval:     make(chan pairApproval, 1),
		expiresAt:    expiresAt,
		disconnected: disconnectedCh,
	}
	sm.pairWaiters[rid] = waiter

	return rid, waiter, nil
}

// ApprovePairing approves a pending pairing and durably commits a new paired
// device, but only after the live requester acknowledges receipt of its one-time
// credential (issue #1299). readOnly marks a device paired while
// require_pairing=false (maps to roleRemoteGuest). It returns the device ID and
// the one-time client token (only returned here; only its HMAC is stored). now is
// passed for testability.
//
// The commit is a transaction: credentials are minted and staged in memory under
// sm.mu, delivered to the waiting pair_request goroutine, and durably persisted
// only after that goroutine reports the requester acknowledged receipt. The wait
// for the ack and durable save runs OFF sm.mu (the session-manager lock is never
// held across the receipt round-trip). On disconnect, send failure, or receipt
// timeout, nothing is persisted. A saveState failure rolls back the in-memory
// device and token index, but is NOT proof nothing reached disk (atomicfile can
// fail after the rename) — see the save-failure branch: the durable outcome is
// commit-unknown, which is why the client retains its credential.
//
// staged, if provided, is invoked exactly once with the daemon TLS pin AFTER all
// guards pass and the credential has been handed to the live waiter, but BEFORE
// the receipt wait. The local approve handler uses it to emit a staged
// "pending" reply carrying the pin, so an invalid/expired/disconnected request
// (which returns before staged runs) yields only an error, never a misleading
// pin + "waiting" (issue #1299).
func (sm *SessionManager) ApprovePairing(requestID string, readOnly bool, now time.Time, staged ...func(tlsPin string)) (deviceID, clientToken string, err error) {
	sm.mu.Lock()

	// Enforce the pending TTL here too, not only opportunistically on add, so an
	// expired request can never be approved.
	sm.expirePendingLocked(now)

	p, ok := sm.pendingPairings[requestID]
	if !ok {
		sm.mu.Unlock()
		return "", "", fmt.Errorf("no pending pairing with id %q (unknown or expired)", requestID)
	}

	waiter, ok := sm.pairWaiters[requestID]
	if !ok {
		// Pairing credentials are one-time delivery material. Refuse to proceed
		// after the requesting connection has gone away: there would be no live
		// recipient for the unhashed token.
		delete(sm.pendingPairings, requestID)
		sm.mu.Unlock()

		return "", "", fmt.Errorf("pairing request %q no longer has a live requester", requestID)
	}

	if disconnectedNow(waiter.disconnected) {
		delete(sm.pairWaiters, requestID)
		delete(sm.pendingPairings, requestID)
		sm.mu.Unlock()

		return "", "", fmt.Errorf("pairing request %q disconnected before approval", requestID)
	}

	key, err := sm.state.EnsurePairingHMACKey()
	if err != nil {
		sm.mu.Unlock()
		return "", "", err
	}

	deviceID, err = randomHex(8)
	if err != nil {
		sm.mu.Unlock()
		return "", "", err
	}

	clientToken, err = generateToken()
	if err != nil {
		sm.mu.Unlock()
		return "", "", err
	}

	hash := hmacToken(key, clientToken)
	device := &PairedDevice{
		ID:          deviceID,
		Label:       p.DeviceLabel,
		PubKey:      p.PubKey,
		TailnetUser: p.Identity.User,
		TailnetNode: p.Identity.Node,
		TokenHash:   hash,
		ReadOnly:    readOnly,
		CreatedAt:   now,
	}

	// The credential is now minted. Remove the request and its waiter so this is a
	// strict one-shot (a concurrent approval finds nothing), but do NOT persist the
	// device yet — that waits on acknowledged delivery below.
	delete(sm.pendingPairings, requestID)
	delete(sm.pairWaiters, requestID)
	tlsPin := sm.remoteTLSPin
	profile := sm.paths.Profile
	sm.mu.Unlock()

	// Hand the credential to the blocked pair_request goroutine and wait — OFF the
	// session-manager lock — for it to deliver pair_response and report whether the
	// live requester acknowledged receipt.
	approval := pairApproval{
		RequestID: requestID,
		DeviceID:  deviceID,
		Token:     clientToken,
		Profile:   profile,
		TLSPin:    tlsPin,
		delivered: make(chan error, 1),
		receipt:   make(chan error, 1),
		committed: make(chan error, 1),
	}
	waiter.approval <- approval

	// The buffered handoff above does NOT prove pair_response reached a live
	// requester: the delivery goroutine may already have exited on timeout/
	// disconnect. Wait for it to confirm the frame was actually written before
	// doing anything the operator would read as success (issue #1299).
	if deliveredErr := awaitPairingDelivered(approval.delivered, waiter, now); deliveredErr != nil {
		// pair_response was never written to a live requester. Unblock the
		// goroutine's committed-drain (harmless if it already exited) and persist
		// nothing — no staged pin is emitted.
		approval.committed <- deliveredErr

		return "", "", fmt.Errorf("pairing request %q credential was not delivered to a live requester: %w", requestID, deliveredErr)
	}

	// Delivery is confirmed. Only now signal the staged "pending" reply, so an
	// unknown/expired/disconnected/undelivered request never emits a premature pin
	// (issue #1299).
	for _, cb := range staged {
		if cb != nil {
			cb(tlsPin)
		}
	}

	receiptErr := awaitPairingReceipt(approval.receipt, waiter, now)
	if receiptErr != nil {
		// The requester never acknowledged receipt (disconnect, send failure, or
		// the request's immutable deadline passed). Persist nothing and tell the
		// delivery goroutine the transaction was abandoned.
		approval.committed <- receiptErr

		return "", "", fmt.Errorf("pairing request %q credential was not delivered to a live requester: %w", requestID, receiptErr)
	}

	// Receipt acknowledged: durably commit the device now.
	sm.mu.Lock()
	sm.state.PairedDevices[deviceID] = device
	sm.deviceTokenIndex[hash] = deviceID

	saveErr := sm.saveState()
	if saveErr != nil {
		// An atomic write may report a directory-fsync error after its rename has
		// already published the new state. Reconcile that commit-unknown outcome
		// against disk before rolling back the live index: otherwise this process
		// reports "invalid token" to the client's recovery probe even though a
		// restart will reload and authorize the exact same grant. Keep the live
		// grant only when disk contains the exact staged device under the same HMAC
		// key; every pre-publication or mismatched outcome still rolls back.
		persisted, loadErr := LoadState(sm.paths.StateFile)
		if loadErr != nil || !pairingGrantPersisted(persisted, key, device) {
			delete(sm.state.PairedDevices, deviceID)
			delete(sm.deviceTokenIndex, hash)
		}
	}
	sm.mu.Unlock()

	// Report the commit outcome so the goroutine emits pair_committed (or an error)
	// to the requester. A later pair_committed write failure does NOT roll the
	// device back: receipt was already acknowledged and the device is durable.
	approval.committed <- saveErr

	if saveErr != nil {
		if sm.log != nil {
			sm.log.Error("pairing state save failed; in-memory index rolled back, durable outcome unknown until reload", "device", deviceID, "request_id", requestID, "err", saveErr)
		}

		return "", "", saveErr
	}

	return deviceID, clientToken, nil
}

// pairingGrantPersisted reports whether an on-disk state contains the exact
// pairing grant staged by ApprovePairing. It deliberately compares every
// persisted device field and the HMAC key: a partial, stale, or unrelated state
// must not make the running daemon authorize a credential after a failed save.
func pairingGrantPersisted(state *State, hmacKey string, want *PairedDevice) bool {
	if state == nil || state.PairingHMACKey != hmacKey || want == nil {
		return false
	}

	got, ok := state.PairedDevices[want.ID]
	if !ok || got == nil {
		return false
	}

	return got.ID == want.ID &&
		got.Label == want.Label &&
		got.PubKey == want.PubKey &&
		got.TailnetUser == want.TailnetUser &&
		got.TailnetNode == want.TailnetNode &&
		got.TokenHash == want.TokenHash &&
		got.ReadOnly == want.ReadOnly &&
		got.CreatedAt.Equal(want.CreatedAt) &&
		got.LastSeenAt.Equal(want.LastSeenAt)
}

// awaitPairingDelivered blocks (off sm.mu) until the delivery goroutine reports
// that pair_response was actually written to the requester, the requester
// disconnects, or the request's immutable deadline passes. It returns nil only
// on confirmed delivery. This is what closes the buffered-handoff race: the
// goroutine may have exited on timeout/disconnect before ever reading the
// approval, in which case `delivered` never arrives and the disconnect/timeout
// paths govern (issue #1299).
func awaitPairingDelivered(delivered <-chan error, waiter *pairWaiter, now time.Time) error {
	timer := time.NewTimer(waiter.expiresAt.Sub(now))
	defer timer.Stop()

	select {
	case err := <-delivered:
		return err
	case <-waiter.disconnected:
		select {
		case err := <-delivered:
			return err
		default:
			return errors.New("requester disconnected before pair_response was delivered")
		}
	case <-timer.C:
		select {
		case err := <-delivered:
			return err
		default:
			return errors.New("pair_response delivery timed out")
		}
	}
}

// awaitPairingReceipt blocks (off sm.mu) until the delivery goroutine reports the
// requester acknowledged receipt, the requester disconnects, or the request's
// immutable deadline passes. It returns nil only on an acknowledged receipt.
func awaitPairingReceipt(receipt <-chan error, waiter *pairWaiter, now time.Time) error {
	timer := time.NewTimer(waiter.expiresAt.Sub(now))
	defer timer.Stop()

	// A nil disconnected channel (a waiter created without a connDone signal)
	// blocks forever in its select case, so the receipt/timeout paths govern.
	select {
	case err := <-receipt:
		return err
	case <-waiter.disconnected:
		// Prefer an ack that already completed before the disconnect was observed.
		select {
		case err := <-receipt:
			return err
		default:
			return errors.New("requester disconnected before acknowledging receipt")
		}
	case <-timer.C:
		select {
		case err := <-receipt:
			return err
		default:
			return errors.New("receipt acknowledgement timed out")
		}
	}
}

// disconnectedNow reports whether ch (a connDone-style channel) is already
// closed. A nil channel is treated as still-connected.
func disconnectedNow(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}

	select {
	case <-ch:
		return true
	default:
		return false
	}
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
