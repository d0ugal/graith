//go:build libghostty && ((cgo && !darwin && !linux) || libghostty_test_unsupported)

package pty

import "errors"

var errLibghosttyUnsupportedOS = errors.New("libghostty backend is supported only on macOS and Linux")

// The libghostty_test_unsupported tag makes this fail-closed selector runnable
// on a supported CI host without pretending a cross-compiled binary executed.
func newTerminal(_, _ int) (Terminal, error) {
	return nil, errLibghosttyUnsupportedOS
}

func TerminalAdoptionCapacity() (maxSessions int, available bool) {
	return 0, false
}

func ProbeTerminalAdoption() (maxSessions int, available bool) { return 0, false }
