//go:build libghostty && libghostty_compare && cgo && ((darwin && arm64) || linux)

package pty

import "time"

func ghosttyTerminalBackendExpectations() terminalBackendExpectations {
	expectations := charmTerminalBackendExpectations()
	expectations.contains1430Panic = false
	expectations.resizePreviews = []string{
		"canny brae bide\n\n\n",
		"ae b\nide",
		"ae bide\n\n",
		"ae bi\nde",
		"ae bide\n\n\n",
	}
	// Ghostty implements all three alternate modes and clears the screen, but
	// retains the main screen's cursor column on entry.
	expectations.alternateScreens = map[int]terminalAlternateExpectation{
		47:   {active: "    bothy", restored: "brae"},
		1047: {active: "    bothy", restored: "brae"},
		1049: {active: "    bothy", restored: "brae"},
	}
	// Graith enables Ghostty's recommended mode 2027, so all remaining
	// multi-codepoint and wide expectations match Charm. Unlike Charm, Ghostty
	// also preserves the combining mark.
	expectations.graphemes["combining"] = terminalGraphemeExpectation{
		cells: []string{"e\u0301", "b"}, cursorX: 2, preview: "e\u0301b",
	}
	// Ghostty keeps grapheme assembly state across Write boundaries, so its
	// byte-fragmented expectations remain identical to whole writes.
	expectations.fragmented = nil

	return expectations
}

func nativeTerminalBackendFactories() []terminalBackendFactory {
	return []terminalBackendFactory{
		{
			name:         "libghostty-helper",
			expectations: ghosttyTerminalBackendExpectations(),
			helperPID: func(term Terminal) (int, bool) {
				helper, ok := term.(*ghosttyProcessTerminal)
				if !ok || helper.cmd == nil || helper.cmd.Process == nil {
					return 0, false
				}

				return helper.cmd.Process.Pid, true
			},
			helperCPUTime: func(term Terminal) (time.Duration, bool) {
				helper, ok := term.(*ghosttyProcessTerminal)
				if !ok || helper.cmd == nil || helper.cmd.ProcessState == nil {
					return 0, false
				}

				return helper.cmd.ProcessState.UserTime() + helper.cmd.ProcessState.SystemTime(), true
			},
			new: func(cols, rows int) (Terminal, error) {
				return newTerminal(cols, rows)
			},
		},
	}
}
