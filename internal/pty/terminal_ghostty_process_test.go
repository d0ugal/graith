//go:build libghostty && cgo && (darwin || linux)

package pty

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestGhosttySnapshotProtocolRoundTrip(t *testing.T) {
	want := TerminalSnapshot{
		Cells: []Cell{
			{Content: "e\u0301", Style: CellStyle{
				FG:   Color{Kind: ColorIndexed, Value: 208},
				BG:   Color{Kind: ColorRGB, Value: 0x0a141e},
				Bold: true, Faint: true, Italic: true, Underline: true,
				Blink: true, Reverse: true, Strikethrough: true,
			}},
			{Content: ""},
		},
		CursorX:       1,
		CursorY:       0,
		CursorVisible: true,
		Cols:          2,
		Rows:          1,
	}

	payload, err := encodeGhosttySnapshot(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeGhosttySnapshot(payload)
	if err != nil {
		t.Fatal(err)
	}

	if got.Cols != want.Cols || got.Rows != want.Rows ||
		got.CursorX != want.CursorX || got.CursorY != want.CursorY ||
		got.CursorVisible != want.CursorVisible {
		t.Fatalf("snapshot metadata = %+v, want %+v", got, want)
	}
	if len(got.Cells) != len(want.Cells) {
		t.Fatalf("snapshot cells = %d, want %d", len(got.Cells), len(want.Cells))
	}
	for i := range want.Cells {
		if got.Cells[i] != want.Cells[i] {
			t.Errorf("cell %d = %+v, want %+v", i, got.Cells[i], want.Cells[i])
		}
	}
}

func TestGhosttySnapshotProtocolRejectsMalformedFrames(t *testing.T) {
	valid, err := encodeGhosttySnapshot(TerminalSnapshot{
		Cells: []Cell{{Content: "braw"}}, Cols: 1, Rows: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string][]byte{
		"empty":               nil,
		"truncated fixed":     valid[:12],
		"cell count":          append([]byte(nil), valid...),
		"content length":      append([]byte(nil), valid...),
		"trailing payload":    append(append([]byte(nil), valid...), 0),
		"invalid color kind":  append([]byte(nil), valid...),
		"invalid cursor bool": append([]byte(nil), valid...),
		"unknown style flag":  append([]byte(nil), valid...),
		"invalid utf8":        append([]byte(nil), valid...),
		"cursor outside grid": append([]byte(nil), valid...),
		"indexed color range": append([]byte(nil), valid...),
		"rgb color range":     append([]byte(nil), valid...),
		"default color value": append([]byte(nil), valid...),
	}
	tests["cell count"][12] = 2
	tests["content length"][13] = 0xff
	tests["invalid color kind"][17] = 0xff
	tests["invalid cursor bool"][8] = 2
	tests["unknown style flag"][19] = 0x80
	tests["invalid utf8"][len(valid)-1] = 0xff
	tests["cursor outside grid"][5] = 1
	tests["indexed color range"][17] = byte(ColorIndexed)
	binary.BigEndian.PutUint32(tests["indexed color range"][21:25], 256)
	tests["rgb color range"][18] = byte(ColorRGB)
	binary.BigEndian.PutUint32(tests["rgb color range"][25:29], 0x01000000)
	tests["default color value"][24] = 1

	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeGhosttySnapshot(payload); !errors.Is(err, errGhosttyHelperProtocol) {
				t.Fatalf("decode error = %v, want protocol violation", err)
			}
		})
	}
}

func TestGhosttySnapshotEncoderBoundsCellContent(t *testing.T) {
	_, err := encodeGhosttySnapshot(TerminalSnapshot{
		Cells: []Cell{{Content: strings.Repeat("a", ghosttyMaxCellContentBytes+1)}},
		Cols:  1,
		Rows:  1,
	})
	if !errors.Is(err, errGhosttyHelperProtocol) {
		t.Fatalf("encode error = %v, want protocol violation", err)
	}
}

func TestGhosttyRequestProtocolRejectsMalformedFramesBeforeAllocation(t *testing.T) {
	valid := ghosttyTestRequest(ghosttyOpWrite, []byte("braw"))

	tests := map[string][]byte{
		"truncated header":  valid[:11],
		"bad magic":         append([]byte(nil), valid...),
		"bad version":       append([]byte(nil), valid...),
		"reserved byte":     append([]byte(nil), valid...),
		"unknown operation": append([]byte(nil), valid...),
		"wrong resize size": ghosttyTestRequest(ghosttyOpResize, []byte{1}),
		"truncated payload": valid[:len(valid)-1],
		"oversized payload": ghosttyTestRequestHeader(ghosttyOpWrite, ghosttyMaxRequestBytes+1),
	}
	tests["bad magic"][0] = 'X'
	tests["bad version"][4]++
	tests["reserved byte"][6] = 1
	tests["unknown operation"][5] = 0xff

	for name, frame := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := readGhosttyRequest(bytes.NewReader(frame)); err == nil {
				t.Fatal("malformed request returned nil error")
			}
		})
	}
}

func TestGhosttyExchangeRejectsMalformedReplies(t *testing.T) {
	valid := ghosttyTestReply(ghosttyOpWrite, ghosttyStatusOK, nil)
	tests := map[string][]byte{
		"truncated header":      valid[:11],
		"bad magic":             append([]byte(nil), valid...),
		"bad version":           append([]byte(nil), valid...),
		"wrong operation":       append([]byte(nil), valid...),
		"reserved byte":         append([]byte(nil), valid...),
		"unknown status":        append([]byte(nil), valid...),
		"invalid status":        ghosttyTestReply(ghosttyOpWrite, ghosttyStatusInvalid, nil),
		"protocol status":       ghosttyTestReply(ghosttyOpWrite, ghosttyStatusProtocol, nil),
		"ok unexpected payload": ghosttyTestReply(ghosttyOpWrite, ghosttyStatusOK, []byte("canny")),
		"error with payload":    ghosttyTestReply(ghosttyOpWrite, ghosttyStatusNative, []byte("dreich")),
		"truncated payload":     ghosttyTestReply(ghosttyOpWrite, ghosttyStatusOK, nil),
		"oversized payload":     ghosttyTestReplyHeader(ghosttyOpWrite, ghosttyStatusOK, ghosttyMaxReplyBytes+1),
	}
	tests["bad magic"][0] = 'X'
	tests["bad version"][4]++
	tests["wrong operation"][5] = ghosttyOpResize
	tests["reserved byte"][7] = 1
	tests["unknown status"][6] = 0xff
	tests["truncated payload"] = ghosttyTestReplyHeader(ghosttyOpWrite, ghosttyStatusOK, 1)

	for name, reply := range tests {
		t.Run(name, func(t *testing.T) {
			err := ghosttyRunScriptedExchange(t, ghosttyOpWrite, []byte("braw"), func(conn net.Conn) {
				_, _ = conn.Write(reply)
			})
			if err == nil {
				t.Fatal("malformed reply returned nil error")
			}
		})
	}

	t.Run("snapshot ok without payload", func(t *testing.T) {
		err := ghosttyRunScriptedExchange(t, ghosttyOpSnapshot, nil, func(conn net.Conn) {
			_, _ = conn.Write(ghosttyTestReply(ghosttyOpSnapshot, ghosttyStatusOK, nil))
		})
		if !errors.Is(err, errGhosttyHelperProtocol) {
			t.Fatalf("snapshot error = %v, want protocol violation", err)
		}
	})

	t.Run("truncated snapshot payload", func(t *testing.T) {
		err := ghosttyRunScriptedExchange(t, ghosttyOpSnapshot, nil, func(conn net.Conn) {
			frame := append(
				ghosttyTestReplyHeader(ghosttyOpSnapshot, ghosttyStatusOK, 29),
				[]byte("short")...,
			)
			_, _ = conn.Write(frame)
		})
		if !errors.Is(err, errGhosttyHelperIO) {
			t.Fatalf("snapshot error = %v, want truncated communication failure", err)
		}
	})
}

func TestGhosttyHelperExitDuringEveryOperation(t *testing.T) {
	operations := map[string]struct {
		op      byte
		payload []byte
	}{
		"create":   {op: ghosttyOpCreate, payload: []byte{0, 20, 0, 3}},
		"write":    {op: ghosttyOpWrite, payload: []byte("braw")},
		"resize":   {op: ghosttyOpResize, payload: []byte{0, 30, 0, 4}},
		"snapshot": {op: ghosttyOpSnapshot},
		"close":    {op: ghosttyOpClose},
	}

	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			err := ghosttyRunScriptedExchange(t, operation.op, operation.payload, func(conn net.Conn) {
				_ = conn.Close()
			})
			if !errors.Is(err, errGhosttyHelperIO) {
				t.Fatalf("exchange error = %v, want communication failure", err)
			}
		})
	}
}

func TestGhosttyExchangeTimeoutPoisonsConnection(t *testing.T) {
	started := time.Now()
	err := ghosttyRunScriptedExchangeWithTimeout(
		t,
		ghosttyOpWrite,
		[]byte("braw"),
		25*time.Millisecond,
		func(conn net.Conn) {
			var one [1]byte
			_, _ = conn.Read(one[:])
		},
	)
	if !errors.Is(err, errGhosttyHelperTimeout) {
		t.Fatalf("exchange error = %v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout took %v, want bounded failure", elapsed)
	}
}

func TestGhosttyNativeFailureIsClassifiedAndPrivate(t *testing.T) {
	terminalData := "braw-terminal-secret credential=dreich /private/croft/session.log"
	err := ghosttyRunScriptedExchange(t, ghosttyOpWrite, []byte(terminalData), func(conn net.Conn) {
		_, _ = conn.Write(ghosttyTestReply(ghosttyOpWrite, ghosttyStatusNative, nil))
	})
	if !errors.Is(err, errGhosttyHelperNative) {
		t.Fatalf("exchange error = %v, want native failure", err)
	}
	if strings.Contains(err.Error(), terminalData) {
		t.Fatalf("native error exposed terminal bytes: %q", err)
	}
}

func TestGhosttyChildEnvironmentIsAllowlisted(t *testing.T) {
	t.Setenv("GRAITH_SECRET_CREDENTIAL", "dreich-secret")
	t.Setenv("HOME", "/private/croft")
	t.Setenv("DYLD_INSERT_LIBRARIES", "/private/bothy.dylib")
	t.Setenv("ASAN_OPTIONS", "abort_on_error=1")
	t.Setenv("GORACE", "halt_on_error=1")

	env := ghosttyChildEnvironment()
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"GRAITH_SECRET_CREDENTIAL", "dreich-secret", "HOME=", "/private/croft", "DYLD_"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("helper environment contains %q: %q", forbidden, joined)
		}
	}
	for _, required := range []string{ghosttyHelperEnv + "=1", "ASAN_OPTIONS=abort_on_error=1", "GORACE=halt_on_error=1"} {
		if !slices.Contains(env, required) {
			t.Errorf("helper environment missing %q: %q", required, env)
		}
	}
}

func TestGhosttySocketpairIsCloseOnExec(t *testing.T) {
	fds, err := ghosttySocketpair()
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])

	for _, fd := range fds {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			t.Fatal(err)
		}
		if flags&unix.FD_CLOEXEC == 0 {
			t.Errorf("socket fd %d is inheritable", fd)
		}
	}
}

func TestGhosttyProcessLimiterBoundsAndReleases(t *testing.T) {
	limiter := newGhosttyProcessLimiter(2)
	if !limiter.acquire() || !limiter.acquire() {
		t.Fatal("limiter rejected an available slot")
	}
	if limiter.acquire() {
		t.Fatal("limiter exceeded its process bound")
	}
	limiter.release()
	if !limiter.acquire() {
		t.Fatal("limiter did not reuse a released slot")
	}
	limiter.release()
	limiter.release()
}

func TestGhosttyHelperExecutesPinnedSelfImage(t *testing.T) {
	sourcePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "graith-pinned")
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	term, err := newGhosttyProcessTerminalWithConfig(20, 3, ghosttyProcessConfig{
		executable: targetPath,
		limiter:    newGhosttyProcessLimiter(1),
		onExecutablePinned: func() {
			if err := os.Remove(targetPath); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(targetPath, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
	})
	if err != nil {
		t.Fatalf("helper did not execute the pinned pre-replacement inode: %v", err)
	}
	defer term.Close()
	reply, err := term.exchange(ghosttyOpPinProbe, nil)
	if err != nil || !bytes.Equal(reply, []byte{1}) {
		t.Fatalf("helper pinned-FD bootstrap = (%v, %v), want closed", reply, err)
	}
}

func TestGhosttyPinProbeReplyWriterBounds(t *testing.T) {
	for name, payload := range map[string][]byte{
		"empty": nil,
		"large": {0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := writeGhosttyReply(io.Discard, ghosttyOpPinProbe, ghosttyStatusOK, payload); !errors.Is(err, errGhosttyHelperProtocol) {
				t.Fatalf("write pin probe reply error = %v, want protocol violation", err)
			}
		})
	}
	var encoded bytes.Buffer
	if err := writeGhosttyReply(&encoded, ghosttyOpPinProbe, ghosttyStatusOK, []byte{1}); err != nil {
		t.Fatalf("write valid pin probe reply: %v", err)
	}
	if got := encoded.Len(); got != 13 {
		t.Fatalf("encoded pin probe reply = %d bytes, want 13", got)
	}
}

func TestGhosttyLimiterExhaustionAndLifecycleRelease(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("exhaustion is immediate", func(t *testing.T) {
		limiter := newGhosttyProcessLimiter(1)
		if !limiter.acquire() {
			t.Fatal("reserve limiter slot")
		}
		defer limiter.release()

		started := time.Now()
		_, err := newGhosttyProcessTerminalWithConfig(20, 3, ghosttyProcessConfig{
			executable: executable,
			limiter:    limiter,
		})
		if !errors.Is(err, errGhosttyHelperLimit) {
			t.Fatalf("constructor error = %v, want resource limit", err)
		}
		if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
			t.Fatalf("limiter exhaustion blocked for %v", elapsed)
		}
	})

	t.Run("close reaps and releases", func(t *testing.T) {
		limiter := newGhosttyProcessLimiter(1)
		term, err := newGhosttyProcessTerminalWithConfig(20, 3, ghosttyProcessConfig{
			executable: executable,
			limiter:    limiter,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := term.Close(); err != nil {
			t.Fatal(err)
		}
		if term.cmd.ProcessState == nil {
			t.Fatal("close did not reap helper")
		}
		if !limiter.acquire() {
			t.Fatal("close did not release helper slot")
		}
		limiter.release()
	})

	t.Run("failed construction reaps and releases", func(t *testing.T) {
		falsePath, err := exec.LookPath("false")
		if err != nil {
			t.Skip("false executable unavailable")
		}
		limiter := newGhosttyProcessLimiter(1)
		var started *exec.Cmd
		term, err := newGhosttyProcessTerminalWithConfig(20, 3, ghosttyProcessConfig{
			executable: falsePath,
			limiter:    limiter,
			onStart: func(cmd *exec.Cmd) {
				started = cmd
			},
		})
		if term != nil || !errors.Is(err, errGhosttyHelperIO) {
			t.Fatalf("constructor result = (%v, %v), want nil communication failure", term, err)
		}
		if started == nil || started.ProcessState == nil {
			t.Fatal("failed constructor did not reap started helper")
		}
		if !limiter.acquire() {
			t.Fatal("failed constructor did not release helper slot")
		}
		limiter.release()
	})
}

func TestGhosttyLimiterExhaustionPreventsSessionCommandLaunch(t *testing.T) {
	held := 0
	for ghosttyHelperLimiter.acquire() {
		held++
	}
	defer func() {
		for range held {
			ghosttyHelperLimiter.release()
		}
	}()

	tempDir := t.TempDir()
	marker := filepath.Join(tempDir, "command-started")
	touch, err := exec.LookPath("touch")
	if err != nil {
		t.Skip("touch is unavailable")
	}

	session, err := NewSession(SessionOpts{
		ID:      "dreich-limit-preflight",
		Command: touch,
		Args:    []string{marker},
		Dir:     tempDir,
		Rows:    24,
		Cols:    80,
		LogPath: filepath.Join(tempDir, "croft.log"),
	})
	if session != nil || !errors.Is(err, errGhosttyHelperLimit) {
		t.Fatalf("constructor result = (%v, %v), want nil helper limit", session, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("limiter-rejected command created its side-effect marker: %v", err)
	}
}

func TestGhosttyHelperResourceLimits(t *testing.T) {
	if os.Getenv("GRAITH_TEST_HELPER_RLIMITS") == "1" {
		if err := hardenGhosttyHelperResources(); err != nil {
			t.Fatal(err)
		}
		var core, files unix.Rlimit
		if err := unix.Getrlimit(unix.RLIMIT_CORE, &core); err != nil {
			t.Fatal(err)
		}
		if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &files); err != nil {
			t.Fatal(err)
		}
		if core.Cur != 0 || core.Max != 0 || files.Cur > ghosttyHelperFDLimit || files.Max > ghosttyHelperFDLimit {
			t.Fatalf("limits core=%+v files=%+v", core, files)
		}

		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestGhosttyHelperResourceLimits$")
	cmd.Env = append(os.Environ(), "GRAITH_TEST_HELPER_RLIMITS=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("resource-limit helper: %v: %s", err, output)
	}
}

func TestGhosttyProcessTerminalLifecycle(t *testing.T) {
	term, err := newGhosttyProcessTerminal(20, 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = term.Close() })

	if term.cmd == nil || term.cmd.Process == nil || term.cmd.Process.Pid == 0 {
		t.Fatal("helper process was not started")
	}
	if _, err := term.Write([]byte("braw e\u0301 你")); err != nil {
		t.Fatal(err)
	}

	snapshot, err := term.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Cells[5].Content; got != "e\u0301" {
		t.Errorf("combined grapheme = %q, want %q", got, "e\u0301")
	}
	if err := term.Resize(30, 4); err != nil {
		t.Fatal(err)
	}
	if term.cache.Cells != nil {
		t.Fatal("resize retained a stale viewport cache")
	}
	if cols, rows := term.Size(); cols != 30 || rows != 4 {
		t.Errorf("size = (%d,%d), want (30,4)", cols, rows)
	}

	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	if err := term.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if term.cmd.ProcessState == nil {
		t.Fatal("close returned before the helper was reaped")
	}
	if _, err := term.Write([]byte("thrawn")); !errors.Is(err, errGhosttyHelperClosed) {
		t.Fatalf("write after close = %v, want helper closed", err)
	}
}

func TestGhosttyProcessTerminalConcurrentClose(t *testing.T) {
	term, err := newGhosttyProcessTerminal(20, 3)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- term.Close()
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent close: %v", err)
		}
	}
	if term.cmd.ProcessState == nil {
		t.Fatal("concurrent close returned before helper reaping")
	}
}

func TestGhosttyWriteRequestLimitDoesNotPoisonHelper(t *testing.T) {
	term, err := newGhosttyProcessTerminal(20, 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = term.Close() })

	if _, err := term.Write(make([]byte, ghosttyMaxRequestBytes+1)); !errors.Is(err, errGhosttyHelperLimit) {
		t.Fatalf("oversized write error = %v, want resource limit", err)
	}
	if _, err := term.Write([]byte("braw after rejected write")); err != nil {
		t.Fatalf("helper poisoned by local request rejection: %v", err)
	}
}

func TestGhosttyHelperCrashReconstructsFromScrollback(t *testing.T) {
	scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "bothy.log"), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	term, err := newGhosttyProcessTerminal(40, 4)
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{
		ID:                   "canny-helper",
		Scrollback:           scrollback,
		screen:               term,
		screenHydrationBytes: defaultScreenHydrationBytes,
		log:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	t.Cleanup(func() {
		_ = session.screen.Close()
		_ = scrollback.Close()
	})

	initial := []byte("braw before crash\r\n")
	if _, err := scrollback.Write(initial); err != nil {
		t.Fatal(err)
	}
	if err := session.writeScreenLocked(initial); err != nil {
		t.Fatal(err)
	}

	if err := term.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-term.waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not exit after kill")
	}

	afterCrash := []byte("canny after crash")
	if _, err := scrollback.Write(afterCrash); err != nil {
		t.Fatal(err)
	}
	if err := session.writeScreenLocked(afterCrash); err == nil {
		t.Fatal("write after helper crash returned nil error")
	}

	preview, err := renderPreviewErr(session.screen)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview, "braw before crash") || !strings.Contains(preview, "canny after crash") {
		t.Fatalf("reconstructed preview = %q", preview)
	}
	if session.screen == term {
		t.Fatal("crashed helper was not replaced")
	}
}

func TestGhosttyTerminalSizeLimit(t *testing.T) {
	if _, _, err := validateGhosttySize(1024, 257); err == nil {
		t.Fatal("oversized terminal returned nil error")
	}
}

func TestGhosttyAdoptHydrationOverRequestLimit(t *testing.T) {
	line := []byte("dreich hydration on the brae\r\n")
	fixture := bytes.Repeat(line, ghosttyMaxRequestBytes/len(line)+1)
	fixture = append(fixture, []byte("\x1b[2J\x1b[Hcanny hydration marker")...)
	if len(fixture) <= ghosttyMaxRequestBytes {
		t.Fatalf("fixture bytes = %d, want more than request limit %d", len(fixture), ghosttyMaxRequestBytes)
	}

	logPath := filepath.Join(t.TempDir(), "strath.log")
	if err := os.WriteFile(logPath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})

	transferredFD, err := unix.Dup(int(r.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	session, err := AdoptSession(AdoptOpts{
		ID:             "canny-large-hydration",
		Fd:             uintptr(transferredFD),
		PID:            cmd.Process.Pid,
		LogPath:        logPath,
		MaxLogSize:     int64(len(fixture) + 1024),
		DefaultRows:    24,
		DefaultCols:    80,
		HydrationBytes: len(fixture),
		PollInterval:   10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("adopt with hydration over request limit: %v", err)
	}
	if got := session.ScreenPreview(); !strings.Contains(got, "canny hydration marker") {
		t.Fatalf("hydrated screen missing final marker: %q", got)
	}

	closeDone := make(chan struct{})
	go func() {
		session.Close()
		close(closeDone)
	}()
	// A synthetic pipe read can remain blocked across close on Darwin. One write
	// lets that read observe teardown; the post-Close write below is the actual
	// endpoint-ownership assertion.
	_, _ = w.Write([]byte("unblock adopted read"))
	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("adopted session close did not finish")
	}
	if _, err := w.Write([]byte("bothy ownership probe")); !errors.Is(err, unix.EPIPE) {
		t.Fatalf("write after adopted endpoint teardown = %v, want EPIPE", err)
	}
}

func TestGhosttyFreezeBlocksGenerationAndSnapshotsUnreapedHelpers(t *testing.T) {
	terminal, err := newGhosttyProcessTerminal(8, 4)
	if err != nil {
		t.Fatal(err)
	}
	helpers, err := FreezeTerminalHelpers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer ThawTerminalHelpers()
	if len(helpers) != 1 || helpers[0].PID <= 0 || helpers[0].StartTime <= 0 {
		t.Fatalf("frozen helper registry = %+v", helpers)
	}
	if _, err := newGhosttyProcessTerminal(8, 4); !errors.Is(err, errTerminalGenerationFrozen) {
		t.Fatalf("generation while frozen error = %v", err)
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGhosttyFreezeDeadlineThawsGeneration(t *testing.T) {
	registry := newGhosttyHelperRegistry()
	if err := registry.begin(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := registry.freeze(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("freeze error = %v, want deadline exceeded", err)
	}
	registry.finish(HelperProcessIdentity{})
	if err := registry.begin(); err != nil {
		t.Fatalf("generation remained frozen after deadline: %v", err)
	}
	registry.finish(HelperProcessIdentity{})
}

func TestGhosttyRegistryRetainsReplacedHelperUntilWait(t *testing.T) {
	registry := newGhosttyHelperRegistry()
	identity := HelperProcessIdentity{PID: 4242, StartTime: 99}
	if err := registry.begin(); err != nil {
		t.Fatal(err)
	}
	registry.finish(identity)

	// Losing the terminal's current-screen reference does not touch the global
	// registry. Only the exact Wait completion removes the old helper.
	if got, err := registry.freeze(context.Background()); err != nil || !slices.Equal(got, []HelperProcessIdentity{identity}) {
		t.Fatalf("snapshot before Wait = %+v", got)
	}
	registry.remove(identity)
	registry.thaw()
	if got, err := registry.freeze(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("snapshot after Wait = %+v", got)
	}
	registry.thaw()
}

func TestGhosttyHelperFailureDuringFreezeRecoversAfterThaw(t *testing.T) {
	terminal, err := newGhosttyProcessTerminal(20, 4)
	if err != nil {
		t.Fatal(err)
	}
	scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "bothy.log"), 1024*1024)
	if err != nil {
		_ = terminal.Close()
		t.Fatal(err)
	}
	session := &Session{
		ID: "bothy-freeze", Scrollback: scrollback, screen: terminal,
		screenFactory: newTerminal, screenHydrationBytes: 1024, log: slog.Default(),
	}
	defer session.Close()
	helpers, err := FreezeTerminalHelpers(context.Background())
	if err != nil || len(helpers) != 1 {
		ThawTerminalHelpers()
		t.Fatalf("FreezeTerminalHelpers = (%+v, %v)", helpers, err)
	}
	payload := []byte("canny output retained during freeze")
	if _, err := scrollback.Write(payload); err != nil {
		ThawTerminalHelpers()
		t.Fatal(err)
	}
	if err := unix.Kill(helpers[0].PID, unix.SIGKILL); err != nil {
		ThawTerminalHelpers()
		t.Fatal(err)
	}
	<-terminal.waitDone

	session.mu.Lock()
	writeErr := session.writeScreenLocked(payload)
	pending := session.screenRecoveryPending
	session.mu.Unlock()
	if writeErr == nil || !errors.Is(writeErr, errTerminalGenerationFrozen) || !pending {
		ThawTerminalHelpers()
		t.Fatalf("write while frozen = (%v, pending=%v)", writeErr, pending)
	}
	ThawTerminalHelpers()
	if err := session.RecoverTerminalAfterUpgrade(); err != nil {
		t.Fatal(err)
	}
	if got := session.ScreenPreview(); !strings.Contains(got, "canny output") {
		t.Fatalf("reconstructed preview = %q", got)
	}
}

func TestGhosttyPeakRSSConcurrentClose(t *testing.T) {
	terminal, err := newGhosttyProcessTerminal(8, 4)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = terminal.PeakRSSBytes()
				}
			}
		}()
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	close(stop)
	readers.Wait()
}

func TestGhosttyPoisonReplayFallsBackOnceAndKeepsLogsPrivate(t *testing.T) {
	poison := []byte("dreich-poison-terminal-secret")
	scrollback, err := NewScrollback(filepath.Join(t.TempDir(), "croft.log"), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scrollback.Close() })
	if _, err := scrollback.Write(poison); err != nil {
		t.Fatal(err)
	}

	failed := &poisonReplayTerminal{cols: 20, rows: 2, poison: poison}
	var replacements []*poisonReplayTerminal
	var logs bytes.Buffer
	session := &Session{
		ID:         "canny-poison",
		Scrollback: scrollback,
		screen:     failed,
		screenFactory: func(cols, rows int) (Terminal, error) {
			replacement := &poisonReplayTerminal{cols: cols, rows: rows, poison: poison}
			replacements = append(replacements, replacement)

			return replacement, nil
		},
		screenHydrationBytes: len(poison),
		log:                  slog.New(slog.NewTextHandler(&logs, nil)),
	}

	if err := session.writeScreenLocked(poison); !errors.Is(err, errGhosttyHelperNative) {
		t.Fatalf("poison write error = %v, want native failure", err)
	}
	if len(replacements) != 2 {
		t.Fatalf("replacement attempts = %d, want hydrated then empty", len(replacements))
	}
	if got := len(replacements[0].writes); got != 1 {
		t.Fatalf("hydrated replacement writes = %d, want one bounded replay", got)
	}
	if got := len(replacements[1].writes); got != 0 {
		t.Fatalf("empty replacement writes = %d, poison was replayed again", got)
	}

	safe := []byte("braw after poison")
	if _, err := scrollback.Write(safe); err != nil {
		t.Fatal(err)
	}
	if err := session.writeScreenLocked(safe); err != nil {
		t.Fatalf("write after poison recovery: %v", err)
	}
	if got := string(replacements[1].writes[0]); got != string(safe) {
		t.Fatalf("post-recovery write = %q, want %q", got, safe)
	}
	if strings.Contains(logs.String(), string(poison)) {
		t.Fatalf("recovery log exposed terminal bytes: %s", logs.String())
	}
}

type poisonReplayTerminal struct {
	cols   int
	rows   int
	poison []byte
	writes [][]byte
}

func (t *poisonReplayTerminal) Write(p []byte) (int, error) {
	t.writes = append(t.writes, append([]byte(nil), p...))
	if bytes.Contains(p, t.poison) {
		return 0, errGhosttyHelperNative
	}

	return len(p), nil
}

func (t *poisonReplayTerminal) Resize(cols, rows int) error {
	t.cols, t.rows = cols, rows

	return nil
}

func (t *poisonReplayTerminal) Size() (int, int) { return t.cols, t.rows }

func (t *poisonReplayTerminal) Cursor() (int, int, bool) { return 0, 0, false }

func (t *poisonReplayTerminal) Cell(_, _ int) Cell { return Cell{Content: " "} }

func (t *poisonReplayTerminal) Close() error { return nil }

func ghosttyRunScriptedExchange(
	t *testing.T,
	op byte,
	payload []byte,
	script func(net.Conn),
) error {
	t.Helper()

	return ghosttyRunScriptedExchangeWithTimeout(t, op, payload, time.Second, script)
}

func ghosttyRunScriptedExchangeWithTimeout(
	t *testing.T,
	op byte,
	payload []byte,
	timeout time.Duration,
	script func(net.Conn),
) error {
	t.Helper()

	parent, child := net.Pipe()
	terminal := &ghosttyProcessTerminal{
		conn:            parent,
		cols:            1,
		rows:            1,
		rpcTimeout:      timeout,
		shutdownTimeout: 10 * time.Millisecond,
		reapTimeout:     10 * time.Millisecond,
	}
	serverDone := make(chan error, 1)
	go func() {
		defer child.Close()
		if _, _, err := readGhosttyRequest(child); err != nil {
			serverDone <- err

			return
		}
		script(child)
		serverDone <- nil
	}()

	_, exchangeErr := terminal.exchange(op, payload)
	_ = parent.Close()
	if serverErr := <-serverDone; serverErr != nil {
		t.Fatalf("scripted server request: %v", serverErr)
	}

	return exchangeErr
}

func ghosttyTestRequest(op byte, payload []byte) []byte {
	frame := ghosttyTestRequestHeader(op, len(payload))

	return append(frame, payload...)
}

func ghosttyTestRequestHeader(op byte, length int) []byte {
	header := make([]byte, 12)
	copy(header, ghosttyRequestMagic[:])
	header[4] = ghosttyProtocolVersion
	header[5] = op
	binary.BigEndian.PutUint32(header[8:12], uint32(length))

	return header
}

func ghosttyTestReply(op, status byte, payload []byte) []byte {
	frame := ghosttyTestReplyHeader(op, status, len(payload))

	return append(frame, payload...)
}

func ghosttyTestReplyHeader(op, status byte, length int) []byte {
	header := make([]byte, 12)
	copy(header, ghosttyReplyMagic[:])
	header[4] = ghosttyProtocolVersion
	header[5] = op
	header[6] = status
	binary.BigEndian.PutUint32(header[8:12], uint32(length))

	return header
}
