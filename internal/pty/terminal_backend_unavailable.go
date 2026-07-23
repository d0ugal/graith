//go:build !libghostty

package pty

import "errors"

var errNativeTerminalRequired = errors.New("native libghostty terminal backend is required; rebuild with -tags=libghostty and CGO_ENABLED=1")

// Ordinary builds deliberately fail closed. The historical pure-Go terminal
// backend is no longer part of graith, and release builds must opt into the
// native helper explicitly.
func newTerminal(_, _ int) (Terminal, error) { return nil, errNativeTerminalRequired }

func TerminalBackend() string { return TerminalBackendUnavailable }

func TerminalAdoptionCapacity() (int, bool) { return 0, false }

func ProbeTerminalAdoption() (int, bool) { return 0, false }
