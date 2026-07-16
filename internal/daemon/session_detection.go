package daemon

import (
	"context"
	"time"

	"github.com/d0ugal/graith/internal/detector"
	"github.com/d0ugal/graith/internal/git"
)

// RunDetectionLoop periodically scans PTY scrollback to detect low-risk agent
// status (active/ready) for all running sessions. Approval status comes from
// hooks or the daemon approval queue, not PTY text.
func (sm *SessionManager) RunDetectionLoop(ctx context.Context) {
	ticker := sm.loopTicker(500 * time.Millisecond)
	// The detection tick is far too frequent to fetch on (network I/O). A
	// slower fetch keeps origin/<base> reasonably fresh so the fallback
	// diverged-from-base count doesn't go stale after remote merges (#197).
	fetchTicker := sm.loopTicker(fetchInterval)
	runDetectionLoop(ctx, ticker, fetchTicker, sm.detectAgentStatuses, sm.fetchRemotes)
}

func runDetectionLoop(
	ctx context.Context,
	detectionTicker, fetchTicker loopTicker,
	detect func(),
	fetch func(context.Context),
) {
	defer detectionTicker.Stop()
	defer fetchTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-detectionTicker.C():
			detect()
		case <-fetchTicker.C():
			fetch(ctx)
		}
	}
}

// fetchInterval is how often the detection loop refreshes remote tracking refs.
const fetchInterval = 5 * time.Minute

// fetchPerRepoTimeout bounds a single `git fetch` so a slow or hung remote
// can't stall the fetch pass for other sessions.
const fetchPerRepoTimeout = 30 * time.Second

// fetchRemotes runs a best-effort `git fetch` for every running session's
// worktree (and included repos) so remote tracking refs stay fresh. Failures
// are logged at debug level and otherwise ignored — a session may be offline
// or have no remote.
func (sm *SessionManager) fetchRemotes(ctx context.Context) {
	sm.mu.RLock()

	seen := make(map[string]struct{})

	var dirs []string

	for _, s := range sm.state.Sessions {
		if s.Status != StatusRunning || s.Mirror {
			continue
		}

		if s.WorktreePath != "" {
			if _, ok := seen[s.WorktreePath]; !ok {
				seen[s.WorktreePath] = struct{}{}
				dirs = append(dirs, s.WorktreePath)
			}
		}

		for i := range s.Includes {
			wp := s.Includes[i].WorktreePath
			if wp == "" {
				continue
			}

			if _, ok := seen[wp]; !ok {
				seen[wp] = struct{}{}
				dirs = append(dirs, wp)
			}
		}
	}

	sm.mu.RUnlock()

	for _, dir := range dirs {
		if ctx.Err() != nil {
			return
		}

		if !git.HasRemote(dir, "origin") {
			continue
		}

		fetchCtx, cancel := context.WithTimeout(ctx, fetchPerRepoTimeout)
		if err := git.FetchRemote(fetchCtx, dir); err != nil {
			sm.log.Debug("periodic fetch failed", "dir", dir, "error", err)
		}

		cancel()
	}
}

// silentSessionThreshold is how long a running session's PTY may produce zero
// output before the daemon flags it as silent. Interactive agents (Claude,
// Codex, …) render their UI within a second or two of starting, so a session
// still at zero bytes well past this window is almost certainly stuck — blocked
// on a pre-render prompt, or otherwise not writing to its PTY (issue #1087).
const silentSessionThreshold = 20 * time.Second

// checkSilentSession warns once per PTY lifetime when a running session has
// produced no PTY output past silentSessionThreshold. This is the signal that
// was missing when #1087 (blank screen after restart) was diagnosed: the agent
// process was alive and writing its transcript, but nothing reached the PTY, so
// scrollback stayed empty and attach showed nothing — with no trace in the log.
func (sm *SessionManager) checkSilentSession(id, name, agent string, pty SessionDriver) {
	sm.checkSilentSessionWithThreshold(id, name, agent, pty, silentSessionThreshold)
}

// checkSilentSessionWithThreshold is checkSilentSession with an injectable
// threshold so tests don't have to wait out the production window.
func (sm *SessionManager) checkSilentSessionWithThreshold(id, name, agent string, pty SessionDriver, threshold time.Duration) {
	if pty.BytesRead() > 0 {
		return
	}

	// An adopted PTY (daemon upgrade re-attaching to a surviving agent) starts
	// at zero bytes even though the agent likely rendered before the upgrade, so
	// zero-output can't be read as "never rendered" — exempt it outright rather
	// than emit a false diagnosis for every idle adopted session (issue #1087).
	if pty.WasAdopted() {
		return
	}

	created := pty.CreatedAt()
	if created.IsZero() || time.Since(created) < threshold {
		return
	}

	// Mark-and-warn under the lock, and only for the PTY still installed for
	// this id. A concurrent restart may have swapped in a new PTY and cleared
	// silentWarned between the snapshot and here; without the identity check a
	// stale snapshot of the old PTY could warn spuriously and consume the new
	// PTY's once-per-lifetime slot.
	sm.mu.Lock()
	if sm.sessions[id] != pty {
		sm.mu.Unlock()
		return
	}

	warned := sm.silentWarned[id]
	sm.silentWarned[id] = true
	sm.mu.Unlock()

	if warned {
		return
	}

	sm.log.Warn("session running but producing no PTY output",
		"session_id", id, "name", name, "agent", agent,
		"running_for", time.Since(created).Round(time.Second),
		"scrollback_path", sm.scrollbackLogPath(id),
		"hint", "agent is alive but has rendered nothing — likely blocked on a pre-render prompt or not writing to the PTY (issue #1087)")
}

func (sm *SessionManager) detectAgentStatuses() {
	sm.mu.RLock()

	type target struct {
		id           string
		name         string
		agent        string
		prevStatus   string
		pty          SessionDriver
		worktreePath string
		baseBranch   string
		repoPath     string
		includes     []IncludedRepoState
		mirror       bool
	}

	var targets []target

	for id, s := range sm.state.Sessions {
		if s.Status != StatusRunning {
			continue
		}

		if ptySess, ok := sm.sessions[id]; ok {
			inc := make([]IncludedRepoState, len(s.Includes))
			copy(inc, s.Includes)
			targets = append(targets, target{
				id: id, name: s.Name, agent: s.Agent, prevStatus: s.AgentStatus, pty: ptySess,
				worktreePath: s.WorktreePath, baseBranch: s.BaseBranch, repoPath: s.RepoPath,
				includes: inc, mirror: s.Mirror,
			})
		}
	}

	sm.mu.RUnlock()

	var toAutoStop []string

	for _, t := range targets {
		sm.checkSilentSession(t.id, t.name, t.agent, t.pty)

		var status string

		// Check if we have an authoritative hook report for this session
		sm.mu.RLock()
		hr, hasHook := sm.hookReports[t.id]
		sm.mu.RUnlock()

		if hasHook && time.Now().Before(hr.AuthoritativeUntil) {
			status = hr.Status
		} else {
			// Fall back to PTY scraping
			content := t.pty.ScreenPreview()
			if content == "" {
				continue
			}

			outputAge := detector.OutputAgeUnknown
			if lastOut := t.pty.LastOutputAt(); !lastOut.IsZero() {
				outputAge = time.Since(lastOut)
			}

			d := detector.New(t.agent)
			status = string(d.Detect(content, outputAge))

			if status == string(detector.StatusUnknown) && t.prevStatus != "" && t.pty.RecentlyAdopted(60*time.Second) {
				status = t.prevStatus
			}
		}

		var (
			dirty    bool
			unpushed int
		)

		if !t.mirror {
			if t.worktreePath != "" && t.repoPath != "" {
				if d, err := git.HasUncommittedChanges(t.worktreePath); err == nil {
					dirty = d
				}

				if t.baseBranch != "" {
					if n, err := git.UnpushedCommitCount(t.worktreePath, t.baseBranch); err == nil {
						unpushed = n
					}
				}
			}

			for i := range t.includes {
				inc := &t.includes[i]
				if d, err := git.HasUncommittedChanges(inc.WorktreePath); err == nil {
					inc.dirty = d
					dirty = dirty || d
				}

				if inc.BaseBranch != "" {
					if n, err := git.UnpushedCommitCount(inc.WorktreePath, inc.BaseBranch); err == nil {
						inc.unpushed = n
						unpushed += n
					}
				}
			}
		}

		if status != t.prevStatus {
			sm.onAgentStatusChange(t.id, t.name, t.prevStatus, status)
		}

		sm.mu.Lock()
		if s, ok := sm.state.Sessions[t.id]; ok {
			if status != s.AgentStatus {
				s.StatusChangedAt = time.Now()
			}

			s.AgentStatus = status
			s.GitDirty = dirty

			s.GitUnpushed = unpushed
			if lastOut := t.pty.LastOutputAt(); !lastOut.IsZero() {
				s.LastOutputAt = &lastOut
				// The session is producing output, so it launched successfully:
				// clear any startup-watchdog restart count so the cap only bounds
				// *consecutive* stuck restarts (#1092).
				resetStuckRestartsLocked(s)
			}

			for i := range s.Includes {
				if i < len(t.includes) {
					s.Includes[i].dirty = t.includes[i].dirty
					s.Includes[i].unpushed = t.includes[i].unpushed
				}
			}

			if sm.checkIdleSession(s) {
				toAutoStop = append(toAutoStop, t.id)
			}
		}
		sm.mu.Unlock()
	}

	for _, id := range toAutoStop {
		sm.mu.RLock()

		s, ok := sm.state.Sessions[id]
		if !ok {
			sm.mu.RUnlock()
			continue
		}

		_, hasClient := sm.attachedClients[id]
		stillIdle := !hasClient && s.AgentStatus == "ready"
		name := s.Name
		idleSince := s.IdleSince

		sm.mu.RUnlock()

		if !stillIdle {
			continue
		}

		var idleDur time.Duration
		if idleSince != nil {
			idleDur = time.Since(*idleSince)
		}

		sm.log.Info("auto-stopping idle session", "session", name, "id", id, "idle_duration", idleDur.Round(time.Second))

		if err := sm.stopWithReason(id, StopReasonIdle, "idle-loop"); err != nil {
			sm.log.Error("failed to auto-stop session", "id", id, "err", err)
		}
	}
}

// checkIdleSession updates the idle tracking for a session and returns true if it
// should be auto-stopped. Caller must hold sm.mu.
func (sm *SessionManager) checkIdleSession(s *SessionState) bool {
	_, hasClient := sm.attachedClients[s.ID]
	isIdle := !hasClient && s.AgentStatus == "ready"

	if isIdle {
		if s.IdleSince == nil {
			now := time.Now()
			s.IdleSince = &now
		}
	} else {
		s.IdleSince = nil
	}

	if s.IdleSince != nil {
		var timeout time.Duration

		switch {
		case s.IdleTimeoutSecs > 0:
			// Per-session override (trigger-spawned auto_cleanup / explicit
			// idle_timeout) takes precedence over the agent default.
			timeout = time.Duration(s.IdleTimeoutSecs) * time.Second
		case s.SystemKind == SystemKindOrchestrator:
			timeout = sm.cfg.Orchestrator.IdleTimeoutDuration()
		default:
			agentCfg := sm.cfg.Agents[s.Agent]
			timeout = agentCfg.IdleTimeoutDuration()
		}

		if timeout > 0 && time.Since(*s.IdleSince) >= timeout {
			return true
		}
	}

	return false
}
