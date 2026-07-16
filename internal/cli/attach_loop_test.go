package cli

import (
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/client"
)

// captureStdout (shared with store_test.go) redirects os.Stdout for the
// duration of fn and returns what was written — resetTerminal writes raw
// escapes straight to stdout.

// TestTerminalResetSequence locks the exact escape blob: it must leave the
// alternate screen, re-show the cursor, and pop the Kitty keyboard protocol —
// the sequences a misbehaving agent most often leaves dangling.
func TestTerminalResetSequence(t *testing.T) {
	seq := terminalResetSequence()

	for _, want := range []string{
		"\x1b[?1049l", // leave alt screen
		"\x1b[?25h",   // show cursor
		"\x1b[<u",     // pop Kitty keyboard
		"\x1b[2J\x1b[H",
	} {
		if !strings.Contains(seq, want) {
			t.Errorf("reset sequence missing %q", want)
		}
	}
}

func TestResetTerminalWritesSequence(t *testing.T) {
	got := captureStdout(t, resetTerminal)
	if got != terminalResetSequence() {
		t.Errorf("resetTerminal wrote %q, want the reset sequence", got)
	}
}

// TestDispatchTerminalExit verifies the detach/quit results end the loop
// (done=true, no error) after resetting the terminal.
func TestDispatchTerminalExit(t *testing.T) {
	for _, result := range []client.PassthroughResult{client.ResultDetached, client.ResultQuit} {
		l := &attachLoop{}

		var (
			done bool
			err  error
		)

		out := captureStdout(t, func() { done, err = l.dispatch(result) })

		if !done || err != nil {
			t.Errorf("dispatch(%v) = (%v, %v), want (true, nil)", result, done, err)
		}

		if out == "" {
			t.Errorf("dispatch(%v) should reset the terminal", result)
		}
	}
}

// TestDispatchUnknownResultContinues: an unrecognised result loops again
// (done=false) with no side effects, mirroring the original switch fall-through.
func TestDispatchUnknownResultContinues(t *testing.T) {
	l := &attachLoop{}

	done, err := l.dispatch(client.PassthroughResult(9999))
	if done || err != nil {
		t.Errorf("dispatch(unknown) = (%v, %v), want (false, nil)", done, err)
	}
}
