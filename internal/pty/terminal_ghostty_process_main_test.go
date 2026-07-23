//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

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
