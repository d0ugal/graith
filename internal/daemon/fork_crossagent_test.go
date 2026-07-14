package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
	"github.com/d0ugal/graith/internal/testutil"
)

// forkTestCleanup kills the forked PTY and removes its worktree.
func forkTestCleanup(t *testing.T, sm *SessionManager, repoDir, forkedID, worktreePath string) {
	t.Helper()
	t.Cleanup(func() {
		sm.mu.RLock()
		sess, ok := sm.sessions[forkedID]
		sm.mu.RUnlock()

		if ok {
			_ = sess.Kill()
			<-sess.Done() // wait for exit so the launch-slot goroutine settles
			sess.Close()
		}

		if worktreePath != "" {
			cmd := testutil.GitCommand("worktree", "remove", "--force", worktreePath)
			cmd.Dir = repoDir
			_ = cmd.Run()
		}
	})
}

// crossAgentForkSM builds a SessionManager with a running Claude source session
// whose transcript is staged on disk, plus a runnable non-claude target agent.
func crossAgentForkSM(t *testing.T) (*SessionManager, string) {
	t.Helper()
	repoDir := initTempGitRepo(t)

	writeClaudeTranscript(t, "braw-agent-id",
		`{"type":"user","uuid":"u1","parentUuid":"","message":{"role":"user","content":"fix the bothy"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"On it, braw."}]}}`,
	)

	cfg := config.Default()
	cfg.FetchOnCreate = false
	// Target agent: sh -c 'sleep 60' tolerates the appended seed positional (it
	// becomes $0) and stays alive so the fork commits cleanly.
	cfg.Agents["bide-agent"] = config.Agent{
		Command: "sh",
		Args:    []string{"-c", "sleep 60"},
	}

	sm := newSMWithConfig(t, cfg)

	sm.state.Sessions["src1"] = &SessionState{
		ID:             "src1",
		Name:           "braw-source",
		RepoPath:       repoDir,
		RepoName:       "croft",
		WorktreePath:   repoDir,
		Branch:         "feat/bothy",
		BaseBranch:     "main",
		Agent:          "claude",
		AgentSessionID: "braw-agent-id",
		Model:          "claude-opus-4-8",
		Status:         StatusRunning,
	}

	return sm, repoDir
}

func TestForkWithAgentCrossAgent(t *testing.T) {
	sm, repoDir := crossAgentForkSM(t)

	forked, err := sm.ForkWithAgent("braw-fork", "src1", "bide-agent", "", 24, 80)
	if err != nil {
		t.Fatalf("ForkWithAgent() unexpected error: %v", err)
	}

	forkTestCleanup(t, sm, repoDir, forked.ID, forked.WorktreePath)

	if forked.Agent != "bide-agent" {
		t.Errorf("forked Agent = %q, want %q", forked.Agent, "bide-agent")
	}

	if forked.ID == "src1" {
		t.Error("cross-agent fork reused the source id")
	}

	if forked.WorktreePath == repoDir {
		t.Error("cross-agent fork reused the source worktree (should branch a new one)")
	}

	// Source session must be untouched — the fork is a new session, the original
	// keeps running its original agent.
	sm.mu.RLock()
	src := sm.state.Sessions["src1"]
	sm.mu.RUnlock()

	if src.Agent != "claude" || src.Status != StatusRunning {
		t.Errorf("source mutated by fork: agent=%q status=%q", src.Agent, src.Status)
	}

	// Provenance recorded.
	if forked.MigratedFrom == nil {
		t.Fatal("cross-agent fork did not record MigratedFrom")
	}

	if forked.MigratedFrom.Agent != "claude" {
		t.Errorf("MigratedFrom.Agent = %q, want claude", forked.MigratedFrom.Agent)
	}

	// The rendered context file exists, is under the new session's own subdir,
	// and carries the source conversation.
	rendered := forked.MigratedFrom.RenderedPath
	if !strings.Contains(rendered, "fork-"+forked.ID) {
		t.Errorf("context path %q not in a per-session subdir", rendered)
	}

	data, err := os.ReadFile(rendered)
	if err != nil {
		t.Fatalf("read rendered context: %v", err)
	}

	if !strings.Contains(string(data), "fix the bothy") {
		t.Errorf("rendered context missing source turn; got:\n%s", data)
	}
	// The rendered doc must use the FORK header, not the migrate header — the
	// fork framing tells the agent the worktree is fresh (issue #1043 review).
	if !strings.Contains(string(data), "Forked conversation context") {
		t.Errorf("rendered context did not use the fork header; got:\n%s", data)
	}

	// The seed prompt must actually reach the launched agent as an argument
	// (guards against it being dropped or the wrong args being used).
	sm.mu.RLock()
	drv := sm.sessions[forked.ID]
	sm.mu.RUnlock()

	ptySess, ok := drv.(*grpty.Session)
	if !ok {
		t.Fatalf("session driver is %T, want *pty.Session", drv)
	}

	if !argvContains(ptySess.Cmd.Args, "forked from a claude session") {
		t.Errorf("launched args do not contain the fork seed prompt: %v", ptySess.Cmd.Args)
	}
}

// argvContains reports whether any arg contains the substring.
func argvContains(args []string, sub string) bool {
	for _, a := range args {
		if strings.Contains(a, sub) {
			return true
		}
	}

	return false
}

func TestForkWithAgentCrossAgentModelOverride(t *testing.T) {
	sm, repoDir := crossAgentForkSM(t)

	forked, err := sm.ForkWithAgent("braw-fork", "src1", "bide-agent", "gpt-thrawn", 24, 80)
	if err != nil {
		t.Fatalf("ForkWithAgent() unexpected error: %v", err)
	}

	forkTestCleanup(t, sm, repoDir, forked.ID, forked.WorktreePath)

	if forked.Model != "gpt-thrawn" {
		t.Errorf("forked Model = %q, want target override gpt-thrawn (not source model)", forked.Model)
	}
}

// TestForkSameAgentIsNativeFork verifies passing --agent equal to the source's
// agent is a native fork: no rendered context, no MigratedFrom.
func TestForkSameAgentIsNativeFork(t *testing.T) {
	repoDir := initTempGitRepo(t)

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["bide-agent"] = config.Agent{Command: "sh", Args: []string{"-c", "sleep 60"}}
	sm := newSMWithConfig(t, cfg)

	sm.state.Sessions["src1"] = &SessionState{
		ID:           "src1",
		Name:         "braw-source",
		RepoPath:     repoDir,
		RepoName:     "croft",
		WorktreePath: repoDir,
		Branch:       "feat/bothy",
		BaseBranch:   "main",
		Agent:        "bide-agent",
		Status:       StatusRunning,
	}

	forked, err := sm.ForkWithAgent("braw-fork", "src1", "bide-agent", "", 24, 80)
	if err != nil {
		t.Fatalf("ForkWithAgent() unexpected error: %v", err)
	}

	forkTestCleanup(t, sm, repoDir, forked.ID, forked.WorktreePath)

	if forked.MigratedFrom != nil {
		t.Errorf("same-agent fork recorded MigratedFrom = %+v, want nil", forked.MigratedFrom)
	}

	if forked.Agent != "bide-agent" {
		t.Errorf("forked Agent = %q, want bide-agent", forked.Agent)
	}
}

// TestForkWithAgentUnsupportedSource rejects a cross-agent fork when the source
// agent has no transcript reader (nothing to seed the new agent with).
func TestForkWithAgentUnsupportedSource(t *testing.T) {
	repoDir := initTempGitRepo(t)

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["bide-agent"] = config.Agent{Command: "sh", Args: []string{"-c", "sleep 60"}}
	cfg.Agents["haar-agent"] = config.Agent{Command: "sh", Args: []string{"-c", "sleep 60"}}
	sm := newSMWithConfig(t, cfg)

	sm.state.Sessions["src1"] = &SessionState{
		ID:           "src1",
		Name:         "dreich-source",
		RepoPath:     repoDir,
		RepoName:     "croft",
		WorktreePath: repoDir,
		Branch:       "feat/haar",
		BaseBranch:   "main",
		Agent:        "haar-agent", // no transcript reader
		Status:       StatusRunning,
	}

	_, err := sm.ForkWithAgent("thrawn-fork", "src1", "bide-agent", "", 24, 80)
	if err == nil {
		t.Fatal("expected error forking from an unsupported source agent")
	}

	if !strings.Contains(err.Error(), "no transcript reader") {
		t.Errorf("error = %v, want mention of missing transcript reader", err)
	}

	// State must not retain a placeholder for the rejected fork.
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for id, s := range sm.state.Sessions {
		if id != "src1" && s.Name == "thrawn-fork" {
			t.Errorf("rejected fork left a session in state: %s", id)
		}
	}
}

// TestForkWithAgentUnknownTarget rejects a cross-agent fork to an agent that is
// not configured.
func TestForkWithAgentUnknownTarget(t *testing.T) {
	sm, _ := crossAgentForkSM(t)

	_, err := sm.ForkWithAgent("thrawn-fork", "src1", "nae-such-agent", "", 24, 80)
	if err == nil {
		t.Fatal("expected error forking to an unknown target agent")
	}

	if !strings.Contains(err.Error(), "unknown target agent") {
		t.Errorf("error = %v, want unknown target agent", err)
	}
}

// TestForkWithAgentEmptyTranscript fails fast (before git setup, no session, no
// staged context) when the source transcript has no usable turns.
func TestForkWithAgentEmptyTranscript(t *testing.T) {
	repoDir := initTempGitRepo(t)

	// A transcript with only metadata → zero usable turns → ErrNoTurns.
	writeClaudeTranscript(t, "haar-agent-id", `{"type":"summary","uuid":"s1"}`)

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["bide-agent"] = config.Agent{Command: "sh", Args: []string{"-c", "sleep 60"}}
	sm := newSMWithConfig(t, cfg)

	sm.state.Sessions["src1"] = &SessionState{
		ID:             "src1",
		Name:           "dreich-source",
		RepoPath:       repoDir,
		RepoName:       "croft",
		WorktreePath:   repoDir,
		Branch:         "feat/haar",
		BaseBranch:     "main",
		Agent:          "claude",
		AgentSessionID: "haar-agent-id",
		Status:         StatusRunning,
	}

	_, err := sm.ForkWithAgent("thrawn-fork", "src1", "bide-agent", "", 24, 80)
	if err == nil {
		t.Fatal("expected error forking from an empty transcript")
	}

	if !strings.Contains(err.Error(), "read source transcript") {
		t.Errorf("error = %v, want read source transcript failure", err)
	}

	assertNoForkContextDir(t, sm, repoDir)
}

// TestForkWithAgentModelRequiresCrossAgent rejects a model override that isn't
// paired with an actual agent change (would otherwise be silently dropped).
func TestForkWithAgentModelRequiresCrossAgent(t *testing.T) {
	sm, _ := crossAgentForkSM(t)

	// No target agent, but a model given.
	_, err := sm.ForkWithAgent("thrawn-fork", "src1", "", "gpt-thrawn", 24, 80)
	if err == nil {
		t.Fatal("expected error: --model without --agent")
	}

	if !strings.Contains(err.Error(), "--model requires") {
		t.Errorf("error = %v, want --model requires a different agent", err)
	}

	// Target agent equal to the source is a same-agent fork; a model is still
	// meaningless there and must be rejected too.
	_, err = sm.ForkWithAgent("thrawn-fork2", "src1", "claude", "gpt-thrawn", 24, 80)
	if err == nil {
		t.Fatal("expected error: --model on a same-agent fork")
	}
}

// TestForkWithAgentContextCleanedOnGitFailure is the regression test for the
// staged-context leak: when git worktree setup fails after the transcript has
// been staged, the fork-<id> context dir must not be left behind.
func TestForkWithAgentContextCleanedOnGitFailure(t *testing.T) {
	sm, repoDir := crossAgentForkSM(t)

	// Point the source at a base branch that doesn't exist so git.SetupSession
	// fails (no remote, fetch disabled), exercising the post-staging error path.
	sm.mu.Lock()
	sm.state.Sessions["src1"].BaseBranch = "nae-such-base"
	sm.mu.Unlock()

	_, err := sm.ForkWithAgent("thrawn-fork", "src1", "bide-agent", "", 24, 80)
	if err == nil {
		t.Fatal("expected git setup to fail on a missing base branch")
	}

	assertNoForkContextDir(t, sm, repoDir)

	// No placeholder session left behind either.
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for id, s := range sm.state.Sessions {
		if id != "src1" && s.Name == "thrawn-fork" {
			t.Errorf("failed fork left a session in state: %s", id)
		}
	}
}

// assertNoForkContextDir asserts no fork-<id> staging dir survives under the
// repo tmp dir.
func assertNoForkContextDir(t *testing.T, sm *SessionManager, repoDir string) {
	t.Helper()

	tmpDir, err := sm.repoTmpDir(repoDir)
	if err != nil {
		t.Fatalf("repoTmpDir: %v", err)
	}

	matches, _ := filepath.Glob(filepath.Join(tmpDir, "fork-*"))
	if len(matches) != 0 {
		t.Errorf("staged fork context dir(s) leaked: %v", matches)
	}
}
