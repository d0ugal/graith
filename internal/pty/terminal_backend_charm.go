//go:build !libghostty

package pty

// newTerminal constructs the production Charm backend. The libghostty spike
// can only replace this selection in a build that explicitly opts into its tag.
func newTerminal(cols, rows int) Terminal {
	return newCharmTerminal(cols, rows)
}
