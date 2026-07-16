package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/testutil"
)

func newTestSessionManager(t *testing.T) *SessionManager {
	t.Helper()
	testutil.IsolateGit(t)

	// Approval gating is opt-in (disabled by default). These tests exercise
	// the hook-generation and approval-queue mechanics, so enable it here.
	cfg := config.Default()
	enabled := true
	cfg.Approvals.Enabled = &enabled

	return newSMWithConfig(t, cfg)
}

// gitOut mirrors the package-level gitRun helper but returns the command's
// combined output (trimmed), for callers that need to inspect it. It keeps the
// hermetic Git environment some git invocations rely on.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := testutil.GitCommand(args...)
	if dir != "" {
		cmd.Dir = dir
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}

	return strings.TrimSpace(string(out))
}

// assertErrContains fails the test if err is nil or its message does not
// contain want.
func assertErrContains(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}

	if !strings.Contains(err.Error(), want) {
		t.Errorf("expected error containing %q, got %v", want, err)
	}
}

// assertCreateMirrorRejected seeds a running source session and asserts
// that creating a session which shares its worktree is rejected because the
// sandbox is not enabled.
func assertCreateMirrorRejected(t *testing.T, sm *SessionManager) {
	t.Helper()

	sm.state.Sessions["src1"] = &SessionState{
		ID:           "src1",
		Name:         "braw-source",
		Agent:        "claude",
		WorktreePath: "/tmp/fake-worktree",
		Status:       StatusRunning,
	}

	_, err := sm.Create(CreateOpts{Name: "canny-reviewer", AgentName: "claude", Mirror: "braw-source", Rows: 24, Cols: 80})
	assertErrContains(t, err, "requires sandbox")
}

// waitExit blocks until the PTY session's process has exited (its Done channel
// closes) or fails the test after a timeout.
func waitExit(t *testing.T, sess SessionDriver) {
	t.Helper()

	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session to exit")
	}
}

// stopAndClosePTY detaches the named session's live PTY from the manager, kills
// it if still running, and waits for it to exit before closing. No-op if the
// session has no live PTY.
func stopAndClosePTY(sm *SessionManager, id string) {
	ptySess, ok := sm.GetPTY(id)
	if !ok {
		return
	}

	sm.mu.Lock()
	delete(sm.sessions, id)
	sm.mu.Unlock()

	if !ptySess.Exited() {
		_ = ptySess.Kill()
	}

	<-ptySess.Done()
	ptySess.Close()
}

// newSMWithConfig builds a SessionManager with the given config and paths
// rooted at a single fresh temp dir. Unlike newTestSessionManager it does not
// force approvals on, so callers control the whole config.
func newSMWithConfig(t *testing.T, cfg *config.Config) *SessionManager {
	t.Helper()
	dir := t.TempDir()

	return NewSessionManager(cfg, config.Paths{
		StateFile:  filepath.Join(dir, "state.json"),
		DataDir:    dir,
		LogDir:     dir,
		RuntimeDir: dir,
		// TmpDir must be a real path under the test's temp root — an empty
		// TmpDir makes repoTmpDir join to a relative "repoName/hash" path off
		// the process cwd, which pollutes the source tree in a writable
		// checkout and fails "operation not permitted" in a read-only one.
		TmpDir: filepath.Join(dir, "tmp"),
	}, slog.Default())
}

func TestGenerateID(t *testing.T) {
	t.Run("length", func(t *testing.T) {
		id := generateID()
		if len(id) != 8 {
			t.Errorf("generateID() length = %d, want 8", len(id))
		}
	})

	t.Run("hex characters", func(t *testing.T) {
		id := generateID()
		for _, c := range id {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				t.Errorf("generateID() contains non-hex char %q in %q", c, id)
			}
		}
	})

	t.Run("no collisions across 1000 calls", func(t *testing.T) {
		seen := make(map[string]struct{}, 1000)

		for range 1000 {
			id := generateID()
			if _, ok := seen[id]; ok {
				t.Fatalf("collision detected: %s", id)
			}

			seen[id] = struct{}{}
		}
	})
}

func TestRepoHash(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		h1 := repoHash("/home/user/repo")

		h2 := repoHash("/home/user/repo")
		if h1 != h2 {
			t.Errorf("repoHash not deterministic: %q != %q", h1, h2)
		}
	})

	t.Run("length", func(t *testing.T) {
		h := repoHash("/home/user/repo")
		if len(h) != 12 {
			t.Errorf("repoHash length = %d, want 12", len(h))
		}
	})

	t.Run("different inputs produce different outputs", func(t *testing.T) {
		inputs := []string{
			"/home/user/repo-a",
			"/home/user/repo-b",
			"/tmp/project",
			"/var/src/code",
		}
		seen := make(map[string]string)

		for _, input := range inputs {
			h := repoHash(input)
			if prev, ok := seen[h]; ok {
				t.Errorf("collision: repoHash(%q) == repoHash(%q) == %q", input, prev, h)
			}

			seen[h] = input
		}
	})
}

func TestNewSessionManager(t *testing.T) {
	cfg := config.Default()
	paths := config.Paths{StateFile: filepath.Join(t.TempDir(), "state.json")}
	log := slog.Default()

	sm := NewSessionManager(cfg, paths, log)

	if sm.state == nil {
		t.Fatal("state is nil")
	}

	if sm.state.Sessions == nil {
		t.Fatal("state.Sessions is nil")
	}

	if sm.sessions == nil {
		t.Fatal("sessions map is nil")
	}

	if sm.attachedClients == nil {
		t.Fatal("attachedClients map is nil")
	}

	if sm.hookReports == nil {
		t.Fatal("hookReports map is nil")
	}

	if sm.cfg != cfg {
		t.Error("cfg not set correctly")
	}

	if sm.paths != paths {
		t.Error("paths not set correctly")
	}

	if sm.log != log {
		t.Error("log not set correctly")
	}
}

func TestRename(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "auld-name", Status: StatusRunning,
		}

		if err := sm.Rename("sess1", "bonnie-name"); err != nil {
			t.Fatalf("Rename() error = %v", err)
		}

		s, ok := sm.state.Sessions["sess1"]
		if !ok {
			t.Fatal("session not found after rename")
		}

		if s.Name != "bonnie-name" {
			t.Errorf("Name = %q, want %q", s.Name, "bonnie-name")
		}
	})

	t.Run("not found", func(t *testing.T) {
		sm := newTestSessionManager(t)

		err := sm.Rename("nonexistent", "bonnie-name")
		if err == nil {
			t.Fatal("expected error for nonexistent session")
		}
	})
}

func TestUpdate(t *testing.T) {
	strPtr := func(s string) *string { return &s }

	t.Run("rename via update", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "auld-name", Status: StatusRunning,
		}

		if err := sm.Update("sess1", strPtr("bonnie-name"), nil); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		if sm.state.Sessions["sess1"].Name != "bonnie-name" {
			t.Errorf("Name = %q, want %q", sm.state.Sessions["sess1"].Name, "bonnie-name")
		}
	})

	t.Run("orphan session", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["ben"] = &SessionState{
			ID: "ben", Name: "ben", Status: StatusRunning,
		}
		sm.state.Sessions["bairn"] = &SessionState{
			ID: "bairn", Name: "bairn", ParentID: "ben", Status: StatusRunning,
		}

		if err := sm.Update("bairn", nil, strPtr("")); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		if sm.state.Sessions["bairn"].ParentID != "" {
			t.Errorf("ParentID = %q, want empty", sm.state.Sessions["bairn"].ParentID)
		}
	})

	t.Run("reparent session", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["p1"] = &SessionState{
			ID: "p1", Name: "ben-one", Status: StatusRunning,
		}
		sm.state.Sessions["p2"] = &SessionState{
			ID: "p2", Name: "ben-two", Status: StatusRunning,
		}
		sm.state.Sessions["bairn"] = &SessionState{
			ID: "bairn", Name: "bairn", ParentID: "p1", Status: StatusRunning,
		}

		if err := sm.Update("bairn", nil, strPtr("p2")); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		if sm.state.Sessions["bairn"].ParentID != "p2" {
			t.Errorf("ParentID = %q, want %q", sm.state.Sessions["bairn"].ParentID, "p2")
		}
	})

	t.Run("self-parent rejected", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw-sess", Status: StatusRunning,
		}

		err := sm.Update("sess1", nil, strPtr("sess1"))
		if err == nil {
			t.Fatal("expected error for self-parent")
		}
	})

	t.Run("cycle rejected", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["grandparent"] = &SessionState{
			ID: "grandparent", Name: "brae", Status: StatusRunning,
		}
		sm.state.Sessions["ben"] = &SessionState{
			ID: "ben", Name: "ben", ParentID: "grandparent", Status: StatusRunning,
		}
		sm.state.Sessions["bairn"] = &SessionState{
			ID: "bairn", Name: "bairn", ParentID: "ben", Status: StatusRunning,
		}

		err := sm.Update("grandparent", nil, strPtr("bairn"))
		if err == nil {
			t.Fatal("expected error for cycle")
		}
	})

	t.Run("nonexistent parent rejected", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw-sess", Status: StatusRunning,
		}

		err := sm.Update("sess1", nil, strPtr("nonexistent"))
		if err == nil {
			t.Fatal("expected error for nonexistent parent")
		}
	})

	t.Run("not found", func(t *testing.T) {
		sm := newTestSessionManager(t)

		err := sm.Update("nonexistent", strPtr("bonnie-name"), nil)
		if err == nil {
			t.Fatal("expected error for nonexistent session")
		}
	})

	t.Run("no changes is no-op", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw-sess", Status: StatusRunning,
		}

		err := sm.Update("sess1", nil, nil)
		if err != nil {
			t.Fatalf("Update() with no changes should succeed, got: %v", err)
		}
	})

	t.Run("system session rejected", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sys"] = &SessionState{
			ID: "sys", Name: "orchestrator", Status: StatusRunning,
			SystemKind: SystemKindOrchestrator,
		}

		err := sm.Update("sys", strPtr("bonnie-name"), nil)
		if err == nil {
			t.Fatal("expected error for system session")
		}
	})

	t.Run("combined name and parent", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["p1"] = &SessionState{
			ID: "p1", Name: "ben-one", Status: StatusRunning,
		}
		sm.state.Sessions["p2"] = &SessionState{
			ID: "p2", Name: "ben-two", Status: StatusRunning,
		}
		sm.state.Sessions["bairn"] = &SessionState{
			ID: "bairn", Name: "auld-name", ParentID: "p1", Status: StatusRunning,
		}

		if err := sm.Update("bairn", strPtr("bonnie-name"), strPtr("p2")); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		s := sm.state.Sessions["bairn"]
		if s.Name != "bonnie-name" {
			t.Errorf("Name = %q, want %q", s.Name, "bonnie-name")
		}

		if s.ParentID != "p2" {
			t.Errorf("ParentID = %q, want %q", s.ParentID, "p2")
		}
	})

	t.Run("failed parent does not mutate name", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "auld-name", Status: StatusRunning,
		}

		err := sm.Update("sess1", strPtr("bonnie-name"), strPtr("nonexistent"))
		if err == nil {
			t.Fatal("expected error for nonexistent parent")
		}

		if sm.state.Sessions["sess1"].Name != "auld-name" {
			t.Errorf("Name = %q, want %q (should not have changed)", sm.state.Sessions["sess1"].Name, "auld-name")
		}
	})
}

func TestList(t *testing.T) {
	tests := []struct {
		name     string
		sessions map[string]*SessionState
		wantLen  int
	}{
		{
			name:     "empty",
			sessions: map[string]*SessionState{},
			wantLen:  0,
		},
		{
			name: "single session",
			sessions: map[string]*SessionState{
				"s1": {ID: "s1", Name: "one", Status: StatusRunning},
			},
			wantLen: 1,
		},
		{
			name: "multiple sessions",
			sessions: map[string]*SessionState{
				"s1": {ID: "s1", Name: "one", Status: StatusRunning},
				"s2": {ID: "s2", Name: "two", Status: StatusStopped},
				"s3": {ID: "s3", Name: "three", Status: StatusErrored},
			},
			wantLen: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := newTestSessionManager(t)
			for k, v := range tt.sessions {
				sm.state.Sessions[k] = v
			}

			got := sm.List()
			if len(got) != tt.wantLen {
				t.Errorf("List() returned %d sessions, want %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestGet(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["abc"] = &SessionState{
			ID: "abc", Name: "braw-session", Status: StatusRunning,
		}

		s, ok := sm.Get("abc")
		if !ok {
			t.Fatal("Get() returned not found for existing session")
		}

		if s.ID != "abc" || s.Name != "braw-session" {
			t.Errorf("Get() = %+v, want ID=abc, Name=braw-session", s)
		}
	})

	t.Run("not found", func(t *testing.T) {
		sm := newTestSessionManager(t)

		_, ok := sm.Get("nonexistent")
		if ok {
			t.Error("Get() returned found for nonexistent session")
		}
	})
}

func TestGetPTY(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		sm := newTestSessionManager(t)
		// We can't easily create a real grpty.Session, but we can test the map lookup
		// by checking that a nil entry is returned properly when set.
		sm.sessions["abc"] = nil

		s, ok := sm.GetPTY("abc")
		if !ok {
			t.Fatal("GetPTY() returned not found for existing session")
		}

		if s != nil {
			t.Errorf("GetPTY() = %v, want nil", s)
		}
	})

	t.Run("not found", func(t *testing.T) {
		sm := newTestSessionManager(t)

		_, ok := sm.GetPTY("nonexistent")
		if ok {
			t.Error("GetPTY() returned found for nonexistent session")
		}
	})
}

func TestKickAttachedClient(t *testing.T) {
	t.Run("kick existing client", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
		}

		kicked := false
		mockConn := &net.UnixConn{}
		sm.SetAttachedClient("sess1", mockConn, func() { kicked = true }, nil)

		sm.KickAttachedClient("sess1")

		if !kicked {
			t.Error("kick function was not called")
		}

		// Verify the client was removed
		sm.mu.RLock()
		_, exists := sm.attachedClients["sess1"]
		sm.mu.RUnlock()

		if exists {
			t.Error("attached client was not removed after kick")
		}
	})

	t.Run("kick non-existing client does not panic", func(t *testing.T) {
		sm := newTestSessionManager(t)

		// Should not panic
		sm.KickAttachedClient("nonexistent")
	})
}

func TestSetAndClearAttachedClient(t *testing.T) {
	t.Run("set then clear", func(t *testing.T) {
		sm := newTestSessionManager(t)

		conn := &net.UnixConn{}
		sm.SetAttachedClient("sess1", conn, func() {}, nil)

		// Verify it was set
		sm.mu.RLock()
		_, exists := sm.attachedClients["sess1"]
		sm.mu.RUnlock()

		if !exists {
			t.Fatal("attached client was not set")
		}

		// Clear with the correct conn
		sm.ClearAttachedClient("sess1", conn)

		sm.mu.RLock()
		_, exists = sm.attachedClients["sess1"]
		sm.mu.RUnlock()

		if exists {
			t.Error("attached client was not cleared")
		}
	})

	t.Run("clear with wrong conn does not remove", func(t *testing.T) {
		sm := newTestSessionManager(t)

		conn1 := &net.UnixConn{}
		conn2 := &net.UnixConn{}

		sm.SetAttachedClient("sess1", conn1, func() {}, nil)

		// Try to clear with a different conn
		sm.ClearAttachedClient("sess1", conn2)

		// Should still exist
		sm.mu.RLock()
		_, exists := sm.attachedClients["sess1"]
		sm.mu.RUnlock()

		if !exists {
			t.Error("attached client was incorrectly removed when clearing with wrong conn")
		}
	})

	t.Run("clear nonexistent session does not panic", func(t *testing.T) {
		sm := newTestSessionManager(t)
		conn := &net.UnixConn{}

		// Should not panic
		sm.ClearAttachedClient("nonexistent", conn)
	})
}

func TestIsAttachedClient(t *testing.T) {
	sm := newTestSessionManager(t)
	conn1 := &net.UnixConn{}
	conn2 := &net.UnixConn{}

	sm.SetAttachedClient("sess1", conn1, func() {}, nil)

	if !sm.IsAttachedClient("sess1", conn1) {
		t.Error("expected true for matching conn")
	}

	if sm.IsAttachedClient("sess1", conn2) {
		t.Error("expected false for different conn")
	}

	if sm.IsAttachedClient("nonexistent", conn1) {
		t.Error("expected false for nonexistent session")
	}

	sm.KickAttachedClient("sess1")

	if sm.IsAttachedClient("sess1", conn1) {
		t.Error("expected false after kick")
	}
}

func TestToSessionInfo(t *testing.T) {
	exitCode := 42
	createdAt := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)

	sess := SessionState{
		ID:             "abc123",
		Name:           "braw",
		RepoPath:       "/home/user/croft",
		RepoName:       "croft",
		WorktreePath:   "/home/user/.local/share/graith/worktrees/abc123",
		Branch:         "user/graith/braw-abc123",
		Agent:          "claude",
		AgentSessionID: "session-id-123",
		Model:          "claude-sonnet-4-5-20250514",
		Status:         StatusStopped,
		ExitCode:       &exitCode,
		CreatedAt:      createdAt,
		HookToolName:   "Bash",
	}

	info := toSessionInfo(sess, config.Default(), nil)

	if info.ID != sess.ID {
		t.Errorf("ID = %q, want %q", info.ID, sess.ID)
	}

	if info.Name != sess.Name {
		t.Errorf("Name = %q, want %q", info.Name, sess.Name)
	}

	if info.RepoPath != sess.RepoPath {
		t.Errorf("RepoPath = %q, want %q", info.RepoPath, sess.RepoPath)
	}

	if info.RepoName != sess.RepoName {
		t.Errorf("RepoName = %q, want %q", info.RepoName, sess.RepoName)
	}

	if info.WorktreePath != sess.WorktreePath {
		t.Errorf("WorktreePath = %q, want %q", info.WorktreePath, sess.WorktreePath)
	}

	if info.Branch != sess.Branch {
		t.Errorf("Branch = %q, want %q", info.Branch, sess.Branch)
	}

	if info.Agent != sess.Agent {
		t.Errorf("Agent = %q, want %q", info.Agent, sess.Agent)
	}

	if info.AgentSessionID != sess.AgentSessionID {
		t.Errorf("AgentSessionID = %q, want %q", info.AgentSessionID, sess.AgentSessionID)
	}

	if info.Status != string(sess.Status) {
		t.Errorf("Status = %q, want %q", info.Status, string(sess.Status))
	}

	if info.ExitCode == nil || *info.ExitCode != exitCode {
		t.Errorf("ExitCode = %v, want %d", info.ExitCode, exitCode)
	}

	wantCreatedAt := createdAt.Format(time.RFC3339)
	if info.CreatedAt != wantCreatedAt {
		t.Errorf("CreatedAt = %q, want %q", info.CreatedAt, wantCreatedAt)
	}

	if info.Model != "claude-sonnet-4-5-20250514" {
		t.Errorf("Model = %q, want %q", info.Model, "claude-sonnet-4-5-20250514")
	}

	if info.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want %q", info.ToolName, "Bash")
	}
}

func TestToSessionInfoNilExitCode(t *testing.T) {
	sess := SessionState{
		ID:        "abc",
		Name:      "braw",
		Status:    StatusRunning,
		CreatedAt: time.Now().UTC(),
	}

	info := toSessionInfo(sess, config.Default(), nil)

	if info.ExitCode != nil {
		t.Errorf("ExitCode = %v, want nil", info.ExitCode)
	}
}

func TestIsConfigStale(t *testing.T) {
	agent := config.Agent{
		Command: "claude",
		Args:    []string{"--model", "opus"},
		Sandbox: config.SandboxConfig{Enabled: true, ReadDirs: []string{"/tmp"}},
	}
	cfg := &config.Config{
		Agents:  map[string]config.Agent{"claude": agent},
		Sandbox: config.SandboxConfig{Enabled: true},
	}

	t.Run("nil creation config is not stale", func(t *testing.T) {
		sess := SessionState{Agent: "claude"}
		if isConfigStale(sess, cfg) {
			t.Error("expected not stale when CreationCfg is nil")
		}
	})

	t.Run("matching config is not stale", func(t *testing.T) {
		sess := SessionState{
			Agent: "claude",
			CreationCfg: &CreationConfig{
				Agent:         agent,
				SandboxConfig: cfg.Sandbox.Merge(agent.Sandbox),
			},
		}
		if isConfigStale(sess, cfg) {
			t.Error("expected not stale when config matches")
		}
	})

	t.Run("changed agent args is stale", func(t *testing.T) {
		sess := SessionState{
			Agent: "claude",
			CreationCfg: &CreationConfig{
				Agent:         config.Agent{Command: "claude", Args: []string{"--model", "sonnet"}},
				SandboxConfig: cfg.Sandbox.Merge(agent.Sandbox),
			},
		}
		if !isConfigStale(sess, cfg) {
			t.Error("expected stale when agent args differ")
		}
	})

	t.Run("changed sandbox config is stale", func(t *testing.T) {
		sess := SessionState{
			Agent: "claude",
			CreationCfg: &CreationConfig{
				Agent:         agent,
				SandboxConfig: config.SandboxConfig{Enabled: true, ReadDirs: []string{"/old"}},
			},
		}
		if !isConfigStale(sess, cfg) {
			t.Error("expected stale when sandbox config differs")
		}
	})

	t.Run("changed global sandbox is stale", func(t *testing.T) {
		sess := SessionState{
			Agent: "claude",
			CreationCfg: &CreationConfig{
				Agent:         agent,
				SandboxConfig: cfg.Sandbox.Merge(agent.Sandbox),
			},
		}

		changedCfg := &config.Config{
			Agents:  map[string]config.Agent{"claude": agent},
			Sandbox: config.SandboxConfig{Enabled: true, WriteDirs: []string{"/new"}},
		}
		if !isConfigStale(sess, changedCfg) {
			t.Error("expected stale when global sandbox config differs")
		}
	})

	t.Run("removed agent is stale", func(t *testing.T) {
		sess := SessionState{
			Agent: "codex",
			CreationCfg: &CreationConfig{
				Agent: config.Agent{Command: "codex"},
			},
		}
		if !isConfigStale(sess, cfg) {
			t.Error("expected stale when agent no longer exists")
		}
	})
}

func TestIsConfigStaleOrchestrator(t *testing.T) {
	agent := config.Agent{
		Command: "claude",
		Args:    []string{"--model", "opus"},
		Sandbox: config.SandboxConfig{Enabled: true, ReadDirs: []string{"/tmp"}},
	}
	cfg := &config.Config{
		Agents:  map[string]config.Agent{"claude": agent},
		Sandbox: config.SandboxConfig{Enabled: true},
		Orchestrator: config.OrchestratorConfig{
			Sandbox: config.OrchestratorSandboxConfig{
				WriteDirs: []string{"~/.config/graith"},
			},
		},
	}

	t.Run("orchestrator uses three-layer merge", func(t *testing.T) {
		sess := SessionState{
			Agent:      "claude",
			SystemKind: SystemKindOrchestrator,
			CreationCfg: &CreationConfig{
				Agent:         agent,
				SandboxConfig: cfg.OrchestratorSandboxMerged("claude"),
			},
		}
		if isConfigStale(sess, cfg) {
			t.Error("expected not stale when orchestrator config matches")
		}
	})

	t.Run("orchestrator stale when orchestrator sandbox changes", func(t *testing.T) {
		sess := SessionState{
			Agent:      "claude",
			SystemKind: SystemKindOrchestrator,
			CreationCfg: &CreationConfig{
				Agent:         agent,
				SandboxConfig: cfg.OrchestratorSandboxMerged("claude"),
			},
		}

		changedCfg := &config.Config{
			Agents:  map[string]config.Agent{"claude": agent},
			Sandbox: config.SandboxConfig{Enabled: true},
			Orchestrator: config.OrchestratorConfig{
				Sandbox: config.OrchestratorSandboxConfig{
					WriteDirs: []string{"~/.config/graith", "/extra"},
				},
			},
		}
		if !isConfigStale(sess, changedCfg) {
			t.Error("expected stale when orchestrator sandbox dirs change")
		}
	})

	t.Run("non-orchestrator unaffected by orchestrator sandbox", func(t *testing.T) {
		sess := SessionState{
			Agent: "claude",
			CreationCfg: &CreationConfig{
				Agent:         agent,
				SandboxConfig: cfg.Sandbox.Merge(agent.Sandbox),
			},
		}
		if isConfigStale(sess, cfg) {
			t.Error("non-orchestrator session should not be stale from orchestrator sandbox")
		}
	})
}

func TestResolveSandboxIgnoresOrchestratorLayer(t *testing.T) {
	t.Run("orchestrator sandbox dirs do not affect resolveSandbox", func(t *testing.T) {
		cfg := config.Default()
		cfg.Sandbox = config.SandboxConfig{Enabled: true}
		cfg.Agents["claude"] = config.Agent{
			Command: "claude",
			Sandbox: config.SandboxConfig{ReadDirs: []string{"/agent-dir"}},
		}

		smWithout := newSMWithConfig(t, cfg)

		cfgWith := config.Default()
		cfgWith.Sandbox = config.SandboxConfig{Enabled: true}
		cfgWith.Agents["claude"] = config.Agent{
			Command: "claude",
			Sandbox: config.SandboxConfig{ReadDirs: []string{"/agent-dir"}},
		}
		cfgWith.Orchestrator = config.OrchestratorConfig{
			Sandbox: config.OrchestratorSandboxConfig{
				ReadDirs:  []string{"/orch-read"},
				WriteDirs: []string{"/orch-write"},
			},
		}

		smWith := newSMWithConfig(t, cfgWith)

		resultWithout, errWithout := smWithout.resolveSandbox("claude")
		resultWith, errWith := smWith.resolveSandbox("claude")

		if errWithout != nil && errWith != nil {
			if errWithout.Error() != errWith.Error() {
				t.Errorf("resolveSandbox errors differ: without=%v, with=%v", errWithout, errWith)
			}
		} else if errWithout != errWith {
			t.Errorf("resolveSandbox error presence differs: without=%v, with=%v", errWithout, errWith)
		}

		if resultWithout != resultWith {
			t.Errorf("resolveSandbox result differs: without=%v, with=%v", resultWithout, resultWith)
		}
	})

	t.Run("orchestrator sandbox cannot enable sandboxing", func(t *testing.T) {
		cfg := config.Default()
		cfg.Sandbox = config.SandboxConfig{Enabled: false}
		cfg.Agents["claude"] = config.Agent{Command: "claude"}
		cfg.Orchestrator = config.OrchestratorConfig{
			Sandbox: config.OrchestratorSandboxConfig{
				WriteDirs: []string{"/orch-write"},
			},
		}

		sm := newSMWithConfig(t, cfg)

		result, _ := sm.resolveSandbox("claude")
		if result {
			t.Error("orchestrator sandbox dirs should not enable sandboxing when global+agent has it disabled")
		}
	})
}

func TestIsConfigStaleOrchestratorGlobalChange(t *testing.T) {
	agent := config.Agent{
		Command: "claude",
		Sandbox: config.SandboxConfig{ReadDirs: []string{"/agent"}},
	}
	cfg := &config.Config{
		Agents:  map[string]config.Agent{"claude": agent},
		Sandbox: config.SandboxConfig{Enabled: true, WriteDirs: []string{"/global"}},
		Orchestrator: config.OrchestratorConfig{
			Sandbox: config.OrchestratorSandboxConfig{
				WriteDirs: []string{"/orch"},
			},
		},
	}

	sess := SessionState{
		Agent:      "claude",
		SystemKind: SystemKindOrchestrator,
		CreationCfg: &CreationConfig{
			Agent:         agent,
			SandboxConfig: cfg.OrchestratorSandboxMerged("claude"),
		},
	}

	changedCfg := &config.Config{
		Agents:  map[string]config.Agent{"claude": agent},
		Sandbox: config.SandboxConfig{Enabled: true, WriteDirs: []string{"/global", "/new-global"}},
		Orchestrator: config.OrchestratorConfig{
			Sandbox: config.OrchestratorSandboxConfig{
				WriteDirs: []string{"/orch"},
			},
		},
	}
	if !isConfigStale(sess, changedCfg) {
		t.Error("orchestrator should be stale when global sandbox changes")
	}
}

func TestIdleTracking(t *testing.T) {
	t.Run("idle since set when detached and ready", func(t *testing.T) {
		sm := newTestSessionManager(t)
		s := &SessionState{
			ID: "s1", Name: "braw", Status: StatusRunning,
			Agent: "claude", AgentStatus: "ready",
		}
		sm.state.Sessions["s1"] = s

		if s.IdleSince != nil {
			t.Fatal("IdleSince should be nil initially")
		}

		sm.checkIdleSession(s)

		if s.IdleSince == nil {
			t.Fatal("IdleSince should be set for detached+ready session")
		}
	})

	t.Run("idle since cleared when client attached", func(t *testing.T) {
		sm := newTestSessionManager(t)
		now := time.Now()
		s := &SessionState{
			ID: "s1", Name: "braw", Status: StatusRunning,
			Agent: "claude", AgentStatus: "ready", IdleSince: &now,
		}
		sm.state.Sessions["s1"] = s
		sm.SetAttachedClient("s1", &net.UnixConn{}, func() {}, nil)

		sm.checkIdleSession(s)

		if s.IdleSince != nil {
			t.Error("IdleSince should be cleared when client is attached")
		}
	})

	t.Run("idle since cleared when agent active", func(t *testing.T) {
		sm := newTestSessionManager(t)
		now := time.Now()
		s := &SessionState{
			ID: "s1", Name: "braw", Status: StatusRunning,
			Agent: "claude", AgentStatus: "active", IdleSince: &now,
		}
		sm.state.Sessions["s1"] = s

		sm.checkIdleSession(s)

		if s.IdleSince != nil {
			t.Error("IdleSince should be cleared when agent is active")
		}
	})

	t.Run("not re-set on subsequent checks", func(t *testing.T) {
		sm := newTestSessionManager(t)
		s := &SessionState{
			ID: "s1", Name: "braw", Status: StatusRunning,
			Agent: "claude", AgentStatus: "ready",
		}
		sm.state.Sessions["s1"] = s

		sm.checkIdleSession(s)
		first := *s.IdleSince

		time.Sleep(time.Millisecond)
		sm.checkIdleSession(s)

		if !s.IdleSince.Equal(first) {
			t.Error("IdleSince should not be updated on subsequent checks")
		}
	})

	t.Run("returns true when idle exceeds timeout", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.cfg.Agents["claude"] = config.Agent{
			Command:     "claude",
			ResumeArgs:  []string{"--resume"},
			IdleTimeout: "100ms",
		}
		past := time.Now().Add(-200 * time.Millisecond)
		s := &SessionState{
			ID: "s1", Name: "braw", Status: StatusRunning,
			Agent: "claude", AgentStatus: "ready", IdleSince: &past,
		}
		sm.state.Sessions["s1"] = s

		shouldStop := sm.checkIdleSession(s)

		if !shouldStop {
			t.Error("should return true when idle duration exceeds timeout")
		}
	})

	t.Run("returns false when idle within timeout", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.cfg.Agents["claude"] = config.Agent{
			Command:     "claude",
			ResumeArgs:  []string{"--resume"},
			IdleTimeout: "1h",
		}
		now := time.Now()
		s := &SessionState{
			ID: "s1", Name: "braw", Status: StatusRunning,
			Agent: "claude", AgentStatus: "ready", IdleSince: &now,
		}
		sm.state.Sessions["s1"] = s

		shouldStop := sm.checkIdleSession(s)

		if shouldStop {
			t.Error("should return false when idle duration is within timeout")
		}
	})

	t.Run("disabled timeout never stops", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.cfg.Agents["codex"] = config.Agent{
			Command:     "codex",
			IdleTimeout: "0",
		}
		past := time.Now().Add(-24 * time.Hour)
		s := &SessionState{
			ID: "s1", Name: "braw", Status: StatusRunning,
			Agent: "codex", AgentStatus: "ready", IdleSince: &past,
		}
		sm.state.Sessions["s1"] = s

		shouldStop := sm.checkIdleSession(s)

		if shouldStop {
			t.Error("should never stop when idle timeout is disabled")
		}
	})
}

func TestHandleHookReport(t *testing.T) {
	t.Run("active event", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1",
			Event:     "PreToolUse",
			ToolName:  "Bash",
		})

		sm.mu.RLock()
		report, ok := sm.hookReports["sess1"]
		sm.mu.RUnlock()

		if !ok {
			t.Fatal("hookReport not found for sess1")
		}

		if report.Status != "active" {
			t.Errorf("Status = %q, want %q", report.Status, "active")
		}

		if report.Event != "PreToolUse" {
			t.Errorf("Event = %q, want %q", report.Event, "PreToolUse")
		}
		// AuthoritativeUntil should be ~30s in the future
		untilDelta := time.Until(report.AuthoritativeUntil)
		if untilDelta < 29*time.Second || untilDelta > 31*time.Second {
			t.Errorf("AuthoritativeUntil delta = %v, want ~30s", untilDelta)
		}
	})

	t.Run("approval event", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID:        "sess1",
			Event:            "Notification",
			NotificationType: "permission_prompt",
		})

		sm.mu.RLock()
		report, ok := sm.hookReports["sess1"]
		sm.mu.RUnlock()

		if !ok {
			t.Fatal("hookReport not found for sess1")
		}

		if report.Status != "approval" {
			t.Errorf("Status = %q, want %q", report.Status, "approval")
		}
		// AuthoritativeUntil should be ~30 minutes in the future (sticky)
		untilDelta := time.Until(report.AuthoritativeUntil)
		if untilDelta < 29*time.Minute || untilDelta > 31*time.Minute {
			t.Errorf("AuthoritativeUntil delta = %v, want ~30m", untilDelta)
		}
	})

	// PermissionRequest is Codex's approval hook and must keep mapping to
	// approval (it carries no subtype).
	t.Run("PermissionRequest maps to approval", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["speir"] = &SessionState{
			ID: "speir", Name: "speir", Status: StatusRunning,
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "speir",
			Event:     "PermissionRequest",
		})

		sm.mu.RLock()
		report, ok := sm.hookReports["speir"]
		sm.mu.RUnlock()

		if !ok {
			t.Fatal("hookReport not found for speir")
		}

		if report.Status != "approval" {
			t.Errorf("Status = %q, want %q", report.Status, "approval")
		}
	})

	t.Run("ready event", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1",
			Event:     "Stop",
		})

		sm.mu.RLock()
		report, ok := sm.hookReports["sess1"]
		sm.mu.RUnlock()

		if !ok {
			t.Fatal("hookReport not found for sess1")
		}

		if report.Status != "ready" {
			t.Errorf("Status = %q, want %q", report.Status, "ready")
		}
	})

	t.Run("unknown session", func(t *testing.T) {
		sm := newTestSessionManager(t)

		// Should not panic
		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "nonexistent",
			Event:     "PreToolUse",
		})

		sm.mu.RLock()
		_, ok := sm.hookReports["nonexistent"]
		sm.mu.RUnlock()

		if ok {
			t.Error("hookReport should not be created for unknown session")
		}
	})

	t.Run("unknown event", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1",
			Event:     "SomeFutureEvent",
		})

		sm.mu.RLock()
		_, ok := sm.hookReports["sess1"]
		sm.mu.RUnlock()

		if ok {
			t.Error("hookReport should not be created for unknown event")
		}
	})

	t.Run("status change updates AgentStatus", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
			AgentStatus: "active",
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1",
			Event:     "Stop",
		})

		sm.mu.RLock()
		sess := sm.state.Sessions["sess1"]
		agentStatus := sess.AgentStatus

		sm.mu.RUnlock()

		if agentStatus != "ready" {
			t.Errorf("AgentStatus = %q, want %q", agentStatus, "ready")
		}
	})

	t.Run("tool name stored", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1",
			Event:     "PreToolUse",
			ToolName:  "Bash",
		})

		sm.mu.RLock()
		report, ok := sm.hookReports["sess1"]
		sess := sm.state.Sessions["sess1"]
		sm.mu.RUnlock()

		if !ok {
			t.Fatal("hookReport not found for sess1")
		}

		if report.ToolName != "Bash" {
			t.Errorf("ToolName = %q, want %q", report.ToolName, "Bash")
		}

		if sess.HookToolName != "Bash" {
			t.Errorf("sess.HookToolName = %q, want %q", sess.HookToolName, "Bash")
		}
	})

	// Notification is now subtype-aware: idle_prompt -> ready, permission_prompt
	// -> approval, and every other subtype (including empty/unparsed) leaves the
	// status untouched. The empty case is the regression guard — the pre-subtype
	// code mapped every Notification to approval, so a timed-out/unparsed hook
	// spuriously flagged a session as needing attention.
	t.Run("notification idle_prompt maps to ready", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["ken"] = &SessionState{
			ID: "ken", Name: "ken", Status: StatusRunning, AgentStatus: "active",
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID:        "ken",
			Event:            "Notification",
			NotificationType: "idle_prompt",
		})

		sm.mu.RLock()
		report, ok := sm.hookReports["ken"]
		agentStatus := sm.state.Sessions["ken"].AgentStatus
		sm.mu.RUnlock()

		if !ok {
			t.Fatal("hookReport not found for ken")
		}

		if report.Status != "ready" {
			t.Errorf("Status = %q, want %q", report.Status, "ready")
		}

		if agentStatus != "ready" {
			t.Errorf("AgentStatus = %q, want %q", agentStatus, "ready")
		}
		// idle_prompt is sticky like Stop (~30m).
		untilDelta := time.Until(report.AuthoritativeUntil)
		if untilDelta < 29*time.Minute || untilDelta > 31*time.Minute {
			t.Errorf("AuthoritativeUntil delta = %v, want ~30m", untilDelta)
		}
	})

	t.Run("notification permission_prompt maps to approval", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["haar"] = &SessionState{
			ID: "haar", Name: "haar", Status: StatusRunning, AgentStatus: "active",
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID:        "haar",
			Event:            "Notification",
			NotificationType: "permission_prompt",
		})

		sm.mu.RLock()
		agentStatus := sm.state.Sessions["haar"].AgentStatus
		sm.mu.RUnlock()

		if agentStatus != "approval" {
			t.Errorf("AgentStatus = %q, want %q", agentStatus, "approval")
		}
	})

	// Informational and unparsed subtypes must not touch status — no hookReport
	// is stored and AgentStatus is left as-is.
	noChangeSubtypes := map[string]string{
		"auth_success":       "auth_success",
		"elicitation_start":  "elicitation_start",
		"elicitation_finish": "elicitation_finish",
		"empty (unparsed)":   "",
	}
	for name, subtype := range noChangeSubtypes {
		t.Run("notification "+name+" leaves status unchanged", func(t *testing.T) {
			sm := newTestSessionManager(t)
			sm.state.Sessions["thrawn"] = &SessionState{
				ID: "thrawn", Name: "thrawn", Status: StatusRunning, AgentStatus: "active",
			}

			sm.HandleHookReport(protocol.StatusReportMsg{
				SessionID:        "thrawn",
				Event:            "Notification",
				NotificationType: subtype,
			})

			sm.mu.RLock()
			_, reported := sm.hookReports["thrawn"]
			agentStatus := sm.state.Sessions["thrawn"].AgentStatus
			sm.mu.RUnlock()

			if reported {
				t.Errorf("subtype %q created a hookReport, want none", subtype)
			}

			if agentStatus != "active" {
				t.Errorf("subtype %q: AgentStatus = %q, want unchanged %q", subtype, agentStatus, "active")
			}
		})
	}

	// Regression: a timed-out/unparsed Notification arrives with an empty
	// subtype. It must NOT become approval (the old code path mapped every
	// Notification to approval, so an idle session with a slow hook flipped to
	// "needs attention").
	t.Run("empty notification does not become approval (regression)", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["dreich"] = &SessionState{
			ID: "dreich", Name: "dreich", Status: StatusRunning, AgentStatus: "ready",
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "dreich",
			Event:     "Notification",
			// NotificationType intentionally empty (stdin didn't parse).
		})

		sm.mu.RLock()
		agentStatus := sm.state.Sessions["dreich"].AgentStatus
		sm.mu.RUnlock()

		if agentStatus == "approval" {
			t.Fatal("empty Notification became approval; regressed to pre-subtype behaviour")
		}

		if agentStatus != "ready" {
			t.Errorf("AgentStatus = %q, want unchanged %q", agentStatus, "ready")
		}
	})
}

func TestHandleHookReportContextPressure(t *testing.T) {
	t.Run("PreCompact sets pressure and leaves status active", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
			AgentStatus: "active",
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1",
			Event:     "PreCompact",
			Trigger:   "auto",
		})

		sm.mu.RLock()
		sess := sm.state.Sessions["sess1"]
		pressure := sess.ContextPressure
		pressureAt := sess.ContextPressureAt
		agentStatus := sess.AgentStatus
		_, hasReport := sm.hookReports["sess1"]
		sm.mu.RUnlock()

		if !pressure {
			t.Error("ContextPressure = false, want true after PreCompact")
		}

		if pressureAt.IsZero() {
			t.Error("ContextPressureAt not set after PreCompact")
		}

		if agentStatus != "active" {
			t.Errorf("AgentStatus = %q, want active (PreCompact must not change it)", agentStatus)
		}

		if hasReport {
			t.Error("PreCompact should not write a hookReport (runtime-signal only)")
		}
	})

	t.Run("PostCompact clears pressure", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
			AgentStatus:     "approval",
			ContextPressure: true,
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1",
			Event:     "PostCompact",
			Trigger:   "manual",
		})

		sm.mu.RLock()
		sess := sm.state.Sessions["sess1"]
		pressure := sess.ContextPressure
		agentStatus := sess.AgentStatus

		sm.mu.RUnlock()

		if pressure {
			t.Error("ContextPressure = true, want false after PostCompact")
		}

		// PostCompact must not clobber a pending approval.
		if agentStatus != "approval" {
			t.Errorf("AgentStatus = %q, want approval (PostCompact must not change it)", agentStatus)
		}
	})

	t.Run("SessionStart clears pressure and sub-agents", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
			ContextPressure: true,
			SubAgents:       map[string]string{"bairn-1": "canny"},
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1",
			Event:     "SessionStart",
		})

		sm.mu.RLock()
		sess := sm.state.Sessions["sess1"]
		sm.mu.RUnlock()

		if sess.ContextPressure {
			t.Error("ContextPressure not cleared on SessionStart")
		}

		if len(sess.SubAgents) != 0 {
			t.Errorf("SubAgents len = %d, want 0 after SessionStart", len(sess.SubAgents))
		}
	})

	t.Run("unknown session does not panic", func(t *testing.T) {
		sm := newTestSessionManager(t)

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "dreich",
			Event:     "PreCompact",
		})
	})
}

func TestHandleHookReportSubagents(t *testing.T) {
	t.Run("start then stop updates the map", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
			AgentStatus: "active",
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1", Event: "SubagentStart",
			AgentID: "bairn-1", AgentType: "canny",
		})
		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1", Event: "SubagentStart",
			AgentID: "bairn-2", AgentType: "thrawn",
		})

		sm.mu.RLock()
		sess := sm.state.Sessions["sess1"]
		count := len(sess.SubAgents)
		typ := sess.SubAgents["bairn-1"]
		status := sess.AgentStatus

		sm.mu.RUnlock()

		if count != 2 {
			t.Fatalf("SubAgents len = %d, want 2", count)
		}

		if typ != "canny" {
			t.Errorf("SubAgents[bairn-1] = %q, want canny", typ)
		}

		if status != "active" {
			t.Errorf("AgentStatus = %q, want active (subagent events must not change it)", status)
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1", Event: "SubagentStop", AgentID: "bairn-1",
		})

		sm.mu.RLock()
		count = len(sm.state.Sessions["sess1"].SubAgents)
		sm.mu.RUnlock()

		if count != 1 {
			t.Errorf("SubAgents len = %d, want 1 after one stop", count)
		}
	})

	t.Run("duplicate stop does not underflow", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
			SubAgents: map[string]string{"bairn-1": "canny"},
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1", Event: "SubagentStop", AgentID: "bairn-1",
		})
		// Duplicate stop for the same id — idempotent no-op.
		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1", Event: "SubagentStop", AgentID: "bairn-1",
		})

		sm.mu.RLock()
		count := len(sm.state.Sessions["sess1"].SubAgents)
		sm.mu.RUnlock()

		if count != 0 {
			t.Errorf("SubAgents len = %d, want 0 (duplicate stop must not underflow/strand)", count)
		}
	})

	t.Run("stop for missing id is a no-op", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
			SubAgents: map[string]string{"bairn-1": "canny"},
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1", Event: "SubagentStop", AgentID: "haar-unknown",
		})

		sm.mu.RLock()
		sess := sm.state.Sessions["sess1"]
		count := len(sess.SubAgents)
		still := sess.SubAgents["bairn-1"]

		sm.mu.RUnlock()

		if count != 1 {
			t.Errorf("SubAgents len = %d, want 1 (missing-id stop must not strand)", count)
		}

		if still != "canny" {
			t.Errorf("SubAgents[bairn-1] = %q, want canny (untouched)", still)
		}
	})

	t.Run("start with empty agent_id is skipped", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1", Event: "SubagentStart", AgentType: "canny",
		})

		sm.mu.RLock()
		count := len(sm.state.Sessions["sess1"].SubAgents)
		sm.mu.RUnlock()

		if count != 0 {
			t.Errorf("SubAgents len = %d, want 0 (id-less start is unusable, skipped)", count)
		}
	})

	t.Run("subagent events preserve ready status", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
			AgentStatus: "ready",
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1", Event: "SubagentStart",
			AgentID: "bairn-1", AgentType: "canny",
		})

		sm.mu.RLock()
		status := sm.state.Sessions["sess1"].AgentStatus
		sm.mu.RUnlock()

		if status != "ready" {
			t.Errorf("AgentStatus = %q, want ready (subagent start must not clobber it)", status)
		}
	})
}

func TestToSessionInfoContextSubagent(t *testing.T) {
	sess := SessionState{
		ID: "sess1", Name: "braw", Status: StatusRunning,
		ContextPressure: true,
		SubAgents:       map[string]string{"bairn-1": "canny", "bairn-2": "thrawn"},
	}

	info := toSessionInfo(sess, config.Default(), nil)

	if !info.ContextPressure {
		t.Error("SessionInfo.ContextPressure = false, want true")
	}

	if info.SubAgentCount != 2 {
		t.Errorf("SessionInfo.SubAgentCount = %d, want 2", info.SubAgentCount)
	}

	// A clean session projects zero-values (omitempty on the wire).
	clean := toSessionInfo(SessionState{ID: "canny", Status: StatusRunning}, config.Default(), nil)
	if clean.ContextPressure {
		t.Error("clean SessionInfo.ContextPressure = true, want false")
	}

	if clean.SubAgentCount != 0 {
		t.Errorf("clean SessionInfo.SubAgentCount = %d, want 0", clean.SubAgentCount)
	}
}

func TestMapSessionEndReason(t *testing.T) {
	cases := []struct {
		reason  string
		want    string
		wantOK  bool
		comment string
	}{
		{"logout", StopReasonUser, true, "human logged out"},
		{"prompt_input_exit", StopReasonUser, true, "human exited the prompt"},
		{"clear", "", false, "/clear is not a process exit"},
		{"resume", "", false, "/resume is not a process exit"},
		{"other", "", false, "other is not proof of a clean exit"},
		{"", "", false, "empty reason maps to nothing"},
		{"future_reason", "", false, "unknown reason is not a clean exit"},
	}

	for _, c := range cases {
		got, ok := mapSessionEndReason(c.reason)
		if got != c.want || ok != c.wantOK {
			t.Errorf("mapSessionEndReason(%q) = (%q, %v), want (%q, %v) — %s",
				c.reason, got, ok, c.want, c.wantOK, c.comment)
		}
	}
}

func TestHandleHookReportSessionEnd(t *testing.T) {
	t.Run("records reason bound to generation without touching AgentStatus", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
			AgentStatus: "active", PIDStartTime: 4242,
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1",
			Event:     "SessionEnd",
			Reason:    "logout",
		})

		sm.mu.RLock()
		sess := sm.state.Sessions["sess1"]
		endReason := sess.SessionEndReason
		gen := sess.SessionEndReasonGen
		agentStatus := sess.AgentStatus
		status := sess.Status
		stopReason := sess.StopReason
		_, hasReport := sm.hookReports["sess1"]
		sm.mu.RUnlock()

		if endReason != "logout" {
			t.Errorf("SessionEndReason = %q, want %q", endReason, "logout")
		}

		if gen != 4242 {
			t.Errorf("SessionEndReasonGen = %d, want 4242 (bound to PIDStartTime)", gen)
		}

		if agentStatus != "active" {
			t.Errorf("AgentStatus = %q, want it left as %q (SessionEnd must not touch it)", agentStatus, "active")
		}

		if status != StatusRunning {
			t.Errorf("Status = %q, want %q — SessionEnd(resume/clear) must not stop the session", status, StatusRunning)
		}

		if stopReason != "" {
			t.Errorf("StopReason = %q, want empty — SessionEnd must not set it directly", stopReason)
		}

		if hasReport {
			t.Error("SessionEnd must not create a hookReport")
		}
	})

	t.Run("unknown session is a no-op", func(t *testing.T) {
		sm := newTestSessionManager(t)

		// Must not panic.
		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "dreich-nonexistent",
			Event:     "SessionEnd",
			Reason:    "logout",
		})
	})

	t.Run("SessionStart clears a pending reason and last message", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "braw", Status: StatusRunning,
			SessionEndReason: "clear", SessionEndReasonGen: 7, LastMessage: "auld output",
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1",
			Event:     "SessionStart",
		})

		sm.mu.RLock()
		sess := sm.state.Sessions["sess1"]
		endReason := sess.SessionEndReason
		gen := sess.SessionEndReasonGen
		lastMsg := sess.LastMessage

		sm.mu.RUnlock()

		if endReason != "" || gen != 0 {
			t.Errorf("after SessionStart: reason=%q gen=%d, want cleared", endReason, gen)
		}

		if lastMsg != "" {
			t.Errorf("after SessionStart: LastMessage = %q, want cleared", lastMsg)
		}
	})
}

func TestHandleHookReportStopCapturesLastMessage(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.state.Sessions["sess1"] = &SessionState{
		ID: "sess1", Name: "braw", Status: StatusRunning,
	}

	sm.HandleHookReport(protocol.StatusReportMsg{
		SessionID:   "sess1",
		Event:       "Stop",
		LastMessage: "the bonnie result is ready",
	})

	sm.mu.RLock()
	sess := sm.state.Sessions["sess1"]
	lastMsg := sess.LastMessage
	agentStatus := sess.AgentStatus

	sm.mu.RUnlock()

	if lastMsg != "the bonnie result is ready" {
		t.Errorf("LastMessage = %q, want captured final message", lastMsg)
	}

	if agentStatus != "ready" {
		t.Errorf("AgentStatus = %q, want %q (Stop still maps to ready)", agentStatus, "ready")
	}
}

// watchSessionExit drives a session through a real PTY exit and returns the
// finalized StopReason. The caller pre-populates state.Sessions[id] (including
// any pending SessionEndReason) to model what the hooks had recorded.
func watchSessionExit(t *testing.T, sm *SessionManager, id string) string {
	t.Helper()

	sess := newTestPTYSession(t, "true")
	waitExit(t, sess)
	sm.sessions[id] = sess
	sm.watchSession(id, sess)

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return sm.state.Sessions[id].StopReason
}

// TestSessionEndCleanShutdownLabelling is the regression test for the
// clean-vs-crash mislabelling: a SessionEnd(logout) recorded before the PTY
// exits must yield StopReasonUser, not StopReasonCrash. It fails against the old
// exit seam (which defaulted straight to crash) and passes with the mapping.
func TestSessionEndCleanShutdownLabelling(t *testing.T) {
	sm := newTestSessionManager(t)

	id := "sess-logout"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "braw", Status: StatusRunning, Agent: "claude",
		SessionEndReason: "logout", // gen 0 matches the manual state's PIDStartTime 0
	}

	if got := watchSessionExit(t, sm, id); got != StopReasonUser {
		t.Errorf("StopReason = %q, want %q — clean logout must not be labelled a crash", got, StopReasonUser)
	}
}

func TestSessionEndExitMappingAdverse(t *testing.T) {
	t.Run("clear then SessionStart then crash yields crash", func(t *testing.T) {
		sm := newTestSessionManager(t)

		id := "sess-clear"
		sm.state.Sessions[id] = &SessionState{
			ID: id, Name: "canny", Status: StatusRunning, Agent: "claude",
		}

		// Claude fires SessionEnd(clear) on /clear WITHOUT exiting, then a fresh
		// SessionStart, then — much later — the process actually crashes.
		sm.HandleHookReport(protocol.StatusReportMsg{SessionID: id, Event: "SessionEnd", Reason: "clear"})
		sm.HandleHookReport(protocol.StatusReportMsg{SessionID: id, Event: "SessionStart"})

		if got := watchSessionExit(t, sm, id); got != StopReasonCrash {
			t.Errorf("StopReason = %q, want %q — a stale clear reason must not label the later crash clean", got, StopReasonCrash)
		}
	})

	t.Run("resume reason falls back to crash on exit", func(t *testing.T) {
		sm := newTestSessionManager(t)

		id := "sess-resume"
		sm.state.Sessions[id] = &SessionState{
			ID: id, Name: "bide", Status: StatusRunning, Agent: "claude",
			SessionEndReason: "resume",
		}

		if got := watchSessionExit(t, sm, id); got != StopReasonCrash {
			t.Errorf("StopReason = %q, want %q — resume is not a process-ending reason", got, StopReasonCrash)
		}
	})

	t.Run("other reason falls back to crash", func(t *testing.T) {
		sm := newTestSessionManager(t)

		id := "sess-other"
		sm.state.Sessions[id] = &SessionState{
			ID: id, Name: "haar", Status: StatusRunning, Agent: "claude",
			SessionEndReason: "other",
		}

		if got := watchSessionExit(t, sm, id); got != StopReasonCrash {
			t.Errorf("StopReason = %q, want %q", got, StopReasonCrash)
		}
	})

	t.Run("explicit gr stop takes precedence over a pending reason", func(t *testing.T) {
		sm := newTestSessionManager(t)

		id := "sess-thrawn"
		sm.state.Sessions[id] = &SessionState{
			ID: id, Name: "thrawn", Status: StatusRunning, Agent: "claude",
			// A gr stop already set a reason; a pending SessionEnd(logout) must
			// NOT overwrite it at the exit seam (already-set wins).
			StopReason:       StopReasonShutdown,
			SessionEndReason: "logout",
		}

		if got := watchSessionExit(t, sm, id); got != StopReasonShutdown {
			t.Errorf("StopReason = %q, want %q — an already-set reason must win over a pending SessionEnd", got, StopReasonShutdown)
		}
	})

	t.Run("stale generation reason is ignored", func(t *testing.T) {
		sm := newTestSessionManager(t)

		id := "sess-stale-gen"
		sm.state.Sessions[id] = &SessionState{
			ID: id, Name: "haar", Status: StatusRunning, Agent: "claude",
			SessionEndReason: "logout",
			// Recorded against a different process generation than the one exiting
			// (PIDStartTime is 0 for this manual state) — must not be consumed.
			SessionEndReasonGen: 99999,
		}

		if got := watchSessionExit(t, sm, id); got != StopReasonCrash {
			t.Errorf("StopReason = %q, want %q — a reason from a stale generation must be ignored", got, StopReasonCrash)
		}
	})
}

func TestDetectAgentStatusesHookAuthority(t *testing.T) {
	// Test that a valid hook report takes precedence over scraping.
	// We can't easily test the full detectAgentStatuses (needs real PTY),
	// but we can test the hookReports lookup logic directly.
	sm := newTestSessionManager(t)

	t.Run("authoritative hook is trusted", func(t *testing.T) {
		sm.mu.Lock()
		sm.hookReports["s1"] = hookReport{
			Status:             "approval",
			Event:              "Notification",
			ReportedAt:         time.Now(),
			AuthoritativeUntil: time.Now().Add(30 * time.Minute),
		}
		sm.mu.Unlock()

		sm.mu.RLock()
		hr, ok := sm.hookReports["s1"]
		sm.mu.RUnlock()

		if !ok {
			t.Fatal("hookReport not found")
		}

		if hr.Status != "approval" {
			t.Errorf("Status = %q, want %q", hr.Status, "approval")
		}

		if !time.Now().Before(hr.AuthoritativeUntil) {
			t.Error("hook should still be authoritative")
		}
	})

	t.Run("expired hook falls through", func(t *testing.T) {
		sm.mu.Lock()
		sm.hookReports["s2"] = hookReport{
			Status:             "active",
			Event:              "PreToolUse",
			ReportedAt:         time.Now().Add(-1 * time.Minute),
			AuthoritativeUntil: time.Now().Add(-30 * time.Second),
		}
		sm.mu.Unlock()

		sm.mu.RLock()
		hr, ok := sm.hookReports["s2"]
		sm.mu.RUnlock()

		if !ok {
			t.Fatal("hookReport not found")
		}

		if time.Now().Before(hr.AuthoritativeUntil) {
			t.Error("hook should be expired")
		}
	})
}

func TestDetectAgentStatuses_MirrorSkipsGit(t *testing.T) {
	sm := newTestSessionManager(t)

	repoDir := initDirtyRepo(t)

	logDir := t.TempDir()

	sharedPty, err := grpty.NewSession(grpty.SessionOpts{
		ID: "shared1", Command: "sleep", Args: []string{"60"},
		Dir: repoDir, Rows: 24, Cols: 80,
		LogPath: filepath.Join(logDir, "shared.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sharedPty.Kill() }()

	normalPty, err := grpty.NewSession(grpty.SessionOpts{
		ID: "normal1", Command: "sleep", Args: []string{"60"},
		Dir: repoDir, Rows: 24, Cols: 80,
		LogPath: filepath.Join(logDir, "normal.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = normalPty.Kill() }()

	sm.state.Sessions["shared1"] = &SessionState{
		ID: "shared1", Name: "bothy-shared", Agent: "claude",
		Status: StatusRunning, WorktreePath: repoDir, RepoPath: repoDir,
		Mirror: true,
	}
	sm.state.Sessions["normal1"] = &SessionState{
		ID: "normal1", Name: "bothy-normal", Agent: "claude",
		Status: StatusRunning, WorktreePath: repoDir, RepoPath: repoDir,
	}
	sm.sessions["shared1"] = sharedPty
	sm.sessions["normal1"] = normalPty

	sm.mu.Lock()
	sm.hookReports["shared1"] = hookReport{
		Status: "active", Event: "Notification",
		ReportedAt: time.Now(), AuthoritativeUntil: time.Now().Add(5 * time.Minute),
	}
	sm.hookReports["normal1"] = hookReport{
		Status: "active", Event: "Notification",
		ReportedAt: time.Now(), AuthoritativeUntil: time.Now().Add(5 * time.Minute),
	}
	sm.mu.Unlock()

	sm.detectAgentStatuses()

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	shared := sm.state.Sessions["shared1"]
	normal := sm.state.Sessions["normal1"]

	if shared.GitDirty {
		t.Error("mirror session should have GitDirty=false (git ops skipped)")
	}

	if shared.GitUnpushed != 0 {
		t.Errorf("mirror session GitUnpushed=%d, want 0", shared.GitUnpushed)
	}

	if normal.GitDirty != true {
		t.Error("normal session should detect GitDirty=true from modified file")
	}

	if shared.AgentStatus != "active" {
		t.Errorf("shared session AgentStatus=%q, want 'active'", shared.AgentStatus)
	}

	if normal.AgentStatus != "active" {
		t.Errorf("normal session AgentStatus=%q, want 'active'", normal.AgentStatus)
	}
}

func TestForkNoRepoSession(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.state.Sessions["norepo1"] = &SessionState{
		ID:     "norepo1",
		Name:   "haar-session",
		Agent:  "claude",
		Status: StatusRunning,
	}

	_, err := sm.Fork("braw-fork", "norepo1", 24, 80)
	assertErrContains(t, err, "no repo")

	if len(sm.state.Sessions) != 1 {
		t.Errorf("expected 1 session (source only), got %d", len(sm.state.Sessions))
	}
}

func TestApplyConfig(t *testing.T) {
	sm := newTestSessionManager(t)
	oldCfg := sm.cfg

	newCfg := config.Default()
	newCfg.DefaultAgent = "codex"
	newCfg.Agents["newagent"] = config.Agent{Command: "newagent"}

	sm.applyConfig(newCfg)

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.cfg != newCfg {
		t.Error("config was not swapped")
	}

	if sm.cfg == oldCfg {
		t.Error("config still points to old config")
	}

	if sm.cfg.DefaultAgent != "codex" {
		t.Errorf("DefaultAgent = %q, want %q", sm.cfg.DefaultAgent, "codex")
	}

	if _, ok := sm.cfg.Agents["newagent"]; !ok {
		t.Error("new agent not present in config")
	}
}

func TestReloadConfig(t *testing.T) {
	dir := t.TempDir()

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("default_agent = \"codex\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm := newTestSessionManager(t)
	sm.configFile = cfgPath

	if err := sm.ReloadConfig(); err != nil {
		t.Fatalf("ReloadConfig() error = %v", err)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.cfg.DefaultAgent != "codex" {
		t.Errorf("DefaultAgent = %q, want %q", sm.cfg.DefaultAgent, "codex")
	}
}

func TestReloadConfigRejectsDataDirChange(t *testing.T) {
	dir := t.TempDir()

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("data_dir = \"/tmp/new-graith\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm := newTestSessionManager(t)
	sm.configFile = cfgPath

	err := sm.ReloadConfig()
	assertErrContains(t, err, "data_dir changed")
}

func TestReloadConfigInvalidFile(t *testing.T) {
	dir := t.TempDir()

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("{{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm := newTestSessionManager(t)
	sm.configFile = cfgPath

	err := sm.ReloadConfig()
	if err == nil {
		t.Fatal("expected error for invalid config file")
	}
}

func TestToSessionInfoMirror(t *testing.T) {
	sess := SessionState{
		ID:           "abc123",
		Name:         "canny-reviewer",
		WorktreePath: "/shared/path",
		Agent:        "claude",
		Status:       StatusRunning,
		Mirror:       true,
		CreatedAt:    time.Now().UTC(),
	}

	info := toSessionInfo(sess, config.Default(), nil)

	if !info.Mirror {
		t.Error("Mirror = false, want true")
	}

	sess.Mirror = false

	info = toSessionInfo(sess, config.Default(), nil)
	if info.Mirror {
		t.Error("Mirror = true, want false")
	}
}

func TestDeleteMirrorSkipsGitTeardown(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSessionManager(config.Default(), config.Paths{
		StateFile: filepath.Join(tmpDir, "state.json"),
		DataDir:   tmpDir,
		LogDir:    tmpDir,
	}, slog.Default())

	scratchDir := filepath.Join(tmpDir, "scratch", "shared1")
	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		t.Fatal(err)
	}

	sm.state.Sessions["shared1"] = &SessionState{
		ID:           "shared1",
		Name:         "canny-reviewer",
		RepoPath:     "/does/not/exist/repo",
		WorktreePath: "/does/not/exist/worktree",
		Branch:       "some-branch",
		Mirror:       true,
		Status:       StatusStopped,
	}

	if err := sm.Delete("shared1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, ok := sm.state.Sessions["shared1"]; ok {
		t.Error("session should be removed from state after delete")
	}

	if _, err := os.Stat(scratchDir); !os.IsNotExist(err) {
		t.Error("scratch dir should be cleaned up after delete")
	}
}

func TestStateSaveLoadMirror(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	state := &State{
		Sessions: map[string]*SessionState{
			"s1": {
				ID: "s1", Name: "canny-reviewer", WorktreePath: "/shared/path",
				Agent: "claude", Status: StatusRunning,
				Mirror: true, CreatedAt: time.Now().UTC(),
			},
		},
	}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	s, ok := loaded.Sessions["s1"]
	if !ok {
		t.Fatal("session not found after load")
	}

	if !s.Mirror {
		t.Error("Mirror not preserved across save/load")
	}
}

// TestMigrateV14ToV15MirrorKeys is a regression test for issue #1021: renaming
// --share-worktree to --mirror changed the persisted keys from shared_worktree
// / shared_worktree_source_id to mirror / mirror_source_id. Without the v15
// migration, an existing mirror session would load with Mirror=false and be
// treated as an ordinary session — deleting it could then remove the *source*
// session's worktree. Load a raw v14 blob written with the old keys and assert
// the renamed fields survive and the legacy fields are cleared.
func TestMigrateV14ToV15MirrorKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	// A v14 state file as the old binary would have written it: the mirror
	// marker lives under the pre-rename keys.
	raw := `{
	  "version": 14,
	  "sessions": {
	    "bide": {
	      "id": "bide",
	      "name": "canny-reviewer",
	      "agent": "claude",
	      "status": "stopped",
	      "worktree_path": "/bothy/source",
	      "shared_worktree": true,
	      "shared_worktree_source_id": "braw-source",
	      "created_at": "2026-01-01T00:00:00Z",
	      "status_changed_at": "2026-01-01T00:00:00Z"
	    }
	  }
	}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Version != CurrentStateVersion {
		t.Errorf("version = %d, want %d after migration", loaded.Version, CurrentStateVersion)
	}

	s, ok := loaded.Sessions["bide"]
	if !ok {
		t.Fatal("session not found after load")
	}

	if !s.Mirror {
		t.Error("Mirror = false, want true (shared_worktree not migrated)")
	}

	if s.MirrorSourceID != "braw-source" {
		t.Errorf("MirrorSourceID = %q, want %q (shared_worktree_source_id not migrated)", s.MirrorSourceID, "braw-source")
	}

	if s.LegacyMirror || s.LegacyMirrorSourceID != "" {
		t.Errorf("legacy fields not cleared: LegacyMirror=%v LegacyMirrorSourceID=%q", s.LegacyMirror, s.LegacyMirrorSourceID)
	}
}

func TestMirrorRequiresSandbox(t *testing.T) {
	assertCreateMirrorRejected(t, newSMWithConfig(t, config.Default()))
}

func TestMirrorRequiresSandboxPerAgent(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox.Enabled = true
	disabled := true
	agent := cfg.Agents["claude"]
	agent.Sandbox = config.SandboxConfig{Disabled: &disabled}
	cfg.Agents["claude"] = agent

	assertCreateMirrorRejected(t, newSMWithConfig(t, cfg))
}

func TestResumeMirrorWithoutSandboxRejects(t *testing.T) {
	cfg := config.Default()
	sm := newSMWithConfig(t, cfg)

	sm.state.Sessions["legacy1"] = &SessionState{
		ID:           "legacy1",
		Name:         "auld-reviewer",
		Agent:        "claude",
		WorktreePath: "/tmp/fake-worktree",
		Mirror:       true,
		Sandboxed:    false,
		Status:       StatusStopped,
	}

	_, err := sm.Resume("legacy1", 24, 80)
	assertErrContains(t, err, "requires sandbox")
}

func TestCreateRollsBackOnSaveStateFailure(t *testing.T) {
	tmpDir := t.TempDir()

	// Place a regular file where writeFileAtomic needs a directory,
	// so MkdirAll fails and saveState returns an error.
	blocker := filepath.Join(tmpDir, "block")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Agents["sleeper"] = config.Agent{
		Command: "sleep",
		Args:    []string{"60"},
	}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(blocker, "sub", "state.json"),
		DataDir:   tmpDir,
		LogDir:    tmpDir,
	}, slog.Default())

	_, err := sm.Create(CreateOpts{Name: "braw-sess", AgentName: "sleeper", NoRepo: true, Rows: 24, Cols: 80})
	if err == nil {
		t.Fatal("expected error when saveState fails, got nil")
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.state.Sessions) != 0 {
		t.Errorf("sessions not rolled back: got %d, want 0", len(sm.state.Sessions))
	}

	if len(sm.sessions) != 0 {
		t.Errorf("PTY sessions not rolled back: got %d, want 0", len(sm.sessions))
	}

	// Scratch dir should be cleaned up
	matches, _ := filepath.Glob(filepath.Join(tmpDir, "scratch", "*"))
	if len(matches) != 0 {
		t.Errorf("orphaned scratch dirs not cleaned up: %v", matches)
	}
}

func TestResumeRollsBackOnSaveStateFailure(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.Default()
	cfg.Agents["sleeper"] = config.Agent{
		Command: "sleep",
		Args:    []string{"60"},
	}

	sm := newSMWithConfig(t, cfg)

	exitCode := 42
	sm.state.Sessions["s1"] = &SessionState{
		ID:           "s1",
		Name:         "braw",
		Agent:        "sleeper",
		Status:       StatusStopped,
		ExitCode:     &exitCode,
		AgentStatus:  "ready",
		WorktreePath: tmpDir,
	}

	// Save valid state first, then break the path
	if err := sm.saveState(); err != nil {
		t.Fatal(err)
	}

	blocker := filepath.Join(tmpDir, "block")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm.paths.StateFile = filepath.Join(blocker, "sub", "state.json")

	_, err := sm.Resume("s1", 24, 80)
	if err == nil {
		t.Fatal("expected error when saveState fails, got nil")
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	s := sm.state.Sessions["s1"]
	if s.Status != StatusStopped {
		t.Errorf("status not rolled back: got %q, want %q", s.Status, StatusStopped)
	}

	if s.ExitCode == nil || *s.ExitCode != 42 {
		t.Errorf("exit code not rolled back: got %v, want 42", s.ExitCode)
	}

	if s.PID != 0 {
		t.Errorf("PID not rolled back: got %d, want 0", s.PID)
	}

	if s.AgentStatus != "ready" {
		t.Errorf("agent status not rolled back: got %q, want %q", s.AgentStatus, "ready")
	}

	if _, ok := sm.sessions["s1"]; ok {
		t.Error("PTY session should be removed after rollback")
	}
}

func TestExpandPathsGlob(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"id_rsa.pub", "id_ed25519.pub", "id_rsa", "config"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	log := slog.Default()

	t.Run("expands globs", func(t *testing.T) {
		got := expandPaths([]string{filepath.Join(dir, "*.pub")}, log, "read")
		if len(got) != 2 {
			t.Fatalf("expandPaths glob = %v, want 2 matches", got)
		}

		for _, p := range got {
			if !strings.HasSuffix(p, ".pub") {
				t.Errorf("unexpected match: %s", p)
			}
		}
	})

	t.Run("existing path kept", func(t *testing.T) {
		got := expandPaths([]string{dir}, log, "read")
		if len(got) != 1 || got[0] != dir {
			t.Errorf("expandPaths existing = %v, want [%s]", got, dir)
		}
	})

	t.Run("non-existent path skipped", func(t *testing.T) {
		got := expandPaths([]string{"/some/nonexistent/path"}, log, "read")
		if len(got) != 0 {
			t.Errorf("expandPaths nonexistent = %v, want []", got)
		}
	})

	t.Run("unmatched glob skipped", func(t *testing.T) {
		pattern := filepath.Join(dir, "*.zzz")

		got := expandPaths([]string{pattern}, log, "read")
		if len(got) != 0 {
			t.Errorf("expandPaths no-match = %v, want []", got)
		}
	})

	t.Run("nil input", func(t *testing.T) {
		got := expandPaths(nil, log, "read")
		if got != nil {
			t.Errorf("expandPaths(nil) = %v, want nil", got)
		}
	})
}

func TestExpandFilePaths(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "bide.json")

	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	log := slog.Default()

	// The whole point of file grants: a not-yet-created file (e.g. a lockfile)
	// must be KEPT, unlike expandPaths which drops non-existent paths.
	t.Run("non-existent file kept", func(t *testing.T) {
		lock := filepath.Join(dir, "bide.json.lock") // does not exist

		got := expandFilePaths([]string{lock}, log, "write")
		if len(got) != 1 || got[0] != lock {
			t.Errorf("expandFilePaths nonexistent = %v, want [%s]", got, lock)
		}
	})

	t.Run("existing file kept", func(t *testing.T) {
		got := expandFilePaths([]string{existing}, log, "write")
		if len(got) != 1 || got[0] != existing {
			t.Errorf("expandFilePaths existing = %v, want [%s]", got, existing)
		}
	})

	t.Run("glob expands to matches", func(t *testing.T) {
		got := expandFilePaths([]string{filepath.Join(dir, "*.json")}, log, "read")
		if len(got) != 1 || got[0] != existing {
			t.Errorf("expandFilePaths glob = %v, want [%s]", got, existing)
		}
	})

	t.Run("unmatched glob skipped", func(t *testing.T) {
		got := expandFilePaths([]string{filepath.Join(dir, "*.zzz")}, log, "read")
		if len(got) != 0 {
			t.Errorf("expandFilePaths no-match = %v, want []", got)
		}
	})

	t.Run("nil input", func(t *testing.T) {
		if got := expandFilePaths(nil, log, "read"); got != nil {
			t.Errorf("expandFilePaths(nil) = %v, want nil", got)
		}
	})
}

func TestResumeRefreshesSandboxConfig(t *testing.T) {
	t.Run("resume uses current config not stored config", func(t *testing.T) {
		tmpDir := t.TempDir()

		updatedDir := filepath.Join(tmpDir, "updated-dir")
		if err := os.MkdirAll(updatedDir, 0o750); err != nil {
			t.Fatal(err)
		}

		cfg := config.Default()
		cfg.Sandbox = config.SandboxConfig{
			Enabled:  true,
			Backend:  "nono",
			ReadDirs: []string{updatedDir},
		}
		cfg.Agents["sleeper"] = config.Agent{
			Command: "sleep",
			Args:    []string{"60"},
		}

		sm := newSMWithConfig(t, cfg)

		sm.state.Sessions["s1"] = &SessionState{
			ID:           "s1",
			Name:         "braw",
			Agent:        "sleeper",
			Status:       StatusStopped,
			Sandboxed:    true,
			WorktreePath: tmpDir,
			SandboxConfig: &config.SandboxConfig{
				Enabled:  true,
				ReadDirs: []string{"/old-creation-time-dir"},
			},
		}

		if err := sm.saveState(); err != nil {
			t.Fatal(err)
		}

		_, err := sm.Resume("s1", 24, 80)
		// Resume fails closed when the configured backend can't enforce on this
		// host (no nono binary / kernel too old). That's expected on many CI
		// hosts; the test only cares that the OLD stored config dir is not
		// reused. Accept any backend-unavailability error and return.
		if err != nil {
			if !strings.Contains(err.Error(), "cannot enforce") &&
				!strings.Contains(err.Error(), "not available") &&
				!strings.Contains(err.Error(), "not found in PATH") {
				t.Fatalf("unexpected error: %v", err)
			}

			return
		}

		sm.mu.RLock()
		s := sm.state.Sessions["s1"]
		sm.mu.RUnlock()

		if s.SandboxConfig == nil {
			t.Fatal("SandboxConfig should not be nil after resume")
		}

		found := false

		for _, d := range s.SandboxConfig.ReadDirs {
			if d == updatedDir {
				found = true
			}

			if d == "/old-creation-time-dir" {
				t.Error("SandboxConfig still contains old creation-time dir after resume")
			}
		}

		if !found {
			t.Errorf("SandboxConfig.ReadDirs = %v, want to contain %s", s.SandboxConfig.ReadDirs, updatedDir)
		}

		stopAndClosePTY(sm, "s1")
	})

	t.Run("resume without sandbox when config disables it", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := config.Default()
		cfg.Sandbox = config.SandboxConfig{Enabled: false}
		cfg.Agents["sleeper"] = config.Agent{
			Command: "sleep",
			Args:    []string{"60"},
		}

		sm := newSMWithConfig(t, cfg)

		sm.state.Sessions["s1"] = &SessionState{
			ID:           "s1",
			Name:         "braw",
			Agent:        "sleeper",
			Status:       StatusStopped,
			Sandboxed:    true,
			WorktreePath: tmpDir,
			SandboxConfig: &config.SandboxConfig{
				Enabled:  true,
				ReadDirs: []string{"/old-dir"},
			},
		}

		if err := sm.saveState(); err != nil {
			t.Fatal(err)
		}

		sess, err := sm.Resume("s1", 24, 80)
		if err != nil {
			t.Fatalf("Resume failed: %v", err)
		}

		if sess.Sandboxed {
			t.Error("session should not be sandboxed after resume with sandbox disabled in config")
		}

		if sess.SandboxConfig != nil {
			t.Errorf("SandboxConfig should be nil when sandbox is disabled, got %+v", sess.SandboxConfig)
		}

		stopAndClosePTY(sm, "s1")
	})

	t.Run("resume rollback restores sandbox fields", func(t *testing.T) {
		tmpDir := t.TempDir()

		cfg := config.Default()
		cfg.Sandbox = config.SandboxConfig{Enabled: false}
		cfg.Agents["sleeper"] = config.Agent{
			Command: "sleep",
			Args:    []string{"60"},
		}

		sm := newSMWithConfig(t, cfg)

		oldConfig := &config.SandboxConfig{
			Enabled:  true,
			ReadDirs: []string{"/old-dir"},
		}
		sm.state.Sessions["s1"] = &SessionState{
			ID:            "s1",
			Name:          "braw",
			Agent:         "sleeper",
			Status:        StatusStopped,
			Sandboxed:     true,
			SandboxConfig: oldConfig,
			WorktreePath:  tmpDir,
		}

		if err := sm.saveState(); err != nil {
			t.Fatal(err)
		}

		// Break the state file path to force saveState failure
		blocker := filepath.Join(tmpDir, "block")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}

		sm.paths.StateFile = filepath.Join(blocker, "sub", "state.json")

		_, err := sm.Resume("s1", 24, 80)
		if err == nil {
			t.Fatal("expected error when saveState fails, got nil")
		}

		sm.mu.RLock()
		s := sm.state.Sessions["s1"]
		sm.mu.RUnlock()

		if !s.Sandboxed {
			t.Error("Sandboxed should be rolled back to true")
		}

		if s.SandboxConfig != oldConfig {
			t.Error("SandboxConfig should be rolled back to original pointer")
		}
	})
}

func newTestPTYSession(t *testing.T, command string, args ...string) *grpty.Session {
	t.Helper()

	sess, err := grpty.NewSession(grpty.SessionOpts{
		ID: "test", Command: command, Args: args,
		Dir: t.TempDir(), Rows: 24, Cols: 80,
		LogPath:    filepath.Join(t.TempDir(), "pty.log"),
		MaxLogSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if !sess.Exited() {
			_ = sess.Kill()
		}

		sess.Close()
	})

	return sess
}

func TestWatchSessionStaleAfterReplace(t *testing.T) {
	sm := newTestSessionManager(t)

	id := "sess-watch"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "braw", Status: StatusRunning, Agent: "claude",
	}

	oldSess := newTestPTYSession(t, "true")
	newSess := newTestPTYSession(t, "sleep", "100")

	// Wait for old process to exit so Done() is closed.
	waitExit(t, oldSess)

	// Replace session in the map before calling watchSession,
	// simulating a Resume that happened while the old process was dying.
	sm.sessions[id] = newSess

	// Call watchSession synchronously — it returns immediately because
	// Done() is already closed. This is deterministic: no goroutine, no sleep.
	sm.watchSession(id, oldSess)

	sm.mu.RLock()
	status := sm.state.Sessions[id].Status
	sm.mu.RUnlock()

	if status != StatusRunning {
		t.Errorf("status = %q, want %q — stale watcher overwrote state", status, StatusRunning)
	}
}

func TestWatchSessionCurrentUpdatesState(t *testing.T) {
	sm := newTestSessionManager(t)

	id := "sess-watch-current"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "braw", Status: StatusRunning, Agent: "claude",
	}

	sess := newTestPTYSession(t, "true")

	// Wait for exit so Done() is closed.
	waitExit(t, sess)

	sm.sessions[id] = sess

	// Call synchronously — deterministic, no sleep.
	sm.watchSession(id, sess)

	sm.mu.RLock()
	status := sm.state.Sessions[id].Status
	exitCode := sm.state.Sessions[id].ExitCode
	_, ptyStillInMap := sm.sessions[id]
	sm.mu.RUnlock()

	if status != StatusStopped {
		t.Errorf("status = %q, want %q", status, StatusStopped)
	}

	if exitCode == nil || *exitCode != 0 {
		t.Errorf("exit code = %v, want 0", exitCode)
	}

	if ptyStillInMap {
		t.Error("PTY session should be removed from sessions map after natural exit")
	}
}

func TestWatchSessionClosesPTYHandles(t *testing.T) {
	sm := newTestSessionManager(t)

	id := "sess-watch-close"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "braw", Status: StatusRunning, Agent: "claude",
	}

	sess := newTestPTYSession(t, "true")

	waitExit(t, sess)

	sm.sessions[id] = sess

	sm.watchSession(id, sess)

	// Verify the PTY master fd was closed by attempting a Stat on it.
	// A closed *os.File returns os.ErrClosed (or similar) on Stat.
	if _, err := sess.Ptmx.Stat(); err == nil {
		t.Error("Ptmx fd should be closed after watchSession, but Stat succeeded")
	}
}

func TestWatchSessionStaleClosesPTYHandles(t *testing.T) {
	sm := newTestSessionManager(t)

	id := "sess-watch-stale-close"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "braw", Status: StatusRunning, Agent: "claude",
	}

	oldSess := newTestPTYSession(t, "true")
	newSess := newTestPTYSession(t, "sleep", "100")

	waitExit(t, oldSess)

	// Simulate Resume replacing the PTY while the old process was exiting.
	sm.sessions[id] = newSess

	sm.watchSession(id, oldSess)

	// The old PTY's handles should still be closed even though the watcher was stale.
	if _, err := oldSess.Ptmx.Stat(); err == nil {
		t.Error("stale oldSess Ptmx fd should be closed after watchSession, but Stat succeeded")
	}
}

func TestWatchSessionDeletedSkipsPublish(t *testing.T) {
	dir := t.TempDir()

	ms, err := NewMsgStore(filepath.Join(dir, "msg.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ms.Close() }()

	sm := newTestSessionManager(t)
	sm.messages = ms

	id := "sess-watch-deleted"

	sess := newTestPTYSession(t, "true")

	waitExit(t, sess)

	// Put the PTY in the sessions map (so the stale check passes)
	// but do NOT put an entry in state.Sessions — simulating Delete
	// having already removed the state entry before the PTY exited.
	sm.sessions[id] = sess

	sm.watchSession(id, sess)

	msgs, err := ms.Read("_system.status", "", false, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(msgs) != 0 {
		t.Errorf("expected 0 status messages for deleted session, got %d: %v", len(msgs), msgs)
	}

	sm.mu.RLock()
	_, ptyStillInMap := sm.sessions[id]
	sm.mu.RUnlock()

	if ptyStillInMap {
		t.Error("PTY session should be removed from sessions map even for deleted sessions")
	}
}

func TestResumeResetsIdleSince(t *testing.T) {
	// Use os.MkdirTemp instead of t.TempDir — on macOS, writeFileAtomic's
	// syncDir can leave a recently-closed directory fd that races with
	// t.TempDir's strict RemoveAll cleanup, causing flaky failures.
	tmpDir, err := os.MkdirTemp("", "TestResumeResetsIdleSince")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for range 5 {
			if err := os.RemoveAll(tmpDir); err == nil {
				return
			}

			time.Sleep(50 * time.Millisecond)
		}
	}()

	cfg := config.Default()
	cfg.Agents["claude"] = config.Agent{
		Command:     "true",
		Args:        []string{},
		ResumeArgs:  []string{},
		IdleTimeout: "5m",
	}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(tmpDir, "state.json"),
		DataDir:   tmpDir,
		LogDir:    tmpDir,
	}, slog.Default())

	past := time.Now().Add(-10 * time.Minute)
	id := "sess-idle"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "bide-idle", Status: StatusStopped, Agent: "claude",
		WorktreePath: tmpDir,
		IdleSince:    &past,
	}

	resumed, err := sm.Resume(id, 24, 80)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	if resumed.Status != StatusRunning {
		t.Errorf("status = %q, want %q", resumed.Status, StatusRunning)
	}

	sm.mu.RLock()
	idleSince := sm.state.Sessions[id].IdleSince
	ptySess := sm.sessions[id]
	sm.mu.RUnlock()

	if idleSince != nil {
		t.Errorf("IdleSince = %v, want nil after Resume", idleSince)
	}

	if ptySess != nil {
		waitExit(t, ptySess)

		ptySess.Close()
		sm.mu.Lock()
		_ = sm.state.Sessions[id]
		sm.mu.Unlock()
	}
}

// --- In-place session tests ---

func initTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "commit", "--allow-empty", "-m", "init")

	return dir
}

// initDirtyRepo creates a git repo with one committed file plus an uncommitted
// modification, so worktree-status checks report it dirty. Returns the path.
func initDirtyRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()

	gitRun(t, repoDir, "init", "-b", "main")

	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	gitRun(t, repoDir, "add", "file.txt")
	gitRun(t, repoDir, "commit", "-m", "init")

	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}

	return repoDir
}

func TestCreateInPlaceRejectsUnconfiguredRepo(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)

	_, err := sm.Create(CreateOpts{Name: "braw", AgentName: "claude", RepoPath: repoDir, InPlace: true, Rows: 24, Cols: 80})
	assertErrContains(t, err, "not configured in [[repos]]")
}

func TestCreateInPlaceMutuallyExclusiveFlags(t *testing.T) {
	sm := newTestSessionManager(t)

	t.Run("in-place with no-repo", func(t *testing.T) {
		_, err := sm.Create(CreateOpts{Name: "braw", AgentName: "claude", NoRepo: true, InPlace: true, Rows: 24, Cols: 80})
		assertErrContains(t, err, "mutually exclusive")
	})

	t.Run("in-place with mirror", func(t *testing.T) {
		_, err := sm.Create(CreateOpts{Name: "braw", AgentName: "claude", Mirror: "some-session", InPlace: true, Rows: 24, Cols: 80})
		assertErrContains(t, err, "mutually exclusive")
	})

	t.Run("in-place with base", func(t *testing.T) {
		_, err := sm.Create(CreateOpts{Name: "braw", AgentName: "claude", RepoPath: "/tmp/whatever", BaseBranch: "main", InPlace: true, Rows: 24, Cols: 80})
		assertErrContains(t, err, "mutually exclusive")
	})
}

func TestCreateInPlaceRejectsConcurrent(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sm.state.Sessions["bide-session"] = &SessionState{
		ID:           "bide-session",
		Name:         "braw-one",
		WorktreePath: repoDir,
		InPlace:      true,
		Status:       StatusRunning,
	}

	_, err := sm.Create(CreateOpts{Name: "canny-two", AgentName: "claude", RepoPath: repoDir, InPlace: true, Rows: 24, Cols: 80})
	assertErrContains(t, err, "already running")
}

func TestCreateInPlaceAllowConcurrentFlag(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sm.state.Sessions["bide-session"] = &SessionState{
		ID:           "bide-session",
		Name:         "braw-one",
		WorktreePath: repoDir,
		InPlace:      true,
		Status:       StatusRunning,
	}

	// With --allow-concurrent, should pass the concurrent check (will fail later on agent start)
	_, err := sm.Create(CreateOpts{Name: "canny-two", AgentName: "claude", RepoPath: repoDir, InPlace: true, AllowConcurrent: true, Rows: 24, Cols: 80})
	if err != nil && strings.Contains(err.Error(), "already running") {
		t.Fatalf("--allow-concurrent should bypass concurrent check, got: %v", err)
	}
}

func TestCreateInPlaceConfigAllowConcurrent(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir, AllowConcurrent: true}}

	sm.state.Sessions["bide-session"] = &SessionState{
		ID:           "bide-session",
		Name:         "braw-one",
		WorktreePath: repoDir,
		InPlace:      true,
		Status:       StatusRunning,
	}

	// Config allow_concurrent should pass the concurrent check
	_, err := sm.Create(CreateOpts{Name: "canny-two", AgentName: "claude", RepoPath: repoDir, InPlace: true, Rows: 24, Cols: 80})
	if err != nil && strings.Contains(err.Error(), "already running") {
		t.Fatalf("config allow_concurrent should bypass concurrent check, got: %v", err)
	}
}

// TestCreateNoFetchSkipsFetch is the regression test for #1012: `gr new
// --no-fetch` must skip the `git fetch origin` that normally runs before the
// worktree is created, so a session can be created from local repo state when
// SSH auth is unavailable (Secretive/biometric, offline). The repo has an
// origin remote pointing at a path that can't be fetched, so a real fetch
// fails — proving the flag is what let creation through.
func TestCreateNoFetchSkipsFetch(t *testing.T) {
	repoDir := initTempGitRepo(t)

	// A remote that exists (so HasRemote(origin) is true) but can't be fetched.
	gitRun(t, repoDir, "remote", "add", "origin", filepath.Join(t.TempDir(), "nonexistent.git"))

	cfg := config.Default()
	cfg.FetchOnCreate = true // fetch is on; only --no-fetch should suppress it
	cfg.Agents["sleeper"] = config.Agent{
		Command: "sleep",
		Args:    []string{"60"},
	}

	t.Run("no-fetch skips the failing fetch", func(t *testing.T) {
		sm := newSMWithConfig(t, cfg)

		sess, err := sm.Create(CreateOpts{Name: "braw", AgentName: "sleeper", RepoPath: repoDir, NoFetch: true, Rows: 24, Cols: 80})
		if err != nil {
			t.Fatalf("Create with NoFetch should skip the fetch and succeed, got: %v", err)
		}

		t.Cleanup(func() {
			sm.mu.RLock()
			live, ok := sm.sessions[sess.ID]
			sm.mu.RUnlock()

			if ok {
				_ = live.Kill()
				live.Close()
			}

			if sess.WorktreePath != "" {
				cmd := testutil.GitCommand("worktree", "remove", "--force", sess.WorktreePath)
				cmd.Dir = repoDir
				_ = cmd.Run()
			}
		})
	})

	t.Run("without no-fetch the fetch runs and fails", func(t *testing.T) {
		sm := newSMWithConfig(t, cfg)

		_, err := sm.Create(CreateOpts{Name: "dreich", AgentName: "sleeper", RepoPath: repoDir, Rows: 24, Cols: 80})
		assertErrContains(t, err, "fetch")
	})
}

func TestDeleteInPlaceLeavesState(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["inplace1"] = &SessionState{
		ID:           "inplace1",
		Name:         "kirk-inplace",
		RepoPath:     "/tmp/my-repo",
		WorktreePath: "/tmp/my-repo",
		InPlace:      true,
		Status:       StatusStopped,
		CreatedAt:    time.Now().UTC(),
	}

	if err := sm.Delete("inplace1"); err != nil {
		t.Fatal(err)
	}

	if _, ok := sm.state.Sessions["inplace1"]; ok {
		t.Error("session should be removed from state after delete")
	}
}

func TestDeleteSystemSessionRejectedWhenEnabled(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.cfg.Orchestrator.Enabled = true

	sm.state.Sessions["orch1"] = &SessionState{
		ID:         "orch1",
		Name:       OrchestratorSessionName,
		SystemKind: SystemKindOrchestrator,
		Status:     StatusStopped,
		CreatedAt:  time.Now().UTC(),
	}

	err := sm.Delete("orch1")
	if err == nil {
		t.Fatal("expected error deleting an enabled system session")
	}

	if _, ok := sm.state.Sessions["orch1"]; !ok {
		t.Error("session should remain in state when delete is rejected")
	}
}

func TestDeleteSystemSessionAllowedWhenDisabled(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.cfg.Orchestrator.Enabled = false

	// The orphaned orchestrator keeps its scratch/tmp tree under
	// DataDir/orchestrator; deleting it must clean that up too.
	orchDir := filepath.Join(sm.paths.DataDir, "orchestrator")
	if err := os.MkdirAll(filepath.Join(orchDir, "scratch"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(orchDir, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}

	sm.state.Sessions["orch1"] = &SessionState{
		ID:         "orch1",
		Name:       OrchestratorSessionName,
		SystemKind: SystemKindOrchestrator,
		Status:     StatusStopped,
		CreatedAt:  time.Now().UTC(),
	}

	if err := sm.Delete("orch1"); err != nil {
		t.Fatalf("delete of disabled system session should succeed: %v", err)
	}

	if _, ok := sm.state.Sessions["orch1"]; ok {
		t.Error("session should be removed from state after delete")
	}

	if _, err := os.Stat(orchDir); !os.IsNotExist(err) {
		t.Errorf("orchestrator scratch/tmp tree should be removed, stat err = %v", err)
	}
}

func TestForkInPlaceRejects(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["inplace1"] = &SessionState{
		ID:           "inplace1",
		Name:         "kirk-inplace",
		RepoPath:     "/tmp/my-repo",
		WorktreePath: "/tmp/my-repo",
		Agent:        "claude",
		InPlace:      true,
		Status:       StatusRunning,
	}

	_, err := sm.Fork("braw-fork", "inplace1", 24, 80)
	assertErrContains(t, err, "in-place")
}

func TestForkUsesSourceBaseBranch(t *testing.T) {
	repoDir := initTempGitRepo(t)

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["sleeper"] = config.Agent{
		Command: "sleep",
		Args:    []string{"60"},
	}

	sm := newSMWithConfig(t, cfg)

	sm.state.Sessions["src1"] = &SessionState{
		ID:             "src1",
		Name:           "braw-source-session",
		RepoPath:       repoDir,
		RepoName:       "testrepo",
		WorktreePath:   repoDir,
		Branch:         "feat/my-feature",
		BaseBranch:     "main",
		Agent:          "sleeper",
		AgentSessionID: "braw-agent-id",
		Status:         StatusRunning,
	}

	forked, err := sm.Fork("braw-fork", "src1", 24, 80)
	if err != nil {
		t.Fatalf("Fork() unexpected error: %v", err)
	}

	t.Cleanup(func() {
		sm.mu.RLock()
		sess, ok := sm.sessions[forked.ID]
		sm.mu.RUnlock()

		if ok {
			_ = sess.Kill()
			sess.Close()
		}

		if forked.WorktreePath != "" {
			cmd := testutil.GitCommand("worktree", "remove", "--force", forked.WorktreePath)
			cmd.Dir = repoDir
			_ = cmd.Run()
		}
	})

	if forked.BaseBranch != "main" {
		t.Errorf("Fork() BaseBranch = %q, want %q", forked.BaseBranch, "main")
	}

	if forked.BaseBranch == "feat/my-feature" {
		t.Error("Fork() incorrectly used source Branch as BaseBranch")
	}
}

// TestForkInheritsYolo verifies a fork of a yolo session is itself yolo, so the
// auto-approve mode propagates and the fork doesn't silently start prompting.
func TestForkInheritsYolo(t *testing.T) {
	repoDir := initTempGitRepo(t)

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["sleeper"] = config.Agent{
		Command: "sleep",
		Args:    []string{"60"},
	}

	sm := newSMWithConfig(t, cfg)

	sm.state.Sessions["src1"] = &SessionState{
		ID:           "src1",
		Name:         "braw-source-session",
		RepoPath:     repoDir,
		RepoName:     "testrepo",
		WorktreePath: repoDir,
		Branch:       "feat/my-feature",
		BaseBranch:   "main",
		Agent:        "sleeper",
		Status:       StatusRunning,
		Yolo:         true,
	}

	forked, err := sm.Fork("braw-fork", "src1", 24, 80)
	if err != nil {
		t.Fatalf("Fork() unexpected error: %v", err)
	}

	t.Cleanup(func() {
		sm.mu.RLock()
		sess, ok := sm.sessions[forked.ID]
		sm.mu.RUnlock()

		if ok {
			_ = sess.Kill()
			sess.Close()
		}

		if forked.WorktreePath != "" {
			cmd := testutil.GitCommand("worktree", "remove", "--force", forked.WorktreePath)
			cmd.Dir = repoDir
			_ = cmd.Run()
		}
	})

	if !forked.Yolo {
		t.Error("Fork() of a yolo session did not inherit Yolo")
	}
}

func TestResumeInPlaceRejectsRemovedConfig(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)

	sm.state.Sessions["inplace1"] = &SessionState{
		ID:           "inplace1",
		Name:         "kirk-inplace",
		RepoPath:     repoDir,
		WorktreePath: repoDir,
		Agent:        "claude",
		InPlace:      true,
		Status:       StatusStopped,
	}

	_, err := sm.Resume("inplace1", 24, 80)
	assertErrContains(t, err, "[[repos]]")
}

func TestResumeInPlaceRejectsConcurrentRunning(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sm.state.Sessions["inplace1"] = &SessionState{
		ID:           "inplace1",
		Name:         "braw-one",
		RepoPath:     repoDir,
		WorktreePath: repoDir,
		Agent:        "claude",
		InPlace:      true,
		Status:       StatusStopped,
	}
	sm.state.Sessions["inplace2"] = &SessionState{
		ID:           "inplace2",
		Name:         "canny-two",
		RepoPath:     repoDir,
		WorktreePath: repoDir,
		Agent:        "claude",
		InPlace:      true,
		Status:       StatusRunning,
	}

	_, err := sm.Resume("inplace1", 24, 80)
	assertErrContains(t, err, "already running")
}

func TestResumeInPlaceRejectsDeletedRepo(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sm.state.Sessions["inplace1"] = &SessionState{
		ID:           "inplace1",
		Name:         "kirk-inplace",
		RepoPath:     repoDir,
		WorktreePath: repoDir,
		Agent:        "claude",
		InPlace:      true,
		Status:       StatusStopped,
	}

	_ = os.RemoveAll(repoDir)

	_, err := sm.Resume("inplace1", 24, 80)
	assertErrContains(t, err, "no longer a git repository")
}

func TestCreateInPlaceBaseRejectedByDaemon(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.Create(CreateOpts{Name: "braw", AgentName: "claude", RepoPath: "/tmp/whatever", BaseBranch: "main", InPlace: true, Rows: 24, Cols: 80})
	assertErrContains(t, err, "mutually exclusive")
}

func TestStateSaveLoadInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	state := &State{
		Version: CurrentStateVersion,
		Sessions: map[string]*SessionState{
			"abc123": {
				ID: "abc123", Name: "inplace-test", Agent: "claude",
				WorktreePath: "/some/repo", InPlace: true,
				Status: StatusRunning, CreatedAt: time.Now().UTC(),
			},
		},
	}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	s := loaded.Sessions["abc123"]
	if !s.InPlace {
		t.Error("InPlace not preserved across save/load")
	}
}

func TestToSessionInfoInPlace(t *testing.T) {
	sess := SessionState{
		ID:           "abc",
		Name:         "braw",
		Agent:        "claude",
		WorktreePath: "/some/repo",
		InPlace:      true,
		Status:       StatusRunning,
		CreatedAt:    time.Now().UTC(),
	}

	info := toSessionInfo(sess, config.Default(), nil)
	if !info.InPlace {
		t.Error("InPlace = false in SessionInfo, want true")
	}

	sess.InPlace = false

	info = toSessionInfo(sess, config.Default(), nil)
	if info.InPlace {
		t.Error("InPlace = true in SessionInfo, want false")
	}
}

func TestSingletonBlocksCreateWhenRunning(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir, Singleton: true}}

	sm.state.Sessions["bide-session"] = &SessionState{
		ID:       "bide-session",
		Name:     "braw-one",
		RepoPath: repoDir,
		Status:   StatusRunning,
	}

	_, err := sm.Create(CreateOpts{Name: "canny-two", AgentName: "claude", RepoPath: repoDir, BaseBranch: "main", Rows: 24, Cols: 80})
	assertErrContains(t, err, "singleton")
}

func TestSingletonAllowsCreateWhenStopped(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir, Singleton: true}}

	sm.state.Sessions["bide-session"] = &SessionState{
		ID:       "bide-session",
		Name:     "braw-one",
		RepoPath: repoDir,
		Status:   StatusStopped,
	}

	_, err := sm.Create(CreateOpts{Name: "canny-two", AgentName: "claude", RepoPath: repoDir, BaseBranch: "main", Rows: 24, Cols: 80})
	if err != nil && strings.Contains(err.Error(), "singleton") {
		t.Fatalf("singleton should not block when existing session is stopped, got: %v", err)
	}
}

func TestInPlaceRejectsRepoWithIncludes(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	incDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir, Includes: []string{incDir}}}

	_, err := sm.Create(CreateOpts{Name: "braw", AgentName: "claude", RepoPath: repoDir, InPlace: true, Rows: 24, Cols: 80})
	assertErrContains(t, err, "includes configured")
}

func TestIncludeAddDirArgs(t *testing.T) {
	tests := []struct {
		name     string
		agent    string
		includes []IncludedRepoState
		want     []string
	}{
		{
			name:     "nil includes yields no args",
			agent:    "claude",
			includes: nil,
			want:     nil,
		},
		{
			name:     "empty includes yields no args",
			agent:    "claude",
			includes: []IncludedRepoState{},
			want:     nil,
		},
		{
			name:  "single include yields one flag pair",
			agent: "claude",
			includes: []IncludedRepoState{
				{RepoName: "bairn", WorktreePath: "/glen/bothy/bairn"},
			},
			want: []string{"--add-dir", "/glen/bothy/bairn"},
		},
		{
			name:  "multiple includes preserve order",
			agent: "codex",
			includes: []IncludedRepoState{
				{RepoName: "bairn", WorktreePath: "/glen/bothy/bairn"},
				{RepoName: "whin", WorktreePath: "/glen/bothy/whin"},
			},
			want: []string{"--add-dir", "/glen/bothy/bairn", "--add-dir", "/glen/bothy/whin"},
		},
		{
			name:  "include without a worktree path is skipped",
			agent: "cursor",
			includes: []IncludedRepoState{
				{RepoName: "haar", WorktreePath: ""},
				{RepoName: "bairn", WorktreePath: "/glen/bothy/bairn"},
			},
			want: []string{"--add-dir", "/glen/bothy/bairn"},
		},
		{
			name:  "all worktree paths empty yields nil not empty slice",
			agent: "claude",
			includes: []IncludedRepoState{
				{RepoName: "haar", WorktreePath: ""},
			},
			want: nil,
		},
		{
			name:  "unsupported agent gets no flags even with includes",
			agent: "opencode",
			includes: []IncludedRepoState{
				{RepoName: "bairn", WorktreePath: "/glen/bothy/bairn"},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := includeAddDirArgs(tt.agent, tt.includes)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("includeAddDirArgs(%q, ...) = %v, want %v", tt.agent, got, tt.want)
			}
		})
	}
}

// TestResumeIncludeSet locks the mirror-resume fix: a resuming mirror session
// must take the source session's includes (sharedSourceIncludes), not its own
// (always empty for a mirror), so --add-dir and GRAITH_INCLUDE_* survive a
// restart. A non-mirror session uses its own includes.
func TestResumeIncludeSet(t *testing.T) {
	own := []IncludedRepoState{{RepoName: "bairn", WorktreePath: "/glen/own/bairn"}}
	shared := []IncludedRepoState{{RepoName: "whin", WorktreePath: "/glen/source/whin"}}

	t.Run("non-mirror uses its own includes", func(t *testing.T) {
		if got := resumeIncludeSet(false, own, shared); !reflect.DeepEqual(got, own) {
			t.Errorf("resumeIncludeSet(false) = %v, want %v", got, own)
		}
	})

	t.Run("mirror uses source includes not its empty own", func(t *testing.T) {
		// A mirror's persisted includes are empty; the bug was using them.
		if got := resumeIncludeSet(true, nil, shared); !reflect.DeepEqual(got, shared) {
			t.Errorf("resumeIncludeSet(true) = %v, want %v", got, shared)
		}
	})
}

func TestAgentSupportsAddDir(t *testing.T) {
	supported := []string{"claude", "codex", "cursor"}
	for _, a := range supported {
		if !agentSupportsAddDir(a) {
			t.Errorf("agentSupportsAddDir(%q) = false, want true", a)
		}
	}

	for _, a := range []string{"opencode", "agy", "", "Claude", "gemini"} {
		if agentSupportsAddDir(a) {
			t.Errorf("agentSupportsAddDir(%q) = true, want false", a)
		}
	}
}

// newRecorderManager builds a SessionManager whose "cursor" agent (a
// --add-dir-capable type) is a shell script that records its own launch argv to
// recordPath, one arg per line, then blocks so the PTY stays alive. The repo is
// configured with the given includes. It returns the manager and record path.
func newRecorderManager(t *testing.T, repoDir string, includes []string) (*SessionManager, string) {
	t.Helper()

	dir := t.TempDir()
	recordPath := filepath.Join(dir, "argv.txt")

	// $0/$@ are exactly the flags graith appends after the "-c" script string.
	script := `printf '%s\n' "$0" "$@" > "$GRAITH_ARGS_RECORD"; exec cat`

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["cursor"] = config.Agent{
		Command:    "sh",
		Args:       []string{"-c", script},
		ResumeArgs: []string{"-c", script},
		ForkArgs:   []string{"-c", script},
		Env:        map[string]string{"GRAITH_ARGS_RECORD": recordPath},
	}
	cfg.Repos = []config.RepoConfig{{Path: repoDir, Includes: includes}}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile:  filepath.Join(dir, "state.json"),
		DataDir:    dir,
		LogDir:     dir,
		RuntimeDir: dir,
		TmpDir:     filepath.Join(dir, "tmp"),
	}, slog.Default())

	return sm, recordPath
}

// TestIncludesPassAddDirOnLaunchAndResume drives a real session whose agent
// records the argv it was launched with, and asserts --add-dir <worktree> is
// passed for each included repo — after the positional prompt — on both the
// initial launch and on resume.
func TestIncludesPassAddDirOnLaunchAndResume(t *testing.T) {
	repoDir := initTempGitRepo(t)
	incA := initTempGitRepo(t)
	incB := initTempGitRepo(t)

	sm, recordPath := newRecorderManager(t, repoDir, []string{incA, incB})

	const prompt = "speir at the bairns"

	created, err := sm.Create(CreateOpts{
		Name: "canny", AgentName: "cursor", RepoPath: repoDir, BaseBranch: "main",
		Prompt: prompt, Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	id := created.ID

	t.Cleanup(func() { stopAndClosePTY(sm, id) })

	if len(created.Includes) != 2 {
		t.Fatalf("created.Includes = %v, want two includes", created.Includes)
	}

	incWorktrees := []string{created.Includes[0].WorktreePath, created.Includes[1].WorktreePath}

	argv := waitForRecordedArgv(t, recordPath, "--add-dir")
	assertAddDirLayout(t, argv, prompt, incWorktrees)

	// Restart exercises the resume path; the flag must be re-added even though
	// resume_args don't carry it.
	if err := os.Remove(recordPath); err != nil {
		t.Fatalf("remove record before resume: %v", err)
	}

	if err := sm.Stop(id); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	waitForStatus(t, sm, id, StatusStopped)

	if _, err := sm.Restart(id, 24, 80); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	argv = waitForRecordedArgv(t, recordPath, "--add-dir")
	for _, wt := range incWorktrees {
		assertContiguousPair(t, argv, "--add-dir", wt)
	}
}

// TestForkPassesAddDir asserts a forked session's agent is launched with
// --add-dir for each included worktree the fork re-created.
func TestForkPassesAddDir(t *testing.T) {
	repoDir := initTempGitRepo(t)
	incDir := initTempGitRepo(t)

	sm, recordPath := newRecorderManager(t, repoDir, []string{incDir})

	source, err := sm.Create(CreateOpts{
		Name: "braw", AgentName: "cursor", RepoPath: repoDir, BaseBranch: "main", Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() source error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, source.ID) })

	// Create returns once the PTY is spawned, before the recorder is guaranteed
	// to have run. Wait for its first write before clearing it so the fork owns
	// the next record deterministically.
	waitForRecordedArgv(t, recordPath, "--add-dir")

	if err := os.Remove(recordPath); err != nil {
		t.Fatalf("remove record before fork: %v", err)
	}

	forked, err := sm.Fork("bairn", source.ID, 24, 80)
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, forked.ID) })

	if len(forked.Includes) != 1 {
		t.Fatalf("forked.Includes = %v, want one include", forked.Includes)
	}

	argv := waitForRecordedArgv(t, recordPath, "--add-dir")
	assertContiguousPair(t, argv, "--add-dir", forked.Includes[0].WorktreePath)
}

// waitForRecordedArgv polls the argv-record file until it contains needle (as
// one line), then returns the full recorded argv as a slice (one element per
// line, trailing blank dropped). Fails the test on timeout.
func waitForRecordedArgv(t *testing.T, recordPath, needle string) []string {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	var last string

	for time.Now().Before(deadline) {
		data, err := os.ReadFile(recordPath)
		if err == nil {
			last = string(data)
			lines := strings.Split(strings.TrimRight(last, "\n"), "\n")

			for _, ln := range lines {
				if ln == needle {
					return lines
				}
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("recorded argv never contained %q; last content:\n%s", needle, last)

	return nil
}

// assertContiguousPair fails unless flag appears in argv immediately followed by
// value — i.e. `--add-dir <path>` was emitted as an adjacent pair, not split.
func assertContiguousPair(t *testing.T, argv []string, flag, value string) {
	t.Helper()

	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag && argv[i+1] == value {
			return
		}
	}

	t.Errorf("argv missing contiguous %q %q pair; got %v", flag, value, argv)
}

// assertAddDirLayout checks the prompt-swallow guarantee: the positional prompt
// appears before the first --add-dir (so Claude's variadic flag can't consume
// it), and every include worktree is emitted as a contiguous --add-dir pair.
func assertAddDirLayout(t *testing.T, argv []string, prompt string, worktrees []string) {
	t.Helper()

	promptIdx, firstFlagIdx := -1, -1

	for i, a := range argv {
		if a == prompt && promptIdx == -1 {
			promptIdx = i
		}

		if a == "--add-dir" && firstFlagIdx == -1 {
			firstFlagIdx = i
		}
	}

	if promptIdx == -1 {
		t.Errorf("prompt %q not found in argv %v", prompt, argv)
	}

	if firstFlagIdx == -1 {
		t.Fatalf("no --add-dir in argv %v", argv)
	}

	if promptIdx != -1 && promptIdx > firstFlagIdx {
		t.Errorf("prompt (index %d) must precede first --add-dir (index %d) so it isn't swallowed; argv %v", promptIdx, firstFlagIdx, argv)
	}

	for _, wt := range worktrees {
		assertContiguousPair(t, argv, "--add-dir", wt)
	}
}

// newClaudeRecorderManager sets up a SessionManager whose "claude" agent records
// the argv it is launched with, for asserting hook/MCP arg injection end-to-end.
func newClaudeRecorderManager(t *testing.T, repoDir string) (*SessionManager, string) {
	t.Helper()

	dir := t.TempDir()
	recordPath := filepath.Join(dir, "argv.txt")

	// $0/$@ are exactly the flags graith appends after the "-c" script string.
	script := `printf '%s\n' "$0" "$@" > "$GRAITH_ARGS_RECORD"; exec cat`

	cfg := config.Default()
	cfg.FetchOnCreate = false
	// Drop the default agent prompt so the recorded argv is just the injected
	// hook/MCP flags — no --append-system-prompt noise to reason about.
	cfg.AgentPrompt = ""
	cfg.Agents["claude"] = config.Agent{
		Command:    "sh",
		Args:       []string{"-c", script},
		ResumeArgs: []string{"-c", script},
		ForkArgs:   []string{"-c", script},
		Env:        map[string]string{"GRAITH_ARGS_RECORD": recordPath},
	}
	cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile:  filepath.Join(dir, "state.json"),
		DataDir:    dir,
		LogDir:     dir,
		RuntimeDir: dir,
		TmpDir:     filepath.Join(dir, "tmp"),
	}, slog.Default())

	return sm, recordPath
}

// assertArgvContains fails unless want appears as one element of argv.
func assertArgvContains(t *testing.T, argv []string, want string) {
	t.Helper()

	for _, a := range argv {
		if a == want {
			return
		}
	}

	t.Errorf("argv missing %q; got %v", want, argv)
}

// valueAfter returns the argv element immediately following flag.
func valueAfter(t *testing.T, argv []string, flag string) string {
	t.Helper()

	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}

	t.Fatalf("argv missing %q with a following value; got %v", flag, argv)

	return ""
}

// TestClaudeSessionInjectsSettingsAndMCP is the #1135 regression at the daemon
// level: a hooks-enabled Claude session must launch with BOTH the --settings
// (lifecycle-hook) arg and the --mcp-config arg, now that the two are produced by
// separate, decoupled code paths (injectHooks vs injectMCPConfig). It guards
// against the split accidentally dropping MCP injection from the launch path.
func TestClaudeSessionInjectsSettingsAndMCP(t *testing.T) {
	repoDir := initTempGitRepo(t)
	sm, recordPath := newClaudeRecorderManager(t, repoDir)

	created, err := sm.Create(CreateOpts{
		Name: "braw", AgentName: "claude", RepoPath: repoDir, BaseBranch: "main",
		AgentHooks: true, SkipModelValidation: true, Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	id := created.ID

	t.Cleanup(func() { stopAndClosePTY(sm, id) })

	argv := waitForRecordedArgv(t, recordPath, "--mcp-config")

	assertArgvContains(t, argv, "--settings")
	assertArgvContains(t, argv, "--mcp-config")

	// The --mcp-config path must reference a real file carrying the auto-injected
	// graith server — proving MCP config is generated and wired even after the
	// split out of the hook path.
	mcpPath := valueAfter(t, argv, "--mcp-config")

	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read mcp config %q: %v", mcpPath, err)
	}

	if !strings.Contains(string(data), "graith") {
		t.Errorf("mcp config should contain the auto-injected graith server; got:\n%s", data)
	}
}

// TestClaudeSessionHooksDisabledSkipsInjection verifies the other side of the
// gate: with hooks disabled (and not yolo), a PTY Claude session gets neither
// --settings nor --mcp-config — MCP still tracks the hook gate for PTY, so this
// stays a pure refactor of the pre-#1135 behaviour.
func TestClaudeSessionHooksDisabledSkipsInjection(t *testing.T) {
	repoDir := initTempGitRepo(t)
	sm, recordPath := newClaudeRecorderManager(t, repoDir)

	created, err := sm.Create(CreateOpts{
		Name: "thrawn", AgentName: "claude", RepoPath: repoDir, BaseBranch: "main",
		AgentHooks: false, SkipModelValidation: true, Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	id := created.ID

	t.Cleanup(func() { stopAndClosePTY(sm, id) })

	// The recorder writes argv as soon as the agent launches; "sh" is always
	// argv[0], so wait on that to know the record is present.
	argv := waitForRecordedArgv(t, recordPath, "sh")

	for _, a := range argv {
		if a == "--settings" || a == "--mcp-config" {
			t.Errorf("hooks-disabled session must not inject %q; argv %v", a, argv)
		}
	}
}

func TestForkSingletonRejects(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir, Singleton: true}}

	sm.state.Sessions["braw-source"] = &SessionState{
		ID:       "braw-source",
		Name:     "braw-source-session",
		RepoPath: repoDir,
		RepoName: "repo",
		Branch:   "braw-branch",
		Agent:    "claude",
		Status:   StatusRunning,
	}

	_, err := sm.Fork("braw-fork", "braw-source", 24, 80)
	assertErrContains(t, err, "singleton")
}

func TestToSessionInfoIncludes(t *testing.T) {
	sess := SessionState{
		ID:           "abc",
		Name:         "braw",
		Agent:        "claude",
		WorktreePath: "/session/mono-repo",
		Status:       StatusRunning,
		CreatedAt:    time.Now().UTC(),
		Includes: []IncludedRepoState{
			{
				RepoPath:     "/home/user/Code/frontend",
				RepoName:     "frontend",
				WorktreePath: "/session/frontend",
				Branch:       "user/graith/test/frontend",
				BaseBranch:   "main",
				dirty:        true,
				unpushed:     3,
			},
		},
	}

	info := toSessionInfo(sess, config.Default(), nil)
	if len(info.Includes) != 1 {
		t.Fatalf("Includes length = %d, want 1", len(info.Includes))
	}

	inc := info.Includes[0]
	if inc.RepoName != "frontend" {
		t.Errorf("RepoName = %q, want %q", inc.RepoName, "frontend")
	}

	if inc.WorktreePath != "/session/frontend" {
		t.Errorf("WorktreePath = %q, want %q", inc.WorktreePath, "/session/frontend")
	}

	if inc.Branch != "user/graith/test/frontend" {
		t.Errorf("Branch = %q, want %q", inc.Branch, "user/graith/test/frontend")
	}

	if !inc.Dirty {
		t.Error("Dirty = false, want true")
	}

	if inc.Unpushed != 3 {
		t.Errorf("Unpushed = %d, want 3", inc.Unpushed)
	}
}

func TestToSessionInfoNoIncludes(t *testing.T) {
	sess := SessionState{
		ID:        "abc",
		Name:      "braw",
		Agent:     "claude",
		Status:    StatusRunning,
		CreatedAt: time.Now().UTC(),
	}

	info := toSessionInfo(sess, config.Default(), nil)
	if len(info.Includes) != 0 {
		t.Errorf("Includes length = %d, want 0", len(info.Includes))
	}
}

func TestStateSaveLoadIncludes(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := NewState()
	state.Sessions["s1"] = &SessionState{
		ID:           "s1",
		Name:         "mono-repo-session",
		RepoPath:     "/home/user/mono-repo",
		RepoName:     "mono-repo",
		WorktreePath: "/data/worktrees/mono-repo/hash/s1/mono-repo",
		Branch:       "user/graith/s1",
		Agent:        "claude",
		Status:       StatusStopped,
		CreatedAt:    time.Now().UTC(),
		Includes: []IncludedRepoState{
			{
				RepoPath:     "/home/user/frontend",
				RepoName:     "frontend",
				WorktreePath: "/data/worktrees/mono-repo/hash/s1/frontend",
				Branch:       "user/graith/s1/frontend",
				BaseBranch:   "main",
			},
			{
				RepoPath:     "/home/user/shared-lib",
				RepoName:     "shared-lib",
				WorktreePath: "/data/worktrees/mono-repo/hash/s1/shared-lib",
				Branch:       "user/graith/s1/shared-lib",
				BaseBranch:   "main",
			},
		},
	}

	if err := SaveState(statePath, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	s := loaded.Sessions["s1"]
	if s == nil {
		t.Fatal("session s1 not found after load")
	}

	if len(s.Includes) != 2 {
		t.Fatalf("Includes length = %d, want 2", len(s.Includes))
	}

	if s.Includes[0].RepoName != "frontend" {
		t.Errorf("Includes[0].RepoName = %q, want %q", s.Includes[0].RepoName, "frontend")
	}

	if s.Includes[1].RepoName != "shared-lib" {
		t.Errorf("Includes[1].RepoName = %q, want %q", s.Includes[1].RepoName, "shared-lib")
	}

	if s.Includes[0].WorktreePath != "/data/worktrees/mono-repo/hash/s1/frontend" {
		t.Errorf("Includes[0].WorktreePath = %q", s.Includes[0].WorktreePath)
	}
}

func TestResumeIncludesValidatesMissingWorktree(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.state.Sessions["s1"] = &SessionState{
		ID:           "s1",
		Name:         "braw",
		Agent:        "claude",
		RepoPath:     "/some/repo",
		WorktreePath: "/some/worktree",
		Status:       StatusStopped,
		Includes: []IncludedRepoState{
			{
				RepoPath:     "/some/included",
				RepoName:     "included",
				WorktreePath: "/does/not/exist",
				Branch:       "test-branch",
				BaseBranch:   "main",
			},
		},
	}

	_, err := sm.Resume("s1", 24, 80)
	assertErrContains(t, err, "no longer a valid git repo")
}

func TestParentIDPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	state := &State{
		Version: CurrentStateVersion,
		Sessions: map[string]*SessionState{
			"child1": {
				ID:           "child1",
				ParentID:     "parent1",
				Name:         "bairn",
				Agent:        "claude",
				WorktreePath: "/some/path",
				Status:       StatusRunning,
				CreatedAt:    time.Now().UTC(),
			},
		},
	}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	s := loaded.Sessions["child1"]
	if s.ParentID != "parent1" {
		t.Errorf("ParentID = %q, want %q", s.ParentID, "parent1")
	}
}

func TestToSessionInfoParentID(t *testing.T) {
	sess := SessionState{
		ID:        "child",
		ParentID:  "parent",
		Name:      "braw",
		Agent:     "claude",
		Status:    StatusRunning,
		CreatedAt: time.Now().UTC(),
	}

	info := toSessionInfo(sess, config.Default(), nil)
	if info.ParentID != "parent" {
		t.Errorf("SessionInfo.ParentID = %q, want %q", info.ParentID, "parent")
	}
}

func TestCollectDescendants(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.state.Sessions = map[string]*SessionState{
		"ben":            {ID: "ben", Name: "ben"},
		"bairn-one":      {ID: "bairn-one", ParentID: "ben", Name: "bairn-one"},
		"bairn-two":      {ID: "bairn-two", ParentID: "ben", Name: "bairn-two"},
		"wee-bairn":      {ID: "wee-bairn", ParentID: "bairn-one", Name: "wee-bairn"},
		"thrawn-session": {ID: "thrawn-session", Name: "thrawn-session"},
	}

	result := sm.collectDescendants("ben")

	if len(result) != 4 {
		t.Fatalf("expected 4 sessions (ben + 3 descendants), got %d: %v", len(result), result)
	}

	resultSet := make(map[string]bool)
	for _, id := range result {
		resultSet[id] = true
	}

	for _, expected := range []string{"ben", "bairn-one", "bairn-two", "wee-bairn"} {
		if !resultSet[expected] {
			t.Errorf("missing expected session %q in result", expected)
		}
	}

	if resultSet["thrawn-session"] {
		t.Error("thrawn-session should not be in result")
	}

	indexOf := make(map[string]int)
	for i, id := range result {
		indexOf[id] = i
	}

	if indexOf["wee-bairn"] > indexOf["bairn-one"] {
		t.Error("wee-bairn should come before bairn-one (leaves first)")
	}

	if indexOf["bairn-one"] > indexOf["ben"] {
		t.Error("bairn-one should come before ben (leaves first)")
	}
}

func TestStateVersionRejectsNewer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	data := []byte(`{"version":999,"sessions":{}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadState(path)
	assertErrContains(t, err, "newer than this binary")
}

func TestRunMessageCleanupLoopReadsConfig(t *testing.T) {
	t.Run("does not exit when config starts at zero", func(t *testing.T) {
		sm := newTestSessionManager(t)

		ms, err := NewMsgStore(filepath.Join(t.TempDir(), "msg.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ms.Close() }()

		sm.SetMsgStore(ms)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})

		go func() {
			sm.RunMessageCleanupLoop(ctx)
			close(done)
		}()

		// Give the goroutine time to start — if the old code were still
		// present it would have returned immediately.
		time.Sleep(50 * time.Millisecond)

		select {
		case <-done:
			t.Fatal("RunMessageCleanupLoop exited early when config values are zero")
		default:
		}

		cancel()
		<-done
	})

	t.Run("picks up config change", func(t *testing.T) {
		sm := newTestSessionManager(t)

		ms, err := NewMsgStore(filepath.Join(t.TempDir(), "msg.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ms.Close() }()

		sm.SetMsgStore(ms)

		if _, err := ms.Publish(PublishOpts{Stream: "blether", SenderID: "s1", SenderName: "agent", Body: "old msg"}); err != nil {
			t.Fatal(err)
		}

		// Config starts at zero — cleanup should be a no-op.
		sm.runMessageCleanupFromConfig()

		msgs, err := ms.Read("blether", "", false, "")
		if err != nil {
			t.Fatal(err)
		}

		if len(msgs) != 1 {
			t.Fatalf("expected 1 message before config change, got %d", len(msgs))
		}

		// Swap config via applyConfig to match the real hot-reload path.
		newCfg := *sm.cfg
		newCfg.Messages.MaxPerStream = 0
		newCfg.Messages.MaxAge = "1ns"
		sm.applyConfig(&newCfg)

		sm.runMessageCleanupFromConfig()

		msgs, err = ms.Read("blether", "", false, "")
		if err != nil {
			t.Fatal(err)
		}

		if len(msgs) != 0 {
			t.Fatalf("expected 0 messages after config change, got %d", len(msgs))
		}
	})
}

func TestResumeRejectsDeletingSession(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.state.Sessions["del1"] = &SessionState{
		ID:           "del1",
		Name:         "dreich-deleting",
		Agent:        "claude",
		Status:       StatusDeleting,
		WorktreePath: "/tmp/whatever",
	}

	_, err := sm.Resume("del1", 24, 80)
	assertErrContains(t, err, "is being deleted")
}

func TestDeleteSetsDeletingStatus(t *testing.T) {
	tmpDir := t.TempDir()

	worktreeDir := filepath.Join(tmpDir, "existing-worktree")
	if err := os.MkdirAll(worktreeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	sm := newTestSessionManager(t)
	sm.state.Sessions["s1"] = &SessionState{
		ID:           "s1",
		Name:         "braw",
		Agent:        "claude",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: worktreeDir,
		Branch:       "graith/braw-s1",
		Status:       StatusStopped,
	}

	// Delete will fail because worktree dir exists but repo path is invalid.
	// The session should be reverted to stopped.
	err := sm.Delete("s1")
	if err == nil {
		t.Fatal("expected error from failed git teardown")
	}

	sm.mu.RLock()
	s, ok := sm.state.Sessions["s1"]
	sm.mu.RUnlock()

	if !ok {
		t.Fatal("session should be kept for retry")
	}

	if s.Status != StatusStopped {
		t.Errorf("status = %q after failed teardown, want %q (reverted from deleting)", s.Status, StatusStopped)
	}
}

func TestDeleteKeepsSessionOnTeardownFailure(t *testing.T) {
	tmpDir := t.TempDir()

	worktreeDir := filepath.Join(tmpDir, "existing-worktree")
	if err := os.MkdirAll(worktreeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	sm := newTestSessionManager(t)
	sm.state.Sessions["fail1"] = &SessionState{
		ID:           "fail1",
		Name:         "dreich-leaky",
		Agent:        "claude",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: worktreeDir,
		Branch:       "graith/leaky-fail1",
		Status:       StatusStopped,
	}

	err := sm.Delete("fail1")
	assertErrContains(t, err, "git teardown failed")

	if _, ok := sm.state.Sessions["fail1"]; !ok {
		t.Error("session should be kept in state when teardown fails")
	}
}

func TestDeleteSucceedsWhenWorktreeAlreadyGone(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["gone1"] = &SessionState{
		ID:           "gone1",
		Name:         "haar-cleaned",
		Agent:        "claude",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: "/nonexistent/worktree",
		Branch:       "graith/gone-gone1",
		Status:       StatusStopped,
	}

	err := sm.Delete("gone1")
	if err != nil {
		t.Fatalf("Delete should succeed when worktree and branch are already gone: %v", err)
	}

	if _, ok := sm.state.Sessions["gone1"]; ok {
		t.Error("session should be removed from state after successful idempotent delete")
	}
}

// assertDeleteRemovesRuntimeFile creates a stopped session, writes a per-session
// runtime artifact at pathFn(sm, id), deletes the session, and asserts the file
// is gone. Shared by the nono-profile and safehouse-fragment cleanup tests so
// they don't duplicate the setup boilerplate.
func assertDeleteRemovesRuntimeFile(t *testing.T, id, name, contents string, pathFn func(*SessionManager, string) string) {
	t.Helper()

	sm := newTestSessionManager(t)
	sm.paths.RuntimeDir = t.TempDir()

	sm.state.Sessions[id] = &SessionState{
		ID:           id,
		Name:         name,
		Agent:        "claude",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: "/nonexistent/worktree",
		Branch:       "graith/" + name + "-" + id,
		Status:       StatusStopped,
	}

	path := pathFn(sm, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := sm.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s should be removed after delete, stat err = %v", filepath.Base(path), err)
	}
}

func TestDeleteRemovesNonoProfile(t *testing.T) {
	assertDeleteRemovesRuntimeFile(t, "braw1", "braw", "{}", (*SessionManager).nonoProfilePath)
}

func TestNonoProfilePathMatchesWrapOpts(t *testing.T) {
	// The leak fix relies on Delete removing exactly the path that
	// sandboxOptsFromConfig tells the nono backend to write. Both now route
	// through nonoProfilePath; this locks that invariant so a future refactor
	// reintroducing a literal path in one site is caught.
	sm := newTestSessionManager(t)
	sm.paths.RuntimeDir = t.TempDir()

	opts := sm.sandboxOptsFromConfig(config.SandboxConfig{}, "braw1", "/nonexistent/worktree", "claude", nil, true)

	if opts.ProfilePath != sm.nonoProfilePath("braw1") {
		t.Errorf("write path %q != cleanup path %q", opts.ProfilePath, sm.nonoProfilePath("braw1"))
	}
}

// TestDeleteRemovesSafehouseFragment: a regression that drops the os.Remove for
// the safehouse --append-profile fragment would leak a stale .sb file under
// RuntimeDir on every session delete.
func TestDeleteRemovesSafehouseFragment(t *testing.T) {
	assertDeleteRemovesRuntimeFile(t, "canny1", "canny", ";; frag\n", (*SessionManager).safehouseFragmentPath)
}

func TestSafehouseFragmentPathMatchesWrapOpts(t *testing.T) {
	// Like TestNonoProfilePathMatchesWrapOpts: Delete must remove exactly the
	// path sandboxOptsFromConfig tells the safehouse backend to write.
	sm := newTestSessionManager(t)
	sm.paths.RuntimeDir = t.TempDir()

	opts := sm.sandboxOptsFromConfig(config.SandboxConfig{}, "braw1", "/nonexistent/worktree", "claude", nil, true)

	if opts.SafehouseFragmentPath != sm.safehouseFragmentPath("braw1") {
		t.Errorf("fragment write path %q != cleanup path %q", opts.SafehouseFragmentPath, sm.safehouseFragmentPath("braw1"))
	}
}

func TestResolveSocketPathResolvesSymlinks(t *testing.T) {
	// Seatbelt/Landlock match canonical paths, so the daemon must symlink-resolve
	// the socket path before granting it — otherwise a data/runtime dir under a
	// symlinked prefix silently re-denies connect (the original bug).
	realDir := t.TempDir()

	linkDir := filepath.Join(t.TempDir(), "brig") // a symlink pointing at realDir
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// The socket file must exist for EvalSymlinks to resolve it.
	sockViaReal := filepath.Join(realDir, "graith.sock")
	if err := os.WriteFile(sockViaReal, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got := resolveSocketPath(filepath.Join(linkDir, "graith.sock"))

	want, err := filepath.EvalSymlinks(sockViaReal)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	if got != want {
		t.Errorf("resolveSocketPath through symlink = %q, want canonical %q", got, want)
	}

	// Falls back to the raw path when nothing resolves (path does not exist).
	missing := filepath.Join(realDir, "nae-such", "graith.sock")
	if got := resolveSocketPath(missing); got != missing {
		t.Errorf("resolveSocketPath(missing) = %q, want raw %q", got, missing)
	}
}

func TestRollbackOrchestratorCreateRemovesNonoProfile(t *testing.T) {
	// createOrchestrator writes a nono profile via sandboxOptsFromConfig before
	// it can fail (PTY start / state persist), and rollback deletes the session
	// from state so no later Delete runs. rollbackOrchestratorCreate must remove
	// the profile itself, mirroring cleanupOnError/forkCleanup.
	sm := newTestSessionManager(t)
	sm.paths.RuntimeDir = t.TempDir()

	sm.state.Sessions["thrawn1"] = &SessionState{
		ID:     "thrawn1",
		Name:   "thrawn",
		Agent:  "claude",
		Status: StatusCreating,
	}

	profilePath := sm.nonoProfilePath("thrawn1")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := os.WriteFile(profilePath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	sm.rollbackOrchestratorCreate("thrawn1")

	if _, ok := sm.state.Sessions["thrawn1"]; ok {
		t.Error("session should be removed from state after rollback")
	}

	if _, err := os.Stat(profilePath); !os.IsNotExist(err) {
		t.Errorf("nono profile should be removed after rollback, stat err = %v", err)
	}
}

func TestDeleteWithChildrenRemovesNonoProfiles(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.paths.RuntimeDir = t.TempDir()

	sm.state.Sessions["ben1"] = &SessionState{
		ID:           "ben1",
		Name:         "ben",
		Agent:        "claude",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: "/nonexistent/worktree",
		Branch:       "graith/ben-ben1",
		Status:       StatusStopped,
	}
	sm.state.Sessions["bairn1"] = &SessionState{
		ID:           "bairn1",
		Name:         "bairn",
		Agent:        "claude",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: "/nonexistent/worktree",
		Branch:       "graith/bairn-bairn1",
		ParentID:     "ben1",
		Status:       StatusStopped,
	}

	for _, id := range []string{"ben1", "bairn1"} {
		p := sm.nonoProfilePath(id)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	if _, err := sm.DeleteWithChildren("ben1", false); err != nil {
		t.Fatalf("DeleteWithChildren: %v", err)
	}

	for _, id := range []string{"ben1", "bairn1"} {
		if _, err := os.Stat(sm.nonoProfilePath(id)); !os.IsNotExist(err) {
			t.Errorf("nono profile for %s should be removed, stat err = %v", id, err)
		}
	}
}

func TestDeleteReparentsChildren(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["grandparent"] = &SessionState{
		ID:     "grandparent",
		Name:   "brae",
		Agent:  "claude",
		Status: StatusStopped,
	}
	sm.state.Sessions["ben"] = &SessionState{
		ID:           "ben",
		Name:         "ben",
		Agent:        "claude",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: "/nonexistent/worktree",
		Branch:       "graith/ben-ben",
		ParentID:     "grandparent",
		Status:       StatusStopped,
	}
	sm.state.Sessions["bairn-one"] = &SessionState{
		ID:       "bairn-one",
		Name:     "bairn-one",
		Agent:    "claude",
		ParentID: "ben",
		Status:   StatusStopped,
	}
	sm.state.Sessions["bairn-two"] = &SessionState{
		ID:       "bairn-two",
		Name:     "bairn-two",
		Agent:    "claude",
		ParentID: "ben",
		Status:   StatusStopped,
	}
	sm.state.Sessions["thrawn-session"] = &SessionState{
		ID:       "thrawn-session",
		Name:     "thrawn-session",
		Agent:    "claude",
		ParentID: "grandparent",
		Status:   StatusStopped,
	}

	err := sm.Delete("ben")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if _, ok := sm.state.Sessions["ben"]; ok {
		t.Error("deleted session should be removed from state")
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.state.Sessions["bairn-one"].ParentID != "grandparent" {
		t.Errorf("bairn-one.ParentID = %q, want %q", sm.state.Sessions["bairn-one"].ParentID, "grandparent")
	}

	if sm.state.Sessions["bairn-two"].ParentID != "grandparent" {
		t.Errorf("bairn-two.ParentID = %q, want %q", sm.state.Sessions["bairn-two"].ParentID, "grandparent")
	}

	if sm.state.Sessions["thrawn-session"].ParentID != "grandparent" {
		t.Errorf("thrawn-session.ParentID = %q, want %q (should be unchanged)", sm.state.Sessions["thrawn-session"].ParentID, "grandparent")
	}
}

func TestDeleteReparentsChildrenToRoot(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["ben"] = &SessionState{
		ID:           "ben",
		Name:         "ben",
		Agent:        "claude",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: "/nonexistent/worktree",
		Branch:       "graith/ben-ben",
		Status:       StatusStopped,
	}
	sm.state.Sessions["bairn"] = &SessionState{
		ID:       "bairn",
		Name:     "bairn",
		Agent:    "claude",
		ParentID: "ben",
		Status:   StatusStopped,
	}

	err := sm.Delete("ben")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.state.Sessions["bairn"].ParentID != "" {
		t.Errorf("bairn.ParentID = %q, want empty (top-level)", sm.state.Sessions["bairn"].ParentID)
	}
}

func TestDeleteWithChildrenKeepsFailedSessions(t *testing.T) {
	tmpDir := t.TempDir()

	worktreeDir := filepath.Join(tmpDir, "existing-child-worktree")
	if err := os.MkdirAll(worktreeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	sm := newTestSessionManager(t)

	sm.state.Sessions["parent1"] = &SessionState{
		ID:     "parent1",
		Name:   "ben",
		Agent:  "claude",
		Status: StatusStopped,
	}
	sm.state.Sessions["child1"] = &SessionState{
		ID:           "child1",
		Name:         "bairn",
		Agent:        "claude",
		ParentID:     "parent1",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: worktreeDir,
		Branch:       "graith/bairn-child1",
		Status:       StatusStopped,
	}

	deleted, err := sm.DeleteWithChildren("parent1", false)
	if err == nil {
		t.Fatal("expected error from failed git teardown")
	}

	if _, ok := sm.state.Sessions["child1"]; !ok {
		t.Error("child session should be kept in state when teardown fails")
	}

	// Child failed but should be reverted to stopped, not stuck in deleting.
	if s := sm.state.Sessions["child1"]; s.Status != StatusStopped {
		t.Errorf("child status = %q, want %q (reverted from deleting)", s.Status, StatusStopped)
	}

	found := false

	for _, id := range deleted {
		if id == "parent1" {
			found = true
		}
	}

	if !found {
		t.Error("parent (no repo) should be in the deleted list")
	}
}

func TestDeleteWithChildrenIdempotent(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["parent1"] = &SessionState{
		ID:     "parent1",
		Name:   "ben",
		Agent:  "claude",
		Status: StatusStopped,
	}
	sm.state.Sessions["child1"] = &SessionState{
		ID:           "child1",
		Name:         "bairn",
		Agent:        "claude",
		ParentID:     "parent1",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: "/nonexistent/worktree",
		Branch:       "graith/bairn-child1",
		Status:       StatusStopped,
	}

	deleted, err := sm.DeleteWithChildren("parent1", false)
	if err != nil {
		t.Fatalf("expected idempotent delete to succeed: %v", err)
	}

	if len(deleted) != 2 {
		t.Errorf("expected 2 deleted sessions, got %d: %v", len(deleted), deleted)
	}

	if _, ok := sm.state.Sessions["parent1"]; ok {
		t.Error("parent should be removed from state")
	}

	if _, ok := sm.state.Sessions["child1"]; ok {
		t.Error("child should be removed from state")
	}
}

func TestDeleteWithChildrenGrandchildren(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["parent1"] = &SessionState{
		ID:     "parent1",
		Name:   "ben",
		Agent:  "claude",
		Status: StatusStopped,
	}
	sm.state.Sessions["child1"] = &SessionState{
		ID:       "child1",
		Name:     "bairn",
		Agent:    "claude",
		ParentID: "parent1",
		Status:   StatusStopped,
	}
	sm.state.Sessions["grandchild1"] = &SessionState{
		ID:       "grandchild1",
		Name:     "wee-bairn",
		Agent:    "claude",
		ParentID: "child1",
		Status:   StatusStopped,
	}

	deleted, err := sm.DeleteWithChildren("parent1", false)
	if err != nil {
		t.Fatalf("DeleteWithChildren failed: %v", err)
	}

	if len(deleted) != 3 {
		t.Errorf("expected 3 deleted sessions, got %d: %v", len(deleted), deleted)
	}

	for _, id := range []string{"parent1", "child1", "grandchild1"} {
		if _, ok := sm.state.Sessions[id]; ok {
			t.Errorf("%s should be removed from state", id)
		}
	}
}

func TestDeleteWithChildrenExcludeRootGrandchildren(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["parent1"] = &SessionState{
		ID:     "parent1",
		Name:   "ben",
		Agent:  "claude",
		Status: StatusRunning,
	}
	sm.state.Sessions["child1"] = &SessionState{
		ID:       "child1",
		Name:     "bairn",
		Agent:    "claude",
		ParentID: "parent1",
		Status:   StatusStopped,
	}
	sm.state.Sessions["grandchild1"] = &SessionState{
		ID:       "grandchild1",
		Name:     "wee-bairn",
		Agent:    "claude",
		ParentID: "child1",
		Status:   StatusStopped,
	}

	deleted, err := sm.DeleteWithChildren("parent1", true)
	if err != nil {
		t.Fatalf("DeleteWithChildren failed: %v", err)
	}

	if len(deleted) != 2 {
		t.Errorf("expected 2 deleted sessions, got %d: %v", len(deleted), deleted)
	}

	if _, ok := sm.state.Sessions["parent1"]; !ok {
		t.Error("parent should NOT be removed from state (excludeRoot)")
	}

	for _, id := range []string{"child1", "grandchild1"} {
		if _, ok := sm.state.Sessions[id]; ok {
			t.Errorf("%s should be removed from state", id)
		}
	}
}

func TestRestartWithChildrenNotFound(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.RestartWithChildren("nonexistent", false, 24, 80)
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestRestartWithChildrenSkipsStarredAndTransient(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["parent1"] = &SessionState{
		ID:     "parent1",
		Name:   "ben",
		Agent:  "claude",
		Status: StatusStopped,
	}
	sm.state.Sessions["starred1"] = &SessionState{
		ID:       "starred1",
		Name:     "starred-child",
		Agent:    "claude",
		ParentID: "parent1",
		Status:   StatusStopped,
		Starred:  true,
	}
	sm.state.Sessions["deleting1"] = &SessionState{
		ID:       "deleting1",
		Name:     "deleting-child",
		Agent:    "claude",
		ParentID: "parent1",
		Status:   StatusDeleting,
	}
	sm.state.Sessions["creating1"] = &SessionState{
		ID:       "creating1",
		Name:     "creating-child",
		Agent:    "claude",
		ParentID: "parent1",
		Status:   StatusCreating,
	}

	restarted, err := sm.RestartWithChildren("parent1", true, 24, 80)
	if err != nil {
		t.Fatalf("RestartWithChildren failed: %v", err)
	}

	if len(restarted) != 0 {
		t.Errorf("expected 0 restarted sessions (all should be skipped or fail), got %d: %v", len(restarted), restarted)
	}

	skippable := map[string]bool{"starred1": true, "deleting1": true, "creating1": true}
	for _, id := range restarted {
		if skippable[id] {
			t.Errorf("session %s should have been skipped", id)
		}
	}
}

func TestRestartWithChildrenExcludeRoot(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["parent1"] = &SessionState{
		ID:     "parent1",
		Name:   "ben",
		Agent:  "claude",
		Status: StatusStopped,
	}
	sm.state.Sessions["child1"] = &SessionState{
		ID:       "child1",
		Name:     "bairn",
		Agent:    "claude",
		ParentID: "parent1",
		Status:   StatusStopped,
	}

	restarted, err := sm.RestartWithChildren("parent1", true, 24, 80)
	if err != nil {
		t.Fatalf("RestartWithChildren failed: %v", err)
	}

	for _, id := range restarted {
		if id == "parent1" {
			t.Error("parent should be excluded when excludeRoot is true")
		}
	}
}

func TestRestartWithChildrenGrandchildren(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["root1"] = &SessionState{
		ID:     "root1",
		Name:   "ben",
		Agent:  "claude",
		Status: StatusStopped,
	}
	sm.state.Sessions["child1"] = &SessionState{
		ID:       "child1",
		Name:     "bairn",
		Agent:    "claude",
		ParentID: "root1",
		Status:   StatusStopped,
	}
	sm.state.Sessions["grandchild1"] = &SessionState{
		ID:       "grandchild1",
		Name:     "wee-bairn",
		Agent:    "claude",
		ParentID: "child1",
		Status:   StatusStopped,
	}

	restarted, err := sm.RestartWithChildren("root1", true, 24, 80)
	if err != nil {
		t.Fatalf("RestartWithChildren failed: %v", err)
	}

	for _, id := range restarted {
		if id == "root1" {
			t.Error("root should be excluded with excludeRoot=true")
		}
	}
}

func TestRestartWithChildrenIncludeRoot(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["parent1"] = &SessionState{
		ID:     "parent1",
		Name:   "ben",
		Agent:  "claude",
		Status: StatusStopped,
	}
	sm.state.Sessions["child1"] = &SessionState{
		ID:       "child1",
		Name:     "bairn",
		Agent:    "claude",
		ParentID: "parent1",
		Status:   StatusStopped,
	}

	restarted, err := sm.RestartWithChildren("parent1", false, 24, 80)
	if err != nil {
		t.Fatalf("RestartWithChildren failed: %v", err)
	}

	for _, id := range restarted {
		if id == "parent1" {
			return
		}
	}
}

func TestDeleteClearsChildParentID(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["ben"] = &SessionState{
		ID:     "ben",
		Name:   "ben",
		Agent:  "claude",
		Status: StatusStopped,
	}
	sm.state.Sessions["bairn-one"] = &SessionState{
		ID:       "bairn-one",
		Name:     "bairn-a",
		Agent:    "claude",
		ParentID: "ben",
		Status:   StatusStopped,
	}
	sm.state.Sessions["bairn-two"] = &SessionState{
		ID:       "bairn-two",
		Name:     "bairn-b",
		Agent:    "claude",
		ParentID: "ben",
		Status:   StatusStopped,
	}
	sm.state.Sessions["wee-bairn"] = &SessionState{
		ID:       "wee-bairn",
		Name:     "wee-bairn",
		Agent:    "claude",
		ParentID: "bairn-one",
		Status:   StatusStopped,
	}

	if err := sm.Delete("ben"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if _, ok := sm.state.Sessions["ben"]; ok {
		t.Error("ben should be removed from state")
	}

	if sm.state.Sessions["bairn-one"].ParentID != "" {
		t.Errorf("bairn-one ParentID = %q, want empty", sm.state.Sessions["bairn-one"].ParentID)
	}

	if sm.state.Sessions["bairn-two"].ParentID != "" {
		t.Errorf("bairn-two ParentID = %q, want empty", sm.state.Sessions["bairn-two"].ParentID)
	}

	if sm.state.Sessions["wee-bairn"].ParentID != "bairn-one" {
		t.Errorf("wee-bairn ParentID = %q, want %q (should be unchanged)", sm.state.Sessions["wee-bairn"].ParentID, "bairn-one")
	}
}

func TestDeleteClearsChildParentIDWhenCreating(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["creating-ben"] = &SessionState{
		ID:     "creating-ben",
		Name:   "creating-ben",
		Agent:  "claude",
		Status: StatusCreating,
	}
	sm.state.Sessions["bairn"] = &SessionState{
		ID:       "bairn",
		Name:     "bairn",
		Agent:    "claude",
		ParentID: "creating-ben",
		Status:   StatusRunning,
	}

	if err := sm.Delete("creating-ben"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if _, ok := sm.state.Sessions["creating-ben"]; ok {
		t.Error("creating-ben should be removed from state")
	}

	if sm.state.Sessions["bairn"].ParentID != "" {
		t.Errorf("bairn ParentID = %q, want empty", sm.state.Sessions["bairn"].ParentID)
	}
}

func TestDeleteWithChildrenClearsOrphanedParentIDs(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["root"] = &SessionState{
		ID:     "root",
		Name:   "ben",
		Agent:  "claude",
		Status: StatusStopped,
	}
	sm.state.Sessions["bairn"] = &SessionState{
		ID:       "child",
		Name:     "bairn",
		Agent:    "claude",
		ParentID: "root",
		Status:   StatusStopped,
	}
	sm.state.Sessions["starred-child"] = &SessionState{
		ID:       "starred-child",
		Name:     "starred-child",
		Agent:    "claude",
		ParentID: "root",
		Starred:  true,
		Status:   StatusStopped,
	}

	deleted, err := sm.DeleteWithChildren("root", false)
	if err != nil {
		t.Fatalf("DeleteWithChildren failed: %v", err)
	}

	if _, ok := sm.state.Sessions["root"]; ok {
		t.Error("root should be removed")
	}

	if _, ok := sm.state.Sessions["bairn"]; ok {
		t.Error("child should be removed")
	}

	starredChild := sm.state.Sessions["starred-child"]
	if starredChild == nil {
		t.Fatal("starred-child should survive (starred)")
	}

	if starredChild.ParentID != "" {
		t.Errorf("starred-child ParentID = %q, want empty (parent was deleted)", starredChild.ParentID)
	}

	_ = deleted
}

func createTestSession(sm *SessionManager, name string) string {
	id := generateID()
	sm.state.Sessions[id] = &SessionState{
		ID:     id,
		Name:   name,
		Status: StatusRunning,
	}

	return id
}

func TestSetSummary(t *testing.T) {
	sm := newTestSessionManager(t)
	id := createTestSession(sm, "braw-session")

	if err := sm.SetSummary(id, "Exploring code", 0); err != nil {
		t.Fatalf("SetSummary failed: %v", err)
	}

	s := sm.state.Sessions[id]
	if s.SummaryText != "Exploring code" {
		t.Errorf("SummaryText = %q, want %q", s.SummaryText, "Exploring code")
	}

	if s.SummarySetAt == nil {
		t.Fatal("SummarySetAt should not be nil")
	}

	if s.SummaryTTL != 0 {
		t.Errorf("SummaryTTL = %d, want 0", s.SummaryTTL)
	}
}

func TestSetSummary_WithTTL(t *testing.T) {
	sm := newTestSessionManager(t)
	id := createTestSession(sm, "braw-session")

	if err := sm.SetSummary(id, "Waiting for CI", 600); err != nil {
		t.Fatalf("SetSummary failed: %v", err)
	}

	s := sm.state.Sessions[id]
	if s.SummaryTTL != 600 {
		t.Errorf("SummaryTTL = %d, want 600", s.SummaryTTL)
	}
}

func TestSetSummary_Clear(t *testing.T) {
	sm := newTestSessionManager(t)
	id := createTestSession(sm, "braw-session")

	_ = sm.SetSummary(id, "Working", 0)

	if err := sm.ClearSummary(id); err != nil {
		t.Fatalf("ClearSummary failed: %v", err)
	}

	s := sm.state.Sessions[id]
	if s.SummaryText != "" {
		t.Errorf("SummaryText should be empty, got %q", s.SummaryText)
	}

	if s.SummarySetAt != nil {
		t.Error("SummarySetAt should be nil")
	}
}

func TestSetSummary_NotFound(t *testing.T) {
	sm := newTestSessionManager(t)
	if err := sm.SetSummary("nonexistent", "text", 0); err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestSetSummary_Validation(t *testing.T) {
	sm := newTestSessionManager(t)
	id := createTestSession(sm, "braw-session")

	longText := strings.Repeat("a", 101)
	if err := sm.SetSummary(id, longText, 0); err == nil {
		t.Error("expected error for text exceeding 100 bytes")
	}

	if err := sm.SetSummary(id, "has\nnewline", 0); err != nil {
		t.Fatalf("should succeed with control chars stripped: %v", err)
	}

	s := sm.state.Sessions[id]
	if strings.Contains(s.SummaryText, "\n") {
		t.Error("newline should have been stripped")
	}
}

func TestWatchSessionWritesLifecycleSummary(t *testing.T) {
	sm := newTestSessionManager(t)

	id := "sess-lifecycle"
	now := time.Now()
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "braw", Status: StatusRunning, Agent: "claude",
		SummaryText:  "Running tests",
		SummarySetAt: &now,
		SummaryTTL:   0,
	}

	sess := newTestPTYSession(t, "true")
	waitExit(t, sess)

	sm.sessions[id] = sess
	sm.watchSession(id, sess)

	sm.mu.RLock()
	s := sm.state.Sessions[id]
	summary := s.SummaryText

	sm.mu.RUnlock()

	if summary != "Exited (was: Running tests)" {
		t.Errorf("SummaryText = %q, want %q", summary, "Exited (was: Running tests)")
	}
}

func TestWatchSessionSkipsWhenStopAllAlreadyWrote(t *testing.T) {
	sm := newTestSessionManager(t)

	id := "sess-shutdown"
	now := time.Now()
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "braw", Status: StatusRunning, Agent: "claude",
		SummaryText:  "Stopped by shutdown (was: Building)",
		SummarySetAt: &now,
		SummaryTTL:   0,
		StopReason:   StopReasonShutdown,
	}

	sess := newTestPTYSession(t, "true")
	waitExit(t, sess)

	sm.sessions[id] = sess
	sm.watchSession(id, sess)

	sm.mu.RLock()
	summary := sm.state.Sessions[id].SummaryText
	sm.mu.RUnlock()

	if summary != "Stopped by shutdown (was: Building)" {
		t.Errorf("SummaryText = %q, want %q — watchSession should not nest shutdown summaries",
			summary, "Stopped by shutdown (was: Building)")
	}
}

func TestStopAllWritesShutdownSummary(t *testing.T) {
	sm := newTestSessionManager(t)

	now := time.Now()
	sm.state.Sessions["s1"] = &SessionState{
		ID: "s1", Name: "braw-worker", Status: StatusRunning, Agent: "claude",
		SummaryText:  "Running tests",
		SummarySetAt: &now,
	}
	sm.state.Sessions["s2"] = &SessionState{
		ID: "s2", Name: "bide-idle", Status: StatusStopped, Agent: "claude",
	}

	sm.StopAll(context.Background())

	s1 := sm.state.Sessions["s1"]
	if s1.SummaryText != "Stopped by shutdown (was: Running tests)" {
		t.Errorf("s1 SummaryText = %q, want %q", s1.SummaryText, "Stopped by shutdown (was: Running tests)")
	}

	if s1.StopReason != StopReasonShutdown {
		t.Errorf("s1 StopReason = %q, want %q", s1.StopReason, StopReasonShutdown)
	}

	s2 := sm.state.Sessions["s2"]
	if s2.SummaryText != "" {
		t.Errorf("s2 SummaryText = %q, want empty — stopped sessions should not get shutdown summary", s2.SummaryText)
	}
}

// TestWatchSessionPreservesHookDerivedStopSummary is the #1034 regression test
// at the real call site: a session whose only visible status was hook-derived
// (empty SummaryText, fresh hook report) must keep that "(was: …)" context when
// watchSession finalizes its stop summary. Fails against pre-fix code, where
// watchSession read s.SummaryText directly and produced a bare "Exited".
func TestWatchSessionPreservesHookDerivedStopSummary(t *testing.T) {
	sm := newTestSessionManager(t)

	id := "sess-hook-only"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "canny", Status: StatusRunning, Agent: "claude",
		// SummaryText intentionally empty — the only visible status was
		// hook-derived ("Using Bash"), computed on the fly and never stored.
	}
	sm.hookReports[id] = hookReport{
		ToolName:           "Bash",
		ReportedAt:         time.Now(),
		AuthoritativeUntil: time.Now().Add(30 * time.Minute),
	}

	sess := newTestPTYSession(t, "true")
	waitExit(t, sess)

	sm.sessions[id] = sess
	sm.watchSession(id, sess)

	sm.mu.RLock()
	summary := sm.state.Sessions[id].SummaryText
	sm.mu.RUnlock()

	if summary != "Exited (was: Using Bash)" {
		t.Errorf("SummaryText = %q, want %q", summary, "Exited (was: Using Bash)")
	}
}

// TestStopAllPreservesHookDerivedStopSummary is the #1034 regression test for
// the shutdown path: StopAll must fall back to the hook report for a hook-only
// session. Fails against pre-fix code (bare "Stopped by shutdown").
func TestStopAllPreservesHookDerivedStopSummary(t *testing.T) {
	sm := newTestSessionManager(t)

	id := "hook-only-worker"
	sm.state.Sessions[id] = &SessionState{
		ID: id, Name: "canny-worker", Status: StatusRunning, Agent: "claude",
	}
	sm.hookReports[id] = hookReport{
		ToolName:           "Bash",
		ReportedAt:         time.Now(),
		AuthoritativeUntil: time.Now().Add(30 * time.Minute),
	}

	sm.StopAll(context.Background())

	s := sm.state.Sessions[id]
	if s.SummaryText != "Stopped by shutdown (was: Using Bash)" {
		t.Errorf("SummaryText = %q, want %q", s.SummaryText, "Stopped by shutdown (was: Using Bash)")
	}
}

func TestStopAllWaitsConcurrently(t *testing.T) {
	sm := newTestSessionManager(t)

	// Use processes that trap SIGTERM and SIGHUP so each goroutine must hit
	// the force-kill timeout. Sequential would be 3*5s=15s; concurrent ~5s.
	// The loop ensures the shell keeps running even if sleep(1) doesn't
	// inherit SIG_IGN on some platforms (observed on macOS CI).
	const n = 3
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("s%d", i)
		sm.state.Sessions[id] = &SessionState{
			ID: id, Name: id, Status: StatusRunning, Agent: "claude",
		}
		sess := newTestPTYSession(t, "sh", "-c", "trap '' TERM HUP; while :; do sleep 30 || true; done")
		sm.sessions[id] = sess
	}

	start := time.Now()

	sm.StopAll(context.Background())

	elapsed := time.Since(start)

	// Concurrent: ~5s (one round of force-kill timeouts).
	// Sequential: ~15s (3 * 5s).
	if elapsed > 8*time.Second {
		t.Errorf("StopAll took %v, expected < 8s — sessions may be waited sequentially", elapsed)
	}

	if elapsed < 4*time.Second {
		t.Errorf("StopAll took %v, expected >= 4s — force-kill timeout may not be working", elapsed)
	}
}

func TestReconcileWritesLifecycleSummaries(t *testing.T) {
	t.Run("creating becomes errored", func(t *testing.T) {
		state := &State{Sessions: map[string]*SessionState{
			"c": {ID: "c", Name: "thrawn-create", Status: StatusCreating},
		}}
		state.Reconcile()

		if state.Sessions["c"].SummaryText != "Interrupted by daemon restart" {
			t.Errorf("SummaryText = %q, want %q", state.Sessions["c"].SummaryText, "Interrupted by daemon restart")
		}
	})

	t.Run("dead running becomes stopped", func(t *testing.T) {
		state := &State{Sessions: map[string]*SessionState{
			"r": {ID: "r", Name: "haar-orphan", Status: StatusRunning, PID: 99999999},
		}}
		state.Reconcile()

		if state.Sessions["r"].SummaryText != "Lost during daemon restart" {
			t.Errorf("SummaryText = %q, want %q", state.Sessions["r"].SummaryText, "Lost during daemon restart")
		}
	})

	t.Run("deleting becomes stopped", func(t *testing.T) {
		state := &State{Sessions: map[string]*SessionState{
			"d": {ID: "d", Name: "thrawn-delete", Status: StatusDeleting},
		}}
		state.Reconcile()

		if state.Sessions["d"].SummaryText != "Delete interrupted by restart" {
			t.Errorf("SummaryText = %q, want %q", state.Sessions["d"].SummaryText, "Delete interrupted by restart")
		}
	})

	t.Run("skips system sessions", func(t *testing.T) {
		state := &State{Sessions: map[string]*SessionState{
			"o": {ID: "o", Name: "orch", Status: StatusCreating, SystemKind: SystemKindOrchestrator},
		}}
		state.Reconcile()

		if state.Sessions["o"].SummaryText != "" {
			t.Errorf("SummaryText = %q, want empty for system session", state.Sessions["o"].SummaryText)
		}
	})
}

// TestConcurrentConfigReadWrite verifies that concurrent config reads via
// notify, approvals, and Config() do not race with applyConfig writes.
// Run with -race to detect data races.
func TestConcurrentConfigReadWrite(t *testing.T) {
	dir := t.TempDir()

	ms, err := NewMsgStore(filepath.Join(dir, "msg.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ms.Close() }()

	sm := newTestSessionManager(t)
	sm.messages = ms
	sm.mu.Lock()
	sm.state.Sessions["sess1"] = &SessionState{
		ID: "sess1", Name: "braw", Status: StatusRunning, Agent: "claude",
	}
	sm.mu.Unlock()

	done := make(chan struct{})

	// Writer: swap config repeatedly via applyConfig.
	go func() {
		defer func() { done <- struct{}{} }()

		for i := 0; i < 200; i++ {
			cfg := config.Default()
			cfg.Notifications.Enabled = i%2 == 0
			cfg.Notifications.OnApproval = true
			cfg.Notifications.OnStopped = true
			cfg.Notifications.Command = "true"
			cfg.Approvals.Timeout = "1s"
			sm.applyConfig(cfg)
		}
	}()

	// Reader 1: onAgentStatusChange reads notification config.
	go func() {
		defer func() { done <- struct{}{} }()

		for i := 0; i < 200; i++ {
			sm.onAgentStatusChange("sess1", "braw", "active", "approval")
		}
	}()

	// Reader 2: Config() snapshots the config.
	go func() {
		defer func() { done <- struct{}{} }()

		for i := 0; i < 200; i++ {
			cfg := sm.Config()
			_ = cfg.DefaultAgent
			_ = cfg.Notifications.Enabled
			_ = cfg.Approvals.TimeoutDuration()
		}
	}()

	// Reader 3: SubmitApproval reads approvals config.
	go func() {
		defer func() { done <- struct{}{} }()

		for i := 0; i < 50; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			sm.SubmitApproval(ctx, protocol.ApprovalRequestMsg{
				RequestID: fmt.Sprintf("r-%d", i),
				SessionID: "sess1",
				ToolName:  "Bash",
			})
			cancel()
		}
	}()

	for i := 0; i < 4; i++ {
		<-done
	}
}

func TestRecordExit_MassExitDetection(t *testing.T) {
	sm := newTestSessionManager(t)

	// Record exits below threshold — no warning expected.
	for i := 0; i < massExitThreshold-1; i++ {
		sm.recordExit()
	}

	if len(sm.recentExits) != massExitThreshold-1 {
		t.Errorf("recentExits = %d, want %d", len(sm.recentExits), massExitThreshold-1)
	}

	// One more should hit the threshold (warning is logged, not checked here).
	sm.recordExit()

	if len(sm.recentExits) != massExitThreshold {
		t.Errorf("recentExits = %d, want %d", len(sm.recentExits), massExitThreshold)
	}
}

func TestRecordExit_PrunesOldEntries(t *testing.T) {
	sm := newTestSessionManager(t)

	// Simulate old exits outside the window.
	old := time.Now().Add(-5 * time.Second)
	for i := 0; i < 3; i++ {
		sm.recentExits = append(sm.recentExits, old)
	}

	sm.recordExit()

	// Old entries should be pruned; only the new one remains.
	if len(sm.recentExits) != 1 {
		t.Errorf("recentExits = %d, want 1 (old entries should be pruned)", len(sm.recentExits))
	}
}

func TestWatchSessionRecordsExitSignal(t *testing.T) {
	sm := newTestSessionManager(t)

	id := "test-signal"
	sm.state.Sessions[id] = &SessionState{
		ID:     id,
		Name:   "signal-test",
		Status: StatusRunning,
	}

	cmd := exec.Command("sh", "-c", "kill -TERM $$")

	sess, err := grpty.NewSession(grpty.SessionOpts{
		ID:      id,
		Command: cmd.Path,
		Args:    cmd.Args[1:],
		Dir:     t.TempDir(),
		Rows:    24,
		Cols:    80,
		LogPath: filepath.Join(t.TempDir(), "test.log"),
	})
	if err != nil {
		t.Fatal(err)
	}

	sm.sessions[id] = sess

	sm.watchSession(id, sess)

	s := sm.state.Sessions[id]
	if s.ExitSignal != "terminated" {
		t.Errorf("ExitSignal = %q, want %q", s.ExitSignal, "terminated")
	}

	if s.Status != StatusStopped {
		t.Errorf("Status = %q, want %q", s.Status, StatusStopped)
	}
}

// putSession is a small helper that inserts a session directly into the
// manager's state without spawning a PTY, so lifecycle/query logic can be
// exercised without a real process or socket.
func putSession(sm *SessionManager, s *SessionState) {
	sm.mu.Lock()
	sm.state.Sessions[s.ID] = s
	sm.mu.Unlock()
}

// spawnContainedSleeper starts a long-lived child in its own process group and
// returns its PID. It is used as a *live* PID fixture for the kill/orphan guard
// tests: the guards under test are supposed to refuse to signal this PID, but if
// one ever regresses, the resulting kill is contained to this child's process
// group (Setpgid) instead of taking down the test runner or unrelated
// processes. Cleanup SIGKILLs the group and reaps the child.
func spawnContainedSleeper(t *testing.T) int {
	t.Helper()

	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}

	pid := cmd.Process.Pid

	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	return pid
}

func TestCovStarUnstarLifecycle(t *testing.T) {
	sm := newTestSessionManager(t)
	putSession(sm, &SessionState{ID: "braw1", Name: "braw", Status: StatusStopped})

	if err := sm.Star("braw1"); err != nil {
		t.Fatalf("Star: %v", err)
	}

	if s, _ := sm.Get("braw1"); !s.Starred {
		t.Fatal("expected session to be starred")
	}

	if err := sm.Unstar("braw1"); err != nil {
		t.Fatalf("Unstar: %v", err)
	}

	if s, _ := sm.Get("braw1"); s.Starred {
		t.Fatal("expected session to be unstarred")
	}

	if err := sm.Star("haar-missing"); err == nil {
		t.Fatal("expected error starring unknown session")
	}

	if err := sm.Unstar("haar-missing"); err == nil {
		t.Fatal("expected error unstarring unknown session")
	}

	// A session being deleted rejects both operations.
	putSession(sm, &SessionState{ID: "thrawn1", Name: "thrawn", Status: StatusDeleting})

	if err := sm.Star("thrawn1"); err == nil {
		t.Fatal("expected error starring a deleting session")
	}

	if err := sm.Unstar("thrawn1"); err == nil {
		t.Fatal("expected error unstarring a deleting session")
	}
}

func TestCovStarBlocksDelete(t *testing.T) {
	sm := newTestSessionManager(t)

	// A no-repo scratch session whose worktree is a real temp dir so Delete
	// can exercise the os.RemoveAll teardown branch.
	dir := filepath.Join(t.TempDir(), "bothy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	putSession(sm, &SessionState{ID: "canny1", Name: "canny", Status: StatusStopped, WorktreePath: dir})

	if err := sm.Star("canny1"); err != nil {
		t.Fatalf("Star: %v", err)
	}

	if err := sm.Delete("canny1"); err == nil {
		t.Fatal("expected starred session to reject Delete")
	}

	if err := sm.Unstar("canny1"); err != nil {
		t.Fatalf("Unstar: %v", err)
	}

	if err := sm.Delete("canny1"); err != nil {
		t.Fatalf("Delete after unstar: %v", err)
	}

	if _, ok := sm.Get("canny1"); ok {
		t.Fatal("expected session removed after Delete")
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected worktree dir removed, stat err = %v", err)
	}
}

func TestCovFleetSummaryCounts(t *testing.T) {
	sm := newTestSessionManager(t)

	putSession(sm, &SessionState{ID: "s1", Status: StatusRunning, AgentStatus: "active"})
	putSession(sm, &SessionState{ID: "s2", Status: StatusRunning, AgentStatus: "approval"})
	putSession(sm, &SessionState{ID: "s3", Status: StatusRunning, AgentStatus: "ready"})
	putSession(sm, &SessionState{ID: "s4", Status: StatusRunning, AgentStatus: ""}) // default -> active
	putSession(sm, &SessionState{ID: "s5", Status: StatusCreating})                 // counts as active
	putSession(sm, &SessionState{ID: "s6", Status: StatusStopped})
	putSession(sm, &SessionState{ID: "s7", Status: StatusErrored})

	f := sm.fleetSummary()

	if f.Total != 7 {
		t.Errorf("Total = %d, want 7", f.Total)
	}

	if f.Active != 3 {
		t.Errorf("Active = %d, want 3 (active + default-running + creating)", f.Active)
	}

	if f.Approval != 1 {
		t.Errorf("Approval = %d, want 1", f.Approval)
	}

	if f.Ready != 1 {
		t.Errorf("Ready = %d, want 1", f.Ready)
	}

	if f.Stopped != 1 {
		t.Errorf("Stopped = %d, want 1", f.Stopped)
	}

	if f.Errored != 1 {
		t.Errorf("Errored = %d, want 1", f.Errored)
	}
}

func TestCovDiagnosticsReportsSessions(t *testing.T) {
	sm := newTestSessionManager(t)
	wt := t.TempDir()
	putSession(sm, &SessionState{ID: "ken1", Name: "ken", Status: StatusRunning, WorktreePath: wt, Token: "tok"})
	putSession(sm, &SessionState{ID: "ken2", Name: "ken-two", Status: StatusStopped})

	d := sm.Diagnostics()

	if d.Fleet.Total != 2 {
		t.Errorf("Fleet.Total = %d, want 2", d.Fleet.Total)
	}

	if len(d.Sessions) != 2 {
		t.Fatalf("len(Sessions) = %d, want 2", len(d.Sessions))
	}

	if d.DaemonPID != os.Getpid() {
		t.Errorf("DaemonPID = %d, want %d", d.DaemonPID, os.Getpid())
	}

	var found bool

	for _, s := range d.Sessions {
		if s.ID == "ken1" {
			found = true

			if !s.WorktreeExists {
				t.Error("expected WorktreeExists true for existing worktree")
			}

			if !s.HasToken {
				t.Error("expected HasToken true")
			}
		}
	}

	if !found {
		t.Fatal("session ken1 not present in diagnostics")
	}
}

// TestDiagnosticsReportsSoftDeletedOwnership verifies recoverable sessions stay
// out of the live health report while still identifying their on-disk resources
// as owned. Doctor consumes this separate ID list during orphan cleanup.
func TestDiagnosticsReportsSoftDeletedOwnership(t *testing.T) {
	sm := newTestSessionManager(t)
	deletedAt := time.Now().UTC()

	putSession(sm, &SessionState{ID: "braw1", Name: "braw", Status: StatusStopped})
	putSession(sm, &SessionState{
		ID: "bide1", Name: "bide", Status: StatusStopped, DeletedAt: &deletedAt,
	})

	d := sm.Diagnostics()

	if d.Fleet.Total != 1 || len(d.Sessions) != 1 || d.Sessions[0].ID != "braw1" {
		t.Errorf("live diagnostics include deleted session: fleet=%+v sessions=%+v", d.Fleet, d.Sessions)
	}

	if len(d.DeletedSessionIDs) != 1 || d.DeletedSessionIDs[0] != "bide1" {
		t.Errorf("DeletedSessionIDs = %v, want [bide1]", d.DeletedSessionIDs)
	}
}

func TestCovKillVerifiedProcess(t *testing.T) {
	sm := newTestSessionManager(t)

	t.Run("non-positive pid", func(t *testing.T) {
		killed, err := sm.killVerifiedProcess(0, 0)
		if killed || err != nil {
			t.Fatalf("got (killed=%v, err=%v), want (false, nil)", killed, err)
		}
	})

	t.Run("dead pid", func(t *testing.T) {
		// A pid that is almost certainly not alive.
		killed, err := sm.killVerifiedProcess(1<<30, 12345)
		if killed || err != nil {
			t.Fatalf("got (killed=%v, err=%v), want (false, nil)", killed, err)
		}
	})

	t.Run("live pid without recorded start time errors", func(t *testing.T) {
		// A live child in its own process group; startTime 0 means identity was
		// never recorded, so the kill is refused rather than risk killing the
		// wrong process. Using a contained child (not os.Getpid) keeps a guard
		// regression from ever terminating the test runner.
		pid := spawnContainedSleeper(t)

		_, err := sm.killVerifiedProcess(pid, 0)
		if err == nil {
			t.Fatal("expected error for live pid with no recorded start time")
		}

		if !isProcessAlive(pid) {
			t.Fatal("sleeper was killed; kill guard should have refused")
		}
	})

	t.Run("live pid with mismatched start time errors", func(t *testing.T) {
		// startTime=1 will not match the real start time, so the identity
		// check fails and no signal is sent.
		pid := spawnContainedSleeper(t)

		_, err := sm.killVerifiedProcess(pid, 1)
		if err == nil {
			t.Fatal("expected identity-mismatch error")
		}

		if !isProcessAlive(pid) {
			t.Fatal("sleeper was killed; identity check should have refused")
		}
	})
}

func TestCovStopWithChildren(t *testing.T) {
	sm := newTestSessionManager(t)

	if _, err := sm.StopWithChildren("haar-missing", false); err == nil {
		t.Fatal("expected error for unknown root")
	}

	// ben (root) -> bairn (running orphan, pid 0) and canny (starred, skipped).
	putSession(sm, &SessionState{ID: "ben1", Name: "ben", Status: StatusRunning})
	putSession(sm, &SessionState{ID: "bairn1", Name: "bairn", ParentID: "ben1", Status: StatusRunning})
	putSession(sm, &SessionState{ID: "canny1", Name: "canny", ParentID: "ben1", Status: StatusRunning, Starred: true})

	stopped, err := sm.StopWithChildren("ben1", false)
	if err != nil {
		t.Fatalf("StopWithChildren: %v", err)
	}

	got := map[string]bool{}
	for _, id := range stopped {
		got[id] = true
	}

	if !got["ben1"] || !got["bairn1"] {
		t.Errorf("expected ben1 and bairn1 stopped, got %v", stopped)
	}

	if got["canny1"] {
		t.Error("starred session canny1 should have been skipped")
	}

	if s, _ := sm.Get("canny1"); s.Status != StatusRunning {
		t.Errorf("starred session status = %q, want running (untouched)", s.Status)
	}

	if s, _ := sm.Get("bairn1"); s.Status != StatusStopped {
		t.Errorf("bairn1 status = %q, want stopped", s.Status)
	}
}

func TestCovStopWithReasonOrphanAndErrors(t *testing.T) {
	sm := newTestSessionManager(t)

	if err := sm.Stop("haar-missing"); err == nil {
		t.Fatal("expected error stopping unknown session")
	}

	putSession(sm, &SessionState{ID: "dreich1", Name: "dreich", Status: StatusStopped})

	if err := sm.Stop("dreich1"); err == nil {
		t.Fatal("expected error stopping a non-running session")
	}

	// Running with no PTY and pid 0: the orphan path treats it as already exited.
	putSession(sm, &SessionState{ID: "bide1", Name: "bide", Status: StatusRunning})

	if err := sm.Stop("bide1"); err != nil {
		t.Fatalf("Stop orphan: %v", err)
	}

	if s, _ := sm.Get("bide1"); s.Status != StatusStopped {
		t.Errorf("status = %q, want stopped", s.Status)
	}
}

func TestCovCleanupOrphanedProcesses(t *testing.T) {
	sm := newTestSessionManager(t)

	// A live child (in its own process group) with no recorded identity: cannot
	// be verified, so it is marked errored rather than killed. A contained child
	// rather than os.Getpid means a regression that killed here can't take down
	// the test runner.
	sleeper := spawnContainedSleeper(t)
	putSession(sm, &SessionState{ID: "scunner1", Name: "scunner", Status: StatusRunning, PID: sleeper, PIDStartTime: 0})
	// Dead pid: not a candidate, left untouched.
	putSession(sm, &SessionState{ID: "whin1", Name: "whin", Status: StatusRunning, PID: 1 << 30})
	// Not running: ignored.
	putSession(sm, &SessionState{ID: "neep1", Name: "neep", Status: StatusStopped, PID: sleeper})

	sm.cleanupOrphanedProcesses()

	if !isProcessAlive(sleeper) {
		t.Fatal("sleeper was killed; unverifiable orphan must not be signalled")
	}

	if s, _ := sm.Get("scunner1"); s.Status != StatusErrored {
		t.Errorf("scunner1 status = %q, want errored (unverifiable orphan)", s.Status)
	}

	if s, _ := sm.Get("whin1"); s.Status != StatusRunning {
		t.Errorf("whin1 status = %q, want running (dead pid, not a candidate)", s.Status)
	}

	if s, _ := sm.Get("neep1"); s.Status != StatusStopped {
		t.Errorf("neep1 status = %q, want stopped (not running)", s.Status)
	}
}

func TestCovExpandPaths(t *testing.T) {
	sm := newTestSessionManager(t)

	if got := expandPaths(nil, sm.log, "read"); got != nil {
		t.Errorf("expandPaths(nil) = %v, want nil", got)
	}

	existing := t.TempDir()
	missing := filepath.Join(existing, "no-such-glen")

	got := expandPaths([]string{existing, missing}, sm.log, "read")
	if len(got) != 1 || got[0] != existing {
		t.Errorf("expandPaths = %v, want [%q]", got, existing)
	}
}

func TestCovAgentBinaryDir(t *testing.T) {
	if got := agentBinaryDir(""); got != "" {
		t.Errorf("agentBinaryDir(\"\") = %q, want empty", got)
	}

	// A path with a separator returns its directory verbatim.
	if got := agentBinaryDir("/opt/bin/claude"); got != "/opt/bin" {
		t.Errorf("agentBinaryDir(/opt/bin/claude) = %q, want /opt/bin", got)
	}

	// A bare command name is resolved via PATH.
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}

	if got := agentBinaryDir("sh"); got != filepath.Dir(shPath) {
		t.Errorf("agentBinaryDir(sh) = %q, want %q", got, filepath.Dir(shPath))
	}

	// An unresolvable bare command yields empty.
	if got := agentBinaryDir("definitely-not-a-real-binary-xyzzy"); got != "" {
		t.Errorf("agentBinaryDir(unknown) = %q, want empty", got)
	}
}

func TestCovDeriveSandboxIncludesWriteDirsNonGit(t *testing.T) {
	sm := newTestSessionManager(t)

	// A non-git worktree path: the git-dir lookup fails, so only the worktree
	// path itself is returned (with a warning).
	nonGit := t.TempDir()
	dirs := sm.deriveSandboxIncludesWriteDirs([]IncludedRepoState{
		{RepoName: "croft", WorktreePath: nonGit},
	})

	if len(dirs) != 1 || dirs[0] != nonGit {
		t.Errorf("deriveSandboxIncludesWriteDirs = %v, want [%q]", dirs, nonGit)
	}
}

func TestCovDeriveSandboxIncludesWriteDirsGit(t *testing.T) {
	sm := newTestSessionManager(t)
	_, clone := setupTestRepo(t)

	worktree := filepath.Join(t.TempDir(), "bothy")
	if err := git.SetupSession(context.Background(), clone, worktree, "graith/skelf-1", "main", false); err != nil {
		t.Fatalf("SetupSession: %v", err)
	}

	gitDir, commonDir, err := git.WorktreeGitDirs(worktree)
	if err != nil {
		t.Fatalf("WorktreeGitDirs: %v", err)
	}

	if gitDir == "" || commonDir == "" {
		t.Fatalf("expected non-empty git dirs, got gitDir=%q commonDir=%q", gitDir, commonDir)
	}

	dirs := sm.deriveSandboxIncludesWriteDirs([]IncludedRepoState{
		{RepoName: "croft", WorktreePath: worktree},
	})

	// Exact, ordered slice: worktree path plus its resolved git dir and common
	// dir — so a regression that appended empty or duplicate strings is caught.
	want := []string{worktree, gitDir, commonDir}
	if len(dirs) != len(want) {
		t.Fatalf("deriveSandboxIncludesWriteDirs = %v, want %v", dirs, want)
	}

	for i := range want {
		if dirs[i] != want[i] {
			t.Errorf("dirs[%d] = %q, want %q", i, dirs[i], want[i])
		}
	}
}

func TestCovTeardownIncludes(t *testing.T) {
	sm := newTestSessionManager(t)
	_, clone := setupTestRepo(t)

	sessionDir := filepath.Join(t.TempDir(), "session")
	mainWorktree := filepath.Join(sessionDir, "croft")

	if err := git.SetupSession(context.Background(), clone, mainWorktree, "graith/skelf-main", "main", false); err != nil {
		t.Fatalf("SetupSession main: %v", err)
	}

	incWorktree := filepath.Join(sessionDir, "bairn")
	if err := git.SetupSession(context.Background(), clone, incWorktree, "graith/skelf-inc", "main", false); err != nil {
		t.Fatalf("SetupSession include: %v", err)
	}

	includes := []IncludedRepoState{
		{RepoPath: clone, RepoName: "bairn", WorktreePath: incWorktree, Branch: "graith/skelf-inc"},
	}

	// Sanity: both branches and worktrees exist before teardown, so the
	// post-conditions below prove teardown actually removed them (not that they
	// were never created).
	if !git.RefExists(clone, "graith/skelf-main") || !git.RefExists(clone, "graith/skelf-inc") {
		t.Fatal("expected both branches to exist before teardown")
	}

	if !git.IsRegisteredWorktree(clone, mainWorktree) || !git.IsRegisteredWorktree(clone, incWorktree) {
		t.Fatal("expected both worktrees registered before teardown")
	}

	if err := sm.teardownIncludes(clone, mainWorktree, "graith/skelf-main", includes); err != nil {
		t.Fatalf("teardownIncludes: %v", err)
	}

	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Errorf("expected session dir removed, stat err = %v", err)
	}

	// The branches and worktree registrations in the source repo must also be
	// gone — removing only the session dir would leave git metadata behind.
	if git.RefExists(clone, "graith/skelf-main") {
		t.Error("main branch still exists after teardown")
	}

	if git.RefExists(clone, "graith/skelf-inc") {
		t.Error("include branch still exists after teardown")
	}

	if git.IsRegisteredWorktree(clone, mainWorktree) {
		t.Error("main worktree still registered after teardown")
	}

	if git.IsRegisteredWorktree(clone, incWorktree) {
		t.Error("include worktree still registered after teardown")
	}
}

func TestCovSetStores(t *testing.T) {
	sm := newTestSessionManager(t)

	// Assign non-nil sentinels and assert the pointers round-trip, so a no-op
	// setter would fail (the fields default to nil after NewSessionManager).
	ms := &MsgStore{}
	mm := &MCPManager{}

	sm.SetMsgStore(ms)
	sm.SetMCPManager(mm)

	if sm.messages != ms {
		t.Error("SetMsgStore did not assign the given pointer")
	}

	if sm.mcpManager != mm {
		t.Error("SetMCPManager did not assign the given pointer")
	}
}

// sleeperCfg returns a SessionManager backed by a temp dir whose only agent is a
// harmless `sleep` process, so lifecycle paths that spawn a real PTY can be
// exercised without a real agent binary.
func sleeperSM(t *testing.T) *SessionManager {
	t.Helper()

	cfg := config.Default()
	cfg.Agents["sleeper"] = config.Agent{Command: "sleep", Args: []string{"300"}}

	return newSMWithConfig(t, cfg)
}

// TestFetchRemotesUpdatesTrackingRefs verifies the periodic fetch pass advances
// the local origin/main ref for a running session's worktree after the remote
// moves on, keeping the diverged-from-base count fresh (#197).
func TestFetchRemotesUpdatesTrackingRefs(t *testing.T) {
	sm := sleeperSM(t)

	worktree := t.TempDir()
	gitOut(t, worktree, "init", "-b", "main")

	if err := os.WriteFile(filepath.Join(worktree, "file.txt"), []byte("braw"), 0o600); err != nil {
		t.Fatal(err)
	}

	gitOut(t, worktree, "add", ".")
	gitOut(t, worktree, "commit", "-m", "init")

	origin := t.TempDir()
	gitOut(t, origin, "init", "--bare", "-b", "main")
	gitOut(t, worktree, "remote", "add", "origin", origin)
	gitOut(t, worktree, "push", "origin", "main")
	gitOut(t, worktree, "fetch", "origin")

	// Advance origin/main from a separate clone.
	other := t.TempDir()
	gitOut(t, other, "clone", origin, ".")

	if err := os.WriteFile(filepath.Join(other, "neep.txt"), []byte("neep"), 0o600); err != nil {
		t.Fatal(err)
	}

	gitOut(t, other, "add", ".")
	gitOut(t, other, "commit", "-m", "bonnie remote commit")
	gitOut(t, other, "push", "origin", "main")

	remoteTip := gitOut(t, other, "rev-parse", "HEAD")

	sm.state.Sessions["glen1"] = &SessionState{
		ID: "glen1", Name: "glen", Agent: "sleeper",
		Status: StatusRunning, WorktreePath: worktree, RepoPath: worktree,
		BaseBranch: "main",
	}

	sm.fetchRemotes(context.Background())

	if after := gitOut(t, worktree, "rev-parse", "origin/main"); after != remoteTip {
		t.Errorf("origin/main not updated by fetchRemotes: got %s, want %s", after, remoteTip)
	}
}

// TestFetchRemotesSkipsNonRunningAndShared verifies fetchRemotes ignores
// sessions that are not running and mirror sessions, and tolerates a worktree
// with no remote without error.
func TestFetchRemotesSkipsNonRunningAndShared(t *testing.T) {
	sm := sleeperSM(t)

	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("braw"), 0o600); err != nil {
		t.Fatal(err)
	}

	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "init")

	// Stopped session, shared session, and a running session with no remote —
	// none should cause fetchRemotes to error out.
	sm.state.Sessions["dreich1"] = &SessionState{
		ID: "dreich1", Status: StatusStopped, WorktreePath: dir, BaseBranch: "main",
	}
	sm.state.Sessions["dreich2"] = &SessionState{
		ID: "dreich2", Status: StatusRunning, Mirror: true, WorktreePath: dir, BaseBranch: "main",
	}
	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", Status: StatusRunning, WorktreePath: dir, BaseBranch: "main",
	}

	// Must not panic or block; no assertion needed beyond completion.
	sm.fetchRemotes(context.Background())
}

// TestFetchRemotesCanceledContext verifies fetchRemotes returns promptly when
// its context is already canceled.
func TestFetchRemotesCanceledContext(t *testing.T) {
	sm := sleeperSM(t)

	dir := t.TempDir()
	sm.state.Sessions["glen1"] = &SessionState{
		ID: "glen1", Status: StatusRunning, WorktreePath: dir, BaseBranch: "main",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sm.fetchRemotes(ctx)
}

// spawnReapableSleeper starts a sleeper in its own process group and reaps it in
// the background, so a SIGTERM/SIGKILL delivered by the code under test clears
// the zombie promptly (letting killProcessGroup's poll loop observe ESRCH and
// return quickly instead of waiting out its 5s SIGKILL deadline).
func spawnReapableSleeper(t *testing.T) int {
	t.Helper()

	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}

	pid := cmd.Process.Pid
	done := make(chan struct{})

	go func() {
		_ = cmd.Wait()

		close(done)
	}()

	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)

		<-done
	})

	return pid
}

// TestLoadStateRoundTripCov2 covers LoadState: reading persisted state back,
// reconciling it, and rebuilding the token index — plus the error path when the
// state file is corrupt.
func TestLoadStateRoundTripCov2(t *testing.T) {
	sm := sleeperSM(t)

	sm.state.Sessions["braw1"] = &SessionState{
		ID: "braw1", Name: "braw", Agent: "sleeper",
		Status: StatusStopped, Token: "tok-braw",
	}
	if err := sm.saveState(); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	loaded := NewSessionManager(sm.cfg, sm.paths, slog.Default())
	if err := loaded.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if _, ok := loaded.state.Sessions["braw1"]; !ok {
		t.Fatal("session braw1 not loaded from state file")
	}

	loaded.mu.RLock()
	owner := loaded.SessionForToken("tok-braw")
	loaded.mu.RUnlock()

	if owner != "braw1" {
		t.Errorf("token index owner = %q, want %q", owner, "braw1")
	}

	// A corrupt state file is recovered by starting fresh (no error), so a bad
	// file on disk can never wedge daemon startup.
	if err := os.WriteFile(sm.paths.StateFile, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	recovered := NewSessionManager(sm.cfg, sm.paths, slog.Default())
	if err := recovered.LoadState(); err != nil {
		t.Fatalf("LoadState on corrupt file should recover, got error: %v", err)
	}

	if len(recovered.state.Sessions) != 0 {
		t.Errorf("recovered session count = %d, want 0 (fresh start)", len(recovered.state.Sessions))
	}
}

// TestKillProcessGroupCov2 covers killProcessGroup across its three branches: a
// refused low PID, an already-dead group (ESRCH → nil), and a live group that
// terminates on SIGTERM.
func TestKillProcessGroupCov2(t *testing.T) {
	t.Run("refuses pid <= 1", func(t *testing.T) {
		for _, pid := range []int{-3, 0, 1} {
			if err := killProcessGroup(pid); err == nil {
				t.Errorf("killProcessGroup(%d) = nil, want refusal error", pid)
			}
		}
	})

	t.Run("dead group returns nil", func(t *testing.T) {
		if err := killProcessGroup(1 << 30); err != nil {
			t.Errorf("killProcessGroup(dead) = %v, want nil", err)
		}
	})

	t.Run("terminates live group", func(t *testing.T) {
		pid := spawnReapableSleeper(t)

		if err := killProcessGroup(pid); err != nil {
			t.Fatalf("killProcessGroup(live) = %v, want nil", err)
		}

		// killProcessGroup only returns once the group is gone.
		if isProcessAlive(pid) {
			t.Error("process still alive after killProcessGroup returned")
		}
	})
}

// TestKillVerifiedProcessSuccessCov2 covers the success branch of
// killVerifiedProcess (recorded identity matches → process group killed),
// complementing the round-1 tests that only exercised the refusal branches.
func TestKillVerifiedProcessSuccessCov2(t *testing.T) {
	sm := sleeperSM(t)
	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported on this platform: %v", err)
	}

	killed, err := sm.killVerifiedProcess(pid, start)
	if err != nil || !killed {
		t.Fatalf("killVerifiedProcess = (killed=%v, err=%v), want (true, nil)", killed, err)
	}

	if isProcessAlive(pid) {
		t.Error("process still alive after a verified kill")
	}
}

// TestCleanupOrphanedProcessesVerifiedKillCov2 covers the verified-kill branch of
// cleanupOrphanedProcesses: a running session whose PID is alive, whose identity
// matches the recorded start time, and which has no live PTY, is treated as an
// orphan and killed with a crash stop reason. Round 1 only covered the
// unverifiable (start time 0) branch.
func TestCleanupOrphanedProcessesVerifiedKillCov2(t *testing.T) {
	sm := sleeperSM(t)
	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported on this platform: %v", err)
	}

	putSession(sm, &SessionState{
		ID: "thrawn1", Name: "thrawn", Agent: "sleeper",
		Status: StatusRunning, PID: pid, PIDStartTime: start,
	})

	sm.cleanupOrphanedProcesses()

	s, _ := sm.Get("thrawn1")
	if s.Status != StatusStopped {
		t.Errorf("status = %q, want stopped (verified orphan killed)", s.Status)
	}

	if s.StopReason != StopReasonCrash {
		t.Errorf("stop reason = %q, want %q", s.StopReason, StopReasonCrash)
	}

	if s.PID != 0 {
		t.Errorf("PID = %d, want cleared to 0", s.PID)
	}
}

// TestAdoptSessionsCov2 covers AdoptSessions handling both a manifest entry for a
// session it doesn't know about (warn + skip) and one it knows about but cannot
// re-attach to (adoption fails → session marked stopped).
func TestAdoptSessionsCov2(t *testing.T) {
	sm := sleeperSM(t)

	sm.state.Sessions["bide1"] = &SessionState{
		ID: "bide1", Name: "bide", Agent: "sleeper", Status: StatusRunning,
	}

	manifest := &UpgradeManifest{
		Sessions: []UpgradeSession{
			// Fd -1 → os.NewFile returns nil → AdoptSession errors out.
			{ID: "bide1", Fd: -1, PID: 1 << 30},
			// Unknown session id: warned and skipped, never created.
			{ID: "ghaist1", Fd: -1, PID: 1},
		},
	}

	if err := sm.AdoptSessions(manifest); err != nil {
		t.Fatalf("AdoptSessions: %v", err)
	}

	s, ok := sm.Get("bide1")
	if !ok {
		t.Fatal("known session vanished after adopt")
	}

	if s.Status != StatusStopped {
		t.Errorf("un-adoptable session status = %q, want stopped", s.Status)
	}

	if _, ok := sm.Get("ghaist1"); ok {
		t.Error("unknown manifest session should not have been created")
	}
}

// TestRestartOrphanKillCov2 covers Restart's no-PTY orphan branch: a session
// recorded as running with a live, identity-verified PID but no live PTY (as
// after a daemon crash) has that process group killed and is transitioned to
// stopped before the resume attempt. Resume then fails on a missing agent, which
// keeps the test free of a respawned process (and the TempDir cleanup races that
// come with one) while still exercising the kill-and-transition logic.
func TestRestartOrphanKillCov2(t *testing.T) {
	sm := sleeperSM(t)
	pid := spawnReapableSleeper(t)

	start, err := grpty.ProcessStartTime(pid)
	if err != nil {
		t.Skipf("ProcessStartTime unsupported on this platform: %v", err)
	}

	putSession(sm, &SessionState{
		ID: "bide-orphan", Name: "bide", Agent: "ghaist-agent",
		Status: StatusRunning, PID: pid, PIDStartTime: start,
	})

	// Resume fails (unknown agent), so Restart surfaces that error — but the
	// orphaned process must already have been killed and the session stopped.
	if _, err := sm.Restart("bide-orphan", 24, 80); err == nil {
		t.Fatal("expected resume-after-kill to fail on unknown agent")
	}

	if isProcessAlive(pid) {
		t.Error("orphaned process still alive after restart")
	}

	s, _ := sm.Get("bide-orphan")
	if s.Status != StatusStopped {
		t.Errorf("status = %q, want stopped after orphan kill", s.Status)
	}

	if s.PID != 0 {
		t.Errorf("PID = %d, want cleared to 0", s.PID)
	}
}

// TestRestartUnknownCov2 covers Restart of a session that does not exist: with no
// PTY and no state entry, it falls through to resume, which reports not-found.
func TestRestartUnknownCov2(t *testing.T) {
	sm := sleeperSM(t)

	if _, err := sm.Restart("nae-sic-session", 24, 80); err == nil {
		t.Fatal("expected error restarting unknown session, got nil")
	}
}

// TestForkErrorsCov2 covers Fork's early validation branches that don't require a
// git worktree or PTY: bad name, missing source, system session, in-place
// source, and unknown agent.
func TestForkErrorsCov2(t *testing.T) {
	sm := sleeperSM(t)

	if _, err := sm.Fork("Bad Name", "whatever", 24, 80); err == nil {
		t.Error("expected name-validation error for invalid fork name")
	}

	if _, err := sm.Fork("bonnie", "ghaist-source", 24, 80); err == nil {
		t.Error("expected not-found error for unknown source session")
	}

	putSession(sm, &SessionState{
		ID: "orch1", Name: "orchestrator", RepoPath: "/tmp/croft",
		SystemKind: SystemKindOrchestrator,
	})

	if _, err := sm.Fork("bonnie", "orch1", 24, 80); err == nil {
		t.Error("expected error forking a system session")
	}

	putSession(sm, &SessionState{
		ID: "inplace1", Name: "inplace", RepoPath: "/tmp/croft", InPlace: true,
	})

	if _, err := sm.Fork("bonnie", "inplace1", 24, 80); err == nil {
		t.Error("expected error forking an in-place session")
	}

	putSession(sm, &SessionState{
		ID: "src1", Name: "source", RepoPath: "/tmp/croft", Agent: "ghaist-agent",
	})

	if _, err := sm.Fork("bonnie", "src1", 24, 80); err == nil {
		t.Error("expected unknown-agent error forking session with missing agent")
	}
}

// TestApplyConfigChangesCov2 covers applyConfig's change-detection branches by
// swapping in a config that differs in every logged dimension.
func TestApplyConfigChangesCov2(t *testing.T) {
	sm := newTestSessionManager(t)
	old := sm.Config()

	newCfg := config.Default()
	newCfg.DefaultAgent = "codex"
	newCfg.BranchPrefix = "bonnie"
	newCfg.FetchOnCreate = !old.FetchOnCreate
	newCfg.GitHubUsername = "speir"
	newCfg.GitPull.Enabled = !old.GitPull.Enabled
	newCfg.GitPull.Interval = "42m"
	newCfg.Sandbox.Enabled = !old.Sandbox.Enabled
	newCfg.Sandbox.ReadDirs = []string{"/tmp/loch"}
	newCfg.Sandbox.WriteDirs = []string{"/tmp/glen"}
	newCfg.Sandbox.ReadFiles = []string{"/tmp/skelf.txt"}
	newCfg.Sandbox.WriteFiles = []string{"/tmp/wynd.txt"}
	newCfg.Sandbox.Features = []string{"process-control"}
	newCfg.Agents["neep"] = config.Agent{Command: "sleep"}
	delete(newCfg.Agents, "claude") // exercise the "removed" branch

	sm.applyConfig(newCfg)

	if got := sm.Config().DefaultAgent; got != "codex" {
		t.Errorf("DefaultAgent after applyConfig = %q, want codex", got)
	}

	if _, ok := sm.Config().Agents["claude"]; ok {
		t.Error("removed agent still present after applyConfig")
	}
}

// TestDetectAgentStatusesGitBranchesCov2 exercises the git-status branches of
// detectAgentStatuses that round 1 left uncovered: a non-shared session with a
// base branch (so UnpushedCommitCount runs) and an included repo (so the
// includes loop runs). A hook report drives the agent status so the test does
// not depend on PTY screen scraping.
func TestDetectAgentStatusesGitBranchesCov2(t *testing.T) {
	sm := sleeperSM(t)

	repoDir := initDirtyRepo(t)

	logDir := t.TempDir()

	pty, err := grpty.NewSession(grpty.SessionOpts{
		ID: "glen1", Command: "sleep", Args: []string{"60"},
		Dir: repoDir, Rows: 24, Cols: 80,
		LogPath: filepath.Join(logDir, "glen.log"),
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = pty.Kill() }()

	sm.state.Sessions["glen1"] = &SessionState{
		ID: "glen1", Name: "glen", Agent: "claude",
		Status: StatusRunning, WorktreePath: repoDir, RepoPath: repoDir,
		BaseBranch: "main",
		Includes: []IncludedRepoState{
			{RepoName: "croft", RepoPath: repoDir, WorktreePath: repoDir, BaseBranch: "main"},
		},
	}
	sm.sessions["glen1"] = pty

	sm.mu.Lock()
	sm.hookReports["glen1"] = hookReport{
		Status: "active", Event: "Notification",
		ReportedAt: time.Now(), AuthoritativeUntil: time.Now().Add(5 * time.Minute),
	}
	sm.mu.Unlock()

	sm.detectAgentStatuses()

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	s := sm.state.Sessions["glen1"]
	if !s.GitDirty {
		t.Error("expected GitDirty=true for modified worktree")
	}

	if s.AgentStatus != "active" {
		t.Errorf("AgentStatus = %q, want active (from hook report)", s.AgentStatus)
	}
}
