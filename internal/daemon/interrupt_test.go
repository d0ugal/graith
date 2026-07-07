package daemon

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
)

// intPtr is a small helper for building pointer-valued interrupt config.
func intPtr(v int) *int { return &v }

func TestInterruptSession_NoLivePTY(t *testing.T) {
	sm := NewSessionManager(config.Default(), config.Paths{}, slog.Default())

	if err := sm.InterruptSession("thrawn-missing"); err == nil {
		t.Fatal("expected error for session with no live PTY, got nil")
	}
}

func TestInterruptSession_UsesAgentConfig(t *testing.T) {
	// The claude agent is configured to press Ctrl-C multiple times with a delay
	// between presses. Point the fixture at that so InterruptSession must resolve
	// the per-agent config to deliver the sequence.
	cfg := config.Default()
	claude := cfg.Agents["claude"]
	claude.InterruptCount = intPtr(3)
	claude.InterruptDelayMs = intPtr(100)
	cfg.Agents["claude"] = claude

	sm := NewSessionManager(cfg, config.Paths{}, slog.Default())

	logPath := filepath.Join(t.TempDir(), "braw.log")

	// Ignore SIGINT so the process survives every press; this lets us observe
	// that the configured count/delay were applied rather than a single 0x03.
	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID: "braw", Command: "sh", Args: []string{"-c", "trap '' INT; sleep 30"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ptySess.Close()

	sm.mu.Lock()
	sm.sessions["braw"] = ptySess
	sm.state.Sessions["braw"] = &SessionState{ID: "braw", Name: "braw", Agent: "claude"}
	sm.mu.Unlock()

	// Give the trap time to install before interrupting.
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	if err := sm.InterruptSession("braw"); err != nil {
		t.Fatalf("InterruptSession: %v", err)
	}

	// 3 presses with a 100ms gap means at least ~200ms of inter-press delay.
	if elapsed := time.Since(start); elapsed < 180*time.Millisecond {
		t.Errorf("InterruptSession returned after %v, want >= ~200ms (agent config not applied)", elapsed)
	}

	if ptySess.Exited() {
		t.Error("process ignoring SIGINT should still be running after interrupt")
	}
}

func TestInterruptSession_DefaultAgentSinglePress(t *testing.T) {
	// An agent with no interrupt config (opencode) should fall back to a single
	// Ctrl-C with no delay, and a bare sleep is killed by it.
	sm := NewSessionManager(config.Default(), config.Paths{}, slog.Default())

	logPath := filepath.Join(t.TempDir(), "canny.log")

	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID: "canny", Command: "sh", Args: []string{"-c", "sleep 30"},
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath: logPath, MaxLogSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ptySess.Close()

	sm.mu.Lock()
	sm.sessions["canny"] = ptySess
	sm.state.Sessions["canny"] = &SessionState{ID: "canny", Name: "canny", Agent: "opencode"}
	sm.mu.Unlock()

	if err := sm.InterruptSession("canny"); err != nil {
		t.Fatalf("InterruptSession: %v", err)
	}

	select {
	case <-ptySess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: single-press interrupt did not terminate the process")
	}
}
