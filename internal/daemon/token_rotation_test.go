package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func newTokenRotationManager(t *testing.T, command string, args ...string) (*SessionManager, string) {
	t.Helper()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Agents["bide-agent"] = config.Agent{Command: command, Args: args, ResumeArgs: args}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(dir, "state.json"),
		DataDir:   dir,
		LogDir:    dir,
	}, slog.Default())

	return sm, dir
}

func seedStoppedSessionToken(t *testing.T, sm *SessionManager, dir, id, token string) {
	t.Helper()

	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "bide", Agent: "bide-agent", Status: StatusStopped,
		WorktreePath: dir, Token: token,
	}

	sm.tokenIndex[token] = id
	if err := sm.saveState(); err != nil {
		t.Fatal(err)
	}
}

func assertRotatedToken(t *testing.T, sm *SessionManager, id, oldToken, newToken string) {
	t.Helper()

	if newToken == "" || newToken == oldToken {
		t.Fatalf("token was not rotated: old=%q new=%q", oldToken, newToken)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if got := sm.SessionForToken(oldToken); got != "" {
		t.Errorf("old token resolves to %q, want empty", got)
	}

	if got := sm.SessionForToken(newToken); got != id {
		t.Errorf("new token resolves to %q, want %q", got, id)
	}

	if got := sm.state.Sessions[id].Token; got != newToken {
		t.Errorf("state token = %q, want %q", got, newToken)
	}
}

func TestResumeRotatesSessionTokenAndEnvironment(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "bide-token")
	sm, dir := newTokenRotationManager(t, "sh", "-c", `printf %s "$GRAITH_TOKEN" > "$TOKEN_FILE"; exec sleep 60`)
	agent := sm.cfg.Agents["bide-agent"]
	agent.Env = map[string]string{"TOKEN_FILE": tokenFile}
	sm.cfg.Agents["bide-agent"] = agent

	const oldToken = "auld-bide-token" //nolint:gosec // test fixture, not a real credential
	seedStoppedSessionToken(t, sm, dir, "bide-id", oldToken)

	resumed, err := sm.Resume("bide-id", 24, 80)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, "bide-id") })

	assertRotatedToken(t, sm, "bide-id", oldToken, resumed.Token)

	var envToken []byte
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		envToken, err = os.ReadFile(tokenFile)
		if err == nil && len(envToken) > 0 {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	if err != nil || len(envToken) == 0 {
		t.Fatalf("read token environment capture: %v", err)
	}

	if got := string(envToken); got != resumed.Token {
		t.Errorf("GRAITH_TOKEN = %q, want %q", got, resumed.Token)
	}

	persisted, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}

	if got := persisted.Sessions["bide-id"].Token; got != resumed.Token {
		t.Errorf("persisted token = %q, want %q", got, resumed.Token)
	}
}

func TestRestartRotatesSessionToken(t *testing.T) {
	sm, dir := newTokenRotationManager(t, "sleep", "60")

	const oldToken = "auld-canny-token" //nolint:gosec // test fixture, not a real credential
	seedStoppedSessionToken(t, sm, dir, "canny-id", oldToken)

	restarted, err := sm.Restart("canny-id", 24, 80)
	if err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, "canny-id") })

	assertRotatedToken(t, sm, "canny-id", oldToken, restarted.Token)
}

func TestResumeSpawnFailureRollsBackSessionTokenIndex(t *testing.T) {
	sm, dir := newTokenRotationManager(t, filepath.Join(dirForMissingCommand(t), "thrawn-agent"))

	const oldToken = "auld-dreich-token" //nolint:gosec // test fixture, not a real credential
	seedStoppedSessionToken(t, sm, dir, "dreich-id", oldToken)

	if _, err := sm.Resume("dreich-id", 24, 80); err == nil {
		t.Fatal("Resume() succeeded with missing command")
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if got := sm.state.Sessions["dreich-id"].Token; got != oldToken {
		t.Errorf("rolled-back state token = %q, want %q", got, oldToken)
	}

	if got := sm.SessionForToken(oldToken); got != "dreich-id" {
		t.Errorf("old token resolves to %q, want dreich-id", got)
	}

	if len(sm.tokenIndex) != 1 {
		t.Errorf("token index has %d entries, want 1: %v", len(sm.tokenIndex), sm.tokenIndex)
	}

	persisted, err := LoadState(sm.paths.StateFile)
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}

	if got := persisted.Sessions["dreich-id"].Token; got != oldToken {
		t.Errorf("persisted rolled-back token = %q, want %q", got, oldToken)
	}
}

func dirForMissingCommand(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}
