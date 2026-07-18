//go:build !libghostty

package pty

import "testing"

func TestDefaultTerminalBackendIsCharm(t *testing.T) {
	term, err := newTerminal(12, 3)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = term.Close() })

	if _, ok := term.(*charmTerminal); !ok {
		t.Fatalf("default terminal backend = %T, want *charmTerminal", term)
	}
}
