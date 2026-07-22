//go:build libghostty && cgo && ((darwin && arm64) || linux)

package pty

func selectedTerminalBackendExpectations() terminalBackendExpectations {
	return commonTerminalBackendExpectations()
}
