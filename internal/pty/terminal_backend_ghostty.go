//go:build libghostty && cgo && (darwin || linux)

package pty

import "context"

// newTerminal selects the process-isolated native backend for an explicit
// libghostty+cgo build. Initialization failures are returned to the caller;
// this path never silently substitutes another emulator.
func newTerminal(cols, rows int) (Terminal, error) {
	return newGhosttyProcessTerminal(cols, rows)
}

// TerminalAdoptionCapacity reports the private helper-process capacity to the
// daemon's cross-binary upgrade preflight.
func TerminalAdoptionCapacity() (maxSessions int, available bool) {
	return ghosttyMaxHelperProcesses, true
}

// ProbeTerminalAdoption proves this exact tagged binary can start, initialize,
// close, and reap a helper rather than advertising build tags alone.
func ProbeTerminalAdoption() (maxSessions int, available bool) {
	terminal, err := newGhosttyProcessTerminal(2, 2)
	if err != nil {
		return 0, false
	}
	if err := terminal.Close(); err != nil {
		return 0, false
	}
	helpers, _ := FreezeTerminalHelpers(context.Background())
	ThawTerminalHelpers()
	if len(helpers) != 0 {
		return 0, false
	}

	return ghosttyMaxHelperProcesses, true
}
