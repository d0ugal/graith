package pty

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	creackpty "github.com/creack/pty"
)

func closePTYTestResource(t *testing.T, closer io.Closer) {
	t.Helper()

	if err := closer.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Errorf("close test resource: %v", err)
	}
}

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
	defer closePTYTestResource(t, scrollback)

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

func TestAsyncScreenRecoveryPublishesOnlyCoherentRawGeneration(t *testing.T) {
	scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "strath.log"), 2*1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = scrollback.Close() })

	initial := []byte("canny initial output\n")
	if _, err := scrollback.Write(initial); err != nil {
		t.Fatal(err)
	}

	blocked := &blockedHydrationTerminal{
		entered: make(chan struct{}), release: make(chan struct{}), cols: 80, rows: 24,
	}
	final := &terminalChunkRecorder{}

	var factoryMu sync.Mutex

	factoryCalls := 0
	lastCols, lastRows := 0, 0
	session := &Session{
		ID: "thrawn-coherent-recovery", Scrollback: scrollback,
		screen: newUnavailableTerminal(80, 24), screenHydrationBytes: 2 * 1024 * 1024,
		screenInitializing: true, screenRecoveryPending: true,
		screenRecoveryCols: 80, screenRecoveryRows: 24,
		setSize: func(*os.File, *creackpty.Winsize) error { return nil },
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	t.Cleanup(session.Close)
	session.screenFactory = func(cols, rows int) (Terminal, error) {
		factoryMu.Lock()
		defer factoryMu.Unlock()

		factoryCalls++
		lastCols, lastRows = cols, rows

		switch factoryCalls {
		case 1:
			return blocked, nil
		case 2:
			return nil, errors.New("injected one-shot recovery failure")
		default:
			return final, nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- session.RecoverTerminalAfterUpgradeContext(ctx) }()

	select {
	case <-blocked.entered:
	case <-ctx.Done():
		t.Fatal("recovery hydration did not block")
	}

	marker := []byte("dreich marker during hydration\n")
	if _, err := scrollback.Write(marker); err != nil {
		t.Fatal(err)
	}

	session.mu.Lock()
	if err := session.writeScreenLocked(marker); err != nil {
		session.mu.Unlock()
		t.Fatal(err)
	}
	session.mu.Unlock()

	resizeDone := make(chan error, 1)
	go func() { resizeDone <- session.Resize(37, 91) }()

	select {
	case err := <-resizeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("resize entered a competing factory during hydration")
	}

	close(blocked.release)

	if err := <-recoveryDone; err != nil {
		t.Fatal(err)
	}

	factoryMu.Lock()
	gotCalls, gotCols, gotRows := factoryCalls, lastCols, lastRows
	factoryMu.Unlock()

	if gotCalls != 3 {
		t.Fatalf("factory calls = %d, want stale discard + one failure + success", gotCalls)
	}

	if gotCols != 91 || gotRows != 37 {
		t.Fatalf("published geometry = (%d, %d), want (91, 37)", gotCols, gotRows)
	}

	wantTail := append(append([]byte(nil), initial...), marker...)
	if got := bytes.Join(final.writes, nil); !bytes.Equal(got, wantTail) {
		t.Fatalf("published replay = %q, want coherent raw tail", got)
	}

	session.mu.RLock()
	initializing := session.screenInitializing
	pending := session.screenRecoveryPending
	session.mu.RUnlock()

	if initializing || pending {
		t.Fatalf("successful current generation remained pending: initializing=%v pending=%v", initializing, pending)
	}
}

func TestAsyncScreenRecoveryRequeuesAfterExhaustedStaleBatch(t *testing.T) {
	scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "bothy.log"), 2*1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = scrollback.Close() })

	initial := []byte("canny retained before recovery\r\n")
	if _, err := scrollback.Write(initial); err != nil {
		t.Fatal(err)
	}

	const invalidations = 4

	entered := make([]chan struct{}, invalidations)

	release := make([]chan struct{}, invalidations)
	for i := range invalidations {
		entered[i] = make(chan struct{})
		release[i] = make(chan struct{})
	}

	var factoryMu sync.Mutex

	factoryCalls := 0
	discardedClosed := 0

	var final *recordingRecoveryTerminal

	session := &Session{
		ID: "thrawn-eventual-recovery", Scrollback: scrollback,
		screen: newUnavailableTerminal(80, 24), screenHydrationBytes: 2 * 1024 * 1024,
		screenInitializing: true, screenRecoveryPending: true,
		screenRecoveryCols: 80, screenRecoveryRows: 24,
		setSize: func(*os.File, *creackpty.Winsize) error { return nil },
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	t.Cleanup(session.Close)
	session.screenFactory = func(cols, rows int) (Terminal, error) {
		factoryMu.Lock()
		defer factoryMu.Unlock()

		call := factoryCalls
		factoryCalls++

		if call >= invalidations {
			base, err := newTerminal(cols, rows)
			if err != nil {
				return nil, err
			}

			final = &recordingRecoveryTerminal{Terminal: base}

			return final, nil
		}

		return &coordinatedRecoveryTerminal{
			entered: entered[call], release: release[call],
			onClose: func() {
				factoryMu.Lock()
				discardedClosed++
				factoryMu.Unlock()
			},
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- session.RecoverTerminalAfterUpgradeContext(ctx) }()

	wantTail := append([]byte(nil), initial...)

	for i := range invalidations {
		select {
		case <-entered[i]:
		case <-ctx.Done():
			t.Fatalf("recovery attempt %d did not hydrate", i+1)
		}

		marker := []byte(fmt.Sprintf("dreich generation %d\r\n", i+1))

		wantTail = append(wantTail, marker...)
		if _, err := scrollback.Write(marker); err != nil {
			t.Fatal(err)
		}

		session.mu.Lock()
		if err := session.writeScreenLocked(marker); err != nil {
			session.mu.Unlock()
			t.Fatal(err)
		}
		session.mu.Unlock()

		if i == invalidations-1 {
			if err := session.Resize(41, 97); err != nil {
				t.Fatal(err)
			}
		}

		close(release[i])
	}

	if err := <-recoveryDone; err != nil {
		t.Fatal(err)
	}

	factoryMu.Lock()
	gotCalls, gotClosed, published := factoryCalls, discardedClosed, final
	factoryMu.Unlock()

	if gotCalls != invalidations+1 || gotClosed != invalidations {
		t.Fatalf("recovery candidates = (calls=%d, closed=%d), want (%d, %d)",
			gotCalls, gotClosed, invalidations+1, invalidations)
	}

	if got := published.bytes(); !bytes.Equal(got, wantTail) {
		t.Fatalf("eventual replay = %q, want exact retained tail", got)
	}

	session.mu.RLock()
	cols, rows := session.screen.Size()
	initializing := session.screenInitializing
	session.mu.RUnlock()

	if cols != 97 || rows != 41 {
		t.Fatalf("eventual recovery geometry = (%d, %d), want (97, 41)", cols, rows)
	}

	preview := session.ScreenPreview()
	if initializing || !strings.Contains(preview, "dreich generation 4") {
		t.Fatalf("quiescent generation did not publish an eventual preview: initializing=%v preview=%q", initializing, preview)
	}
}

func TestRecoveryBatchCancellationJoinsBoundedStages(t *testing.T) {
	largeTail := bytes.Repeat([]byte("canny"), terminalWriteChunkBytes/5+32*1024)
	newRecoveringSession := func(id string) *Session {
		t.Helper()

		scrollback, err := NewScrollback(filepath.Join(t.TempDir(), id+".log"), 2*1024*1024)
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { _ = scrollback.Close() })

		if _, err := scrollback.Write(largeTail); err != nil {
			t.Fatal(err)
		}

		return &Session{
			ID: id, Scrollback: scrollback,
			screen: newUnavailableTerminal(80, 24), screenHydrationBytes: len(largeTail),
			screenInitializing: true, screenRecoveryPending: true,
			screenRecoveryCols: 80, screenRecoveryRows: 24,
			log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
	}

	constructionStarted := make(chan struct{})
	constructionCandidate := newDelayedRecoveryTerminal(0)
	construction := newRecoveringSession("canny-bounded-construction")
	construction.screenFactory = func(_, _ int) (Terminal, error) {
		close(constructionStarted)
		time.Sleep(150 * time.Millisecond)

		return constructionCandidate, nil
	}
	hydrationCandidate := newDelayedRecoveryTerminal(150 * time.Millisecond)
	hydration := newRecoveringSession("dreich-bounded-hydration")
	hydration.screenFactory = func(_, _ int) (Terminal, error) { return hydrationCandidate, nil }

	ctx, cancel := context.WithCancel(context.Background())

	batchDone := make(chan []error, 1)
	go func() {
		batchDone <- RecoverTerminalSessionsAfterUpgrade(ctx, []*Session{construction, hydration})
	}()

	select {
	case <-constructionStarted:
	case <-time.After(time.Second):
		t.Fatal("bounded construction did not start")
	}

	select {
	case <-hydrationCandidate.entered:
	case <-time.After(time.Second):
		t.Fatal("bounded hydration did not start")
	}

	canceledAt := time.Now()

	cancel()

	var results []error
	select {
	case results = <-batchDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("canceled recovery batch exceeded the bounded in-flight operation")
	}

	if time.Since(canceledAt) > 450*time.Millisecond {
		t.Fatal("canceled recovery batch returned outside the operation bound")
	}

	for i, err := range results {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("recovery %d error = %v, want cancellation", i, err)
		}
	}

	for name, candidate := range map[string]*delayedRecoveryTerminal{
		"construction": constructionCandidate, "hydration": hydrationCandidate,
	} {
		if !candidate.isClosed() {
			t.Errorf("%s candidate was not joined and closed", name)
		}
	}

	if hydrationCandidate.writeCount() != 1 {
		t.Fatalf("hydration writes after cancellation = %d, want one in-flight chunk only", hydrationCandidate.writeCount())
	}

	lockCtx, lockCancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer lockCancel()

	construction.screenRecoveryMu.Lock()
	lockStarted := time.Now()
	err := construction.RecoverTerminalAfterUpgradeContext(lockCtx)
	construction.screenRecoveryMu.Unlock()

	if !errors.Is(err, context.DeadlineExceeded) || time.Since(lockStarted) > 150*time.Millisecond {
		t.Fatalf("recovery lock cancellation = %v after %v", err, time.Since(lockStarted))
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
			defer closePTYTestResource(t, scrollback)

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
	defer closePTYTestResource(t, scrollback)

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

func TestSessionObservationsDoNotWaitForTerminalWrite(t *testing.T) {
	ptmx, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = writer.Close() })

	scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "croft.log"), 1024)
	if err != nil {
		t.Fatal(err)
	}

	term := newBlockingSessionTerminal(false)
	term.blockWrite = true

	lastOutputAt := time.Now().Add(-time.Minute)
	session := &Session{
		ID: "canny-observations", Ptmx: ptmx, Scrollback: scrollback,
		screen: term, readDone: make(chan struct{}), log: slog.Default(),
	}
	session.exited.Store(true)
	session.lastOutputAt = lastOutputAt
	session.peakRSSBytes.Store(42)

	go session.readLoop()

	t.Cleanup(session.Close)
	t.Cleanup(func() { term.releaseOnce.Do(func() { close(term.release) }) })

	if _, err := writer.Write([]byte("dreich output")); err != nil {
		t.Fatal(err)
	}

	select {
	case <-term.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal write did not block")
	}

	type observations struct {
		exited       bool
		lastOutputAt time.Time
		peakRSSBytes int64
	}

	observed := make(chan observations, 1)
	go func() {
		observed <- observations{
			exited:       session.Exited(),
			lastOutputAt: session.LastOutputAt(),
			peakRSSBytes: session.PeakRSSBytes(),
		}
	}()

	select {
	case got := <-observed:
		if !got.exited || !got.lastOutputAt.Equal(lastOutputAt) || got.peakRSSBytes != 42 {
			t.Fatalf("observations = %+v, want exited with timestamp %v and peak RSS 42", got, lastOutputAt)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("session observations blocked behind terminal write")
	}
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
	defer closePTYTestResource(t, peer)

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

type blockedHydrationTerminal struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	cols    int
	rows    int
}

func (t *blockedHydrationTerminal) Write(p []byte) (int, error) {
	t.once.Do(func() { close(t.entered) })
	<-t.release

	return len(p), nil
}

func (t *blockedHydrationTerminal) Resize(cols, rows int) error {
	t.cols, t.rows = cols, rows

	return nil
}

func (t *blockedHydrationTerminal) Size() (int, int)         { return t.cols, t.rows }
func (t *blockedHydrationTerminal) Cursor() (int, int, bool) { return 0, 0, false }
func (t *blockedHydrationTerminal) Cell(_, _ int) Cell       { return Cell{Content: " "} }
func (t *blockedHydrationTerminal) Close() error             { return nil }

type coordinatedRecoveryTerminal struct {
	entered   chan struct{}
	release   chan struct{}
	onClose   func()
	enterOnce sync.Once
	closeOnce sync.Once
}

func (t *coordinatedRecoveryTerminal) Write(p []byte) (int, error) {
	t.enterOnce.Do(func() { close(t.entered) })
	<-t.release

	return len(p), nil
}
func (*coordinatedRecoveryTerminal) Resize(_, _ int) error    { return nil }
func (*coordinatedRecoveryTerminal) Size() (int, int)         { return 80, 24 }
func (*coordinatedRecoveryTerminal) Cursor() (int, int, bool) { return 0, 0, false }
func (*coordinatedRecoveryTerminal) Cell(_, _ int) Cell       { return Cell{Content: " "} }
func (t *coordinatedRecoveryTerminal) Close() error {
	t.closeOnce.Do(func() {
		if t.onClose != nil {
			t.onClose()
		}
	})

	return nil
}

type delayedRecoveryTerminal struct {
	mu        sync.Mutex
	entered   chan struct{}
	enterOnce sync.Once
	delay     time.Duration
	writes    int
	closed    bool
}

func newDelayedRecoveryTerminal(delay time.Duration) *delayedRecoveryTerminal {
	return &delayedRecoveryTerminal{entered: make(chan struct{}), delay: delay}
}

func (t *delayedRecoveryTerminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.writes++
	t.mu.Unlock()
	t.enterOnce.Do(func() { close(t.entered) })
	time.Sleep(t.delay)

	return len(p), nil
}
func (*delayedRecoveryTerminal) Resize(_, _ int) error    { return nil }
func (*delayedRecoveryTerminal) Size() (int, int)         { return 80, 24 }
func (*delayedRecoveryTerminal) Cursor() (int, int, bool) { return 0, 0, false }
func (*delayedRecoveryTerminal) Cell(_, _ int) Cell       { return Cell{Content: " "} }
func (t *delayedRecoveryTerminal) Close() error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()

	return nil
}
func (t *delayedRecoveryTerminal) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.closed
}
func (t *delayedRecoveryTerminal) writeCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.writes
}

type recordingRecoveryTerminal struct {
	Terminal

	mu     sync.Mutex
	writes []byte
}

func (t *recordingRecoveryTerminal) Write(p []byte) (int, error) {
	n, err := t.Terminal.Write(p)
	t.mu.Lock()
	t.writes = append(t.writes, p[:n]...)
	t.mu.Unlock()

	return n, err
}

func (t *recordingRecoveryTerminal) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()

	return append([]byte(nil), t.writes...)
}

type blockingSessionTerminal struct {
	blockResize bool
	blockWrite  bool
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

func (t *blockingSessionTerminal) Write(p []byte) (int, error) {
	if t.blockWrite {
		t.block()
	}

	return len(p), nil
}

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
