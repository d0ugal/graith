package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
	"github.com/fsnotify/fsnotify"
)

// prrefwatch.go accelerates PR detection. The PR/CI watcher (prwatch.go) resolves
// each session's PR by polling gh on a timer, so a freshly pushed branch is only
// noticed on the next tick (up to the base tick plus batch-cap/negative-cache
// latency). This watcher puts a cheap fsnotify watch on each running session's
// git refs and, when a push/commit/checkout touches them, sends the session ID to
// the PR-watch loop's kick channel — which polls that session immediately. The
// GitHub poll stays the always-on fallback, so a degraded or missing watch only
// costs latency, never awareness. See
// docs/design/2026-07-14-pr-ref-watch-design.md.

// The reconcile cadence and per-watch debounce are now [pr_watch.advanced] config
// knobs (ref_reconcile_interval / ref_debounce), resolved through the
// config.PRWatchConfig accessors. The reconcile cadence is read once when the loop
// starts; the debounce is updated in place on live watchers after config reload.

type prRefWatchState struct {
	mu       sync.Mutex
	watchers map[string]*prRefWatcher // session ID -> watcher
}

func newPRRefWatchState() *prRefWatchState {
	return &prRefWatchState{watchers: make(map[string]*prRefWatcher)}
}

// prRefWatcher is one session's git-refs watch: an fsnotify watcher over the
// ref-bearing git directories, a cancel for its event goroutine, and a per-watch
// debounce timer.
type prRefWatcher struct {
	sessionID string
	worktree  string
	watcher   *fsnotify.Watcher
	cancel    context.CancelFunc
	// debounceDur is the current pr_watch.advanced.ref_debounce policy. A
	// non-positive value (e.g. a bare test-constructed watcher) falls back to the
	// config default in notePRRefChange.
	debounceDur time.Duration

	bmu         sync.Mutex
	debounce    *time.Timer
	lastChange  time.Time
	debounceGen uint64
	canceled    bool
}

// RunPRRefWatchLoop reconciles per-session git-refs watchers against live
// sessions each tick. Started from RunPRWatchLoop and sharing its lifecycle + gh
// gate; the loop's own poll is the fallback if this degrades.
func (sm *SessionManager) RunPRRefWatchLoop(ctx context.Context) {
	if sm.prRefWatch == nil {
		return // accelerator not wired (e.g. a bare test SessionManager)
	}

	// Reconcile cadence is read once at loop start; changing ref_reconcile_interval
	// takes effect on the next daemon (re)start.
	ticker := time.NewTicker(sm.Config().PRWatch.RefReconcileIntervalDuration())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			sm.teardownAllPRRefWatchers()
			return
		case <-ticker.C:
			if !sm.Config().PRWatch.Enabled {
				// Feature toggled off at runtime: drop watchers and idle.
				sm.teardownAllPRRefWatchers()
				continue
			}

			sm.reconcilePRRefWatchers(ctx)
		}
	}
}

// reconcilePRRefWatchers creates a watcher for each newly-eligible session and
// tears down watchers whose session is gone/stopped/soft-deleted. Locks are
// released before create/teardown (which lock) to avoid re-entrancy.
func (sm *SessionManager) reconcilePRRefWatchers(ctx context.Context) {
	cfgSnapshot := sm.Config()
	debounce := cfgSnapshot.PRWatch.RefDebounceDuration()
	desired := sm.prRefEligibleSessions()

	sm.prRefWatch.mu.Lock()

	var toRemove []string

	for id := range sm.prRefWatch.watchers {
		if _, ok := desired[id]; !ok {
			toRemove = append(toRemove, id)
		}
	}
	sm.prRefWatch.mu.Unlock()

	for _, id := range toRemove {
		sm.teardownPRRefWatcher(id)
	}

	for id, worktree := range desired {
		sm.prRefWatch.mu.Lock()
		existing, exists := sm.prRefWatch.watchers[id]
		sm.prRefWatch.mu.Unlock()

		if exists {
			sm.updatePRRefWatcherDebounce(cfgSnapshot, existing, debounce)
			continue
		}

		sm.createPRRefWatcherWithConfig(ctx, id, worktree, cfgSnapshot)
	}
}

// prRefEligibleSessions returns running, non-soft-deleted sessions with a
// worktree that the PR watcher would poll (has a repo, not mirror, not in-place).
// Only running sessions are watched: PR association happens while the agent is
// pushing, and the poll fallback covers a stopped session.
func (sm *SessionManager) prRefEligibleSessions() map[string]string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	out := make(map[string]string)

	for id, s := range sm.state.Sessions {
		if s.Status != StatusRunning || s.IsSoftDeleted() {
			continue
		}

		if s.RepoPath == "" || s.WorktreePath == "" || s.Mirror || s.InPlace {
			continue
		}

		out[id] = s.WorktreePath
	}

	return out
}

// createPRRefWatcher resolves a worktree's ref directories, registers fsnotify
// watches, and starts the event goroutine. Fail-open: if the git dirs can't be
// resolved or no watch can be added, no watcher is created and the poll covers
// the session.
func (sm *SessionManager) createPRRefWatcher(ctx context.Context, id, worktree string) {
	sm.createPRRefWatcherWithConfig(ctx, id, worktree, sm.Config())
}

func (sm *SessionManager) createPRRefWatcherWithConfig(ctx context.Context, id, worktree string, cfgSnapshot *config.Config) {
	dirs := gitRefWatchDirs(worktree)
	if len(dirs) == 0 {
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		if sm.log != nil {
			sm.log.Warn("pr-watch: fsnotify unavailable for ref watch", "session", id, "err", err)
		}

		return
	}

	added := 0

	for _, d := range dirs {
		if aerr := watcher.Add(d); aerr == nil {
			added++
		}
	}

	if added == 0 {
		_ = watcher.Close()
		return
	}

	wctx, cancel := context.WithCancel(ctx)
	w := &prRefWatcher{
		sessionID:   id,
		worktree:    worktree,
		watcher:     watcher,
		cancel:      cancel,
		debounceDur: cfgSnapshot.PRWatch.RefDebounceDuration(),
	}

	// Publish only if the config generation used to build the watcher is still
	// current. applyConfig snapshots the watcher map after swapping sm.cfg, so
	// holding sm.mu for this final fast insertion closes the otherwise possible
	// gap where an old-generation watcher could appear after the reload update.
	sm.mu.RLock()
	if sm.cfg != cfgSnapshot {
		sm.mu.RUnlock()
		cancel()
		_ = watcher.Close()
		return
	}

	sm.prRefWatch.mu.Lock()
	// A concurrent reconcile may have created one already; keep the existing.
	if _, exists := sm.prRefWatch.watchers[id]; exists {
		sm.prRefWatch.mu.Unlock()
		sm.mu.RUnlock()
		cancel()

		_ = watcher.Close()

		return
	}

	sm.prRefWatch.watchers[id] = w
	sm.prRefWatch.mu.Unlock()
	sm.mu.RUnlock()

	go sm.runPRRefWatcher(wctx, w)

	if sm.log != nil {
		sm.log.Debug("pr-watch: ref watch started", "session", id, "dirs", added)
	}
}

// gitRefWatchDirs returns the directories to watch for a worktree's ref changes:
// the per-worktree gitdir (HEAD/ORIG_HEAD) and its logs (worktree reflog), plus
// the common dir (packed-refs/HEAD) and its refs + logs subtrees (heads, remotes,
// tags, and reflogs — a push updates the remote-tracking ref + its reflog). The
// object store is deliberately excluded — it is high-churn and irrelevant to PR
// resolution. refs/ and logs/ subtrees are walked so nested branch namespaces
// (e.g. refs/heads/user/feature) are covered; new nested dirs are added on the
// fly in the event handler. Returns nil (fail-open) when the git dirs can't be
// resolved.
func gitRefWatchDirs(worktree string) []string {
	if worktree == "" {
		return nil
	}

	gitDir, err := git.RunOutput(worktree, "rev-parse", "--absolute-git-dir")
	if err != nil || gitDir == "" {
		return nil
	}

	commonDir, err := git.RunOutput(worktree, "rev-parse", "--git-common-dir")
	if err != nil || commonDir == "" {
		commonDir = gitDir
	}

	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktree, commonDir)
	}

	gitDir = filepath.Clean(gitDir)
	commonDir = filepath.Clean(commonDir)

	seen := map[string]bool{}

	var dirs []string

	addDir := func(p string) {
		if seen[p] {
			return
		}

		if info, serr := os.Stat(p); serr == nil && info.IsDir() {
			seen[p] = true
			dirs = append(dirs, p)
		}
	}

	addTree := func(root string) {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, werr error) error {
			if werr != nil {
				return nil //nolint:nilerr // skip unreadable entries, keep walking
			}

			if d.IsDir() {
				addDir(path)
			}

			return nil
		})
	}

	addDir(gitDir)                            // HEAD, ORIG_HEAD (worktree-local)
	addTree(filepath.Join(gitDir, "logs"))    // worktree reflog (commit/checkout)
	addDir(commonDir)                         // packed-refs, HEAD
	addTree(filepath.Join(commonDir, "refs")) // heads/remotes/tags (nested)
	addTree(filepath.Join(commonDir, "logs")) // reflogs incl. push

	return dirs
}

// runPRRefWatcher is the per-watch event loop: any ref/log write, create, rename,
// or remove arms the debounce; on quiet it kicks an immediate poll.
func (sm *SessionManager) runPRRefWatcher(ctx context.Context, w *prRefWatcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			sm.handlePRRefEvent(w, ev)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}

			if sm.log != nil {
				sm.log.Debug("pr-watch: ref watcher error", "session", w.sessionID, "err", err)
			}
		}
	}
}

// handlePRRefEvent filters and debounces one fsnotify event. A new directory
// under a watched tree (e.g. a nested branch namespace) is added to the watch so
// later writes inside it are seen; the create itself already counts as a change.
func (sm *SessionManager) handlePRRefEvent(w *prRefWatcher, ev fsnotify.Event) {
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
		return // Chmod-only noise
	}

	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			_ = w.watcher.Add(ev.Name)
		}
	}

	sm.notePRRefChange(w)
}

// notePRRefChange (re)arms the debounce timer that fires a single kick.
func (sm *SessionManager) notePRRefChange(w *prRefWatcher) {
	w.bmu.Lock()
	defer w.bmu.Unlock()

	if w.canceled {
		return
	}

	dur := w.debounceDur
	if dur <= 0 {
		dur = (config.PRWatchConfig{}).RefDebounceDuration()
	}

	w.lastChange = time.Now()
	sm.armPRRefDebounceLocked(w, dur)
}

// armPRRefDebounceLocked replaces the pending timer without losing its event.
// debounceGen makes a callback whose Stop lost a race harmless, so a live
// policy update cannot produce a duplicate kick. Caller holds w.bmu.
func (sm *SessionManager) armPRRefDebounceLocked(w *prRefWatcher, delay time.Duration) {
	if w.debounce != nil {
		w.debounce.Stop()
	}

	w.debounceGen++
	gen := w.debounceGen

	w.debounce = time.AfterFunc(max(delay, 0), func() {
		// Re-check canceled: the timer may fire after teardown stopped it (Stop
		// returns false once the callback is already scheduled). Mirrors filewatch's
		// watchFire guard — a post-teardown kick is otherwise harmless (pollKicked
		// re-validates) but would needlessly burn the kick cooldown / a gh call.
		w.bmu.Lock()
		canceled := w.canceled || w.debounceGen != gen
		if !canceled {
			w.debounce = nil
		}
		w.bmu.Unlock()

		if canceled {
			return
		}

		sm.kickPRWatch(w.sessionID)
	})
}

// updatePRRefWatcherDebounce retimes an active watcher's pending event against
// its original last-change time. Shortening can fire immediately; lengthening
// preserves the event and extends only its quiet window. cfgSnapshot guards
// against an older reconcile overwriting a newer applyConfig update.
func (sm *SessionManager) updatePRRefWatcherDebounce(cfgSnapshot *config.Config, w *prRefWatcher, dur time.Duration) {
	if w == nil {
		return
	}

	w.bmu.Lock()
	defer w.bmu.Unlock()

	if sm.Config() != cfgSnapshot || w.canceled {
		return
	}

	w.debounceDur = dur
	if w.debounce == nil || w.lastChange.IsZero() {
		return
	}

	sm.armPRRefDebounceLocked(w, dur-time.Since(w.lastChange))
}

// updateActivePRRefWatcherDebounce applies a freshly-published config to every
// existing watcher. Watchers created concurrently either publish before the
// config swap and appear in this snapshot, or fail their generation check.
func (sm *SessionManager) updateActivePRRefWatcherDebounce(cfgSnapshot *config.Config) {
	if sm.prRefWatch == nil {
		return
	}

	sm.prRefWatch.mu.Lock()
	watchers := make([]*prRefWatcher, 0, len(sm.prRefWatch.watchers))
	for _, w := range sm.prRefWatch.watchers {
		watchers = append(watchers, w)
	}
	sm.prRefWatch.mu.Unlock()

	dur := cfgSnapshot.PRWatch.RefDebounceDuration()
	for _, w := range watchers {
		sm.updatePRRefWatcherDebounce(cfgSnapshot, w, dur)
	}
}

// kickPRWatch asks RunPRWatchLoop to poll a session immediately. Non-blocking: a
// full channel drops the kick. A dropped kick clears the session's nextPoll so the
// next tick re-polls it promptly — otherwise a session parked on the multi-minute
// negative cache could stay stranded when its kick is lost under burst/fan-out.
func (sm *SessionManager) kickPRWatch(id string) {
	select {
	case sm.prWatch.kick <- id:
	default:
		sm.prWatch.mu.Lock()
		delete(sm.prWatch.nextPoll, id)
		sm.prWatch.mu.Unlock()

		if sm.log != nil {
			sm.log.Debug("pr-watch: kick channel full, forcing next-tick re-poll", "session", id)
		}
	}
}

func (sm *SessionManager) teardownPRRefWatcher(id string) {
	sm.prRefWatch.mu.Lock()
	w := sm.prRefWatch.watchers[id]
	delete(sm.prRefWatch.watchers, id)
	sm.prRefWatch.mu.Unlock()

	if w == nil {
		return
	}

	stopWatcherResources(w.cancel, w.watcher, &w.bmu, &w.canceled, &w.debounce)
}

func (sm *SessionManager) teardownAllPRRefWatchers() {
	sm.prRefWatch.mu.Lock()

	ids := make([]string, 0, len(sm.prRefWatch.watchers))
	for id := range sm.prRefWatch.watchers {
		ids = append(ids, id)
	}
	sm.prRefWatch.mu.Unlock()

	for _, id := range ids {
		sm.teardownPRRefWatcher(id)
	}
}
