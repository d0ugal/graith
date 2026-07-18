//go:build libghostty && cgo && (darwin || linux)

package pty

import (
	"bytes"
	"testing"
)

func FuzzGhosttyHelperWrite(f *testing.F) {
	f.Add([]byte("braw canny\r\n"))
	f.Add([]byte("\x1b[1;38;5;208mbraw\x1b[0m"))
	f.Add([]byte("e\u0301 你 😀\r\n"))
	f.Add([]byte("\x1b[?1049hbothy\x1b[?1049l"))
	f.Add([]byte{0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x68, 0x1b, 0x5b, 0x7a, 0x00})
	// Reduced deterministic parser edge cases from the shared corpus.
	f.Add([]byte("\x1b[2;133r\x1b[1S"))
	f.Add([]byte("\x1b[999;999z\x1b]2;haar\x18canny"))
	f.Add([]byte("\x1bP1;2|thrawn\x18braw"))
	f.Add([]byte("e\u0301👩‍💻♥️🇬🇧你"))

	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 1024*1024 {
			t.Skip()
		}

		term, err := newGhosttyProcessTerminal(80, 24)
		if err != nil {
			t.Fatal(err)
		}
		defer term.Close()

		if _, err := term.Write(input); err != nil {
			// A helper exit is a contained native failure and therefore a useful
			// fuzz finding. Fatal preserves the generated input in the Go fuzz
			// cache without bringing down the parent test process.
			t.Fatal(err)
		}
		if _, err := term.Snapshot(); err != nil {
			t.Fatal(err)
		}
	})
}

func FuzzGhosttySnapshotDecoder(f *testing.F) {
	valid, err := encodeGhosttySnapshot(TerminalSnapshot{
		Cells: []Cell{{Content: "canny"}}, Cols: 1, Rows: 1,
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte("thrawn"))

	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 1024*1024 {
			t.Skip()
		}
		_, _ = decodeGhosttySnapshot(payload)
	})
}

func FuzzGhosttyRequestDecoder(f *testing.F) {
	f.Add(ghosttyTestRequest(ghosttyOpWrite, []byte("braw")))
	f.Add(ghosttyTestRequest(ghosttyOpResize, []byte{0, 80, 0, 24}))
	f.Add([]byte("dreich"))

	f.Fuzz(func(t *testing.T, frame []byte) {
		if len(frame) > ghosttyMaxRequestBytes+12 {
			t.Skip()
		}
		_, _, _ = readGhosttyRequest(bytes.NewReader(frame))
	})
}
