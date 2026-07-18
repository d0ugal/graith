//go:build !libghostty || (libghostty_compare && (!cgo || (!darwin && !linux)))

package pty

func nativeTerminalBackendFactories() []terminalBackendFactory {
	return nil
}
