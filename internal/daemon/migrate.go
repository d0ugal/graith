package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/d0ugal/graith/internal/agent/transcript"
	"github.com/d0ugal/graith/internal/config"
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
	srcNativeIDLocator := sess.NativeIDLocator
	srcModel := sess.Model
	srcCodex := cloneCodexOptions(sess.Codex)
	srcFreshStart := sess.FreshStart
	worktreePath := sess.WorktreePath
	repoPath := sess.RepoPath
	isOrchestrator := sess.SystemKind == SystemKindOrchestrator
	status := sess.Status
	softDeleted := sess.IsSoftDeleted()
	name := sess.Name

	sm.mu.RUnlock()

	// A soft-deleted session is stopped-and-hidden; migrate re-launches the agent
	// (like fork/resume), so it must refuse by raw ID rather than resurrect a
	// hidden session or mutate its agent metadata. Guard before any transcript
	// I/O or state mutation.
	if softDeleted {
		return SessionState{}, errSoftDeleted(name)
	}

	if status == StatusCreating || status == StatusDeleting {
		return SessionState{}, fmt.Errorf("session %q is %s — cannot migrate now", id, status)
	}

	if targetAgent == "" {
		return SessionState{}, errors.New("migrate requires a target agent")
	}

	if targetAgent == srcAgent {
		return SessionState{}, fmt.Errorf("session %q already uses agent %q", id, targetAgent)
	}

	cfg := sm.Config()

	targetCfg, ok := cfg.Agents[targetAgent]
	if !ok {
		return SessionState{}, fmt.Errorf("unknown target agent %q", targetAgent)
	}

	if !transcript.Supported(transcriptKind(srcNativeIDLocator, srcAgent)) {
		return SessionState{}, fmt.Errorf("migration from agent %q is not supported (no transcript reader)", srcAgent)
	}

	if targetModel != "" {
		if err := validateModel(targetCfg, targetModel); err != nil {
			return SessionState{}, err
		}
	}

	// --- Phase 0: read + render the source transcript (fail fast) ---
	conv, err := transcript.Read(transcriptKind(srcNativeIDLocator, srcAgent), srcAgentSessionID, worktreePath)
	if err != nil {
		return SessionState{}, fmt.Errorf("read source transcript: %w", err)
	}

	rendered := conv.Render(transcript.RenderOptions{
		MaxBytes:      cfg.Transcript.MaxContextBytesOrDefault(),
		MaxToolOutput: cfg.Transcript.MaxToolOutputBytesOrDefault(),
	})

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

		sm.logStopping(id, sm.sessionName(id), StopReasonUser, "migrate", ptySess)

		if err := sm.teardownLiveDriver(context.Background(), ptySess); err != nil {
			_ = os.RemoveAll(contextDir)
			return SessionState{}, fmt.Errorf("stop source agent: %w", err)
		}

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

	// Overwriting MigratedFrom drops the only pointer to any previously-staged
	// context dir (e.g. a cross-agent fork's fork-<id> dir, whose path differs
	// from this migrate's migrate-<id> dir). Remove the prior one first so it
	// isn't orphaned on disk. A no-op when the path is the same or empty.
	if s.MigratedFrom != nil && s.MigratedFrom.RenderedPath != contextPath {
		sm.removeMigrationContext(s)
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
	// Typed options belong to the source agent; keep only those the target agent's
	// option_args can consume and drop the rest, so state stays consistent with the
	// create-time guard (#1186) and a custom alias that declares matching groups
	// inherits them (#1236).
	s.Codex = optionsForAgent(targetCfg, s.Codex)

	// The token count describes the session's CURRENT agent. Migration swaps the
	// agent, so the source agent's usage no longer applies — clear it (and evict
	// the parse cache) so the token loop re-derives from the new agent's
	// transcript rather than mislabelling the old count as the new agent's.
	s.Tokens = nil

	s.FreshStart = true // force a fresh start (agent.Args + seed), not resume_args
	s.NativeIDLocator = cfg.Agents[targetAgent].NativeIDLocator()

	if cfg.Agents[targetAgent].ForcesNativeID() {
		s.AgentSessionID = newAgentSessionID()
	} else {
		s.AgentSessionID = ""
	}

	saveErr := sm.saveState()
	sm.mu.Unlock()

	if sm.tokens != nil {
		sm.tokens.evict(id)
	}

	if saveErr != nil {
		return SessionState{}, fmt.Errorf("persist migration swap: %w", saveErr)
	}

	// --- start the target agent in the same worktree, seeded ---
	seed := transcript.BuildSeedPrompt(srcAgent, contextPath)
	res, startErr := sm.resumeWithSummaryAndPromptFromConfig(cfg, id, rows, cols, "Migrated from "+srcAgent, seed)

	// Post-start health check: a PTY that spawns but exits immediately (bad
	// auth/config — the likely outage case) is not a healthy start.
	if startErr == nil && !sm.survivedStartup(id, cfg.Migration.HealthWindowDuration()) {
		startErr = fmt.Errorf("target agent %q exited immediately after start (likely auth/config failure)", targetAgent)
	}

	if startErr != nil {
		// Ensure the (likely dead) target process is fully stopped before restore.
		var targetStopErr error

		if p, ok := sm.GetPTY(id); ok && !p.Exited() {
			// Record the reason on state before the kill so the "session exited"
			// line agrees with the "stopping session" line — a successful target
			// start cleared StopReason, so without this the exit would default to
			// crash while the stop line says user (issue #1104).
			sm.mu.Lock()
			if s, ok := sm.state.Sessions[id]; ok {
				s.StopReason = StopReasonUser
			}
			sm.mu.Unlock()

			sm.logStopping(id, sm.sessionName(id), StopReasonUser, "migrate-revert", p)
			targetStopErr = sm.teardownLiveDriver(context.Background(), p)
		}
		// Revert to the original agent, keeping MigratedFrom so a both-failed
		// terminal state stays recoverable.
		sm.mu.Lock()
		if s, ok := sm.state.Sessions[id]; ok {
			s.Agent = srcAgent
			s.Model = srcModel
			s.Codex = srcCodex
			s.AgentSessionID = srcAgentSessionID
			s.NativeIDLocator = srcNativeIDLocator
			s.FreshStart = srcFreshStart
			s.Status = StatusStopped
			s.PID = 0
			s.PIDStartTime = 0
			_ = sm.saveState()
		}
		sm.mu.Unlock()

		if targetStopErr != nil {
			return SessionState{}, fmt.Errorf(
				"migrate to %q failed and its driver could not be stopped; original %q left stopped, rendered context at %s: %w",
				targetAgent, srcAgent, contextPath, errors.Join(startErr, targetStopErr))
		}

		if _, rerr := sm.resumeWithSummaryAndPromptFromConfig(cfg, id, rows, cols, "Restored after failed migrate to "+targetAgent, ""); rerr != nil {
			// Both agents failed: leave the session Stopped with the original
			// fields, retaining MigratedFrom + the rendered context for recovery.
			return SessionState{}, fmt.Errorf(
				"migrate to %q failed (%w) and restoring %q also failed (%w); session left stopped, rendered context at %s",
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
// concurrent mirror / in-place sessions. First, scrapeSessionID
// refuses to guess when multiple distinct native ids match the (since, cwd)
// window. That alone isn't enough — staggered rollout writes leave a window
// where only a sibling's rollout exists and would be mis-assigned — so second,
// before recording an id we skip capture entirely if it would cross-assign
// (wouldCrossAssign): the id is already another session's, or a same-agent
// sibling shared the cwd during our capture window. Either way the session
// falls back to a non-pinned resume (issue #844).
//
// stateRoot is the agent's effective state root (e.g. CODEX_HOME from the
// session's launch env); pass "" to fall back to the daemon's default. locator
// is the agent's config-declared native-id locator (config.NativeIDLocator*),
// resolved at launch so a custom alias/wrapper scrapes with the same strategy as
// the built-in it wraps (issue #1236); an empty locator means no capture.
// Cross-assignment detection compares each sibling's PERSISTED launch locator, so
// a reload that changed/removed an alias's locator after another session started
// cannot make us cross-assign that session's rollout.
func (sm *SessionManager) captureNativeSessionID(id, agent, locator, worktreePath, stateRoot string, since time.Time, expectedPID int, expectedPIDStartTime int64) {
	if locator == "" {
		return
	}

	for i := 0; i < 40; i++ {
		time.Sleep(250 * time.Millisecond)

		sid, ok := scrapeSessionID(locator, worktreePath, stateRoot, since)
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
		case sm.wouldCrossAssign(id, locator, worktreePath, sid, since):
			// the scraped id can't be safely attributed to us — either another
			// same-agent session shared this cwd during our capture window, or
			// the id is already claimed by another session. The scrape's own
			// ambiguity guard only trips once *both* rollouts exist, but
			// staggered writes leave a window where only a sibling's rollout is
			// present and it would be mis-assigned. Skip capture; resume falls
			// back to --last rather than pinning the wrong conversation (#844).
			sm.mu.Unlock()
			sm.log.Warn("native session id capture skipped: ambiguous shared cwd", "session_id", id, "agent", agent, "locator", locator, "worktree", worktreePath)

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
//   - a different session that uses the SAME native-id locator shares this
//     worktree and was live during our capture window: it is currently
//     running/creating, or it stopped only after `since` (our start). Such a
//     sibling may have written the rollout we just scraped, and the scrape can't
//     say whose it is. Siblings are matched by each session's PERSISTED launch
//     locator, not agent name or current config: a custom alias and the built-in
//     it wraps write to the same rollout store, and comparing the launch-time
//     value means a later reload that changed/removed an alias's locator can't
//     make us miss an older-generation sibling (issue #1236). A session with an
//     empty persisted locator (pre-#1236, unknown) sharing the worktree is
//     treated conservatively as a possible sibling so its rollout is never
//     cross-assigned. A sibling that stopped before `since` is excluded — its
//     rollout predates our window and the scrape's mtime filter already skips it.
//
// This closes the staggered-write race in #844 including the variant where the
// sibling exits between the scrape and this check. Callers must hold sm.mu.
func (sm *SessionManager) wouldCrossAssign(id, locator, worktreePath, sid string, since time.Time) bool {
	want := canonWorktree(worktreePath)

	for other, s := range sm.state.Sessions {
		if other == id {
			continue
		}

		if sid != "" && s.AgentSessionID == sid {
			return true
		}

		if canonWorktree(s.WorktreePath) != want {
			continue
		}

		// Skip only a sibling with a KNOWN, different locator; an empty (unknown)
		// persisted locator is treated conservatively as a possible same-store
		// sibling.
		if s.NativeIDLocator != locator && s.NativeIDLocator != "" {
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

// transcriptKind returns the on-disk transcript/id kind for a session: its
// persisted native-id locator when set, else a conservative fallback to the agent
// name for pre-#1236 sessions that predate the locator field. A custom
// alias/wrapper that declares locator "claude"/"codex" thus reads, confirms, and
// token-counts against the right on-disk format instead of failing an
// agent-name-keyed transcript lookup (issue #1236).
func transcriptKind(locator, agent string) string {
	if locator != "" {
		return locator
	}

	return agent
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
func scrapeSessionID(locator, worktreePath, stateRoot string, since time.Time) (string, bool) {
	switch locator {
	case config.NativeIDLocatorCodex:
		return transcript.CodexSessionIDSince(stateRoot, worktreePath, since)
	default:
		return "", false
	}
}
