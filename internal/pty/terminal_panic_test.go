//go:build !libghostty || libghostty_compare

package pty

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	cvt "github.com/charmbracelet/x/vt"
)

// TestScrollUpDeleteLineAreaFixtureReproducesUpstreamPanic pins the reduced,
// synthetic sequence behind the production failure. An oversized DECSTBM
// region followed by SU reaches ultraviolet.Buffer.DeleteLineArea with a
// bottom edge beyond the 24-row buffer.
func TestScrollUpDeleteLineAreaFixtureReproducesUpstreamPanic(t *testing.T) {
	emu := cvt.NewEmulator(80, 24)
	defer func() { _ = emu.Close() }()

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("fixture no longer reproduces the upstream parser panic")
		}

		stack := debug.Stack()
		for _, frame := range [][]byte{[]byte("DeleteLineArea"), []byte("ScrollUp")} {
			if !bytes.Contains(stack, frame) {
				t.Errorf("panic stack missing %q; stack:\n%s", frame, stack)
			}
		}
	}()

	_, _ = emu.Write(terminalParserPanicFixture(t))
}

func TestCharmTerminalContainsParserPanic(t *testing.T) {
	term := newCharmTerminal(80, 24)

	t.Cleanup(func() { _ = term.Close() })

	n, err := term.Write(terminalParserPanicFixture(t))
	if err == nil {
		t.Fatal("Write returned nil error for parser panic")
	}

	if n != 0 {
		t.Errorf("Write count = %d, want 0 after parser panic", n)
	}

	if got := err.Error(); got != "terminal parser panic" {
		t.Errorf("Write error = %q, want sanitized parser failure", got)
	}
}

func TestCharmTerminalParserPanicDoesNotExposeRecoveredValue(t *testing.T) {
	term := newCharmTerminal(80, 24)

	t.Cleanup(func() { _ = term.Close() })

	const recoveredValue = "dreich parser payload"

	term.emu.RegisterCsiHandler('z', func(ansi.Params) bool {
		panic(recoveredValue)
	})

	_, err := term.Write([]byte("\x1b[z"))
	if err == nil {
		t.Fatal("Write returned nil error for parser panic")
	}

	if strings.Contains(err.Error(), recoveredValue) {
		t.Fatalf("Write error exposed recovered value: %q", err)
	}
}

func TestReadLoopContainsParserPanicAndResetsScreen(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "scrollback.log")

	sb, err := NewScrollback(logPath, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	var logBuf syncBuf

	s := &Session{
		ID:                   "canny-live",
		Ptmx:                 r,
		Scrollback:           sb,
		screen:               newCharmTerminal(80, 24),
		screenHydrationBytes: defaultScreenHydrationBytes,
		readDone:             make(chan struct{}),
		createdAt:            time.Now(),
		log:                  slog.New(slog.NewJSONHandler(&logBuf, nil)),
	}

	go s.readLoop()

	t.Cleanup(func() {
		_ = w.Close()
		s.Close()
	})

	fixture := terminalParserPanicFixture(t)
	if _, err := w.Write(fixture); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for s.BytesRead() < int64(len(fixture)) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	if got := s.BytesRead(); got != int64(len(fixture)) {
		t.Fatalf("bytes read after fixture = %d, want %d", got, len(fixture))
	}

	if _, err := w.Write([]byte("canny after reset")); err != nil {
		t.Fatal(err)
	}

	deadline = time.Now().Add(5 * time.Second)
	for !strings.Contains(s.ScreenPreview(), "canny after reset") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	if got := s.ScreenPreview(); !strings.Contains(got, "canny after reset") {
		t.Fatalf("screen did not recover after parser panic: %q", got)
	}

	var parserLog map[string]any

	for _, line := range bytes.Split(logBuf.Bytes(), []byte("\n")) {
		var record map[string]any
		if json.Unmarshal(line, &record) == nil && record["msg"] == "terminal parser failed; screen reset" {
			parserLog = record
			break
		}
	}

	if parserLog == nil {
		t.Fatalf("missing terminal parser failure log: %s", logBuf.Bytes())
	}

	if parserLog["session"] != "canny-live" {
		t.Errorf("parser failure session = %v, want canny-live", parserLog["session"])
	}

	if parserLog["error"] != "terminal parser panic" {
		t.Errorf("parser failure error = %v, want sanitized parser failure", parserLog["error"])
	}
}
