//go:build !libghostty

package pty

// newTerminal constructs the rollback backend in ordinary pure-Go builds. A
// libghostty release candidate selects the native helper explicitly at build
// time, so the two implementations cannot be mixed or silently substituted.
func newTerminal(cols, rows int) (Terminal, error) {
	return newCharmTerminal(cols, rows), nil
}
