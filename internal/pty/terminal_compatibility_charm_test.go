//go:build !libghostty

package pty

func selectedTerminalBackendExpectations() terminalBackendExpectations {
	expectations := commonTerminalBackendExpectations()
	expectations.issue1430Error = errTerminalParserPanic
	expectations.resizePreviews = []string{
		"canny brae bide\n\n\n",
		"cann\n",
		"cann\n\n",
		"cann\n",
		"cann\n\n\n",
	}
	expectations.alternateScreens = map[int]terminalAlternateExpectation{
		// Charm ignores mode 47, while 1047 and 1049 clear, home, and
		// restore the main screen.
		47:   {active: "braebothy", restored: "braebothy"},
		1047: {active: "bothy", restored: "brae"},
		1049: {active: "bothy", restored: "brae"},
	}
	// Charm drops a combining mark, but clusters the remaining
	// multi-codepoint graphemes into one wide cell.
	expectations.graphemes["combining"] = terminalGraphemeExpectation{
		cells: []string{"e", "b"}, cursorX: 2, preview: "eb",
	}
	expectations.fragmented = map[string]terminalGraphemeExpectation{
		// Charm's parser preserves incomplete UTF-8 between writes, but
		// commits each completed codepoint before a later write can extend
		// it into these multi-codepoint clusters.
		"zwj": {
			cells: []string{"👩", "", "💻", "", "b"}, cursorX: 5, preview: "👩💻b",
		},
		"variation_selector": {
			cells: []string{"♥", "b"}, cursorX: 2, preview: "♥b",
		},
		"regional_indicator": {
			cells: []string{"🇬", "", "🇧", "", "b"}, cursorX: 5, preview: "🇬🇧b",
		},
	}

	return expectations
}
