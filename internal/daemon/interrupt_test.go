package daemon

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
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

// TestInterruptFromAttachedClientMarksUserInput verifies that an interrupt
// control message from the attached client records user input on the session,
// so the attached-user idle window (e.g. inbox-notification injection) treats
// an interactive Ctrl-C as activity — matching the raw data path it replaced
// (issue #857). Regression test: before the fix the interrupt handler skipped
// NotifyUserInput entirely.
func TestInterruptFromAttachedClientMarksUserInput(t *testing.T) {
	h := newTestHarness(t)

	// Ignore SIGINT so the interrupt doesn't exit the process — otherwise
	// WaitForUserIdle short-circuits on exit rather than reflecting the
	// recorded user input.
	logPath := filepath.Join(h.sm.paths.LogDir, "braw-int.log")

	sess, err := grpty.NewSession(grpty.SessionOpts{
		ID: "braw-int", Command: "sh", Args: []string{"-c", "trap '' INT; sleep 30"},
		Dir: t.TempDir(), Rows: 24, Cols: 80, LogPath: logPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = sess.Kill()
		<-sess.Done()
		sess.Close()
	})

	h.sm.mu.Lock()
	h.sm.state.Sessions["braw-int"] = &SessionState{
		ID: "braw-int", Name: "braw-interrupt", Agent: "opencode",
		Status: StatusRunning, CreatedAt: time.Now().UTC(),
	}
	h.sm.sessions["braw-int"] = sess
	h.sm.mu.Unlock()

	// Give the trap time to install before interrupting.
	time.Sleep(200 * time.Millisecond)

	// Drain frames in the background so the handler's attach-time scrollback
	// data frame never blocks the unbuffered net.Pipe while the test is busy
	// in WaitForUserIdle. Control envelopes are routed to a channel.
	ctrlCh := make(chan protocol.Envelope, 16)

	go func() {
		for {
			frame, err := h.reader.ReadFrame()
			if err != nil {
				return
			}

			if frame.Channel == protocol.ChannelControl {
				if env, err := protocol.DecodeControl(frame.Payload); err == nil {
					ctrlCh <- env
				}
			}
		}
	}()

	readCtrl := func() protocol.Envelope {
		t.Helper()

		select {
		case env := <-ctrlCh:
			return env
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for control message")

			return protocol.Envelope{}
		}
	}

	// Attach so the interrupt is treated as coming from the attached client.
	h.sendControl(t, "attach", protocol.AttachMsg{SessionID: "braw-int"})

	if env := readCtrl(); env.Type != "attached" {
		t.Fatalf("expected attached, got %q", env.Type)
	}

	// Before any interrupt the session is idle: WaitForUserIdle returns fast.
	start := time.Now()
	if !sess.WaitForUserIdle(150*time.Millisecond, time.Second) {
		t.Fatal("expected idle before interrupt")
	}

	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("expected immediate idle before interrupt, took %v", elapsed)
	}

	// Interrupt from the attached client should record user input.
	h.sendControl(t, "interrupt", protocol.InterruptMsg{SessionID: "braw-int"})

	if env := readCtrl(); env.Type != "interrupted" {
		t.Fatalf("expected interrupted, got %q", env.Type)
	}

	// The idle window must now reset — WaitForUserIdle should wait ~150ms.
	start = time.Now()
	if !sess.WaitForUserIdle(150*time.Millisecond, 2*time.Second) {
		t.Fatal("expected idle=true after the window elapses")
	}

	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("interrupt did not reset the user-idle window (returned in %v)", elapsed)
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

	// Retry the interrupt on a short cadence rather than firing once. Just
	// after fork the child may not yet be the PTY's foreground process group,
	// so an early Ctrl-C is buffered as a literal byte instead of becoming
	// SIGINT — a startup race that flakes on Linux under -race (see the twin
	// comment in internal/pty/session_test.go's TestSessionInterrupt).
	deadline := time.After(10 * time.Second)

	retry := time.NewTicker(200 * time.Millisecond)
	defer retry.Stop()

	if err := sm.InterruptSession("canny"); err != nil {
		t.Fatalf("InterruptSession: %v", err)
	}

	for {
		select {
		case <-ptySess.Done():
			return
		case <-retry.C:
			// A resend after the process has exited returns an error; that's
			// the interrupt working, so ignore it and let Done() observe it.
			_ = sm.InterruptSession("canny")
		case <-deadline:
			t.Fatal("timeout: single-press interrupt did not terminate the process")
		}
	}
}
