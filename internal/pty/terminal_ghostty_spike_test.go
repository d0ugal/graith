//go:build libghostty && cgo

package pty

func experimentalTerminalSpikeFactories() []terminalSpikeFactory {
	return []terminalSpikeFactory{
		{
			name:             "libghostty",
			combiningContent: "e\u0301",
			shrinkFirstLine:  "cann",
			new: func(cols, rows int) (Terminal, error) {
				return newGhosttyTerminal(cols, rows)
			},
		},
	}
}
