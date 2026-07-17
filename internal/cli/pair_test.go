package cli

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

// TestRunPairApproveLoopLegacyPrintsPin guards cross-version approval (issue
// #1299 gate 10): a legacy (pre-receipt) daemon sends only the final
// pair_approved with the TLS pin — no staged pair_approval_pending — so the new
// CLI must still print the pin, or the operator has nothing to compare.
func TestRunPairApproveLoopLegacyPrintsPin(t *testing.T) {
	payload, err := protocol.EncodeControl("pair_approved", protocol.PairResponseMsg{DeviceID: "dev-1", TLSPinSPKI: "loch-pin"})
	if err != nil {
		t.Fatal(err)
	}

	env, err := protocol.DecodeControl(payload)
	if err != nil {
		t.Fatal(err)
	}

	sent := false
	read := func() (protocol.Envelope, error) {
		if sent {
			return protocol.Envelope{}, io.EOF
		}

		sent = true

		return env, nil
	}

	var lines []string

	printf := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }

	if err := runPairApproveLoop(read, printf); err != nil {
		t.Fatalf("legacy approve loop: %v", err)
	}

	if got := strings.Count(strings.Join(lines, ""), "loch-pin"); got != 1 {
		t.Fatalf("legacy approval must print the TLS pin exactly once, got %d: %v", got, lines)
	}
}

// TestRunPairApproveLoopTwoStagePrintsPinOnce guards that a current daemon's
// two-stage reply prints the pin exactly once (from the staged frame), not again
// at the final pair_approved.
func TestRunPairApproveLoopTwoStagePrintsPinOnce(t *testing.T) {
	pendingPayload, _ := protocol.EncodeControl("pair_approval_pending", protocol.PairApprovalPendingMsg{RequestID: "req-1", TLSPinSPKI: "loch-pin"})
	pendingEnv, _ := protocol.DecodeControl(pendingPayload)
	approvedPayload, _ := protocol.EncodeControl("pair_approved", protocol.PairResponseMsg{RequestID: "req-1", DeviceID: "dev-1", TLSPinSPKI: "loch-pin"})
	approvedEnv, _ := protocol.DecodeControl(approvedPayload)

	seq := []protocol.Envelope{pendingEnv, approvedEnv}
	i := 0
	read := func() (protocol.Envelope, error) {
		e := seq[i]
		i++

		return e, nil
	}

	var lines []string

	printf := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }

	if err := runPairApproveLoop(read, printf); err != nil {
		t.Fatalf("two-stage approve loop: %v", err)
	}

	if got := strings.Count(strings.Join(lines, ""), "loch-pin"); got != 1 {
		t.Fatalf("pin should be printed exactly once, got %d: %v", got, lines)
	}
}

// TestControlErrorCov2DecodesMessage confirms controlError surfaces the
// daemon's error message verbatim from an error envelope.
func TestControlErrorCov2DecodesMessage(t *testing.T) {
	raw, err := protocol.EncodeControl("error", protocol.ErrorMsg{Message: "the loch is thrawn"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	env, err := protocol.DecodeControl(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	got := controlError(env)
	if got == nil {
		t.Fatal("expected an error")
	}

	if got.Error() != "the loch is thrawn" {
		t.Errorf("controlError = %q, want %q", got.Error(), "the loch is thrawn")
	}
}

// TestControlErrorCov2EmptyPayload ensures a malformed/empty error envelope
// still yields a (blank-message) error rather than panicking — the decode
// failure is swallowed and the empty message is formatted.
func TestControlErrorCov2EmptyPayload(t *testing.T) {
	env := protocol.Envelope{Type: "error"}

	got := controlError(env)
	if got == nil {
		t.Fatal("expected a non-nil error even for an empty payload")
	}

	if got.Error() != "" {
		t.Errorf("controlError with empty payload = %q, want empty string", got.Error())
	}
}
