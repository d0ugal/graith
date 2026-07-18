//go:build libghostty && cgo

package pty

func nativeTerminalBackendFactories() []terminalBackendFactory {
	return []terminalBackendFactory{
		{
			name:             "libghostty-helper",
			combiningContent: "e\u0301",
			shrinkFirstLine:  "cann",
			new: func(cols, rows int) (Terminal, error) {
				return newTerminal(cols, rows)
			},
		},
	}
}
