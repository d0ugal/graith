//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

import (
	"fmt"
	"strings"
	"testing"
)

// terminalBackendFactory keeps the compatibility corpus independent of the
// selected implementation. Each build exercises exactly its production
// backend; the retired comparison tag no longer links two emulators together.
type terminalBackendFactory struct {
	name         string
	new          func(cols, rows int) (Terminal, error)
	expectations terminalBackendExpectations
}

func selectedTerminalBackendFactory() terminalBackendFactory {
	return terminalBackendFactory{
		name:         TerminalBackend(),
		new:          newTerminal,
		expectations: selectedTerminalBackendExpectations(),
	}
}

func newTerminalBackendTestTerm(
	t testing.TB,
	factory terminalBackendFactory,
	cols, rows int,
) Terminal {
	t.Helper()

	term, err := factory.new(cols, rows)
	if err != nil {
		t.Fatalf("new %s terminal: %v", factory.name, err)
	}

	t.Cleanup(func() {
		if err := term.Close(); err != nil {
			t.Errorf("close %s terminal: %v", factory.name, err)
		}
	})

	return term
}

func syntheticTerminalWorkload(minBytes int) []byte {
	var fixture strings.Builder
	fixture.Grow(minBytes + 128)

	for i := 0; fixture.Len() < minBytes; i++ {
		fmt.Fprintf(
			&fixture,
			"\x1b[3%dm%06d canny braw brae e\u0301 你 😀\x1b[0m\r\n",
			i%8,
			i,
		)
	}

	return []byte(fixture.String())
}
