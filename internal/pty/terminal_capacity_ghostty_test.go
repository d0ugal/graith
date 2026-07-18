//go:build libghostty && cgo && (darwin || linux)

package pty

import "testing"

func TestTerminalAdoptionCapacity(t *testing.T) {
	maxSessions, available := TerminalAdoptionCapacity()
	if !available || maxSessions != ghosttyMaxHelperProcesses {
		t.Fatalf("capacity = (%d, %t), want %d and available",
			maxSessions, available, ghosttyMaxHelperProcesses)
	}
}
