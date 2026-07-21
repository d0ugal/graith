//go:build !libghostty

package pty

// newTerminal constructs the rollback backend in ordinary pure-Go builds. A
// libghostty release candidate selects the native helper explicitly at build
// time, so the two implementations cannot be mixed or silently substituted.
func newTerminal(cols, rows int) (Terminal, error) {
	return newCharmTerminal(cols, rows), nil
}

// TerminalBackend reports the terminal-screen backend selected by this build.
func TerminalBackend() string { return TerminalBackendCharm }

// TerminalAdoptionCapacity reports the number of terminal models this binary
// can reconstruct during daemon exec adoption. Zero means no fixed process
// capacity. The result is consumed only by the private upgrade preflight.
func TerminalAdoptionCapacity() (maxSessions int, available bool) {
	return 0, true
}

func ProbeTerminalAdoption() (maxSessions int, available bool) { return 0, true }
