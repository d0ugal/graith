//go:build libghostty && !cgo

package pty

import "errors"

var errLibghosttyRequiresCGO = errors.New("libghostty backend requires CGO_ENABLED=1")

// A libghostty build is an explicit native selection. Returning an error keeps
// a misconfigured build observable instead of silently changing semantics.
func newTerminal(_, _ int) (Terminal, error) {
	return nil, errLibghosttyRequiresCGO
}
