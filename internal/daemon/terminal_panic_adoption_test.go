package daemon

import (
	"bytes"
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

	goodR, goodW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = goodR.Close() })

	goodFD, err := syscall.Dup(int(goodR.Fd()))
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = goodW.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		sm.watchers.Wait()
	})

	manifest := &UpgradeManifest{Sessions: []UpgradeSession{
		{ID: "thrawn-fash", Fd: badFD, PID: cmd.Process.Pid},
		{ID: "canny-braw", Fd: goodFD, PID: cmd.Process.Pid},
	}}

	if err := sm.AdoptSessions(manifest); err != nil {
		t.Fatalf("AdoptSessions: %v", err)
	}

	bad, _ := sm.Get("thrawn-fash")
	if bad.Status != StatusStopped {
		t.Errorf("failed hydration status = %q, want %q", bad.Status, StatusStopped)
	}

	if bad.SummaryText != "Lost during daemon upgrade" {
		t.Errorf("failed hydration summary = %q, want lost-upgrade summary", bad.SummaryText)
	}

	if _, ok := sm.GetPTY("thrawn-fash"); ok {
		t.Error("failed hydration retained a live PTY")
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
		if json.Unmarshal(line, &record) == nil && record["msg"] == "failed to adopt session" {
			failureLog = record
			break
		}
	}

	if failureLog == nil {
		t.Fatalf("missing adoption failure log: %s", logBuf.String())
	}

	if failureLog["id"] != "thrawn-fash" {
		t.Errorf("adoption failure id = %v, want thrawn-fash", failureLog["id"])
	}

	if failureLog["err"] != "hydrate terminal screen: terminal parser panic" {
		t.Errorf("adoption failure error = %v, want sanitized parser failure", failureLog["err"])
	}

	if strings.Contains(logBuf.String(), "dreich-payload-must-not-be-logged") {
		t.Fatal("adoption failure log exposed scrollback contents")
	}

	_ = badW.Close()
	_ = goodW.Close()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

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
