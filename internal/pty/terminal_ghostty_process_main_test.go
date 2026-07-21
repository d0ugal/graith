//go:build libghostty && cgo && ((darwin && arm64) || linux)

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
