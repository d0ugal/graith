package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// shAgent is a fake agent whose command stays alive and ignores extra
// positional args (the seed prompt), so migration's start step succeeds in
// tests without a real agent binary.
func shAgent() config.Agent {
	return config.Agent{Command: "/bin/sh", Args: []string{"-c", "sleep 30"}}
}

func newMigrateTestManager(t *testing.T) *SessionManager {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.Sandbox.Enabled = false
	cfg.Agents["claude"] = shAgent()
	cfg.Agents["codex"] = shAgent()
	return NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(tmpDir, "state.json"),
		DataDir:   tmpDir,
		LogDir:    tmpDir,
		TmpDir:    filepath.Join(tmpDir, "tmp"),
	}, slog.Default())
}

func TestMigrateRejectsBadTargets(t *testing.T) {
	sm := newMigrateTestManager(t)
	sm.state.Sessions["s1"] = &SessionState{
		ID: "s1", Name: "braw-bothy", Agent: "claude",
		AgentSessionID: "sid-1", Status: StatusStopped,
		WorktreePath: t.TempDir(), RepoPath: t.TempDir(),
	}

	if _, err := sm.Migrate("s1", "claude", "", 24, 80); err == nil {
		t.Error("expected error migrating to the same agent")
	}
	if _, err := sm.Migrate("s1", "bogus-agent", "", 24, 80); err == nil {
		t.Error("expected error for unknown target agent")
	}
	if _, err := sm.Migrate("missing", "codex", "", 24, 80); err == nil {
		t.Error("expected error for missing session")
	}

	// Unsupported source agent (no transcript reader).
	sm.state.Sessions["s2"] = &SessionState{
		ID: "s2", Name: "thrawn", Agent: "cursor",
		Status: StatusStopped, WorktreePath: t.TempDir(), RepoPath: t.TempDir(),
	}
	if _, err := sm.Migrate("s2", "claude", "", 24, 80); err == nil {
		t.Error("expected error migrating from unsupported source agent")
	}
}

func TestMigrateInPlaceSwap(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	sm := newMigrateTestManager(t)
	repo := initTempGitRepo(t)

	// Bound the async codex id-capture scan to an empty temp dir so it never
	// touches the real ~/.codex and finds nothing (no state writes).
	t.Setenv("CODEX_HOME", t.TempDir())

	// Stage a Claude transcript at the globbed location.
	claudeRoot := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeRoot)
	sid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	projDir := filepath.Join(claudeRoot, "projects", "-some-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := `{"type":"user","uuid":"u1","parentUuid":"","message":{"role":"user","content":"fix the bothy"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"done, braw"}]}}
`
	if err := os.WriteFile(filepath.Join(projDir, sid+".jsonl"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	sm.state.Sessions["m1"] = &SessionState{
		ID: "m1", Name: "braw-bothy", Agent: "claude",
		AgentSessionID: sid, Status: StatusStopped,
		WorktreePath: repo, RepoPath: repo, CreatedAt: time.Now(),
	}

	res, err := sm.Migrate("m1", "codex", "", 24, 80)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Drain the spawned process and let watchSession persist the stop before
	// the test's temp dirs are torn down (avoids a cleanup write race).
	defer func() {
		if p, ok := sm.GetPTY("m1"); ok {
			_ = p.Kill()
			<-p.Done()
		}
		time.Sleep(250 * time.Millisecond)
	}()

	if res.Agent != "codex" {
		t.Errorf("result agent = %q, want codex", res.Agent)
	}

	sm.mu.RLock()
	s := sm.state.Sessions["m1"]
	sm.mu.RUnlock()

	if s.Agent != "codex" {
		t.Errorf("session agent = %q, want codex", s.Agent)
	}
	if s.AgentSessionID != "" {
		t.Errorf("codex agent session id = %q, want empty (captured async)", s.AgentSessionID)
	}
	if s.WorktreePath != repo {
		t.Errorf("worktree changed: %q, want %q (in-place migrate retains worktree)", s.WorktreePath, repo)
	}
	if s.MigratedFrom == nil || s.MigratedFrom.Agent != "claude" {
		t.Fatalf("MigratedFrom = %+v, want agent claude", s.MigratedFrom)
	}
	if s.MigratedFrom.AgentSessionID != sid {
		t.Errorf("MigratedFrom session id = %q, want %q", s.MigratedFrom.AgentSessionID, sid)
	}
	if _, err := os.Stat(s.MigratedFrom.RenderedPath); err != nil {
		t.Errorf("rendered context file missing: %v", err)
	}
	if s.FreshStart {
		t.Error("FreshStart should be cleared after a successful seeded start")
	}
}

// TestMigrateRestoresOnTargetFailure exercises the post-start health check: a
// target that exits immediately must trigger restore of the original agent.
func TestMigrateRestoresOnTargetFailure(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	sm := newMigrateTestManager(t)
	// Target codex exits immediately; source claude stays alive on restore.
	sm.cfg.Agents["codex"] = config.Agent{Command: "/bin/sh", Args: []string{"-c", "true"}}
	repo := initTempGitRepo(t)

	claudeRoot := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeRoot)
	sid := "11111111-2222-3333-4444-555555555555"
	projDir := filepath.Join(claudeRoot, "projects", "-p")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, sid+".jsonl"), []byte(
		`{"type":"user","uuid":"u1","parentUuid":"","message":{"role":"user","content":"bide a wee"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm.state.Sessions["m2"] = &SessionState{
		ID: "m2", Name: "canny-croft", Agent: "claude",
		AgentSessionID: sid, Status: StatusStopped,
		WorktreePath: repo, RepoPath: repo, CreatedAt: time.Now(),
	}

	_, err := sm.Migrate("m2", "codex", "", 24, 80)
	defer func() {
		if p, ok := sm.GetPTY("m2"); ok {
			_ = p.Kill()
			<-p.Done()
		}
		time.Sleep(250 * time.Millisecond)
	}()
	if err == nil {
		t.Fatal("expected migrate to fail when target exits immediately")
	}

	sm.mu.RLock()
	s := sm.state.Sessions["m2"]
	sm.mu.RUnlock()
	if s.Agent != "claude" {
		t.Errorf("agent = %q after failed migrate, want claude (restored)", s.Agent)
	}
	if s.AgentSessionID != sid {
		t.Errorf("agent session id = %q, want restored %q", s.AgentSessionID, sid)
	}
	if s.MigratedFrom != nil {
		t.Errorf("MigratedFrom = %+v, want nil after successful restore", s.MigratedFrom)
	}
	if s.Status != StatusRunning {
		t.Errorf("status = %q, want running (original restored)", s.Status)
	}
	// The per-session migration context dir should be cleaned up after restore.
	tmpDir, derr := sm.repoTmpDir(repo)
	if derr != nil {
		t.Fatal(derr)
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, "migrate-m2")); statErr == nil {
		t.Error("migration context dir should be removed after successful restore")
	}
}
