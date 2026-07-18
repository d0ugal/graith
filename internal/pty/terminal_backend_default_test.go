//go:build !libghostty || !cgo

package pty

func nativeTerminalBackendFactories() []terminalBackendFactory {
	return nil
}
