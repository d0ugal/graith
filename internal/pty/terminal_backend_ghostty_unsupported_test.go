//go:build libghostty && cgo && !linux && (!darwin || !arm64)

package pty

import (
	"errors"
	"testing"
)

func TestLibghosttyBackendRejectsUnsupportedPlatform(t *testing.T) {
	if got := TerminalBackend(); got != TerminalBackendLibghosttyHelper {
		t.Fatalf("TerminalBackend() = %q, want %q", got, TerminalBackendLibghosttyHelper)
	}

	term, err := newTerminal(12, 3)
	if term != nil {
		_ = term.Close()

		t.Fatalf("unsupported libghostty terminal = %T, want nil", term)
	}

	if !errors.Is(err, errLibghosttyUnsupportedPlatform) {
		t.Fatalf("unsupported libghostty error = %v, want %v", err, errLibghosttyUnsupportedPlatform)
	}
}
