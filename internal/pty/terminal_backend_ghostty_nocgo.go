//go:build libghostty && !cgo

package pty

// The experiment must not make CGO_ENABLED=0 builds fail. Without cgo the
// libghostty selector is unavailable and the production Go backend remains.
func newTerminal(cols, rows int) Terminal {
	return newCharmTerminal(cols, rows)
}
