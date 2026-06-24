package daemon

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/d0ugal/graith/internal/agent/transcript"
)

// Migrate swaps the agent on an existing session in place: it renders the
// current agent's conversation to a neutral Markdown file, stops the current
// agent, changes the session's agent type, and restarts it in the *same*
// worktree seeded with that file. The session keeps its id, name, worktree and
// branch — only the agent type changes — so all code state (commits and
// uncommitted edits) carries over with no branching.
//
// Ordering is chosen so a doomed migration aborts before the running agent is
// touched (read+render happen first and fail fast), and so the context file
// exists before the new agent starts. If the target agent fails to start, the
// original agent is restored.
//
// See docs/design/2026-06-24-cross-agent-conversation-migration-design.md.
func (sm *SessionManager) Migrate(id, targetAgent, targetModel string, rows, cols uint16) (SessionState, error) {
	// --- snapshot + validate ---
	sm.mu.RLock()
	sess, ok := sm.state.Sessions[id]
	if !ok {
		sm.mu.RUnlock()
		return SessionState{}, fmt.Errorf("session %q not found", id)
	}
	srcAgent := sess.Agent
	srcAgentSessionID := sess.AgentSessionID
	srcModel := sess.Model
	srcFreshStart := sess.FreshStart
	worktreePath := sess.WorktreePath
	repoPath := sess.RepoPath
	isOrchestrator := sess.SystemKind == SystemKindOrchestrator
	status := sess.Status
	sm.mu.RUnlock()

	if status == StatusCreating || status == StatusDeleting {
		return SessionState{}, fmt.Errorf("session %q is %s — cannot migrate now", id, status)
	}
	if targetAgent == "" {
		return SessionState{}, fmt.Errorf("migrate requires a target agent")
	}
	if targetAgent == srcAgent {
		return SessionState{}, fmt.Errorf("session %q already uses agent %q", id, targetAgent)
	}
	cfg := sm.Config()
	targetCfg, ok := cfg.Agents[targetAgent]
	if !ok {
		return SessionState{}, fmt.Errorf("unknown target agent %q", targetAgent)
	}
	if !transcript.Supported(srcAgent) {
		return SessionState{}, fmt.Errorf("migration from agent %q is not supported (no transcript reader)", srcAgent)
	}
	if targetModel != "" {
		if err := validateModel(targetCfg, targetModel); err != nil {
			return SessionState{}, err
		}
	}

	// --- Phase 0: read + render the source transcript (fail fast) ---
	conv, err := transcript.Read(srcAgent, srcAgentSessionID, worktreePath)
	if err != nil {
		return SessionState{}, fmt.Errorf("read source transcript: %w", err)
	}
	rendered := conv.Render(transcript.RenderOptions{})

	// --- stage the rendered context in the session's tmp dir ---
	var tmpDir string
	switch {
	case isOrchestrator:
		tmpDir = sm.orchestratorTmpDir()
	case repoPath != "":
		tmpDir, err = sm.repoTmpDir(repoPath)
		if err != nil {
			return SessionState{}, err
		}
	default:
		return SessionState{}, fmt.Errorf("session %q has no tmp dir to stage migration context", id)
	}
	// Stage in a per-session subdirectory rather than the shared repo tmp root.
	contextDir := filepath.Join(tmpDir, "migrate-"+id)
	if err := os.MkdirAll(contextDir, 0o700); err != nil {
		return SessionState{}, fmt.Errorf("create migration context dir: %w", err)
	}
	contextPath := filepath.Join(contextDir, "context.md")
	if err := writeFileAtomic(contextPath, []byte(rendered)); err != nil {
		return SessionState{}, fmt.Errorf("write migration context: %w", err)
	}

	// --- stop the current agent (Restart-style: kill and wait for exit) ---
	if ptySess, hasPTY := sm.GetPTY(id); hasPTY && !ptySess.Exited() {
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.StopReason = StopReasonUser
		}
		sm.mu.Unlock()
		if err := ptySess.Kill(); err != nil {
			_ = os.RemoveAll(contextDir)
			return SessionState{}, fmt.Errorf("stop source agent: %w", err)
		}
		<-ptySess.Done()
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok && s.Status == StatusRunning {
			ec := ptySess.ExitCode()
			s.Status = StatusStopped
			s.StatusChangedAt = time.Now()
			s.ExitCode = &ec
			s.PID = 0
			s.PIDStartTime = 0
			_ = sm.saveState()
		}
		sm.mu.Unlock()
	}

	// --- clean up the old agent's hooks (e.g. cursor's in-worktree files) ---
	sm.cleanupHooks(id, srcAgent, worktreePath)

	// --- swap agent fields under lock ---
	sm.mu.Lock()
	s, ok := sm.state.Sessions[id]
	if !ok {
		sm.mu.Unlock()
		return SessionState{}, fmt.Errorf("session %q deleted during migrate", id)
	}
	s.MigratedFrom = &MigrationInfo{
		Agent:          srcAgent,
		Model:          srcModel,
		AgentSessionID: srcAgentSessionID,
		RenderedPath:   contextPath,
		At:             time.Now().UTC(),
	}
	s.Agent = targetAgent
	s.Model = targetModel
	s.FreshStart = true // force a fresh start (agent.Args + seed), not resume_args
	if targetAgent == "claude" {
		s.AgentSessionID = newAgentSessionID()
	} else {
		s.AgentSessionID = ""
	}
	saveErr := sm.saveState()
	sm.mu.Unlock()
	if saveErr != nil {
		return SessionState{}, fmt.Errorf("persist migration swap: %w", saveErr)
	}

	// --- start the target agent in the same worktree, seeded ---
	seed := transcript.BuildSeedPrompt(srcAgent, contextPath)
	startedAt := time.Now()
	res, startErr := sm.resumeWithSummaryAndPrompt(id, rows, cols, "Migrated from "+srcAgent, seed)

	// Post-start health check: a PTY that spawns but exits immediately (bad
	// auth/config — the likely outage case) is not a healthy start.
	if startErr == nil && !sm.survivedStartup(id, migrateHealthWindow) {
		startErr = fmt.Errorf("target agent %q exited immediately after start (likely auth/config failure)", targetAgent)
	}

	if startErr != nil {
		// Ensure the (likely dead) target process is fully stopped before restore.
		if p, ok := sm.GetPTY(id); ok && !p.Exited() {
			_ = p.Kill()
			<-p.Done()
		}
		// Revert to the original agent, keeping MigratedFrom so a both-failed
		// terminal state stays recoverable.
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.Agent = srcAgent
			s.Model = srcModel
			s.AgentSessionID = srcAgentSessionID
			s.FreshStart = srcFreshStart
			s.Status = StatusStopped
			s.PID = 0
			s.PIDStartTime = 0
			_ = sm.saveState()
		}
		sm.mu.Unlock()
		if _, rerr := sm.resumeWithSummary(id, rows, cols, "Restored after failed migrate to "+targetAgent); rerr != nil {
			// Both agents failed: leave the session Stopped with the original
			// fields, retaining MigratedFrom + the rendered context for recovery.
			return SessionState{}, fmt.Errorf(
				"migrate to %q failed (%v) and restoring %q also failed (%v); session left stopped, rendered context at %s",
				targetAgent, startErr, srcAgent, rerr, contextPath)
		}
		// Original restored: drop the migration record and its context file.
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.MigratedFrom = nil
			_ = sm.saveState()
		}
		sm.mu.Unlock()
		_ = os.RemoveAll(contextDir)
		return SessionState{}, fmt.Errorf("migrate to %q failed: %w (original %q agent restored)", targetAgent, startErr, srcAgent)
	}

	// --- best-effort Codex session-id capture (async, never blocks) ---
	if targetAgent == "codex" {
		go sm.captureCodexSessionID(id, worktreePath, startedAt)
	}
	return res, nil
}

// migrateHealthWindow is how long Migrate waits to confirm the target agent
// survived startup before declaring the migration successful.
const migrateHealthWindow = 1500 * time.Millisecond

// survivedStartup reports whether the session's agent process is still alive
// after a short window.
func (sm *SessionManager) survivedStartup(id string, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		p, ok := sm.GetPTY(id)
		if !ok || p.Exited() {
			return false
		}
		time.Sleep(150 * time.Millisecond)
	}
	return true
}

// removeMigrationContext deletes a session's staged migration context dir, if
// any. Called on session delete so the rendered conversation does not linger.
func (sm *SessionManager) removeMigrationContext(s *SessionState) {
	if s == nil || s.MigratedFrom == nil || s.MigratedFrom.RenderedPath == "" {
		return
	}
	_ = os.RemoveAll(filepath.Dir(s.MigratedFrom.RenderedPath))
}

// newAgentSessionID generates a UUID-format id matching the format Create/Fork
// use for Claude sessions.
func newAgentSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// captureCodexSessionID polls for the rollout the freshly-started Codex agent
// writes, and records its id so later resume/migrate-back is deterministic.
// Best-effort: on timeout the id is left empty and Codex falls back to
// resume --last.
func (sm *SessionManager) captureCodexSessionID(id, worktreePath string, since time.Time) {
	for i := 0; i < 12; i++ {
		time.Sleep(250 * time.Millisecond)
		path, ok := transcript.LocateCodexSince(worktreePath, since)
		if !ok {
			continue
		}
		sid, ok := transcript.CodexRolloutID(path)
		if !ok || sid == "" {
			continue
		}
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok && s.Agent == "codex" {
			s.AgentSessionID = sid
			_ = sm.saveState()
		}
		sm.mu.Unlock()
		return
	}
	sm.log.Warn("codex session id capture timed out", "session_id", id)
}
