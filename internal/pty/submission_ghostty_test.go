//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

import "testing"

func TestSubmissionFollowsTerminalPasteMode(t *testing.T) {
	terminal := newTerminalTestTerm(t, 80, 24)

	s, slave := submissionPTY(t, terminal)
	for _, test := range []struct{ mode, want string }{
		{"\x1b[?2004h", "\x1b[200~bothy\x1b[201~\r"},
		{"\x1b[?2004l", "bothy\r"},
		{"\x1b[?2004h", "\x1b[200~bothy\x1b[201~\r"},
	} {
		if _, err := terminal.Write([]byte(test.mode)); err != nil {
			t.Fatal(err)
		}

		if err := s.WriteInputAndSubmit([]byte("bothy")); err != nil {
			t.Fatal(err)
		}

		if got := string(readSubmission(t, slave, len(test.want))); got != test.want {
			t.Fatalf("after mode %q: input = %q, want %q", test.mode, got, test.want)
		}
	}
}
