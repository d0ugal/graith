package daemon

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	grpty "github.com/d0ugal/graith/internal/pty"
)

func terminalParserPanicFixture(t *testing.T) []byte {
	t.Helper()

	encoded, err := os.ReadFile(filepath.Join("..", "pty", "testdata", "scrollup-delete-line-area-panic.hex"))
	if err != nil {
		t.Fatal(err)
	}

	fixture, err := hex.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}

	return fixture
}

func TestAdoptSessionsContinuesAfterTerminalHydrationPanic(t *testing.T) {
	sm := sleeperSM(t)

	var logBuf syncBuffer

	sm.log = slog.New(slog.NewJSONHandler(&logBuf, nil))

	for _, id := range []string{"thrawn-fash", "canny-braw"} {
		sm.state.Sessions[id] = &SessionState{
			ID: id, Name: id, Agent: "sleeper", Status: StatusRunning,
		}
	}

	badScrollback := append(terminalParserPanicFixture(t), []byte("dreich-payload-must-not-be-logged")...)
	if err := os.WriteFile(filepath.Join(sm.paths.LogDir, "thrawn-fash.log"), badScrollback, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(sm.paths.LogDir, "canny-braw.log"), []byte("canny scrollback"), 0o600); err != nil {
		t.Fatal(err)
	}

	badR, badW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = badR.Close()
		_ = badW.Close()
	})

	badFD, err := syscall.Dup(int(badR.Fd()))
	if err != nil {
		t.Fatal(err)
	}

	if err := setDescriptorFlags(badFD, syscall.FD_CLOEXEC); err != nil {
		t.Fatal(err)
	}

	goodR, goodW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = goodR.Close() })

	goodFD, err := syscall.Dup(int(goodR.Fd()))
	if err != nil {
		t.Fatal(err)
	}

	if err := setDescriptorFlags(goodFD, syscall.FD_CLOEXEC); err != nil {
		t.Fatal(err)
	}

	badCmd := exec.Command("sleep", "30")
	if err := badCmd.Start(); err != nil {
		t.Fatal(err)
	}

	goodCmd := exec.Command("sleep", "30")
	if err := goodCmd.Start(); err != nil {
		_ = badCmd.Process.Kill()
		_ = badCmd.Wait()

		t.Fatal(err)
	}

	badStart, err := grpty.ProcessStartTime(badCmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}

	goodStart, err := grpty.ProcessStartTime(goodCmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}

	for id, identity := range map[string]struct {
		pid   int
		start int64
	}{
		"thrawn-fash": {badCmd.Process.Pid, badStart},
		"canny-braw":  {goodCmd.Process.Pid, goodStart},
	} {
		sm.state.Sessions[id].PID = identity.pid
		sm.state.Sessions[id].PIDStartTime = identity.start
	}

	t.Cleanup(func() {
		_ = goodW.Close()
		_ = badW.Close()
		_ = badCmd.Process.Kill()
		_ = badCmd.Wait()
		_ = goodCmd.Process.Kill()
		_ = goodCmd.Wait()
		sm.watchers.Wait()
	})

	manifest := &UpgradeManifest{Sessions: []UpgradeSession{
		{ID: "thrawn-fash", Fd: badFD,
			ScrollbackFd: openUpgradeScrollbackFD(t, filepath.Join(sm.paths.LogDir, "thrawn-fash.log")),
			PID:          badCmd.Process.Pid, PIDStartTime: badStart},
		{ID: "canny-braw", Fd: goodFD,
			ScrollbackFd: openUpgradeScrollbackFD(t, filepath.Join(sm.paths.LogDir, "canny-braw.log")),
			PID:          goodCmd.Process.Pid, PIDStartTime: goodStart},
	}}

	if _, err := sm.AdoptSessions(manifest); err != nil {
		t.Fatalf("AdoptSessions: %v", err)
	}
	// Run normally schedules derived-screen reconstruction in its owned
	// post-commit background generation. Exercise that phase explicitly here.
	sm.recoverTerminalScreensAfterUpgrade(context.Background())

	bad, _ := sm.Get("thrawn-fash")
	if bad.Status != StatusRunning {
		t.Errorf("failed hydration status = %q, want %q", bad.Status, StatusRunning)
	}

	badPTY, ok := sm.GetPTY("thrawn-fash")
	if !ok {
		t.Fatal("failed hydration did not retain the live PTY")
	}

	if err := badCmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("failed hydration killed the running agent: %v", err)
	}

	if _, ok := sm.GetPTY("canny-braw"); !ok {
		t.Fatal("later manifest entry was not adopted")
	}

	good, _ := sm.Get("canny-braw")
	if good.Status != StatusRunning {
		t.Errorf("later manifest entry status = %q, want %q", good.Status, StatusRunning)
	}

	var failureLog map[string]any

	for _, line := range bytes.Split([]byte(logBuf.String()), []byte("\n")) {
		var record map[string]any
		if json.Unmarshal(line, &record) == nil &&
			record["msg"] == "terminal recovery hydration failed; using empty screen" {
			failureLog = record
			break
		}
	}

	if failureLog == nil {
		t.Fatalf("missing adoption failure log: %s", logBuf.String())
	}

	if failureLog["session"] != "thrawn-fash" {
		t.Errorf("adoption fallback session = %v, want thrawn-fash", failureLog["session"])
	}

	if failureLog["error"] != "terminal parser panic" {
		t.Errorf("adoption fallback error = %v, want sanitized parser failure", failureLog["error"])
	}

	if strings.Contains(logBuf.String(), "dreich-payload-must-not-be-logged") {
		t.Fatal("adoption failure log exposed scrollback contents")
	}

	if _, err := badW.Write([]byte("\x1b[2J\x1b[Hcanny-live-after-fallback")); err != nil {
		t.Fatalf("write after hydration fallback: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(badPTY.ScreenPreview(), "canny-live-after-fallback") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if got := badPTY.ScreenPreview(); !strings.Contains(got, "canny-live-after-fallback") {
		t.Fatalf("adopted screen did not remain serviceable: %q", got)
	}

	tail, err := badPTY.ScrollbackFile().TailBytes(256 * 1024)
	if err != nil || !bytes.Contains(tail, badScrollback) {
		t.Fatalf("raw scrollback was not preserved: bytes=%d err=%v", len(tail), err)
	}

	_ = badW.Close()
	_ = goodW.Close()
	_ = badCmd.Process.Kill()
	_ = badCmd.Wait()
	_ = goodCmd.Process.Kill()
	_ = goodCmd.Wait()

	watchersDone := make(chan struct{})

	go func() {
		sm.watchers.Wait()
		close(watchersDone)
	}()

	select {
	case <-watchersDone:
	case <-time.After(2 * time.Second):
		t.Fatal("adopted session did not stop during cleanup")
	}
}
