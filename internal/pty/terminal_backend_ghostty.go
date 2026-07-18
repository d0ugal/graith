//go:build libghostty && cgo && (darwin || linux)

package pty

// newTerminal selects the process-isolated native backend for an explicit
// libghostty+cgo build. Initialization failures are returned to the caller;
// this path never silently substitutes another emulator.
func newTerminal(cols, rows int) (Terminal, error) {
	return newGhosttyProcessTerminal(cols, rows)
}
