package cli

import (
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

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
