//go:build !libghostty || !cgo || (!linux && (!darwin || !arm64))

package pty

import "context"

// FreezeTerminalHelpers is a no-op when this binary cannot launch native
// helpers. It shares the daemon upgrade path without exposing the backend.
func FreezeTerminalHelpers(context.Context) ([]HelperProcessIdentity, error) { return nil, nil }

func ThawTerminalHelpers() {}

func ClosePinnedTerminalExecutable() {}

func PreparePinnedTerminalExecutable() error { return nil }

func ReleasePinnedTerminalExecutablePathForExec() error { return nil }

func RestorePinnedTerminalExecutableAfterExec() error { return nil }
