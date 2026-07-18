package pty

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	creackpty "github.com/creack/pty"
)

func TestNewSessionTerminalFailureDoesNotStartCommand(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is unavailable")
	}

	tempDir := t.TempDir()
	marker := filepath.Join(tempDir, "command-started")
	wantErr := errors.New("canny terminal construction failure")

	factory := func(_, _ int) (Terminal, error) {
		// Before the ordering fix, the user command was already live here. Give
		// that command ample time to expose its side effect so this regression is
		// deterministic rather than depending on scheduler luck.
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if _, statErr := os.Stat(marker); statErr == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}

		return nil, wantErr
	}

	session, err := newSessionWithTerminalFactory(SessionOpts{
		ID:      "thrawn-preflight",
		Command: shell,
		Args:    []string{"-c", `printf canny > "$1"`, "sh", marker},
		Dir:     tempDir,
		Rows:    24,
		Cols:    80,
		LogPath: filepath.Join(tempDir, "strath.log"),
	}, factory)
	if session != nil || !errors.Is(err, wantErr) {
		t.Fatalf("constructor result = (%v, %v), want nil terminal failure", session, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected command created its side-effect marker: %v", err)
	}
}

func TestQuiescedSessionRejectsNewInputWithoutBlocking(t *testing.T) {
	readDone := make(chan struct{})
	close(readDone)
	s := &Session{readDone: readDone}
	release, err := s.QuiesceIOForUpgrade(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	checks := []struct {
		name string
		fn   func() error
	}{
		{name: "write", fn: func() error { return s.WriteInput([]byte("canny")) }},
		{name: "submit", fn: func() error { return s.WriteInputAndSubmit([]byte("dreich")) }},
		{name: "interrupt", fn: func() error { return s.Interrupt(2, time.Second) }},
	}
	for _, check := range checks {
		result := make(chan error, 1)
		go func() { result <- check.fn() }()
		select {
		case err := <-result:
			if !errors.Is(err, errSessionIOQuiesced) {
				t.Errorf("%s error = %v, want quiesced refusal", check.name, err)
			}
		case <-time.After(250 * time.Millisecond):
			t.Errorf("%s blocked behind upgrade write lock", check.name)
		}
	}
}

func TestSessionCloseSerializesScreenOperations(t *testing.T) {
	operations := map[string]func(*Session){
		"preview":  func(s *Session) { _ = s.ScreenPreview() },
		"snapshot": func(s *Session) { _ = s.ScreenSnapshot() },
		"resize":   func(s *Session) { _ = s.Resize(25, 80) },
	}

	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			term := newBlockingSessionTerminal(name == "resize")
			scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "bothy.log"), 1024)
			if err != nil {
				t.Fatal(err)
			}

			ptmx, peer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = peer.Close() })

			readDone := make(chan struct{})
			close(readDone)
			session := &Session{
				ID:         "canny-close-race",
				Ptmx:       ptmx,
				Scrollback: scrollback,
				screen:     term,
				readDone:   readDone,
				log:        slog.Default(),
			}
			t.Cleanup(session.Close)

			t.Cleanup(func() { term.releaseOnce.Do(func() { close(term.release) }) })

			opDone := make(chan struct{})
			go func() {
				operation(session)
				close(opDone)
			}()

			select {
			case <-term.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("screen operation did not start")
			}

			closeStarted := make(chan struct{})
			closeDone := make(chan struct{})
			go func() {
				close(closeStarted)
				session.Close()
				close(closeDone)
			}()
			<-closeStarted

			select {
			case <-term.closeCalled:
				t.Fatal("Close overtook an in-flight screen operation")
			case <-time.After(100 * time.Millisecond):
			}

			term.releaseOnce.Do(func() { close(term.release) })
			select {
			case <-opDone:
			case <-time.After(5 * time.Second):
				t.Fatal("screen operation did not finish")
			}
			select {
			case <-closeDone:
			case <-time.After(5 * time.Second):
				t.Fatal("Close did not finish")
			}

			if got := session.ScreenPreview(); got != "" {
				t.Fatalf("preview after close = %q, want empty", got)
			}
			if err := session.Resize(25, 80); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("resize after close = %v, want os.ErrClosed", err)
			}
		})
	}
}

func TestTerminalFailureDuringFreezeReplaysAfterThaw(t *testing.T) {
	scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "croft.log"), 2*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer scrollback.Close()
	payload := []byte("canny raw output")
	if _, err := scrollback.Write(payload); err != nil {
		t.Fatal(err)
	}

	frozen := true
	replacement := &terminalChunkRecorder{}
	session := &Session{
		ID: "thrawn-freeze", Scrollback: scrollback,
		screen: &failingSessionTerminal{}, screenHydrationBytes: len(payload),
		log: slog.Default(),
	}
	session.screenFactory = func(_, _ int) (Terminal, error) {
		if frozen {
			return nil, errTerminalGenerationFrozen
		}

		return replacement, nil
	}

	session.mu.Lock()
	err = session.writeScreenLocked([]byte("dreich"))
	pending := session.screenRecoveryPending
	session.mu.Unlock()
	if err == nil || !errors.Is(err, errTerminalGenerationFrozen) || !pending {
		t.Fatalf("frozen write result = (%v, pending=%v)", err, pending)
	}
	frozen = false
	if err := session.RecoverTerminalAfterUpgrade(); err != nil {
		t.Fatal(err)
	}
	if got := bytes.Join(replacement.writes, nil); !bytes.Equal(got, payload) {
		t.Fatalf("replayed bytes = %q, want %q", got, payload)
	}
}

func TestFrozenScreenOperationsRecordRecoveryGeometry(t *testing.T) {
	tests := []struct {
		name               string
		operation          func(*Session)
		wantCols, wantRows int
	}{
		{name: "preview", operation: func(s *Session) { _ = s.ScreenPreview() }, wantCols: 80, wantRows: 24},
		{name: "snapshot", operation: func(s *Session) { _ = s.ScreenSnapshot() }, wantCols: 80, wantRows: 24},
		{name: "resize_then_reads", operation: func(s *Session) {
			_ = s.Resize(37, 91)
			_ = s.ScreenPreview()
			_ = s.ScreenSnapshot()
		}, wantCols: 91, wantRows: 37},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "croft.log"), 1024)
			if err != nil {
				t.Fatal(err)
			}
			defer scrollback.Close()
			if _, err := scrollback.Write([]byte("canny recovery")); err != nil {
				t.Fatal(err)
			}

			frozen := true
			gotCols, gotRows := 0, 0
			replacement := &terminalChunkRecorder{}
			session := &Session{
				ID: "thrawn-" + tt.name, Scrollback: scrollback,
				screen: &failingRecoveryTerminal{}, screenHydrationBytes: 1024,
				log:     slog.Default(),
				setSize: func(*os.File, *creackpty.Winsize) error { return nil },
			}
			session.screenFactory = func(cols, rows int) (Terminal, error) {
				if frozen {
					return nil, errTerminalGenerationFrozen
				}
				gotCols, gotRows = cols, rows

				return replacement, nil
			}

			tt.operation(session)
			if !session.screenRecoveryPending || session.screenRecoveryCols != tt.wantCols ||
				session.screenRecoveryRows != tt.wantRows {
				t.Fatalf("pending recovery geometry = (%v, %d, %d), want (true, %d, %d)",
					session.screenRecoveryPending, session.screenRecoveryCols, session.screenRecoveryRows,
					tt.wantCols, tt.wantRows)
			}
			frozen = false
			if err := session.RecoverTerminalAfterUpgrade(); err != nil {
				t.Fatal(err)
			}
			if gotCols != tt.wantCols || gotRows != tt.wantRows {
				t.Fatalf("recovery geometry = (%d, %d), want (%d, %d)", gotCols, gotRows, tt.wantCols, tt.wantRows)
			}
		})
	}
}

func TestScreenShortWriteTriggersRecovery(t *testing.T) {
	scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "bothy.log"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer scrollback.Close()
	payload := []byte("canny durable output")
	if _, err := scrollback.Write(payload); err != nil {
		t.Fatal(err)
	}
	replacement := &terminalChunkRecorder{}
	session := &Session{
		ID: "thrawn-short-write", Scrollback: scrollback,
		screen: &shortWriteSessionTerminal{}, screenHydrationBytes: 1024,
		screenFactory: func(_, _ int) (Terminal, error) { return replacement, nil },
		log:           slog.Default(),
	}
	session.mu.Lock()
	err = session.writeScreenLocked([]byte("dreich"))
	session.mu.Unlock()
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short screen write error = %v", err)
	}
	if session.screen != replacement {
		t.Fatal("short screen write did not install a reconstructed terminal")
	}
}

func TestReadLoopSerializesAppendAndScreenApplication(t *testing.T) {
	ptmx, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "croft.log"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &terminalChunkRecorder{}
	appendReached := make(chan struct{})
	releaseAppend := make(chan struct{})
	var hookOnce sync.Once
	session := &Session{
		ID: "canny-append-order", Ptmx: ptmx, Scrollback: scrollback,
		screen: recorder, readDone: make(chan struct{}), log: slog.Default(),
		afterScrollbackAppend: func() {
			hookOnce.Do(func() {
				close(appendReached)
				<-releaseAppend
			})
		},
	}
	go session.readLoop()
	payload := []byte("canny once")
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	<-appendReached
	previewDone := make(chan struct{})
	go func() {
		_ = session.ScreenPreview()
		close(previewDone)
	}()
	select {
	case <-previewDone:
		t.Fatal("preview interleaved after scrollback append but before screen application")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseAppend)
	<-previewDone
	_ = writer.Close()
	<-session.readDone
	if got := bytes.Join(recorder.writes, nil); !bytes.Equal(got, payload) {
		t.Fatalf("screen writes = %q, want one application of %q", got, payload)
	}
	_ = ptmx.Close()
	_ = scrollback.Close()
}

func TestQuiesceRefusesAfterScrollbackAppendFailure(t *testing.T) {
	ptmx, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "dreich.log"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := scrollback.Close(); err != nil {
		t.Fatal(err)
	}
	appendAttempted := make(chan struct{})
	var once sync.Once
	session := &Session{
		ID: "canny-scrollback-failure", Ptmx: ptmx, Scrollback: scrollback,
		screen: &terminalChunkRecorder{}, readDone: make(chan struct{}), log: slog.Default(),
		afterScrollbackAppend: func() { once.Do(func() { close(appendAttempted) }) },
	}
	go session.readLoop()
	if _, err := writer.Write([]byte("thrawn raw bytes")); err != nil {
		t.Fatal(err)
	}
	<-appendAttempted
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if release, err := session.QuiesceIOForUpgrade(ctx); err == nil {
		release()
		t.Fatal("quiesce accepted a session with failed authoritative append")
	}
	_ = writer.Close()
	select {
	case <-session.readDone:
	case <-time.After(time.Second):
		t.Fatal("read loop did not stop after writer closed")
	}
	_ = ptmx.Close()
}

func TestSessionResizeOwnsPTYThroughSetSize(t *testing.T) {
	ptmx, peer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	readDone := make(chan struct{})
	close(readDone)
	entered := make(chan struct{})
	release := make(chan struct{})
	session := &Session{
		ID: "canny-resize-owner", Ptmx: ptmx, screen: &terminalChunkRecorder{},
		readDone: readDone, log: slog.Default(),
		setSize: func(file *os.File, _ *creackpty.Winsize) error {
			if file != ptmx {
				t.Errorf("setSize file = %p, want owned PTY %p", file, ptmx)
			}
			close(entered)
			<-release

			return nil
		},
	}
	resizeDone := make(chan struct{})
	go func() {
		_ = session.Resize(25, 80)
		close(resizeDone)
	}()
	<-entered
	closeDone := make(chan struct{})
	go func() {
		session.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("Close released the PTY while setSize was in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-resizeDone
	<-closeDone
}

type failingSessionTerminal struct{}

func (*failingSessionTerminal) Write([]byte) (int, error) { return 0, errors.New("terminal failed") }
func (*failingSessionTerminal) Resize(_, _ int) error     { return nil }
func (*failingSessionTerminal) Size() (int, int)          { return 80, 24 }
func (*failingSessionTerminal) Cursor() (int, int, bool)  { return 0, 0, false }
func (*failingSessionTerminal) Cell(_, _ int) Cell        { return Cell{Content: " "} }
func (*failingSessionTerminal) Close() error              { return nil }

type failingRecoveryTerminal struct{ failingSessionTerminal }

func (*failingRecoveryTerminal) Resize(_, _ int) error { return errors.New("terminal failed") }
func (*failingRecoveryTerminal) Snapshot() (TerminalSnapshot, error) {
	return TerminalSnapshot{}, errors.New("terminal failed")
}

type shortWriteSessionTerminal struct{ failingSessionTerminal }

func (*shortWriteSessionTerminal) Write(p []byte) (int, error) { return len(p) - 1, nil }

type blockingSessionTerminal struct {
	blockResize bool
	entered     chan struct{}
	release     chan struct{}
	closeCalled chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
	closeOnce   sync.Once
}

func newBlockingSessionTerminal(blockResize bool) *blockingSessionTerminal {
	return &blockingSessionTerminal{
		blockResize: blockResize,
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
		closeCalled: make(chan struct{}),
	}
}

func (t *blockingSessionTerminal) block() {
	t.enterOnce.Do(func() { close(t.entered) })
	<-t.release
}

func (t *blockingSessionTerminal) Write(p []byte) (int, error) { return len(p), nil }

func (t *blockingSessionTerminal) Resize(_, _ int) error {
	if t.blockResize {
		t.block()
	}

	return nil
}

func (t *blockingSessionTerminal) Size() (int, int) { return 1, 1 }

func (t *blockingSessionTerminal) Cursor() (int, int, bool) { return 0, 0, false }

func (t *blockingSessionTerminal) Cell(_, _ int) Cell { return Cell{Content: " "} }

func (t *blockingSessionTerminal) Snapshot() (TerminalSnapshot, error) {
	if !t.blockResize {
		t.block()
	}

	return TerminalSnapshot{Cells: []Cell{{Content: " "}}, Cols: 1, Rows: 1}, nil
}

func (t *blockingSessionTerminal) Close() error {
	t.closeOnce.Do(func() { close(t.closeCalled) })

	return nil
}
