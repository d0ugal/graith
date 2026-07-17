package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

// driveReceipt simulates the pair_request delivery goroutine for a unit test of
// ApprovePairing: it consumes the delivered credential and reports the given
// receipt outcome, then drains the commit result so ApprovePairing's committed
// send never leaks. It runs in the background because ApprovePairing blocks on
// the receipt round-trip.
//
// ackReceipt=true acknowledges receipt (drives a durable commit); false reports
// a receipt failure (the requester never confirmed), so the transaction is
// abandoned. A bounded fallback prevents a stuck goroutine if ApprovePairing
// errors before delivering.
func driveReceipt(t *testing.T, waiter *pairWaiter, ackReceipt bool) {
	t.Helper()

	go func() {
		select {
		case appr, ok := <-waiter.approval:
			if !ok {
				return
			}

			// Mirror the real delivery goroutine: confirm pair_response delivery
			// first, then report the receipt outcome.
			appr.delivered <- nil

			if ackReceipt {
				appr.receipt <- nil
			} else {
				appr.receipt <- errSimulatedNoReceipt
			}

			<-appr.committed
		case <-time.After(3 * time.Second):
		}
	}()
}

// errSimulatedNoReceipt models a requester that never acknowledged receipt.
var errSimulatedNoReceipt = &pairingTestError{"simulated: no receipt"}

type pairingTestError struct{ msg string }

func (e *pairingTestError) Error() string { return e.msg }

// approveWithReceipt is the happy-path helper: it drives an acknowledged receipt
// and returns ApprovePairing's committed result.
func approveWithReceipt(t *testing.T, sm *SessionManager, waiter *pairWaiter, rid string, readOnly bool, now time.Time) (string, string, error) {
	t.Helper()
	driveReceipt(t, waiter, true)

	return sm.ApprovePairing(rid, readOnly, now)
}

// TestHandlePairApproveStagesPendingBeforeCommit guards the local-approve ↔
// device-confirm deadlock (issue #1299): handlePairApprove must send the staged
// pair_approval_pending (with the TLS pin) immediately, before ApprovePairing
// blocks awaiting the device's receipt ack, and only send the terminal
// pair_approved after the device acknowledges and the daemon commits.
func TestHandlePairApproveStagesPendingBeforeCommit(t *testing.T) {
	sm := newPairingSM(t)
	now := time.Now()

	rid, waiter, err := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{}, now)
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu         sync.Mutex
		sent       []string
		pendingPin string
	)

	send := func(msgType string, payload any) {
		mu.Lock()
		defer mu.Unlock()

		sent = append(sent, msgType)

		if pp, ok := payload.(protocol.PairApprovalPendingMsg); ok {
			pendingPin = pp.TLSPinSPKI
		}
	}

	payload, err := protocol.EncodeControl("pair_approve", protocol.PairApproveMsg{RequestID: rid})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := protocol.DecodeControl(payload)
	if err != nil {
		t.Fatal(err)
	}

	// A controllable delivery goroutine: it delivers pair_response (which triggers
	// the staged pin) then holds before acking, so the test can observe that the
	// staged pending frame arrives after delivery but before the ack/commit.
	ackGate := make(chan struct{})

	go func() {
		appr, ok := <-waiter.approval
		if !ok {
			return
		}

		appr.delivered <- nil // delivery confirmed → staged pin fires
		<-ackGate             // hold before acknowledging
		appr.receipt <- nil
		<-appr.committed
	}()

	done := make(chan struct{})

	go func() {
		handlePairApprove(sm, authContext{role: roleLocalHuman}, send, msg, sm.log)
		close(done)
	}()

	// The staged pending frame arrives once delivery is confirmed, before the ack.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(sent)
		mu.Unlock()

		if got >= 1 {
			break
		}

		time.Sleep(2 * time.Millisecond)
	}

	mu.Lock()
	if len(sent) != 1 || sent[0] != "pair_approval_pending" {
		got := append([]string(nil), sent...)
		mu.Unlock()
		t.Fatalf("before the device ack, expected only [pair_approval_pending], got %v", got)
	}

	if pendingPin != sm.RemoteTLSPin() {
		mu.Unlock()
		t.Fatalf("staged pin = %q, want %q", pendingPin, sm.RemoteTLSPin())
	}
	mu.Unlock()

	// Release the ack; approval commits and replies.
	close(ackGate)
	<-done

	mu.Lock()
	defer mu.Unlock()

	if len(sent) != 2 || sent[0] != "pair_approval_pending" || sent[1] != "pair_approved" {
		t.Fatalf("expected [pair_approval_pending, pair_approved], got %v", sent)
	}
}

// approveReplies runs handlePairApprove for requestID and returns the ordered
// list of control-message types it sent. When driveWaiter is non-nil it is
// called after handlePairApprove starts, so the caller can drive (or not) the
// receipt for cases that reach the wait.
func approveReplies(t *testing.T, sm *SessionManager, requestID string, driveWaiter func()) []string {
	t.Helper()

	var (
		mu   sync.Mutex
		sent []string
	)

	send := func(msgType string, _ any) {
		mu.Lock()
		sent = append(sent, msgType)
		mu.Unlock()
	}

	payload, err := protocol.EncodeControl("pair_approve", protocol.PairApproveMsg{RequestID: requestID})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := protocol.DecodeControl(payload)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})

	go func() {
		handlePairApprove(sm, authContext{role: roleLocalHuman}, send, msg, sm.log)
		close(done)
	}()

	if driveWaiter != nil {
		driveWaiter()
	}

	<-done

	mu.Lock()
	defer mu.Unlock()

	return append([]string(nil), sent...)
}

// TestHandlePairApproveInvalidRequestsReplyErrorOnly guards the staged-ordering
// fix (issue #1299): an unknown, expired, or disconnected request must reply with
// only an error — never a premature pair_approval_pending (pin + "waiting").
func TestHandlePairApproveInvalidRequestsReplyErrorOnly(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		sm := newPairingSM(t)

		got := approveReplies(t, sm, "no-such-request", nil)
		if len(got) != 1 || got[0] != "error" {
			t.Fatalf("unknown request: reply = %v, want only [error]", got)
		}
	})

	t.Run("expired", func(t *testing.T) {
		sm := newPairingSM(t)
		now := time.Now()

		rid, _, err := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{}, now)
		if err != nil {
			t.Fatal(err)
		}

		// Force expiry: drive the pending deadline into the past before approving.
		sm.mu.Lock()
		sm.pendingPairings[rid].ExpiresAt = now.Add(-time.Minute)
		sm.mu.Unlock()

		got := approveReplies(t, sm, rid, nil)
		if len(got) != 1 || got[0] != "error" {
			t.Fatalf("expired request: reply = %v, want only [error]", got)
		}
	})

	t.Run("disconnected", func(t *testing.T) {
		sm := newPairingSM(t)
		now := time.Now()
		disconnected := make(chan struct{})
		close(disconnected)

		rid, _, err := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{}, now, disconnected)
		if err != nil {
			t.Fatal(err)
		}

		got := approveReplies(t, sm, rid, nil)
		if len(got) != 1 || got[0] != "error" {
			t.Fatalf("disconnected request: reply = %v, want only [error]", got)
		}
	})
}

// TestApprovePairingNoStagedPinWhenDeliveryFails guards the buffered-handoff race
// (issue #1299): if pair_response is never delivered to a live requester, the
// staged "pending" pin must NOT be emitted and nothing durable is committed —
// the buffered approval handoff alone does not prove delivery.
func TestApprovePairingNoStagedPinWhenDeliveryFails(t *testing.T) {
	sm := newPairingSM(t)
	now := time.Now()

	rid, waiter, err := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{}, now)
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu     sync.Mutex
		staged bool
	)

	stagedCb := func(_ string) {
		mu.Lock()
		staged = true
		mu.Unlock()
	}

	// Delivery goroutine consumes the credential but reports a write failure, so
	// pair_response never reached the requester.
	go func() {
		appr, ok := <-waiter.approval
		if !ok {
			return
		}

		appr.delivered <- errSimulatedNoReceipt
		<-appr.committed
	}()

	_, _, apErr := sm.ApprovePairing(rid, false, now, stagedCb)
	if apErr == nil {
		t.Fatal("ApprovePairing must fail when pair_response is not delivered")
	}

	mu.Lock()
	if staged {
		mu.Unlock()
		t.Fatal("staged pin was emitted despite pair_response never being delivered")
	}
	mu.Unlock()

	if _, paired := sm.ListPairings(); len(paired) != 0 {
		t.Fatalf("device committed despite failed delivery: %d paired", len(paired))
	}
}

// TestApprovePairingCommitsOnlyAfterReceipt is the core #1299 guard: with a
// requester that acknowledges receipt, ApprovePairing durably commits the device
// and its token index.
func TestApprovePairingCommitsOnlyAfterReceipt(t *testing.T) {
	sm := newPairingSM(t)
	now := time.Now()

	rid, waiter, err := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{}, now)
	if err != nil {
		t.Fatal(err)
	}

	deviceID, token, err := approveWithReceipt(t, sm, waiter, rid, false, now)
	if err != nil {
		t.Fatalf("ApprovePairing with acknowledged receipt: %v", err)
	}

	if deviceID == "" || token == "" {
		t.Fatal("expected a device ID and token on a committed pairing")
	}

	if d := sm.DeviceForToken(token); d == nil || d.ID != deviceID {
		t.Fatal("committed device did not resolve by token")
	}
}

// TestApprovePairingRollsBackOnDisconnectBeforeAck is the deterministic race the
// round-3 tribunal required: the requester disconnects after approval begins
// (the credential was delivered) but before acknowledging receipt. No durable
// paired device or token index entry may remain.
func TestApprovePairingRollsBackOnDisconnectBeforeAck(t *testing.T) {
	sm := newPairingSM(t)
	now := time.Now()
	disconnected := make(chan struct{})

	rid, waiter, err := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{}, now, disconnected)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the delivery goroutine: consume the credential (delivery began),
	// then never acknowledge — the requester vanishes.
	began := make(chan struct{})

	go func() {
		appr, ok := <-waiter.approval
		if !ok {
			return
		}

		// pair_response was delivered; then the requester vanishes before acking.
		appr.delivered <- nil
		close(began)
		<-disconnected
		appr.receipt <- errSimulatedNoReceipt
		<-appr.committed
	}()

	done := make(chan struct{})

	var (
		deviceID string
		token    string
		apErr    error
	)

	go func() {
		deviceID, token, apErr = sm.ApprovePairing(rid, false, now)
		close(done)
	}()

	<-began             // credential delivered; delivery has "begun"
	close(disconnected) // requester disconnects before acknowledging receipt
	<-done

	if apErr == nil {
		t.Fatal("ApprovePairing must fail when the requester disconnects before acknowledging receipt")
	}

	if deviceID != "" || token != "" {
		t.Fatalf("no credential should be returned on a failed receipt, got device=%q token=%q", deviceID, token)
	}

	// The crux: nothing durable may remain.
	if _, paired := sm.ListPairings(); len(paired) != 0 {
		t.Fatalf("a durable device was stranded without acknowledged receipt: %d paired", len(paired))
	}

	sm.mu.RLock()
	idxLen := len(sm.deviceTokenIndex)
	devLen := len(sm.state.PairedDevices)
	sm.mu.RUnlock()

	if idxLen != 0 || devLen != 0 {
		t.Fatalf("token index/device state committed without receipt: index=%d devices=%d", idxLen, devLen)
	}
}

// TestApprovePairingRollsBackOnReceiptTimeout proves that if the requester never
// acknowledges within the request's immutable deadline, nothing is committed.
func TestApprovePairingRollsBackOnReceiptTimeout(t *testing.T) {
	sm := newPairingSM(t)
	now := time.Now()

	rid, waiter, err := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{}, now)
	if err != nil {
		t.Fatal(err)
	}

	// The pending TTL has a config minimum (1m), so shorten just the receipt wait
	// by driving the waiter's deadline directly — the request itself is not expired.
	waiter.expiresAt = now.Add(20 * time.Millisecond)

	// Delivery goroutine delivers pair_response but never acknowledges; the
	// receipt wait must time out at the request's deadline.
	go func() {
		appr, ok := <-waiter.approval
		if !ok {
			return
		}

		appr.delivered <- nil
		<-appr.committed // eventually receives the abandon outcome
	}()

	deviceID, token, apErr := sm.ApprovePairing(rid, false, now)
	if apErr == nil {
		t.Fatal("ApprovePairing must fail when receipt is never acknowledged")
	}

	if deviceID != "" || token != "" {
		t.Fatal("no credential should be returned on a receipt timeout")
	}

	if _, paired := sm.ListPairings(); len(paired) != 0 {
		t.Fatalf("device committed despite receipt timeout: %d paired", len(paired))
	}
}

// TestApprovePairingSaveFailureRollsBackInMemory proves that when the durable
// save fails after an acknowledged receipt, the in-memory device and token index
// are rolled back so this running daemon does not serve a device it cannot be
// sure it persisted. (The durable on-disk outcome is commit-unknown — atomicfile
// can fail after the rename — which is precisely why the client retains its
// credential; issue #1299.)
func TestApprovePairingSaveFailureRollsBackInMemory(t *testing.T) {
	sm := newPairingSM(t)
	now := time.Now()

	rid, waiter, err := sm.AddPendingPairing("bairn", testPubKey(t), TailnetIdentity{User: "u", Node: "ben"}, now)
	if err != nil {
		t.Fatal(err)
	}

	// Point StateFile under a regular file so saveState hits ENOTDIR.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if werr := os.WriteFile(blocker, []byte("x"), 0o600); werr != nil {
		t.Fatal(werr)
	}

	sm.paths.StateFile = filepath.Join(blocker, "state.json")

	_, _, apErr := approveWithReceipt(t, sm, waiter, rid, false, now)
	if apErr == nil {
		t.Fatal("ApprovePairing must fail when saveState fails")
	}

	if _, paired := sm.ListPairings(); len(paired) != 0 {
		t.Fatalf("no device should be persisted on save failure, got %d", len(paired))
	}

	sm.mu.RLock()
	idxLen := len(sm.deviceTokenIndex)
	sm.mu.RUnlock()

	if idxLen != 0 {
		t.Fatalf("token index left committed on save failure: %d entries", idxLen)
	}
}
