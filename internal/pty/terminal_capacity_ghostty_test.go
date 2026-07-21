//go:build libghostty && cgo && ((darwin && arm64) || linux)

package pty

import "testing"

func TestTerminalAdoptionCapacity(t *testing.T) {
	if got := TerminalBackend(); got != TerminalBackendLibghosttyHelper {
		t.Fatalf("terminal backend identifier = %q, want %q", got, TerminalBackendLibghosttyHelper)
	}

	maxSessions, available := TerminalAdoptionCapacity()
	if !available || maxSessions != ghosttyMaxHelperProcesses {
		t.Fatalf("capacity = (%d, %t), want %d and available",
			maxSessions, available, ghosttyMaxHelperProcesses)
	}
}
