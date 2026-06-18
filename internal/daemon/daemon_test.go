package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	grpty "github.com/d0ugal/graith/internal/pty"
)

func newTestSessionManager(t *testing.T) *SessionManager {
	t.Helper()
	tmpDir := t.TempDir()
	return NewSessionManager(config.Default(), config.Paths{
		StateFile: filepath.Join(tmpDir, "state.json"),
		DataDir:   tmpDir,
		LogDir:    tmpDir,
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
			ID: "sess1", Name: "old-name", Status: StatusRunning,
		}

		if err := sm.Rename("sess1", "new-name"); err != nil {
			t.Fatalf("Rename() error = %v", err)
		}

		s, ok := sm.state.Sessions["sess1"]
		if !ok {
			t.Fatal("session not found after rename")
		}
		if s.Name != "new-name" {
			t.Errorf("Name = %q, want %q", s.Name, "new-name")
		}
	})

	t.Run("not found", func(t *testing.T) {
		sm := newTestSessionManager(t)

		err := sm.Rename("nonexistent", "new-name")
		if err == nil {
			t.Fatal("expected error for nonexistent session")
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
			ID: "abc", Name: "test-session", Status: StatusRunning,
		}

		s, ok := sm.Get("abc")
		if !ok {
			t.Fatal("Get() returned not found for existing session")
		}
		if s.ID != "abc" || s.Name != "test-session" {
			t.Errorf("Get() = %+v, want ID=abc, Name=test-session", s)
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
			ID: "sess1", Name: "test", Status: StatusRunning,
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
	cost := 0.12
	ctxPct := 55.5

	sess := SessionState{
		ID:                 "abc123",
		Name:               "fix-bug",
		RepoPath:           "/home/user/repo",
		RepoName:           "repo",
		WorktreePath:       "/home/user/.local/share/graith/worktrees/abc123",
		Branch:             "user/graith/fix-bug-abc123",
		Agent:              "claude",
		AgentSessionID:     "session-id-123",
		Status:             StatusStopped,
		ExitCode:           &exitCode,
		CreatedAt:          createdAt,
		HookModel:          "claude-sonnet-4-5-20250514",
		HookToolName:       "Bash",
		HookCostUSD:        &cost,
		HookContextPercent: &ctxPct,
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
	if info.CostUSD == nil || *info.CostUSD != 0.12 {
		t.Errorf("CostUSD = %v, want 0.12", info.CostUSD)
	}
	if info.ContextPercent == nil || *info.ContextPercent != 55.5 {
		t.Errorf("ContextPercent = %v, want 55.5", info.ContextPercent)
	}
}

func TestToSessionInfoNilExitCode(t *testing.T) {
	sess := SessionState{
		ID:        "abc",
		Name:      "test",
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
		tmpDir := t.TempDir()
		cfg := config.Default()
		cfg.Sandbox = config.SandboxConfig{Enabled: true}
		cfg.Agents["claude"] = config.Agent{
			Command: "claude",
			Sandbox: config.SandboxConfig{ReadDirs: []string{"/agent-dir"}},
		}

		smWithout := NewSessionManager(cfg, config.Paths{
			StateFile: filepath.Join(tmpDir, "state1.json"),
			DataDir:   tmpDir,
			LogDir:    tmpDir,
		}, slog.Default())

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

		smWith := NewSessionManager(cfgWith, config.Paths{
			StateFile: filepath.Join(tmpDir, "state2.json"),
			DataDir:   tmpDir,
			LogDir:    tmpDir,
		}, slog.Default())

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
		tmpDir := t.TempDir()
		cfg := config.Default()
		cfg.Sandbox = config.SandboxConfig{Enabled: false}
		cfg.Agents["claude"] = config.Agent{Command: "claude"}
		cfg.Orchestrator = config.OrchestratorConfig{
			Sandbox: config.OrchestratorSandboxConfig{
				WriteDirs: []string{"/orch-write"},
			},
		}

		sm := NewSessionManager(cfg, config.Paths{
			StateFile: filepath.Join(tmpDir, "state.json"),
			DataDir:   tmpDir,
			LogDir:    tmpDir,
		}, slog.Default())

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
			ID: "s1", Name: "test", Status: StatusRunning,
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
			ID: "s1", Name: "test", Status: StatusRunning,
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
			ID: "s1", Name: "test", Status: StatusRunning,
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
			ID: "s1", Name: "test", Status: StatusRunning,
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
			ID: "s1", Name: "test", Status: StatusRunning,
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
			ID: "s1", Name: "test", Status: StatusRunning,
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
			ID: "s1", Name: "test", Status: StatusRunning,
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
			ID: "sess1", Name: "test", Status: StatusRunning,
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
			ID: "sess1", Name: "test", Status: StatusRunning,
		}

		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "sess1",
			Event:     "Notification",
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

	t.Run("ready event", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["sess1"] = &SessionState{
			ID: "sess1", Name: "test", Status: StatusRunning,
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
			ID: "sess1", Name: "test", Status: StatusRunning,
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
			ID: "sess1", Name: "test", Status: StatusRunning,
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
			ID: "sess1", Name: "test", Status: StatusRunning,
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

	t.Run("enrichment data accumulated", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["s1"] = &SessionState{
			ID: "s1", Name: "test", Status: StatusRunning, Agent: "claude",
		}

		cost := 0.05
		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "s1",
			Event:     "Stop",
			Model:     "claude-sonnet-4-5-20250514",
			Usage:     &protocol.UsageReport{CostUSD: &cost},
		})

		sm.mu.RLock()
		hr := sm.hookReports["s1"]
		sess := sm.state.Sessions["s1"]
		sm.mu.RUnlock()

		if hr.Model != "claude-sonnet-4-5-20250514" {
			t.Errorf("Model = %q, want %q", hr.Model, "claude-sonnet-4-5-20250514")
		}
		if hr.CostUSD == nil || *hr.CostUSD != 0.05 {
			t.Errorf("CostUSD = %v, want 0.05", hr.CostUSD)
		}
		if sess.HookModel != "claude-sonnet-4-5-20250514" {
			t.Errorf("sess.HookModel = %q, want %q", sess.HookModel, "claude-sonnet-4-5-20250514")
		}
		if sess.HookCostUSD == nil || *sess.HookCostUSD != 0.05 {
			t.Errorf("sess.HookCostUSD = %v, want 0.05", sess.HookCostUSD)
		}

		// Send another event without cost — cost should be preserved
		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "s1",
			Event:     "PreToolUse",
			ToolName:  "Bash",
		})

		sm.mu.RLock()
		hr2 := sm.hookReports["s1"]
		sess2 := sm.state.Sessions["s1"]
		sm.mu.RUnlock()

		if hr2.CostUSD == nil || *hr2.CostUSD != 0.05 {
			t.Errorf("CostUSD should be preserved, got %v", hr2.CostUSD)
		}
		if hr2.Model != "claude-sonnet-4-5-20250514" {
			t.Errorf("Model should be preserved, got %q", hr2.Model)
		}
		if sess2.HookCostUSD == nil || *sess2.HookCostUSD != 0.05 {
			t.Errorf("sess.HookCostUSD should be preserved, got %v", sess2.HookCostUSD)
		}
		if sess2.HookModel != "claude-sonnet-4-5-20250514" {
			t.Errorf("sess.HookModel should be preserved, got %q", sess2.HookModel)
		}
	})

	t.Run("context percent accumulated", func(t *testing.T) {
		sm := newTestSessionManager(t)
		sm.state.Sessions["s1"] = &SessionState{
			ID: "s1", Name: "test", Status: StatusRunning, Agent: "claude",
		}

		pct := 42.5
		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "s1",
			Event:     "PostToolUse",
			Context:   &protocol.ContextReport{Percent: &pct},
		})

		sm.mu.RLock()
		hr := sm.hookReports["s1"]
		sess := sm.state.Sessions["s1"]
		sm.mu.RUnlock()

		if hr.ContextPercent == nil || *hr.ContextPercent != 42.5 {
			t.Errorf("ContextPercent = %v, want 42.5", hr.ContextPercent)
		}
		if sess.HookContextPercent == nil || *sess.HookContextPercent != 42.5 {
			t.Errorf("sess.HookContextPercent = %v, want 42.5", sess.HookContextPercent)
		}

		// Send another event without context — should be preserved
		sm.HandleHookReport(protocol.StatusReportMsg{
			SessionID: "s1",
			Event:     "PreToolUse",
			ToolName:  "Read",
		})

		sm.mu.RLock()
		hr2 := sm.hookReports["s1"]
		sm.mu.RUnlock()

		if hr2.ContextPercent == nil || *hr2.ContextPercent != 42.5 {
			t.Errorf("ContextPercent should be preserved, got %v", hr2.ContextPercent)
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

func TestDetectAgentStatuses_SharedWorktreeSkipsGit(t *testing.T) {
	sm := newTestSessionManager(t)

	repoDir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	runGit("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	logDir := t.TempDir()
	sharedPty, err := grpty.NewSession(grpty.SessionOpts{
		ID: "shared1", Command: "sleep", Args: []string{"60"},
		Dir: repoDir, Rows: 24, Cols: 80,
		LogPath: filepath.Join(logDir, "shared.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sharedPty.Kill()

	normalPty, err := grpty.NewSession(grpty.SessionOpts{
		ID: "normal1", Command: "sleep", Args: []string{"60"},
		Dir: repoDir, Rows: 24, Cols: 80,
		LogPath: filepath.Join(logDir, "normal.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer normalPty.Kill()

	sm.state.Sessions["shared1"] = &SessionState{
		ID: "shared1", Name: "shared-test", Agent: "claude",
		Status: StatusRunning, WorktreePath: repoDir, RepoPath: repoDir,
		SharedWorktree: true,
	}
	sm.state.Sessions["normal1"] = &SessionState{
		ID: "normal1", Name: "normal-test", Agent: "claude",
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
		t.Error("shared worktree session should have GitDirty=false (git ops skipped)")
	}
	if shared.GitUnpushed != 0 {
		t.Errorf("shared worktree session GitUnpushed=%d, want 0", shared.GitUnpushed)
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
		Name:   "scratch-session",
		Agent:  "claude",
		Status: StatusRunning,
	}

	_, err := sm.Fork("forked", "norepo1", 24, 80)
	if err == nil {
		t.Fatal("Fork() should fail for no-repo source session")
	}
	if !strings.Contains(err.Error(), "no repo") {
		t.Errorf("Fork() error = %q, want error mentioning 'no repo'", err)
	}
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
	if err == nil {
		t.Fatal("expected error when data_dir changes")
	}
	if !strings.Contains(err.Error(), "data_dir changed") {
		t.Errorf("error = %q, want it to mention data_dir changed", err)
	}
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

func TestToSessionInfoSharedWorktree(t *testing.T) {
	sess := SessionState{
		ID:             "abc123",
		Name:           "reviewer",
		WorktreePath:   "/shared/path",
		Agent:          "claude",
		Status:         StatusRunning,
		SharedWorktree: true,
		CreatedAt:      time.Now().UTC(),
	}

	info := toSessionInfo(sess, config.Default(), nil)

	if !info.SharedWorktree {
		t.Error("SharedWorktree = false, want true")
	}

	sess.SharedWorktree = false
	info = toSessionInfo(sess, config.Default(), nil)
	if info.SharedWorktree {
		t.Error("SharedWorktree = true, want false")
	}
}

func TestDeleteSharedWorktreeSkipsGitTeardown(t *testing.T) {
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
		ID:             "shared1",
		Name:           "reviewer",
		RepoPath:       "/does/not/exist/repo",
		WorktreePath:   "/does/not/exist/worktree",
		Branch:         "some-branch",
		SharedWorktree: true,
		Status:         StatusStopped,
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

func TestStateSaveLoadSharedWorktree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := &State{
		Sessions: map[string]*SessionState{
			"s1": {
				ID: "s1", Name: "reviewer", WorktreePath: "/shared/path",
				Agent: "claude", Status: StatusRunning,
				SharedWorktree: true, CreatedAt: time.Now().UTC(),
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
	if !s.SharedWorktree {
		t.Error("SharedWorktree not preserved across save/load")
	}
}

func TestShareWorktreeRequiresSandbox(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.Default()
	sm := NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(tmpDir, "state.json"),
		DataDir:   tmpDir,
		LogDir:    tmpDir,
	}, slog.Default())

	sm.state.Sessions["src1"] = &SessionState{
		ID:           "src1",
		Name:         "source",
		Agent:        "claude",
		WorktreePath: "/tmp/fake-worktree",
		Status:       StatusRunning,
	}

	_, err := sm.Create("reviewer", "claude", "", "", "", "", "", false, "source", false, false, false, 24, 80)
	if err == nil {
		t.Fatal("expected error when --share-worktree used without sandbox, got nil")
	}
	if !strings.Contains(err.Error(), "requires sandbox") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestShareWorktreeRequiresSandboxPerAgent(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.Sandbox.Enabled = true
	disabled := true
	agent := cfg.Agents["claude"]
	agent.Sandbox = config.SandboxConfig{Disabled: &disabled}
	cfg.Agents["claude"] = agent

	sm := NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(tmpDir, "state.json"),
		DataDir:   tmpDir,
		LogDir:    tmpDir,
	}, slog.Default())

	sm.state.Sessions["src1"] = &SessionState{
		ID:           "src1",
		Name:         "source",
		Agent:        "claude",
		WorktreePath: "/tmp/fake-worktree",
		Status:       StatusRunning,
	}

	_, err := sm.Create("reviewer", "claude", "", "", "", "", "", false, "source", false, false, false, 24, 80)
	if err == nil {
		t.Fatal("expected error when --share-worktree used with per-agent sandbox disabled, got nil")
	}
	if !strings.Contains(err.Error(), "requires sandbox") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestResumeSharedWorktreeWithoutSandboxRejects(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.Default()
	sm := NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(tmpDir, "state.json"),
		DataDir:   tmpDir,
		LogDir:    tmpDir,
	}, slog.Default())

	sm.state.Sessions["legacy1"] = &SessionState{
		ID:             "legacy1",
		Name:           "legacy-reviewer",
		Agent:          "claude",
		WorktreePath:   "/tmp/fake-worktree",
		SharedWorktree: true,
		Sandboxed:      false,
		Status:         StatusStopped,
	}

	_, err := sm.Resume("legacy1", 24, 80)
	if err == nil {
		t.Fatal("expected error when resuming shared-worktree session without sandbox, got nil")
	}
	if !strings.Contains(err.Error(), "requires sandbox") {
		t.Errorf("unexpected error message: %v", err)
	}
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

	_, err := sm.Create("test-sess", "sleeper", "", "", "", "", "", true, "", false, false, false, 24, 80)
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
	stateFile := filepath.Join(tmpDir, "state.json")

	cfg := config.Default()
	cfg.Agents["sleeper"] = config.Agent{
		Command: "sleep",
		Args:    []string{"60"},
	}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile: stateFile,
		DataDir:   tmpDir,
		LogDir:    tmpDir,
	}, slog.Default())

	exitCode := 42
	sm.state.Sessions["s1"] = &SessionState{
		ID:           "s1",
		Name:         "test",
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

func TestResumeRefreshesSandboxConfig(t *testing.T) {
	t.Run("resume uses current config not stored config", func(t *testing.T) {
		tmpDir := t.TempDir()
		updatedDir := filepath.Join(tmpDir, "updated-dir")
		if err := os.MkdirAll(updatedDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := config.Default()
		cfg.Sandbox = config.SandboxConfig{
			Enabled:  true,
			ReadDirs: []string{updatedDir},
		}
		cfg.Agents["sleeper"] = config.Agent{
			Command: "sleep",
			Args:    []string{"60"},
		}

		sm := NewSessionManager(cfg, config.Paths{
			StateFile:  filepath.Join(tmpDir, "state.json"),
			DataDir:    tmpDir,
			LogDir:     tmpDir,
			RuntimeDir: tmpDir,
		}, slog.Default())

		sm.state.Sessions["s1"] = &SessionState{
			ID:           "s1",
			Name:         "test",
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
		// Resume will fail because safehouse isn't installed, but if it gets
		// far enough to update the session state, we can check the config.
		// On macOS without safehouse, resolveSandbox returns an error.
		if err != nil {
			// Check that the error is about safehouse availability, not about
			// using the old config.
			if !strings.Contains(err.Error(), "not available") {
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

		ptySess, ok := sm.GetPTY("s1")
		if ok {
			sm.mu.Lock()
			delete(sm.sessions, "s1")
			sm.mu.Unlock()
			if !ptySess.Exited() {
				_ = ptySess.Kill()
			}
			<-ptySess.Done()
			ptySess.Close()
		}
	})

	t.Run("resume without sandbox when config disables it", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := config.Default()
		cfg.Sandbox = config.SandboxConfig{Enabled: false}
		cfg.Agents["sleeper"] = config.Agent{
			Command: "sleep",
			Args:    []string{"60"},
		}

		sm := NewSessionManager(cfg, config.Paths{
			StateFile: filepath.Join(tmpDir, "state.json"),
			DataDir:   tmpDir,
			LogDir:    tmpDir,
		}, slog.Default())

		sm.state.Sessions["s1"] = &SessionState{
			ID:           "s1",
			Name:         "test",
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

		ptySess, ok := sm.GetPTY("s1")
		if ok {
			sm.mu.Lock()
			delete(sm.sessions, "s1")
			sm.mu.Unlock()
			if !ptySess.Exited() {
				_ = ptySess.Kill()
			}
			<-ptySess.Done()
			ptySess.Close()
		}
	})

	t.Run("resume rollback restores sandbox fields", func(t *testing.T) {
		tmpDir := t.TempDir()
		stateFile := filepath.Join(tmpDir, "state.json")

		cfg := config.Default()
		cfg.Sandbox = config.SandboxConfig{Enabled: false}
		cfg.Agents["sleeper"] = config.Agent{
			Command: "sleep",
			Args:    []string{"60"},
		}

		sm := NewSessionManager(cfg, config.Paths{
			StateFile: stateFile,
			DataDir:   tmpDir,
			LogDir:    tmpDir,
		}, slog.Default())

		oldConfig := &config.SandboxConfig{
			Enabled:  true,
			ReadDirs: []string{"/old-dir"},
		}
		sm.state.Sessions["s1"] = &SessionState{
			ID:            "s1",
			Name:          "test",
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
		ID: id, Name: "test", Status: StatusRunning, Agent: "claude",
	}

	oldSess := newTestPTYSession(t, "true")
	newSess := newTestPTYSession(t, "sleep", "100")

	// Wait for old process to exit so Done() is closed.
	select {
	case <-oldSess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for old session to exit")
	}

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
		ID: id, Name: "test", Status: StatusRunning, Agent: "claude",
	}

	sess := newTestPTYSession(t, "true")

	// Wait for exit so Done() is closed.
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session to exit")
	}

	sm.sessions[id] = sess

	// Call synchronously — deterministic, no sleep.
	sm.watchSession(id, sess)

	sm.mu.RLock()
	status := sm.state.Sessions[id].Status
	exitCode := sm.state.Sessions[id].ExitCode
	sm.mu.RUnlock()

	if status != StatusStopped {
		t.Errorf("status = %q, want %q", status, StatusStopped)
	}
	if exitCode == nil || *exitCode != 0 {
		t.Errorf("exit code = %v, want 0", exitCode)
	}
}

func TestWatchSessionDeletedSkipsPublish(t *testing.T) {
	dir := t.TempDir()
	ms, err := NewMsgStore(filepath.Join(dir, "msg.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()

	sm := newTestSessionManager(t)
	sm.messages = ms

	id := "sess-watch-deleted"

	sess := newTestPTYSession(t, "true")

	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session to exit")
	}

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
		ID: id, Name: "idle-test", Status: StatusStopped, Agent: "claude",
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
		select {
		case <-ptySess.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for session to exit")
		}
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
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("commit", "--allow-empty", "-m", "init")
	return dir
}

func TestCreateInPlaceRejectsUnconfiguredRepo(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	_, err := sm.Create("test", "claude", repoDir, "", "", "", "", false, "", false, true, false, 24, 80)
	if err == nil {
		t.Fatal("expected error for unconfigured repo")
	}
	if !strings.Contains(err.Error(), "not configured in [[repos]]") {
		t.Errorf("error = %q, want mention of [[repos]]", err.Error())
	}
}

func TestCreateInPlaceMutuallyExclusiveFlags(t *testing.T) {
	sm := newTestSessionManager(t)

	t.Run("in-place with no-repo", func(t *testing.T) {
		_, err := sm.Create("test", "claude", "", "", "", "", "", true, "", false, true, false, 24, 80)
		if err == nil {
			t.Fatal("expected error for --in-place with --no-repo")
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("error = %q, want mutually exclusive", err.Error())
		}
	})

	t.Run("in-place with share-worktree", func(t *testing.T) {
		_, err := sm.Create("test", "claude", "", "", "", "", "", false, "some-session", false, true, false, 24, 80)
		if err == nil {
			t.Fatal("expected error for --in-place with --share-worktree")
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("error = %q, want mutually exclusive", err.Error())
		}
	})

	t.Run("in-place with base", func(t *testing.T) {
		_, err := sm.Create("test", "claude", "/tmp/whatever", "main", "", "", "", false, "", false, true, false, 24, 80)
		if err == nil {
			t.Fatal("expected error for --in-place with --base")
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("error = %q, want mutually exclusive", err.Error())
		}
	})
}

func TestCreateInPlaceRejectsConcurrent(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sm.state.Sessions["existing"] = &SessionState{
		ID:           "existing",
		Name:         "first",
		WorktreePath: repoDir,
		InPlace:      true,
		Status:       StatusRunning,
	}

	_, err := sm.Create("second", "claude", repoDir, "", "", "", "", false, "", false, true, false, 24, 80)
	if err == nil {
		t.Fatal("expected error for concurrent in-place session")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error = %q, want mention of already running", err.Error())
	}
}

func TestCreateInPlaceAllowConcurrentFlag(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sm.state.Sessions["existing"] = &SessionState{
		ID:           "existing",
		Name:         "first",
		WorktreePath: repoDir,
		InPlace:      true,
		Status:       StatusRunning,
	}

	// With --allow-concurrent, should pass the concurrent check (will fail later on agent start)
	_, err := sm.Create("second", "claude", repoDir, "", "", "", "", false, "", false, true, true, 24, 80)
	if err != nil && strings.Contains(err.Error(), "already running") {
		t.Fatalf("--allow-concurrent should bypass concurrent check, got: %v", err)
	}
}

func TestCreateInPlaceConfigAllowConcurrent(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir, AllowConcurrent: true}}

	sm.state.Sessions["existing"] = &SessionState{
		ID:           "existing",
		Name:         "first",
		WorktreePath: repoDir,
		InPlace:      true,
		Status:       StatusRunning,
	}

	// Config allow_concurrent should pass the concurrent check
	_, err := sm.Create("second", "claude", repoDir, "", "", "", "", false, "", false, true, false, 24, 80)
	if err != nil && strings.Contains(err.Error(), "already running") {
		t.Fatalf("config allow_concurrent should bypass concurrent check, got: %v", err)
	}
}

func TestDeleteInPlaceLeavesState(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["inplace1"] = &SessionState{
		ID:           "inplace1",
		Name:         "my-inplace",
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

func TestForkInPlaceRejects(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["inplace1"] = &SessionState{
		ID:           "inplace1",
		Name:         "my-inplace",
		RepoPath:     "/tmp/my-repo",
		WorktreePath: "/tmp/my-repo",
		Agent:        "claude",
		InPlace:      true,
		Status:       StatusRunning,
	}

	_, err := sm.Fork("forked", "inplace1", 24, 80)
	if err == nil {
		t.Fatal("expected error forking an in-place session")
	}
	if !strings.Contains(err.Error(), "in-place") {
		t.Errorf("error = %q, want mention of in-place", err.Error())
	}
}

func TestForkUsesSourceBaseBranch(t *testing.T) {
	repoDir := initTempGitRepo(t)
	tmpDir := t.TempDir()

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["sleeper"] = config.Agent{
		Command: "sleep",
		Args:    []string{"60"},
	}

	sm := NewSessionManager(cfg, config.Paths{
		StateFile: filepath.Join(tmpDir, "state.json"),
		DataDir:   tmpDir,
		LogDir:    tmpDir,
	}, slog.Default())

	sm.state.Sessions["src1"] = &SessionState{
		ID:             "src1",
		Name:           "source-session",
		RepoPath:       repoDir,
		RepoName:       "testrepo",
		WorktreePath:   repoDir,
		Branch:         "feat/my-feature",
		BaseBranch:     "main",
		Agent:          "sleeper",
		AgentSessionID: "test-agent-id",
		Status:         StatusRunning,
	}

	forked, err := sm.Fork("forked", "src1", 24, 80)
	if err != nil {
		t.Fatalf("Fork() unexpected error: %v", err)
	}
	t.Cleanup(func() {
		if sess, ok := sm.sessions[forked.ID]; ok {
			_ = sess.Kill()
			sess.Close()
		}
		if forked.WorktreePath != "" {
			cmd := exec.Command("git", "worktree", "remove", "--force", forked.WorktreePath)
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

func TestResumeInPlaceRejectsRemovedConfig(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)

	sm.state.Sessions["inplace1"] = &SessionState{
		ID:           "inplace1",
		Name:         "my-inplace",
		RepoPath:     repoDir,
		WorktreePath: repoDir,
		Agent:        "claude",
		InPlace:      true,
		Status:       StatusStopped,
	}

	_, err := sm.Resume("inplace1", 24, 80)
	if err == nil {
		t.Fatal("expected error resuming in-place session after repo removed from config")
	}
	if !strings.Contains(err.Error(), "[[repos]]") {
		t.Errorf("error = %q, want mention of [[repos]]", err.Error())
	}
}

func TestResumeInPlaceRejectsConcurrentRunning(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sm.state.Sessions["inplace1"] = &SessionState{
		ID:           "inplace1",
		Name:         "first",
		RepoPath:     repoDir,
		WorktreePath: repoDir,
		Agent:        "claude",
		InPlace:      true,
		Status:       StatusStopped,
	}
	sm.state.Sessions["inplace2"] = &SessionState{
		ID:           "inplace2",
		Name:         "second",
		RepoPath:     repoDir,
		WorktreePath: repoDir,
		Agent:        "claude",
		InPlace:      true,
		Status:       StatusRunning,
	}

	_, err := sm.Resume("inplace1", 24, 80)
	if err == nil {
		t.Fatal("expected error: another in-place session is running in the same repo")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error = %q, want mention of already running", err.Error())
	}
}

func TestResumeInPlaceRejectsDeletedRepo(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir}}

	sm.state.Sessions["inplace1"] = &SessionState{
		ID:           "inplace1",
		Name:         "my-inplace",
		RepoPath:     repoDir,
		WorktreePath: repoDir,
		Agent:        "claude",
		InPlace:      true,
		Status:       StatusStopped,
	}

	os.RemoveAll(repoDir)

	_, err := sm.Resume("inplace1", 24, 80)
	if err == nil {
		t.Fatal("expected error resuming after repo deleted")
	}
	if !strings.Contains(err.Error(), "no longer a git repository") {
		t.Errorf("error = %q, want mention of no longer a git repository", err.Error())
	}
}

func TestCreateInPlaceBaseRejectedByDaemon(t *testing.T) {
	sm := newTestSessionManager(t)
	_, err := sm.Create("test", "claude", "/tmp/whatever", "main", "", "", "", false, "", false, true, false, 24, 80)
	if err == nil {
		t.Fatal("expected error for --in-place with --base")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %q, want mutually exclusive", err.Error())
	}
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
		Name:         "test",
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

	sm.state.Sessions["existing"] = &SessionState{
		ID:       "existing",
		Name:     "first",
		RepoPath: repoDir,
		Status:   StatusRunning,
	}

	_, err := sm.Create("second", "claude", repoDir, "main", "", "", "", false, "", false, false, false, 24, 80)
	if err == nil {
		t.Fatal("expected error for singleton repo with running session")
	}
	if !strings.Contains(err.Error(), "singleton") {
		t.Errorf("error = %q, want mention of singleton", err.Error())
	}
}

func TestSingletonAllowsCreateWhenStopped(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir, Singleton: true}}

	sm.state.Sessions["existing"] = &SessionState{
		ID:       "existing",
		Name:     "first",
		RepoPath: repoDir,
		Status:   StatusStopped,
	}

	_, err := sm.Create("second", "claude", repoDir, "main", "", "", "", false, "", false, false, false, 24, 80)
	if err != nil && strings.Contains(err.Error(), "singleton") {
		t.Fatalf("singleton should not block when existing session is stopped, got: %v", err)
	}
}

func TestInPlaceRejectsRepoWithIncludes(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	incDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir, Includes: []string{incDir}}}

	_, err := sm.Create("test", "claude", repoDir, "", "", "", "", false, "", false, true, false, 24, 80)
	if err == nil {
		t.Fatal("expected error for --in-place with includes configured")
	}
	if !strings.Contains(err.Error(), "includes configured") {
		t.Errorf("error = %q, want mention of includes configured", err.Error())
	}
}

func TestForkSingletonRejects(t *testing.T) {
	sm := newTestSessionManager(t)
	repoDir := initTempGitRepo(t)
	sm.cfg.Repos = []config.RepoConfig{{Path: repoDir, Singleton: true}}

	sm.state.Sessions["source"] = &SessionState{
		ID:       "source",
		Name:     "source-session",
		RepoPath: repoDir,
		RepoName: "repo",
		Branch:   "test-branch",
		Agent:    "claude",
		Status:   StatusRunning,
	}

	_, err := sm.Fork("forked", "source", 24, 80)
	if err == nil {
		t.Fatal("expected error for fork of singleton session")
	}
	if !strings.Contains(err.Error(), "singleton") {
		t.Errorf("error = %q, want mention of singleton", err.Error())
	}
}

func TestToSessionInfoIncludes(t *testing.T) {
	sess := SessionState{
		ID:           "abc",
		Name:         "test",
		Agent:        "claude",
		WorktreePath: "/session/dem-dev",
		Status:       StatusRunning,
		CreatedAt:    time.Now().UTC(),
		Includes: []IncludedRepoState{
			{
				RepoPath:     "/home/user/Code/grafana",
				RepoName:     "grafana",
				WorktreePath: "/session/grafana",
				Branch:       "user/graith/test/grafana",
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
	if inc.RepoName != "grafana" {
		t.Errorf("RepoName = %q, want %q", inc.RepoName, "grafana")
	}
	if inc.WorktreePath != "/session/grafana" {
		t.Errorf("WorktreePath = %q, want %q", inc.WorktreePath, "/session/grafana")
	}
	if inc.Branch != "user/graith/test/grafana" {
		t.Errorf("Branch = %q, want %q", inc.Branch, "user/graith/test/grafana")
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
		Name:      "test",
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
		Name:         "dem-dev-session",
		RepoPath:     "/home/user/dem-dev",
		RepoName:     "dem-dev",
		WorktreePath: "/data/worktrees/dem-dev/hash/s1/dem-dev",
		Branch:       "user/graith/s1",
		Agent:        "claude",
		Status:       StatusStopped,
		CreatedAt:    time.Now().UTC(),
		Includes: []IncludedRepoState{
			{
				RepoPath:     "/home/user/grafana",
				RepoName:     "grafana",
				WorktreePath: "/data/worktrees/dem-dev/hash/s1/grafana",
				Branch:       "user/graith/s1/grafana",
				BaseBranch:   "main",
			},
			{
				RepoPath:     "/home/user/session-replay",
				RepoName:     "session-replay",
				WorktreePath: "/data/worktrees/dem-dev/hash/s1/session-replay",
				Branch:       "user/graith/s1/session-replay",
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
	if s.Includes[0].RepoName != "grafana" {
		t.Errorf("Includes[0].RepoName = %q, want %q", s.Includes[0].RepoName, "grafana")
	}
	if s.Includes[1].RepoName != "session-replay" {
		t.Errorf("Includes[1].RepoName = %q, want %q", s.Includes[1].RepoName, "session-replay")
	}
	if s.Includes[0].WorktreePath != "/data/worktrees/dem-dev/hash/s1/grafana" {
		t.Errorf("Includes[0].WorktreePath = %q", s.Includes[0].WorktreePath)
	}
}

func TestResumeIncludesValidatesMissingWorktree(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.state.Sessions["s1"] = &SessionState{
		ID:           "s1",
		Name:         "test",
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
	if err == nil {
		t.Fatal("expected error for missing included worktree")
	}
	if !strings.Contains(err.Error(), "no longer a valid git repo") {
		t.Errorf("error = %q, want mention of no longer a valid git repo", err.Error())
	}
}

func TestParentIDPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := &State{
		Version: CurrentStateVersion,
		Sessions: map[string]*SessionState{
			"child1": {
				ID:           "child1",
				ParentID:     "parent1",
				Name:         "child",
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
		Name:      "test",
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
		"root":       {ID: "root", Name: "root"},
		"child1":     {ID: "child1", ParentID: "root", Name: "child1"},
		"child2":     {ID: "child2", ParentID: "root", Name: "child2"},
		"grandchild": {ID: "grandchild", ParentID: "child1", Name: "grandchild"},
		"unrelated":  {ID: "unrelated", Name: "unrelated"},
	}

	result := sm.collectDescendants("root")

	if len(result) != 4 {
		t.Fatalf("expected 4 sessions (root + 3 descendants), got %d: %v", len(result), result)
	}

	resultSet := make(map[string]bool)
	for _, id := range result {
		resultSet[id] = true
	}
	for _, expected := range []string{"root", "child1", "child2", "grandchild"} {
		if !resultSet[expected] {
			t.Errorf("missing expected session %q in result", expected)
		}
	}
	if resultSet["unrelated"] {
		t.Error("unrelated session should not be in result")
	}

	indexOf := make(map[string]int)
	for i, id := range result {
		indexOf[id] = i
	}
	if indexOf["grandchild"] > indexOf["child1"] {
		t.Error("grandchild should come before child1 (leaves first)")
	}
	if indexOf["child1"] > indexOf["root"] {
		t.Error("child1 should come before root (leaves first)")
	}
}

func TestStateVersionRejectsNewer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	data := []byte(`{"version":999,"sessions":{}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadState(path)
	if err == nil {
		t.Fatal("expected error loading state with newer version")
	}
	if !strings.Contains(err.Error(), "newer than this binary") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunMessageCleanupLoopReadsConfig(t *testing.T) {
	t.Run("does not exit when config starts at zero", func(t *testing.T) {
		sm := newTestSessionManager(t)
		ms, err := NewMsgStore(filepath.Join(t.TempDir(), "msg.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer ms.Close()
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
		defer ms.Close()
		sm.SetMsgStore(ms)

		if _, err := ms.Publish("test-stream", "s1", "agent", "old msg", "", ""); err != nil {
			t.Fatal(err)
		}

		// Config starts at zero — cleanup should be a no-op.
		sm.runMessageCleanupFromConfig()
		msgs, err := ms.Read("test-stream", "", false, "")
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

		msgs, err = ms.Read("test-stream", "", false, "")
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
		Name:         "being-deleted",
		Agent:        "claude",
		Status:       StatusDeleting,
		WorktreePath: "/tmp/whatever",
	}

	_, err := sm.Resume("del1", 24, 80)
	if err == nil {
		t.Fatal("expected error resuming a deleting session")
	}
	if !strings.Contains(err.Error(), "is being deleted") {
		t.Errorf("error = %q, want mention of 'is being deleted'", err.Error())
	}
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
		Name:         "test",
		Agent:        "claude",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: worktreeDir,
		Branch:       "graith/test-s1",
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
		Name:         "leaky",
		Agent:        "claude",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: worktreeDir,
		Branch:       "graith/leaky-fail1",
		Status:       StatusStopped,
	}

	err := sm.Delete("fail1")
	if err == nil {
		t.Fatal("expected error from failed git teardown")
	}
	if !strings.Contains(err.Error(), "git teardown failed") {
		t.Errorf("error = %q, want mention of git teardown", err.Error())
	}

	if _, ok := sm.state.Sessions["fail1"]; !ok {
		t.Error("session should be kept in state when teardown fails")
	}
}

func TestDeleteSucceedsWhenWorktreeAlreadyGone(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.state.Sessions["gone1"] = &SessionState{
		ID:           "gone1",
		Name:         "already-cleaned",
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

func TestDeleteWithChildrenKeepsFailedSessions(t *testing.T) {
	tmpDir := t.TempDir()
	worktreeDir := filepath.Join(tmpDir, "existing-child-worktree")
	if err := os.MkdirAll(worktreeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	sm := newTestSessionManager(t)

	sm.state.Sessions["parent1"] = &SessionState{
		ID:     "parent1",
		Name:   "parent",
		Agent:  "claude",
		Status: StatusStopped,
	}
	sm.state.Sessions["child1"] = &SessionState{
		ID:           "child1",
		Name:         "child",
		Agent:        "claude",
		ParentID:     "parent1",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: worktreeDir,
		Branch:       "graith/child-child1",
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
		Name:   "parent",
		Agent:  "claude",
		Status: StatusStopped,
	}
	sm.state.Sessions["child1"] = &SessionState{
		ID:           "child1",
		Name:         "child",
		Agent:        "claude",
		ParentID:     "parent1",
		RepoPath:     "/nonexistent/repo",
		WorktreePath: "/nonexistent/worktree",
		Branch:       "graith/child-child1",
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
	id := createTestSession(sm, "test-session")

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
	id := createTestSession(sm, "test-session")

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
	id := createTestSession(sm, "test-session")

	sm.SetSummary(id, "Working", 0)
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
	id := createTestSession(sm, "test-session")

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
		ID: id, Name: "test", Status: StatusRunning, Agent: "claude",
		SummaryText:  "Running tests",
		SummarySetAt: &now,
		SummaryTTL:   0,
	}

	sess := newTestPTYSession(t, "true")
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session to exit")
	}

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
		ID: id, Name: "test", Status: StatusRunning, Agent: "claude",
		SummaryText:  "Stopped by shutdown (was: Building)",
		SummarySetAt: &now,
		SummaryTTL:   0,
		StopReason:   StopReasonShutdown,
	}

	sess := newTestPTYSession(t, "true")
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session to exit")
	}

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
		ID: "s1", Name: "worker", Status: StatusRunning, Agent: "claude",
		SummaryText:  "Running tests",
		SummarySetAt: &now,
	}
	sm.state.Sessions["s2"] = &SessionState{
		ID: "s2", Name: "idle", Status: StatusStopped, Agent: "claude",
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

func TestReconcileWritesLifecycleSummaries(t *testing.T) {
	t.Run("creating becomes errored", func(t *testing.T) {
		state := &State{Sessions: map[string]*SessionState{
			"c": {ID: "c", Name: "stuck-create", Status: StatusCreating},
		}}
		state.Reconcile()
		if state.Sessions["c"].SummaryText != "Interrupted by daemon restart" {
			t.Errorf("SummaryText = %q, want %q", state.Sessions["c"].SummaryText, "Interrupted by daemon restart")
		}
	})

	t.Run("dead running becomes stopped", func(t *testing.T) {
		state := &State{Sessions: map[string]*SessionState{
			"r": {ID: "r", Name: "orphan", Status: StatusRunning, PID: 99999999},
		}}
		state.Reconcile()
		if state.Sessions["r"].SummaryText != "Lost during daemon restart" {
			t.Errorf("SummaryText = %q, want %q", state.Sessions["r"].SummaryText, "Lost during daemon restart")
		}
	})

	t.Run("deleting becomes stopped", func(t *testing.T) {
		state := &State{Sessions: map[string]*SessionState{
			"d": {ID: "d", Name: "stuck-delete", Status: StatusDeleting},
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
	defer ms.Close()

	sm := newTestSessionManager(t)
	sm.messages = ms
	sm.mu.Lock()
	sm.state.Sessions["sess1"] = &SessionState{
		ID: "sess1", Name: "test", Status: StatusRunning, Agent: "claude",
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
			sm.onAgentStatusChange("sess1", "test", "active", "approval")
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
