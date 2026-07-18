//go:build libghostty

package pty

import (
	"errors"
	"testing"
)

func TestLibghosttyRejectsUnsupportedOS(t *testing.T) {
	term, err := newUnsupportedLibghosttyTerminal(80, 24)
	if term != nil {
		t.Fatalf("newTerminal() terminal = %T, want nil", term)
	}
	if !errors.Is(err, errLibghosttyUnsupportedOS) {
		t.Fatalf("newTerminal() error = %v, want %v", err, errLibghosttyUnsupportedOS)
	}
}
