package daemon

import (
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/agent/transcript"
	"github.com/d0ugal/graith/internal/config"
)

// startWatcher launches watchSession in a tracked goroutine. The watcher is
// registered with sm.watchers so StopAll can wait for its post-exit work
// (state writes, status publish) to finish during shutdown.
func (sm *SessionManager) startWatcher(id string, sess SessionDriver) {
	sm.watchers.Add(1)
	// Ask the monitor for a baseline promptly; the buffered kick coalesces a
	// burst of launches into one process-table scan.
	select {
	case sm.resourceKick <- struct{}{}:
	default:
	}

	go func() {
		defer sm.watchers.Done()

		sm.watchSession(id, sess)
	}()
}

// watchSession waits for a PTY session to exit and updates state accordingly.
// If the session has been replaced (e.g. by Resume) or removed (e.g. by Delete),
// the watcher is stale and skips the state update and status event.
func (sm *SessionManager) watchSession(id string, sess SessionDriver) {
	<-sess.Done()

	sm.mu.Lock()
	stale := sm.sessions[id] != sess
	stateAtExit, stateExists := sm.state.Sessions[id]
	stateDeleted := !stateExists || stateAtExit.IsSoftDeleted()

	var (
		name           string
		deleted        bool
		isOrchestrator bool
		stopReason     string
		sandboxed      bool
	)

	if !stale {
		if s, ok := sm.state.Sessions[id]; ok {
			name = s.Name
			isOrchestrator = s.SystemKind == SystemKindOrchestrator

			prevSummary, prevSetAt := sm.prevStopSummaryLocked(s, id)

			prevTTL := sm.cfg.Status.TTLDuration()
			if s.SummaryTTL > 0 {
				prevTTL = time.Duration(s.SummaryTTL) * time.Second
			}

			// A watchdog give-up (StopReasonWatchdog) has already set StatusErrored
			// and a descriptive summary before killing the PTY; preserve both so
			// budget exhaustion stays distinguishable from an ordinary stop/crash.
			watchdogGaveUp := s.StopReason == StopReasonWatchdog

			exitCode := sess.ExitCode()

			if !watchdogGaveUp {
				s.Status = StatusStopped
			}

			s.StatusChangedAt = time.Now()
			s.ExitCode = &exitCode
			s.PID = 0

			// Capture the exiting process generation before zeroing it so a
			// pending SessionEnd reason is consumed only for the same process.
			exitingGen := s.PIDStartTime
			s.PIDStartTime = 0

			if s.StopReason == "" {
				// Precedence: an already-set StopReason (e.g. an explicit gr stop)
				// has already skipped this block. Otherwise a process-ending
				// SessionEnd reason bound to THIS generation maps to a StopReason;
				// otherwise fall back to crash (a hard crash never emits SessionEnd,
				// and clear/resume/other leave no clean-exit reason).
				if mapped, ok := mapSessionEndReason(s.SessionEndReason); ok && s.SessionEndReasonGen == exitingGen {
					s.StopReason = mapped
				} else {
					s.StopReason = StopReasonCrash
				}
			}

			if lastOut := sess.LastOutputAt(); !lastOut.IsZero() {
				s.LastOutputAt = &lastOut
			}

			if sig := sess.ExitSignal(); sig != 0 && s.StopReason == StopReasonCrash {
				s.ExitSignal = sig.String()
			}

			// Capture the finalized reason + sandbox flag under the lock so the
			// "session exited" log below can attribute the stop and label the
			// peak-RSS source without racing a concurrent resume (issue #1104).
			stopReason = s.StopReason
			sandboxed = s.Sandboxed

			if !watchdogGaveUp && (s.StopReason != StopReasonShutdown || s.SummaryText == "") {
				text := formatStopSummary(s.StopReason, s.ExitCode, s.ExitSignal, prevSummary, prevSetAt, prevTTL)
				applyLifecycleSummaryLocked(s, text)
			}

			if err := sm.saveState(); err != nil {
				sm.log.Error("failed to save state after session exit", "id", id, "err", err)
			}

			if s.StopReason == StopReasonCrash {
				sm.recordExit()
			}

			delete(sm.sessions, id)
		} else {
			deleted = true

			delete(sm.sessions, id)
		}
	}
	sm.mu.Unlock()

	// Always close the exited PTY's handles. The watcher owns the sess pointer
	// it was passed, regardless of whether the map entry was replaced (stale)
	// or removed (deleted). Double-close is safe: os.File.Close returns
	// ErrClosed (ignored) and readDone is a closed channel (instant receive).
	sess.Close()
	signalRequest := sm.takeSignalRequest(id, sess.ProcessPID())

	if stale {
		// A hard delete removes both state and the live-driver map entry before
		// this watcher observes exit. Discard its bounded resource history; a
		// stale watcher for a replacement generation must leave the replacement's
		// samples intact.
		if stateDeleted {
			sm.discardResourceSamples(id)
		}

		sm.log.Info("ignoring stale session exit", "id", id, "exit_code", sess.ExitCode())

		return
	}

	if deleted {
		sm.discardResourceSamples(id)
		sm.log.Info("ignoring exit for deleted session", "id", id, "exit_code", sess.ExitCode())
		sm.reopenTodosForSession(id)

		return
	}

	// A stopped/crashed owner must not strand its claimed todo items: reopen them
	// so a sibling can pick the work up (issue #591).
	sm.reopenTodosForSession(id)

	if stopReason == "" {
		stopReason = StopReasonCrash
	}

	logAttrs := []any{
		"id", id,
		"name", name,
		"stop_reason", stopReason,
		"exit_code", sess.ExitCode(),
		"pid", sess.ProcessPID(),
		"pgid", sess.Pgid(),
	}
	if sig := sess.ExitSignal(); sig != 0 {
		logAttrs = append(logAttrs, "signal", sig.String())
	}

	category, signalSource := classifyExit(sess.ExitCode(), sess.ExitSignal(), signalRequest)

	logAttrs = append(logAttrs, "exit_category", category, "signal_source", signalSource)
	if signalRequest != nil {
		logAttrs = append(logAttrs,
			"signal_request", signalRequest.Signal.String(),
			"signal_request_initiator", signalRequest.Initiator,
			"signal_request_at", signalRequest.At)
	}

	if rss := sess.PeakRSSBytes(); rss > 0 && sess.ExitCode() != 0 {
		// peak_rss_mb comes from the waited child's rusage, which is the direct
		// child graith spawned — the sandbox wrapper when sandboxed, not the
		// agent underneath it. Labelling the source (issue #1104) stops a
		// single-digit wrapper RSS from being read as the agent's footprint.
		logAttrs = append(logAttrs, "peak_rss_mb", rss/(1024*1024), "peak_rss_proc", peakRSSProcLabel(sandboxed))
	}

	sm.log.Info("session exited", logAttrs...)
	sm.logAbnormalExitReport(id, name, stopReason, sess, signalRequest)

	sm.onAgentStatusChange(id, name, "running", "stopped")

	if isOrchestrator {
		sm.notifyOrchestratorExit(id)
	}

	// A trigger-spawned session with auto_cleanup configured is soft-deleted now
	// that it has stopped, so finished briefing/report sessions don't accumulate.
	sm.autoCleanupStopped(id)
}

const (
	massExitWindow    = 2 * time.Second
	massExitThreshold = 5
)

// recordExit tracks session exit times and logs a warning when many sessions
// exit within a short window, which typically indicates an external signal
// (e.g. macOS jetsam/memory pressure killing processes). Caller must hold sm.mu.
func (sm *SessionManager) recordExit() {
	now := time.Now()
	cutoff := now.Add(-massExitWindow)

	// Prune old entries.
	start := 0
	for start < len(sm.recentExits) && sm.recentExits[start].Before(cutoff) {
		start++
	}

	sm.recentExits = append(sm.recentExits[start:], now)

	if len(sm.recentExits) == massExitThreshold {
		sm.log.Warn("mass session exit detected: likely external signal (e.g. OOM killer, jetsam)",
			"count", len(sm.recentExits),
			"window", massExitWindow.String())
	}
}

// argsNeedAgentID reports whether any arg still contains the raw
// {agent_session_id} template token (checked before ExpandSlice runs).
func argsNeedAgentID(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, "{agent_session_id}") {
			return true
		}
	}

	return false
}

// argsNeedForkSourceID reports whether any arg contains the raw
// {fork_source_agent_session_id} template token (checked before ExpandSlice).
func argsNeedForkSourceID(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, "{fork_source_agent_session_id}") {
			return true
		}
	}

	return false
}

// resolveResumeArgs picks the args for resuming a session and applies the
// empty-id guard. resume_args are used unless absent or FreshStart, in which
// case agent.Args (a fresh start) is used. When the chosen args template
// {agent_session_id} but no native id was captured, expanding would emit a
// literal empty arg (e.g. `opencode --session ""`), which misbehaves — so Codex
// falls back to its cwd-scoped `resume --last` and other agents start fresh.
// The check inspects the RAW args before expansion (the token is gone after
// ExpandSlice). Returns the args plus an optional log note when a fallback fired.
func resolveResumeArgs(agent config.Agent, sessAgent, sessAgentSessionID string, freshStart bool) ([]string, string) {
	resumeArgs := agent.ResumeArgs
	if len(resumeArgs) == 0 || freshStart {
		resumeArgs = agent.Args
	}

	if !freshStart && sessAgentSessionID == "" && argsNeedAgentID(resumeArgs) {
		if sessAgent == "codex" {
			return []string{"resume", "--last"}, "no native id captured; codex resuming --last"
		}
		// Fresh start. Guard against agent.Args *also* templating the id (e.g. a
		// future forced agent whose force was gated off) — returning it would
		// re-introduce the empty `--session ""` arg. Drop to no args in that case.
		if argsNeedAgentID(agent.Args) {
			return nil, "no native id captured; starting fresh (dropped id-templated args)"
		}

		return agent.Args, "no native id captured; starting fresh"
	}

	return resumeArgs, ""
}

// forcedIDConversationExists reports whether a forced-id agent (Claude) has a
// non-empty on-disk transcript for the given session id. Resuming such an agent
// uses `--resume <id>`, which fails hard ("No conversation found with session
// ID") when no conversation was ever persisted for that id — e.g. the first
// launch was killed before (or mid-) writing the transcript. A zero-byte
// transcript is that same wedge (the file was created but nothing written), so
// it counts as "no conversation". Any locate error (including no match) is
// likewise treated as "no conversation".
//
// Note: the locator resolves Claude's config root from the daemon's own
// environment (CLAUDE_CONFIG_DIR / ~/.claude), matching the token-accounting
// loop (pollTokens). A per-agent `[agents.claude.env] CLAUDE_CONFIG_DIR`
// override is not threaded through here yet — that's a shared limitation of the
// transcript subsystem, tracked separately.
func forcedIDConversationExists(agent, agentSessionID, worktreePath string) bool {
	sources, err := transcript.Locate(agent, agentSessionID, worktreePath)
	if err != nil {
		return false
	}

	for _, s := range sources {
		if s.Size > 0 {
			return true
		}
	}

	return false
}

// resolveForcedIDResume decides how to resume a forced-id agent (Claude) based
// solely on whether a conversation exists on disk for its session id — the
// stored freshStart flag is deliberately NOT trusted as the primary signal:
//
//   - conversation exists → native --resume (fresh=false), even if a stale
//     freshStart=true was left behind by a fresh start that crashed between
//     persisting the minted id and clearing the flag. Safe because a forced-id
//     freshStart id is always freshly minted, so a transcript under it means the
//     fresh start already ran far enough to write one.
//   - no conversation, not already fresh → the #1091 wedge: mint a new id and
//     start fresh so `claude --resume <id>` can't fail with "No conversation
//     found". Reported via fellBack so the caller clears the one-shot flag once
//     the start succeeds.
//   - no conversation, already fresh (migration seed / a prior pending fallback)
//     → keep the minted id and the pending fresh start unchanged.
//
// Non-forced agents and empty ids are returned unchanged.
func resolveForcedIDResume(agent, agentSessionID, worktreePath string, freshStart bool, mint func() string) (id string, fresh, fellBack bool) {
	if !forcesID(agent) || agentSessionID == "" {
		return agentSessionID, freshStart, false
	}

	if forcedIDConversationExists(agent, agentSessionID, worktreePath) {
		return agentSessionID, false, false
	}

	if freshStart {
		return agentSessionID, true, false
	}

	return mint(), true, true
}
