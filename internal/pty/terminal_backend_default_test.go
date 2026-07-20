//go:build !libghostty || (libghostty_compare && (!cgo || (!linux && (!darwin || !arm64))))

package pty

func nativeTerminalBackendFactories() []terminalBackendFactory {
	return nil
}
