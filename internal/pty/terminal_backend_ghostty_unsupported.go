//go:build libghostty && cgo && !darwin && !linux

package pty

func newTerminal(width, height int) (Terminal, error) {
	return newUnsupportedLibghosttyTerminal(width, height)
}

func TerminalAdoptionCapacity() (maxSessions int, available bool) {
	return 0, false
}

func ProbeTerminalAdoption() (maxSessions int, available bool) { return 0, false }
