package pty

import (
	"bytes"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	creackpty "github.com/creack/pty"
	"golang.org/x/term"
)

// This harness uses a real raw-mode PTY but no terminal helper or live agent.
// Leaving the slave unread until all writes finish models a stalled TUI: kernel
// buffering erases the writer's text-to-Enter delay from the consumer's clock.
func submissionPTY(t *testing.T, screen Terminal) (*Session, *os.File) {
	t.Helper()

	master, slave, err := creackpty.Open()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = master.Close(); _ = slave.Close() })

	if _, err := term.MakeRaw(int(slave.Fd())); err != nil {
		t.Fatal(err)
	}

	s := &Session{Ptmx: master, screen: screen}
	s.SetInputDelay(time.Millisecond)

	return s, slave
}

func readSubmission(t *testing.T, slave *os.File, size int) []byte {
	t.Helper()

	result := make(chan []byte, 1)
	go func() { b := make([]byte, size); n, _ := io.ReadFull(slave, b); result <- b[:n] }()

	select {
	case b := <-result:
		return b
	case <-time.After(3 * time.Second):
		t.Fatal("PTY did not receive complete submission")
		return nil
	}
}

func pasteTerminal(enabled bool) *snapshotTestTerminal {
	return &snapshotTestTerminal{snapshot: TerminalSnapshot{InputModes: TerminalInputModes{BracketedPaste: enabled}}}
}

type failedSubmissionTerminal struct{ snapshotTestTerminal }

func (*failedSubmissionTerminal) Snapshot() (TerminalSnapshot, error) {
	return TerminalSnapshot{}, errors.New("canny screen unavailable")
}

func TestSubmissionFraming(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		screen       Terminal
		input        string
		want         string
		initializing bool
		closed       bool
		raw          bool
	}{
		"negotiated paste":          {screen: pasteTerminal(true), input: "braw", want: "\x1b[200~braw\x1b[201~\r"},
		"multiline literal":         {screen: pasteTerminal(true), input: "braw\ncanny", want: "\x1b[200~braw\ncanny\x1b[201~\r"},
		"disabled":                  {screen: pasteTerminal(false), input: "braw", want: "braw\r"},
		"unknown":                   {input: "braw", want: "braw\r"},
		"failed snapshot":           {screen: &failedSubmissionTerminal{}, input: "braw", want: "braw\r"},
		"hydrating":                 {screen: pasteTerminal(true), initializing: true, input: "braw", want: "braw\r"},
		"empty submit":              {screen: pasteTerminal(true), want: "\r"},
		"closed screen":             {screen: pasteTerminal(true), closed: true, input: "braw", want: "braw\r"},
		"embedded Return keys":      {screen: pasteTerminal(true), input: "braw\rcanny", want: "braw\rcanny\r"},
		"embedded Tab key":          {screen: pasteTerminal(true), input: "braw\t", want: "braw\t\r"},
		"raw control keys":          {screen: pasteTerminal(true), input: "\x03", want: "\x03\r"},
		"raw escape keys":           {screen: pasteTerminal(true), input: "\x1b[A", want: "\x1b[A\r"},
		"embedded paste terminator": {screen: pasteTerminal(true), input: "braw\x1b[201~canny", want: "braw\x1b[201~canny\r"},
		"no newline remains raw":    {screen: pasteTerminal(true), input: "braw", want: "braw", raw: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, slave := submissionPTY(t, test.screen)
			s.screenInitializing = test.initializing
			s.closed = test.closed

			var err error
			if test.raw {
				err = s.WriteInput([]byte(test.input))
			} else {
				err = s.WriteInputAndSubmit([]byte(test.input))
			}

			if err != nil {
				t.Fatal(err)
			}
			// A sentinel detects extra CRs and framing bytes, including after empty input.
			if err := s.WriteInput([]byte("|croft|")); err != nil {
				t.Fatal(err)
			}

			want := test.want + "|croft|"
			if got := string(readSubmission(t, slave, len(want))); got != want {
				t.Fatalf("PTY input = %q, want %q", got, want)
			}
		})
	}
}

// consumeDelayed models the relevant Codex paste rule at one consumer instant:
// raw text activates Enter suppression, suppressed CR adds a newline; explicit
// paste clears that state. It deliberately does not model rendering or modes.
func consumeDelayed(t *testing.T, wire []byte) ([]string, string) {
	t.Helper()

	var submitted []string

	var draft strings.Builder

	suppressed := false

	for len(wire) > 0 {
		if bytes.HasPrefix(wire, []byte("\x1b[200~")) {
			end := bytes.Index(wire[6:], []byte("\x1b[201~"))
			if end < 0 {
				t.Fatal("unterminated paste")
			}

			draft.Write(wire[6 : 6+end])
			wire = wire[12+end:]
			suppressed = false
		} else {
			b := wire[0]
			wire = wire[1:]

			if b == '\r' && !suppressed {
				submitted = append(submitted, draft.String())
				draft.Reset()
			} else {
				if b == '\r' {
					b = '\n'
				}

				draft.WriteByte(b)

				suppressed = true
			}
		}
	}

	return submitted, draft.String()
}

func TestSubmissionDelayedInboxConsumer(t *testing.T) {
	t.Parallel()

	for name, delay := range map[string]time.Duration{"old explicit delay": 50 * time.Millisecond, "current default": 150 * time.Millisecond} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			hints := []string{"New message from braw. Read: gr msg inbox --ack", "New message from canny. Read: gr msg inbox --ack"}
			s, slave := submissionPTY(t, pasteTerminal(true))
			s.SetInputDelay(delay)

			size := 0

			for _, hint := range hints {
				if err := s.WriteInputAndSubmit([]byte(hint)); err != nil {
					t.Fatal(err)
				}

				size += len(hint) + 13
			}

			wire := readSubmission(t, slave, size)

			submitted, draft := consumeDelayed(t, wire)
			if !slices.Equal(submitted, hints) || draft != "" {
				t.Fatalf("submitted %q, stranded draft %q; want each hint once", submitted, draft)
			}
			// Control proves the old text/sleep/CR stream strands both notifications.
			raw := []byte(strings.Join(hints, "\r") + "\r")

			submitted, draft = consumeDelayed(t, raw)
			if len(submitted) != 0 || draft != strings.Join(hints, "\n")+"\n" {
				t.Fatalf("raw control: submitted %q, draft %q", submitted, draft)
			}
		})
	}
}

func TestSubmissionConcurrentWriters(t *testing.T) {
	t.Parallel()
	s, slave := submissionPTY(t, pasteTerminal(true))

	var wg sync.WaitGroup
	for _, text := range []string{"braw", "canny"} {
		wg.Go(func() {
			if err := s.WriteInputAndSubmit([]byte(text)); err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()

	submitted, draft := consumeDelayed(t, readSubmission(t, slave, len("brawcanny")+26))
	slices.Sort(submitted)

	if !slices.Equal(submitted, []string{"braw", "canny"}) || draft != "" {
		t.Fatalf("interleaved or duplicated submissions: %q, draft %q", submitted, draft)
	}
}
