//go:build libghostty && cgo && !linux && (!darwin || !arm64)

package pty

import "errors"

var errLibghosttyUnsupportedPlatform = errors.New("libghostty backend is supported only on macOS arm64 and Linux")

func newTerminal(_, _ int) (Terminal, error) {
	return nil, errLibghosttyUnsupportedPlatform
}

// TerminalBackend reports the explicitly selected backend even when this
// binary cannot initialize it on the current platform.
func TerminalBackend() string { return TerminalBackendLibghosttyHelper }

func TerminalAdoptionCapacity() (maxSessions int, available bool) {
	return 0, false
}

func ProbeTerminalAdoption() (maxSessions int, available bool) { return 0, false }
