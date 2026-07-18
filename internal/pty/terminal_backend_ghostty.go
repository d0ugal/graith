//go:build libghostty && cgo

package pty

// newTerminal selects the experimental adapter only for an explicit
// libghostty+cgo build. Initialization failure retains the known Go backend;
// a production migration would need an observable, error-returning factory
// before enabling this selector in release artifacts.
func newTerminal(cols, rows int) Terminal {
	terminal, err := newGhosttyTerminal(cols, rows)
	if err != nil {
		return newCharmTerminal(cols, rows)
	}

	return terminal
}
