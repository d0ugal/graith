//go:build !libghostty

package pty

import "testing"

func TestTerminalAdoptionCapacity(t *testing.T) {
	maxSessions, available := TerminalAdoptionCapacity()
	if available || maxSessions != 0 {
		t.Fatalf("capacity = (%d, %t), want unavailable", maxSessions, available)
	}
}
