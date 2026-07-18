//go:build libghostty && !cgo

package pty

import (
	"errors"
	"testing"
)

func TestLibghosttyBackendRequiresCGO(t *testing.T) {
	term, err := newTerminal(12, 3)
	if term != nil {
		_ = term.Close()

		t.Fatalf("CGO-disabled libghostty terminal = %T, want nil", term)
	}

	if !errors.Is(err, errLibghosttyRequiresCGO) {
		t.Fatalf("CGO-disabled libghostty error = %v, want %v", err, errLibghosttyRequiresCGO)
	}
}
