package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/detector"
	grpty "github.com/d0ugal/graith/internal/pty"
)

// maxStuckRestarts caps how many consecutive startup-watchdog restarts a single
// session may receive before it is marked errored instead of being restarted
// again. This prevents a restart storm for a session that is fundamentally
// broken (as opposed to merely losing a launch race). The counter resets to 0
// once the session produces output (see resetStuckRestartsLocked).
const maxStuckRestarts = 3

// watchdogInterval is how often the startup watchdog scans for stuck sessions.
// It is a var so tests can shrink it.
var watchdogInterval = 15 * time.Second

// launchSlotPollInterval is how often releaseLaunchSlotWhenSettled polls a
// freshly-spawned session for its first output. It is a var so tests can shrink
// it.
var launchSlotPollInterval = 100 * time.Millisecond

// launchThrottle bounds how many agent spawns may be in their startup window at
// once (#1092). A slot is acquired just before the PTY spawn and held across the
// heavyweight agent-init window — released only when the session produces its
// first output or a settle timeout elapses — so a burst of launches starts in a
// bounded, staggered fashion instead of stampeding.
//
// The capacity can change on config reload: resize swaps in a fresh channel.
// A slot acquired against the old channel captures that channel in its release
// closure, so it releases harmlessly against it even after a resize.
type launchThrottle struct {
	mu      sync.Mutex
	sem     chan struct{}
	waiting int // launches currently blocked in acquire (for log context)
}

func newLaunchThrottle(maxConcurrent int) *launchThrottle {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}

	return &launchThrottle{sem: make(chan struct{}, maxConcurrent)}
}

// resize changes the concurrency limit. A no-op when the capacity is unchanged.
func (lt *launchThrottle) resize(maxConcurrent int) {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}

	lt.mu.Lock()
	defer lt.mu.Unlock()

	if cap(lt.sem) == maxConcurrent {
		return
	}

	lt.sem = make(chan struct{}, maxConcurrent)
}

// launchSlot is a held throttle slot. Callers must eventually call release
// exactly once (it is idempotent). The remaining fields capture state at acquire
// time for logging: inflight/capacity are the slots held/total, queued is how
// many launches were waiting for a slot (including this one) at the moment it
// blocked, and waited is how long the acquire blocked.
type launchSlot struct {
	release  func()
	inflight int
	capacity int
	queued   int
	waited   time.Duration
}

// acquire blocks until a slot is free (or ctx is cancelled), returning a
// launchSlot whose release frees it. The returned release is bound to the
// channel that was current at acquire time, so a concurrent resize is safe.
func (lt *launchThrottle) acquire(ctx context.Context) (launchSlot, error) {
	lt.mu.Lock()
	sem := lt.sem
	lt.waiting++
	queued := lt.waiting // how many are waiting for a slot right now (incl. us)
	lt.mu.Unlock()

	start := time.Now()

	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		lt.mu.Lock()
		lt.waiting--
		lt.mu.Unlock()

		return launchSlot{}, ctx.Err()
	}

	waited := time.Since(start)

	lt.mu.Lock()
	lt.waiting--
	inflight := len(sem)
	capacity := cap(sem)
	lt.mu.Unlock()

	var once sync.Once

	return launchSlot{
		release: func() {
			once.Do(func() { <-sem })
		},
		inflight: inflight,
		capacity: capacity,
		queued:   queued,
		waited:   waited,
	}, nil
}

// acquireLaunchSlot blocks until a launch slot is free, logging the queue
// position and concurrency so bursts are diagnosable from logs alone. The
// caller must arrange for the slot to be released: on spawn failure call
// slot.release() directly; on success hand it to releaseLaunchSlotWhenSettled.
func (sm *SessionManager) acquireLaunchSlot(ctx context.Context, id, name string) (launchSlot, error) {
	// A manager constructed without NewSessionManager (some narrow unit tests)
	// has no throttle; treat that as unbounded rather than panicking.
	if sm.launch == nil {
		return launchSlot{release: func() {}}, nil
	}

	slot, err := sm.launch.acquire(ctx)
	if err != nil {
		return launchSlot{}, err
	}

	sm.log.Info("launch slot acquired",
		"id", id, "name", name,
		"inflight", slot.inflight, "capacity", slot.capacity,
		"queued", slot.queued, "waited_ms", slot.waited.Milliseconds())

	return slot, nil
}

// releaseLaunchSlotWhenSettled holds the throttle slot across the session's
// startup window, releasing it once the session produces its first output or the
// configured settle timeout elapses — whichever comes first. It runs in the
// background so Create/Resume return promptly. The time-to-first-output is
// logged so slow startups are visible.
func (sm *SessionManager) releaseLaunchSlotWhenSettled(slot launchSlot, id, name string, sess *grpty.Session) {
	settle := sm.Config().Launch.SettleTimeoutDuration()
	if settle <= 0 {
		slot.release()
		return
	}

	// Read the poll interval here, in the caller's goroutine, so a test that
	// swaps the global (and restores it via cleanup) never races the reader.
	poll := launchSlotPollInterval

	// Deliberately NOT tracked by sm.watchers: this goroutine only releases a
	// throttle slot and logs — no post-exit state work — and it always
	// self-terminates (first output / Exited / Done / settle deadline). Keeping
	// it out of the shutdown WaitGroup avoids an Add-during-Wait race with
	// StopAll and prevents a large settle_timeout from extending shutdown.
	go func() {
		defer slot.release()

		start := time.Now()

		ticker := time.NewTicker(poll)
		defer ticker.Stop()

		deadline := time.After(settle)

		for {
			if !sess.LastOutputAt().IsZero() {
				sm.log.Info("launch settled: first output",
					"id", id, "name", name,
					"startup_ms", time.Since(start).Milliseconds())

				return
			}

			if sess.Exited() {
				sm.log.Info("launch settled: session exited before first output",
					"id", id, "name", name,
					"startup_ms", time.Since(start).Milliseconds())

				return
			}

			select {
			case <-ticker.C:
			case <-deadline:
				sm.log.Info("launch slot released after settle timeout without output",
					"id", id, "name", name,
					"settle", settle.String())

				return
			case <-sess.Done():
				return
			}
		}
	}()
}

// RunStartupWatchdogLoop periodically scans for sessions that are stuck in
// startup — running, but never having produced output, sitting at agent_status
// "unknown" past the configured startup_timeout — and restarts them fresh
// (#1092). The timeout is re-read each tick so config reloads take effect.
func (sm *SessionManager) RunStartupWatchdogLoop(ctx context.Context) {
	ticker := time.NewTicker(watchdogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sm.checkStuckLaunches(ctx)
		}
	}
}

// stuckSession is a snapshot of a session the watchdog has decided to recover.
type stuckSession struct {
	id       string
	name     string
	age      time.Duration
	peakRSS  int64
	status   string
	pid      int
	attempts int
	pty      *grpty.Session
}

// checkStuckLaunches finds and recovers sessions stuck in startup.
func (sm *SessionManager) checkStuckLaunches(ctx context.Context) {
	timeout := sm.Config().Launch.StartupTimeoutDuration()
	if timeout <= 0 {
		return // watchdog disabled
	}

	stuck := sm.stuckLaunchCandidates(time.Now(), timeout)

	for _, st := range stuck {
		if ctx.Err() != nil {
			return
		}

		//nolint:contextcheck // the restart deliberately detaches from the tick ctx: a recovered session must outlive this watchdog pass, so Restart/Resume use their own bounded background timeouts (mirrors the orchestrator supervisor).
		sm.recoverStuckLaunch(st, timeout)
	}
}

// stuckLaunchCandidates snapshots the sessions the watchdog considers stuck in
// startup: running, never having produced output, at agent_status "unknown"/"",
// with a live non-exited PTY, and older than timeout. Split out from
// checkStuckLaunches so the selection logic is unit-testable.
func (sm *SessionManager) stuckLaunchCandidates(now time.Time, timeout time.Duration) []stuckSession {
	var stuck []stuckSession

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for id, s := range sm.state.Sessions {
		if s.Status != StatusRunning || s.IsSoftDeleted() {
			continue
		}

		// The orchestrator has its own supervisor (orchestrator.go) that manages
		// fresh-start restarts; don't double-manage it here.
		if s.SystemKind == SystemKindOrchestrator {
			continue
		}

		if s.AgentStatus != "" && s.AgentStatus != string(detector.StatusUnknown) {
			continue
		}

		// StatusChangedAt is the age reference. For a genuinely stuck launch the
		// agent status settles at "unknown" and stops changing, so this measures
		// from roughly the first detection tick — well within a minutes-scale
		// timeout.
		if now.Sub(s.StatusChangedAt) < timeout {
			continue
		}

		ptySess, ok := sm.sessions[id]
		if !ok || ptySess.Exited() {
			continue // no live process to babysit; the watcher will handle exit
		}

		// The LIVE PTY is the source of truth for "this launch produced output".
		// The persisted SessionState.LastOutputAt is deliberately NOT used: it
		// survives across resumes, so a session that emitted output in an earlier
		// process life but is now stuck on a fresh silent PTY must still be caught
		// (#1092 — the watchdog's own recovery path resumes, so this matters).
		if !ptySess.LastOutputAt().IsZero() {
			continue
		}

		stuck = append(stuck, stuckSession{
			id:       id,
			name:     s.Name,
			age:      now.Sub(s.StatusChangedAt),
			peakRSS:  ptySess.PeakRSSBytes(),
			status:   s.AgentStatus,
			pid:      s.PID,
			attempts: s.StuckRestarts,
			pty:      ptySess,
		})
	}

	return stuck
}

// recoverStuckLaunch kills a stuck session and restarts it fresh, or marks it
// errored (and kills it) if it has already exhausted its restart budget.
//
// The candidate was snapshotted under a prior RLock and this runs under a later
// Lock, so it re-validates the full stuck predicate — including PTY-pointer
// identity — before acting. Between snapshot and action the session may have
// produced output, been stopped by the user, or been restarted onto a different
// (healthy) PTY; without the recheck the watchdog could kill a now-healthy
// process.
func (sm *SessionManager) recoverStuckLaunch(st stuckSession, timeout time.Duration) {
	logCtx := []any{
		"id", st.id,
		"name", st.name,
		"age", st.age.Round(time.Second).String(),
		"peak_rss_mb", st.peakRSS / (1024 * 1024),
		"agent_status", st.status,
		"pid", st.pid,
		"attempt", st.attempts + 1,
		"startup_timeout", timeout.String(),
	}

	// Claim the recovery atomically: re-check the predicate under the lock and
	// commit the state mutation in the same critical section. If it no longer
	// holds, the session recovered or changed on its own — leave it alone.
	sm.mu.Lock()

	s, ok := sm.state.Sessions[st.id]
	if !ok || s.Status != StatusRunning || sm.sessions[st.id] != st.pty ||
		st.pty.Exited() || !st.pty.LastOutputAt().IsZero() ||
		(s.AgentStatus != "" && s.AgentStatus != string(detector.StatusUnknown)) {
		sm.mu.Unlock()
		sm.log.Info("startup watchdog: stuck session recovered or changed before action, skipping", "id", st.id)

		return
	}

	giveUp := s.StuckRestarts >= maxStuckRestarts
	if giveUp {
		s.StopReason = StopReasonWatchdog
		s.Status = StatusErrored
		s.StatusChangedAt = time.Now()
		applyLifecycleSummaryLocked(s, "Stuck on launch and exceeded watchdog restart budget")
	} else {
		// Mark for a fresh start so a forced-id agent (Claude) uses --session-id
		// rather than --resume against a conversation that was never persisted —
		// dovetailing with the resume-fallback fix (#1091). No StopReason is set:
		// Restart sets its own, and a stale StopReasonWatchdog here would just be
		// overwritten.
		s.FreshStart = true
		s.StuckRestarts++
	}

	_ = sm.saveState()
	sm.mu.Unlock()

	if giveUp {
		sm.log.Warn("startup watchdog giving up on stuck session (restart budget exhausted)", logCtx...)

		// Kill the zombie so it doesn't linger. StopReasonWatchdog is already set,
		// so watchSession preserves the errored status and summary above.
		if err := st.pty.Kill(); err != nil {
			sm.log.Error("failed to kill stuck session after giving up", "id", st.id, "err", err)
		}

		return
	}

	sm.log.Warn("startup watchdog restarting stuck session", logCtx...)

	// Restart kills the live PTY, waits for exit, then resumes. Use a small
	// default geometry; the client resizes on attach.
	if err := sm.doRestartStuck(st.id, 24, 80); err != nil {
		sm.log.Error("startup watchdog failed to restart stuck session", "id", st.id, "err", err)
		return
	}

	sm.log.Info("startup watchdog restarted stuck session", "id", st.id, "name", st.name)
}

// doRestartStuck runs the watchdog's recovery action: the injected restartStuck
// seam in tests, or Restart in production.
func (sm *SessionManager) doRestartStuck(id string, rows, cols uint16) error {
	if sm.restartStuck != nil {
		return sm.restartStuck(id, rows, cols)
	}

	_, err := sm.Restart(id, rows, cols)

	return err
}

// resetStuckRestartsLocked clears a session's watchdog restart counter once it
// has produced output, so the cap only bounds *consecutive* stuck restarts.
// Caller must hold sm.mu.
func resetStuckRestartsLocked(s *SessionState) {
	if s.StuckRestarts != 0 {
		s.StuckRestarts = 0
	}
}
