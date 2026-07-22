package pty

import (
	"errors"
	"fmt"
)

// RunNativeTerminalSelfTest exercises the native backend through the exact
// process-isolated path used by the daemon. Release validation invokes this on
// the unpacked executable so architecture, helper startup, FFI calls, terminal
// mutation, snapshot decoding, resize, close, and child reaping are all proven
// against the final bytes rather than a separate test binary.
func RunNativeTerminalSelfTest() (returnErr error) {
	if TerminalBackend() != TerminalBackendLibghosttyHelper {
		return errors.New("native terminal backend is not selected")
	}

	term, err := newTerminal(20, 3)
	if err != nil {
		return fmt.Errorf("create native terminal: %w", err)
	}

	closed := false
	defer func() {
		if closed {
			return
		}

		if err := term.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("close native terminal: %w", err)
		}
	}()

	input := []byte("braw e\u0301 \u4f60")
	if written, err := term.Write(input); err != nil {
		return fmt.Errorf("write native terminal: %w", err)
	} else if written != len(input) {
		return fmt.Errorf("write native terminal: wrote %d bytes, want %d", written, len(input))
	}

	snapshot, err := snapshotTerminal(term)
	if err != nil {
		return fmt.Errorf("snapshot native terminal: %w", err)
	}

	if snapshot.Cols != 20 || snapshot.Rows != 3 || len(snapshot.Cells) != 60 {
		return errors.New("snapshot native terminal: unexpected geometry")
	}

	if snapshot.Cells[0].Content != "b" || snapshot.Cells[5].Content != "e\u0301" {
		return errors.New("snapshot native terminal: unexpected cell content")
	}

	if err := term.Resize(30, 4); err != nil {
		return fmt.Errorf("resize native terminal: %w", err)
	}

	resized, err := snapshotTerminal(term)
	if err != nil {
		return fmt.Errorf("snapshot resized native terminal: %w", err)
	}

	if resized.Cols != 30 || resized.Rows != 4 || len(resized.Cells) != 120 {
		return errors.New("snapshot resized native terminal: unexpected geometry")
	}

	if err := term.Close(); err != nil {
		return fmt.Errorf("close native terminal: %w", err)
	}

	closed = true

	return nil
}
