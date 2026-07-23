//go:build libghostty && (!cgo || !((darwin && arm64) || (linux && (amd64 || arm64))))

package pty

import "testing"

func TestTerminalAdoptionCapacity(t *testing.T) {
	maxSessions, available := TerminalAdoptionCapacity()
	if available || maxSessions != 0 {
		t.Fatalf("capacity = (%d, %t), want unavailable", maxSessions, available)
	}
}
