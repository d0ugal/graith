//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

func selectedTerminalBackendExpectations() terminalBackendExpectations {
	return commonTerminalBackendExpectations()
}
