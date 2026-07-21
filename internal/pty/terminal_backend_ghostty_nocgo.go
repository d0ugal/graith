//go:build libghostty && !cgo

package pty

import "errors"

var errLibghosttyRequiresCGO = errors.New("libghostty backend requires CGO_ENABLED=1")

// A libghostty build is an explicit native selection. Returning an error keeps
// a misconfigured build observable instead of silently changing semantics.
func newTerminal(_, _ int) (Terminal, error) {
	return nil, errLibghosttyRequiresCGO
}

// TerminalBackend reports the explicitly selected backend even when this
// binary cannot initialize it because cgo was disabled.
func TerminalBackend() string { return TerminalBackendLibghosttyHelper }

func TerminalAdoptionCapacity() (maxSessions int, available bool) {
	return 0, false
}

func ProbeTerminalAdoption() (maxSessions int, available bool) { return 0, false }
