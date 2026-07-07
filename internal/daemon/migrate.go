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
	if forcesID(targetAgent) {
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

	// Native session-id capture is handled inside resumeWithSummaryAndPrompt
	// (called above), which uses the session's effective CODEX_HOME and the
	// real post-spawn timestamp. A second capture here would be redundant and
	// would scrape the daemon's default root instead.
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

// forcesID reports whether graith assigns the agent's native session id at
// launch (the agent accepts a client-supplied id via its args). For these
// agents graith mints the id with newAgentSessionID; for all others the id must
// be captured from the agent's own on-disk state after start (captureNativeSessionID).
//
// Only Claude is forced today. OpenCode also has a --session flag, but forcing
// it is gated on verifying it accepts a brand-new id in graith's UUID format at
// first start (OpenCode's native ids are not UUIDs) — until then OpenCode falls
// back to a fresh conversation via the empty-id guard rather than shipping a
// broken `--session ""`.
func forcesID(agent string) bool {
	return agent == "claude"
}

// scrapesID reports whether graith can recover an agent's native session id by
// reading its on-disk transcript after start. Only agents with a known,
// reverse-engineered state layout are listed; others resume fresh until a
// locator is added (e.g. Agy under ~/.gemini, Cursor under ~/.cursor/projects).
func scrapesID(agent string) bool {
	return agent == "codex"
}

// captureNativeSessionID polls for the native session id a freshly-started,
// self-minting agent writes to disk and records it so later resume is
// deterministic. Best-effort: on timeout the id is left empty and the agent
// falls back to its configured empty-id behaviour (Codex: `resume --last`;
// others: fresh start).
//
// It never overwrites a non-empty id. A generation check ((expectedPID,
// expectedPIDStartTime) vs the stored values) stops a stale goroutine from an
// earlier start clobbering a later one — PID alone is insufficient because a
// resume holds the *old* PID in state until the new one is committed (and PIDs
// can be reused), so the start time disambiguates the generation.
//
// Concurrency note: two guards keep this from cross-assigning ids between
// concurrent shared-worktree / in-place sessions. First, scrapeSessionID
// refuses to guess when multiple distinct native ids match the (since, cwd)
// window. That alone isn't enough — staggered rollout writes leave a window
// where only a sibling's rollout exists and would be mis-assigned — so second,
// before recording an id we skip capture entirely if it would cross-assign
// (wouldCrossAssign): the id is already another session's, or a same-agent
// sibling shared the cwd during our capture window. Either way the session
// falls back to a non-pinned resume (issue #844).
//
// stateRoot is the agent's effective state root (e.g. CODEX_HOME from the
// session's launch env); pass "" to fall back to the daemon's default.
func (sm *SessionManager) captureNativeSessionID(id, agent, worktreePath, stateRoot string, since time.Time, expectedPID int, expectedPIDStartTime int64) {
	if !scrapesID(agent) {
		return
	}

	for i := 0; i < 40; i++ {
		time.Sleep(250 * time.Millisecond)

		sid, ok := scrapeSessionID(agent, worktreePath, stateRoot, since)
		if !ok || sid == "" {
			continue
		}

		sm.mu.Lock()

		s, exists := sm.state.Sessions[id]
		switch {
		case !exists || s.Agent != agent:
			// session gone or migrated away — nothing to record.
		case s.AgentSessionID != "":
			// already captured (or forced) — never clobber a good id.
		case expectedPID != 0 && (s.PID != expectedPID || (expectedPIDStartTime != 0 && s.PIDStartTime != expectedPIDStartTime)):
			// a newer start replaced this process generation; its own capture
			// goroutine owns the id now.
		case sm.wouldCrossAssign(id, agent, worktreePath, sid, since):
			// the scraped id can't be safely attributed to us — either another
			// same-agent session shared this cwd during our capture window, or
			// the id is already claimed by another session. The scrape's own
			// ambiguity guard only trips once *both* rollouts exist, but
			// staggered writes leave a window where only a sibling's rollout is
			// present and it would be mis-assigned. Skip capture; resume falls
			// back to --last rather than pinning the wrong conversation (#844).
			sm.mu.Unlock()
			sm.log.Warn("native session id capture skipped: ambiguous shared cwd", "session_id", id, "agent", agent, "worktree", worktreePath)

			return
		default:
			s.AgentSessionID = sid
			_ = sm.saveState()
		}
		sm.mu.Unlock()

		return
	}

	sm.log.Warn("native session id capture timed out", "session_id", id, "agent", agent)
}

// wouldCrossAssign reports whether recording `sid` for session `id` risks
// pinning another session's conversation. It refuses in two cases:
//
//   - `sid` is already recorded as another session's AgentSessionID — a native
//     id is unique to one conversation, so it must never back two sessions,
//     regardless of that session's cwd or current status.
//   - a different same-agent session shares this worktree and was live during
//     our capture window: it is currently running/creating, or it stopped only
//     after `since` (our start). Such a sibling may have written the rollout we
//     just scraped, and the scrape can't say whose it is. A sibling that stopped
//     before `since` is excluded — its rollout predates our window and the
//     scrape's mtime filter already skips it.
//
// This closes the staggered-write race in #844 including the variant where the
// sibling exits between the scrape and this check. Callers must hold sm.mu.
func (sm *SessionManager) wouldCrossAssign(id, agent, worktreePath, sid string, since time.Time) bool {
	want := canonWorktree(worktreePath)

	for other, s := range sm.state.Sessions {
		if other == id {
			continue
		}

		if sid != "" && s.AgentSessionID == sid {
			return true
		}

		if s.Agent != agent || canonWorktree(s.WorktreePath) != want {
			continue
		}

		switch s.Status {
		case StatusRunning, StatusCreating:
			return true
		case StatusStopped, StatusErrored:
			if s.StatusChangedAt.After(since) {
				return true
			}
		}
	}

	return false
}

// canonWorktree cleans a worktree path (resolving symlinks when possible) so
// two spellings of the same directory compare equal.
func canonWorktree(p string) string {
	if p == "" {
		return ""
	}

	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}

	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}

	return filepath.Clean(p)
}

// scrapeSessionID reads the native session id for a single agent from its
// on-disk state, matching only files created at/after `since` in worktreePath.
// It returns no id when the match is ambiguous (multiple distinct ids in the
// same cwd window), so callers never pin the wrong conversation.
func scrapeSessionID(agent, worktreePath, stateRoot string, since time.Time) (string, bool) {
	switch agent {
	case "codex":
		return transcript.CodexSessionIDSince(stateRoot, worktreePath, since)
	default:
		return "", false
	}
}
