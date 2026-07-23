//go:build !libghostty

package pty

import "testing"

func TestDefaultTerminalBackendFailsClosed(t *testing.T) {
	if got := TerminalBackend(); got != TerminalBackendUnavailable {
		t.Fatalf("TerminalBackend() = %q, want %q", got, TerminalBackendUnavailable)
	}

	if _, err := newTerminal(80, 24); err != errNativeTerminalRequired {
		t.Fatalf("newTerminal error = %v, want %v", err, errNativeTerminalRequired)
	}
}
