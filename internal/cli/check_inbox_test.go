package cli

import "testing"

// TestCheckInboxCov2NoSessionIsNoOp verifies the hook command is a silent
// no-op outside a graith session: with GRAITH_SESSION_ID unset it must return
// nil immediately without attempting a daemon connection, so it never blocks or
// errors an agent hook running on a plain shell.
func TestCheckInboxCov2NoSessionIsNoOp(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "")

	if err := checkInboxCmd.RunE(checkInboxCmd, nil); err != nil {
		t.Fatalf("check-inbox outside a session should be a no-op, got %v", err)
	}
}
