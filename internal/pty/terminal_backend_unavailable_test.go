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

func TestDefaultTerminalHelpersRemainCovered(t *testing.T) {
	cols, rows := clampSize(0, -1)
	if cols != 1 || rows != 1 {
		t.Fatalf("clampSize(0, -1) = (%d, %d), want (1, 1)", cols, rows)
	}

	term := newUnavailableTerminal(2, 1)

	_ = renderFrame(term)
	_ = renderPreview(term)

	if fixture := terminalParserPanicFixture(t); len(fixture) == 0 {
		t.Fatal("terminal parser panic fixture is empty")
	}
}
