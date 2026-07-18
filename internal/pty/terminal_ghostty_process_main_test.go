//go:build libghostty && cgo && (darwin || linux)

package pty

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	ClosePinnedTerminalExecutable()
	os.Exit(code)
}
