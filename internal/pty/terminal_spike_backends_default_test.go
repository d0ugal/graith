//go:build !libghostty || !cgo

package pty

func experimentalTerminalSpikeFactories() []terminalSpikeFactory {
	return nil
}
