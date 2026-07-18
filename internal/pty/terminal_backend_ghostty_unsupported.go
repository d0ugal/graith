//go:build libghostty && cgo && !darwin && !linux

package pty

import "errors"

var errLibghosttyUnsupportedOS = errors.New("libghostty backend is supported only on macOS and Linux")

func newTerminal(_, _ int) (Terminal, error) {
	return nil, errLibghosttyUnsupportedOS
}

func TerminalAdoptionCapacity() (maxSessions int, available bool) {
	return 0, false
}

func ProbeTerminalAdoption() (maxSessions int, available bool) { return 0, false }
