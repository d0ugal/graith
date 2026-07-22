//go:build !libghostty

package pty

import "testing"

func TestNativeTerminalSelfTestRejectsDefaultBackend(t *testing.T) {
	if err := RunNativeTerminalSelfTest(); err == nil {
		t.Fatal("native terminal self-test accepted the default backend")
	}
}
