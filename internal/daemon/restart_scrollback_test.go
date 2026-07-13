package daemon

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
)

// syncBuffer is a concurrency-safe bytes.Buffer for capturing slog output while
// the PTY read loop logs from its own goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// waitForStatus polls a session's lifecycle status until it matches want or the
// deadline elapses.
func waitForStatus(t *testing.T, sm *SessionManager, id string, want SessionStatus) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sm.mu.RLock()
		s, ok := sm.state.Sessions[id]

		var got SessionStatus
		if ok {
			got = s.Status
		}

		sm.mu.RUnlock()

		if got == want {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for session %s to reach status %q", id, want)
}

// waitForScrollback polls the live PTY's scrollback until it contains want.
func waitForScrollback(t *testing.T, sm *SessionManager, id, want string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ptySess, ok := sm.GetPTY(id); ok {
			if tail, err := ptySess.Scrollback.TailBytes(64 * 1024); err == nil && strings.Contains(string(tail), want) {
				return
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %q in scrollback of session %s", want, id)
}

// TestRestartCapturesScrollback is a regression test for issue #1087: a stopped
// session that is restarted produced no visible output on attach because the
// restart lifecycle did not reliably reconnect the scrollback pipeline. The
// test drives Create → Stop → Restart with a fake agent that prints a distinct
// marker on resume, then asserts the marker is captured both in the live PTY
// scrollback and in the on-disk scrollback log that `gr logs` reads.
func TestRestartCapturesScrollback(t *testing.T) {
	repo := initTempGitRepo(t)
	dir := t.TempDir()

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["echo"] = config.Agent{
		Command:    "sh",
		Args:       []string{"-c", "echo FRESH-OUTPUT; exec cat"},
		ResumeArgs: []string{"-c", "echo RESUME-OUTPUT; exec cat"},
	}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile:  filepath.Join(dir, "state.json"),
		DataDir:    dir,
		LogDir:     dir,
		RuntimeDir: dir,
	}, slog.Default())

	created, err := sm.Create(CreateOpts{
		Name: "bide", AgentName: "echo", RepoPath: repo, BaseBranch: "main", Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	id := created.ID

	t.Cleanup(func() { stopAndClosePTY(sm, id) })

	waitForScrollback(t, sm, id, "FRESH-OUTPUT")

	// Stop and wait for the watcher to record the stopped status.
	if err := sm.Stop(id); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	waitForStatus(t, sm, id, StatusStopped)

	// Restart the stopped session.
	restarted, err := sm.Restart(id, 24, 80)
	if err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	if restarted.Status != StatusRunning {
		t.Fatalf("restarted status = %q, want %q", restarted.Status, StatusRunning)
	}

	// The live PTY's scrollback must capture output produced after restart.
	waitForScrollback(t, sm, id, "RESUME-OUTPUT")

	// The on-disk scrollback log (what `gr logs` reads for a session) must also
	// contain the post-restart output.
	deadline := time.Now().Add(5 * time.Second)

	var logContent string

	for time.Now().Before(deadline) {
		if data, rerr := os.ReadFile(sm.scrollbackLogPath(id)); rerr == nil {
			logContent = string(data)
			if strings.Contains(logContent, "RESUME-OUTPUT") {
				return
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("post-restart output missing from on-disk scrollback log %s; got %q", sm.scrollbackLogPath(id), logContent)
}

// TestRestartRunningSessionCapturesScrollback covers restarting a *running*
// session (not previously stopped): the old PTY is killed and a new one spawned
// in one operation. The post-restart scrollback must still be captured.
func TestRestartRunningSessionCapturesScrollback(t *testing.T) {
	repo := initTempGitRepo(t)
	dir := t.TempDir()

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["echo"] = config.Agent{
		Command:    "sh",
		Args:       []string{"-c", "echo FRESH-OUTPUT; exec cat"},
		ResumeArgs: []string{"-c", "echo RESUME-OUTPUT; exec cat"},
	}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile:  filepath.Join(dir, "state.json"),
		DataDir:    dir,
		LogDir:     dir,
		RuntimeDir: dir,
	}, slog.Default())

	created, err := sm.Create(CreateOpts{
		Name: "canny", AgentName: "echo", RepoPath: repo, BaseBranch: "main", Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	id := created.ID

	t.Cleanup(func() { stopAndClosePTY(sm, id) })

	waitForScrollback(t, sm, id, "FRESH-OUTPUT")

	// Restart directly from the running state.
	if _, err := sm.Restart(id, 24, 80); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	waitForScrollback(t, sm, id, "RESUME-OUTPUT")
}

// newSilentPTY spawns a PTY session whose agent produces no output but stays
// alive (`exec cat` blocks on stdin), so BytesRead stays 0. The caller is
// responsible for closing it.
func newSilentPTY(t *testing.T, dir, id string) *grpty.Session {
	t.Helper()

	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID:         id,
		Command:    "sh",
		Args:       []string{"-c", "exec cat"},
		Dir:        dir,
		Rows:       24,
		Cols:       80,
		LogPath:    filepath.Join(dir, id+".log"),
		MaxLogSize: 1 << 20,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	return ptySess
}

// TestCheckSilentSessionWarnsOnce verifies the zero-output diagnostic (#1087):
// a running session with a live PTY that has produced no output past the
// threshold is warned about exactly once per PTY lifetime.
func TestCheckSilentSessionWarnsOnce(t *testing.T) {
	dir := t.TempDir()

	var logs syncBuffer

	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	sm := NewSessionManager(config.Default(), config.Paths{
		StateFile: filepath.Join(dir, "state.json"), DataDir: dir, LogDir: dir, RuntimeDir: dir,
	}, log)

	ptySess := newSilentPTY(t, dir, "dreich")
	t.Cleanup(func() { _ = ptySess.Kill(); <-ptySess.Done(); ptySess.Close() })

	// Let the PTY age past the (tiny) test threshold.
	time.Sleep(30 * time.Millisecond)

	sm.checkSilentSessionWithThreshold("dreich", "dreich-sess", "claude", ptySess, 10*time.Millisecond)
	sm.checkSilentSessionWithThreshold("dreich", "dreich-sess", "claude", ptySess, 10*time.Millisecond)

	got := logs.String()
	if n := strings.Count(got, "producing no PTY output"); n != 1 {
		t.Fatalf("expected exactly one silent-session warning, got %d\nlog:\n%s", n, got)
	}

	if !strings.Contains(got, "dreich-sess") || !strings.Contains(got, "1087") {
		t.Errorf("warning missing session name or issue reference:\n%s", got)
	}
}

// TestCheckSilentSessionQuietWhenOutput verifies the diagnostic does not fire
// for a session that has produced output or is still within the threshold.
func TestCheckSilentSessionQuietWhenOutput(t *testing.T) {
	dir := t.TempDir()

	var logs syncBuffer

	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	sm := NewSessionManager(config.Default(), config.Paths{
		StateFile: filepath.Join(dir, "state.json"), DataDir: dir, LogDir: dir, RuntimeDir: dir,
	}, log)

	// Within the threshold: even a silent PTY must not warn yet.
	quiet := newSilentPTY(t, dir, "haar")
	t.Cleanup(func() { _ = quiet.Kill(); <-quiet.Done(); quiet.Close() })
	sm.checkSilentSessionWithThreshold("haar", "haar-sess", "claude", quiet, time.Hour)

	// Past the threshold but with output: cat echoes typed input, so BytesRead
	// becomes non-zero and the session is not silent.
	noisy := newSilentPTY(t, dir, "bonnie")
	t.Cleanup(func() { _ = noisy.Kill(); <-noisy.Done(); noisy.Close() })

	if err := noisy.WriteInput([]byte("chatter\n")); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for noisy.BytesRead() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if noisy.BytesRead() == 0 {
		t.Fatal("expected cat to echo input, but PTY produced no output")
	}

	sm.checkSilentSessionWithThreshold("bonnie", "bonnie-sess", "claude", noisy, time.Nanosecond)

	if got := logs.String(); strings.Contains(got, "producing no PTY output") {
		t.Fatalf("did not expect a silent-session warning:\n%s", got)
	}
}
