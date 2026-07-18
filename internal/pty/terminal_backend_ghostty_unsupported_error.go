//go:build libghostty

package pty

import "errors"

var errLibghosttyUnsupportedOS = errors.New("libghostty backend is supported only on macOS and Linux")

// newUnsupportedLibghosttyTerminal is shared by the production unsupported-OS
// selector and its host-runnable regression test. Keeping source selection in
// terminal_backend_ghostty_unsupported.go avoids a production test hook.
func newUnsupportedLibghosttyTerminal(_, _ int) (Terminal, error) {
	return nil, errLibghosttyUnsupportedOS
}
