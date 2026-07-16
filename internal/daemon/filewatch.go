package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/ignore"
	"github.com/fsnotify/fsnotify"
)

// watchRetryBackoff returns the delay before the retry-th recreation attempt of
// a degraded binding: base, 2×base, 4×base, … capped at maxBackoff. base and
// maxBackoff come from [triggers.advanced] (watch_retry_base_backoff /
// watch_retry_max_backoff).
func watchRetryBackoff(retry int, base, maxBackoff time.Duration) time.Duration {
	if retry < 1 {
		retry = 1
	}

	if base <= 0 {
		base = time.Second
	}

	if maxBackoff <= 0 {
		maxBackoff = base
	}

	if base > maxBackoff {
		base = maxBackoff
	}

	d := base
	for i := 1; i < retry; i++ {
		d *= 2
		if d >= maxBackoff {
			return maxBackoff
		}
	}

	return d
}

// gitignoreFilename is the file whose changes trigger a live rebuild of a
// binding's git ignore matcher and a reconcile of its watched directories.
const gitignoreFilename = ".gitignore"

// mandatoryWatchIgnores are always applied on top of the configurable
// [triggers.advanced] watch_builtin_ignores list, and can never be un-ignored:
// watching .git churns constantly and creates a feedback loop.
var mandatoryWatchIgnores = []string{".git/", ".git"}

// RunFileWatchLoop is the daemon-owned file-watch (#593) trigger source. It
// reconciles bindings (watch trigger × matching live session) against live
// fsnotify watchers each tick, and feeds debounced, filtered events into the
// shared trigger action executor.
func (sm *SessionManager) RunFileWatchLoop(ctx context.Context) {
	// The reconcile cadence is read once at loop start from [triggers.advanced]
	// watch_reconcile_interval, so changing it needs a daemon restart.
	ticker := time.NewTicker(sm.Config().TriggersRuntime.WatchReconcileIntervalDuration())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			sm.teardownAllBindings()
			return
		case <-ticker.C:
			cfgSnapshot := sm.Config()
			sm.reconcileBindingsWithConfig(ctx, sm.allTriggersFromConfig(cfgSnapshot), time.Now(), cfgSnapshot)
		}
	}
}

// reconcileBindings ensures one binding per (enabled watch trigger, matching
// running session) and tears down bindings whose session is gone/stopped.
func (sm *SessionManager) reconcileBindings(ctx context.Context, triggers []config.TriggerConfig, now time.Time) {
	sm.reconcileBindingsWithConfig(ctx, triggers, now, sm.Config())
}

func (sm *SessionManager) reconcileBindingsWithConfig(ctx context.Context, triggers []config.TriggerConfig, now time.Time, cfgSnapshot *config.Config) {
	builtinIgnores := cfgSnapshot.TriggersRuntime.WatchBuiltinIgnores()
	builtinFP := watchBuiltinFingerprint(builtinIgnores)
	desired := make(map[string]*config.TriggerConfig) // bindingKey -> trigger

	for i := range triggers {
		t := &triggers[i]
		if !t.IsWatch() || !t.TriggerEnabled() {
			continue
		}

		if rt := sm.getTriggerRuntime(t.Name); rt != nil && rt.Paused {
			continue
		}

		// A scenario-embedded trigger only binds to sessions within its own
		// scenario; a config-origin role trigger matches globally (scenarioID "").
		scenarioID, _, _ := parseScenarioTriggerName(t.Name)

		for _, sess := range sm.matchingWatchSessions(t.Watch, scenarioID) {
			desired[bindingKey(t.Name, sess.id)] = t
		}
	}

	// Tear down bindings no longer desired.
	sm.triggers.mu.Lock()

	var toRemove []string

	for key := range sm.triggers.bindings {
		if _, ok := desired[key]; !ok {
			toRemove = append(toRemove, key)
		}
	}
	sm.triggers.mu.Unlock()

	for _, key := range toRemove {
		sm.teardownBinding(key)
	}

	// Create newly desired bindings; recreate any whose definition changed; and
	// retry degraded ones whose backoff has elapsed. A degraded binding (e.g. one
	// that hit the inotify watch limit) stays in the map so its status surfaces,
	// but is recreated once its nextRetryAt passes — so it recovers on its own
	// when the limit clears, without a source-session restart (issue #1029).
	for key, t := range desired {
		fp := triggerFingerprint(t)

		sm.triggers.mu.Lock()
		existing, exists := sm.triggers.bindings[key]
		retryDue := exists && existing.degraded != "" && !existing.nextRetryAt.IsZero() && !now.Before(existing.nextRetryAt)
		sm.triggers.mu.Unlock()

		if exists && !retryDue {
			existingFP, existingBuiltinFP := existing.fingerprints()
			// A same-named watch trigger whose fire-affecting definition
			// (paths/ignore/debounce/action/policy) changed leaves the binding
			// key matching, so a plain existence check would keep the stale
			// matcher and debounce. Tear it down and recreate on fingerprint
			// change (mirrors reconcileSchedules' fingerprint reset). A healthy or
			// backoff-waiting binding whose definition is unchanged stays put.
			if existingFP == fp {
				if existingBuiltinFP != builtinFP {
					sm.updateWatchBindingBuiltinIgnores(cfgSnapshot, existing, builtinIgnores, builtinFP)
				}

				continue
			}

			// Don't tear down mid-serialised-action: a fire on the recreated
			// binding (which starts with a cleared inFlight guard) could
			// double-spawn an ensure-reviewer reactor while the old fire is
			// still inside its reserve→create. Defer the recreate to a later
			// tick; the fingerprint guard in watchFire keeps the stale binding
			// from firing the new definition in the meantime, so inFlight only
			// clears (never re-arms) and this converges once the fire finishes.
			if existing.actionInFlight() {
				continue
			}

			sm.teardownBinding(key)
		}

		sess := sm.sessionForBindingKey(key)
		if sess.id == "" {
			continue
		}

		sm.createBinding(ctx, t, sess, now, cfgSnapshot, builtinIgnores, builtinFP)
	}
}

type watchSession struct {
	id, name, worktree string
}

// matchingWatchSessions returns running, non-soft-deleted sessions matching the
// watch selector (repo or role). A non-empty scenarioID additionally scopes a
// role match to that scenario's members, so a scenario-embedded role trigger
// only binds inside its own scenario (config-origin triggers pass "").
func (sm *SessionManager) matchingWatchSessions(w *config.WatchConfig, scenarioID string) []watchSession {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var out []watchSession

	for id, s := range sm.state.Sessions {
		if s.Status != StatusRunning || s.IsSoftDeleted() || s.WorktreePath == "" {
			continue
		}

		match := false

		switch {
		case w.Repo != "":
			match = config.ResolvePath(s.RepoPath) == config.ResolvePath(w.Repo)
		case w.Role != "":
			match = s.ScenarioRole == w.Role && (scenarioID == "" || s.ScenarioID == scenarioID)
		}

		if match {
			out = append(out, watchSession{id: id, name: s.Name, worktree: s.WorktreePath})
		}
	}

	return out
}

func (sm *SessionManager) sessionForBindingKey(key string) watchSession {
	parts := strings.SplitN(key, "\x00", 2)
	if len(parts) != 2 {
		return watchSession{}
	}

	id := parts[1]

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	s, ok := sm.state.Sessions[id]
	if !ok || s.Status != StatusRunning || s.IsSoftDeleted() {
		return watchSession{}
	}

	return watchSession{id: id, name: s.Name, worktree: s.WorktreePath}
}

// createBinding sets up a recursive fsnotify watcher for a binding and starts
// its event goroutine. If the watcher degrades (e.g. the inotify watch limit is
// exhausted) the binding is recorded as degraded with a backoff-scheduled retry
// rather than running; the reconcile loop recreates it once nextRetryAt passes.
// now is injected so the retry schedule is testable.
func (sm *SessionManager) createBinding(ctx context.Context, t *config.TriggerConfig, sess watchSession, now time.Time, cfgSnapshot *config.Config, builtinIgnores []string, builtinFP string) {
	key := bindingKey(t.Name, sess.id)
	matcher := newWatchMatcher(sess.worktree, t.Watch, builtinIgnores)

	// Carry forward the retry count from a prior degraded binding for this key so
	// the backoff keeps growing across repeated failures instead of resetting on
	// each retry attempt.
	sm.triggers.mu.Lock()

	prevRetries := 0
	if prev, ok := sm.triggers.bindings[key]; ok {
		prevRetries = prev.retryCount
	}
	sm.triggers.mu.Unlock()

	// A watcher we can't even allocate (e.g. the inotify *instance* limit) is a
	// degraded outcome like a failed Add — record it with a backoff so a retry
	// doesn't busy-loop on every reconcile tick.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		sm.recordDegradedBinding(key, t, sess, "fsnotify.NewWatcher failed: "+err.Error(), prevRetries+1, now, cfgSnapshot, builtinFP)
		return
	}

	if degraded := addWatchRecursive(sm.watchAddFunc(watcher), sess.worktree, matcher); degraded != "" {
		_ = watcher.Close()

		sm.recordDegradedBinding(key, t, sess, degraded, prevRetries+1, now, cfgSnapshot, builtinFP)

		return
	}

	bctx, cancel := context.WithCancel(ctx)

	b := &watchBinding{
		triggerName:        t.Name,
		sessionID:          sess.id,
		worktree:           sess.worktree,
		fingerprint:        triggerFingerprint(t),
		builtinFingerprint: builtinFP,
		watcher:            watcher,
		matcher:            matcher,
		changed:            make(map[string]bool),
		cancel:             cancel,
		// Re-adopt an existing reactor (tagged TriggerID/TriggerReactor) so
		// ensure-reviewer reuse survives a daemon restart or binding recreation
		// instead of spawning a duplicate.
		reactorID: sm.findReactor(t.Name, sess.id),
	}

	if !sm.publishWatchBinding(cfgSnapshot, key, b) {
		cancel()

		_ = watcher.Close()

		return
	}

	go sm.runBinding(bctx, t.Name, b, matcher)

	sm.log.Info("trigger: watching", "trigger", t.Name, "session", sess.name, "worktree", sess.worktree)
}

// recordDegradedBinding stores (replacing any prior one) a degraded binding for
// key with a backoff-scheduled retry. It holds no live watcher or goroutine — a
// prior degraded binding already closed its watcher and never started one, and a
// prior healthy binding for this key is only ever recreated after teardown — so
// the reconcile loop simply recreates this entry once nextRetryAt passes.
func (sm *SessionManager) recordDegradedBinding(key string, t *config.TriggerConfig, sess watchSession, reason string, attempt int, now time.Time, cfgSnapshot *config.Config, builtinFP string) {
	runtimeCfg := cfgSnapshot.TriggersRuntime
	b := &watchBinding{
		triggerName: t.Name,
		sessionID:   sess.id,
		worktree:    sess.worktree,
		// Record the current fingerprint so an unchanged-definition degraded
		// binding is recreated by the backoff path (retryDue), not the
		// definition-changed path — otherwise an empty fingerprint would read as
		// "definition changed" and recreate immediately, bypassing the backoff.
		fingerprint:        triggerFingerprint(t),
		builtinFingerprint: builtinFP,
		changed:            make(map[string]bool),
		reactorID:          sm.findReactor(t.Name, sess.id),
		degraded:           reason,
		retryCount:         attempt,
		nextRetryAt: now.Add(watchRetryBackoff(attempt,
			runtimeCfg.WatchRetryBaseBackoffDuration(), runtimeCfg.WatchRetryMaxBackoffDuration())),
	}

	sm.log.Warn("trigger: watcher degraded, will retry",
		"trigger", t.Name, "reason", reason,
		"attempt", attempt, "retry_at", b.nextRetryAt.Format(time.RFC3339))

	sm.publishWatchBinding(cfgSnapshot, key, b)
}

// publishWatchBinding closes the reload race between a slow filesystem walk and
// applyConfig. The final map insertion happens while the config generation is
// pinned under sm.mu; an old-generation builder simply declines to publish and
// the next reconcile creates the current binding.
func (sm *SessionManager) publishWatchBinding(cfgSnapshot *config.Config, key string, b *watchBinding) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.cfg != cfgSnapshot {
		return false
	}

	sm.triggers.mu.Lock()
	sm.triggers.bindings[key] = b
	sm.triggers.mu.Unlock()

	return true
}

// updateWatchBindingBuiltinIgnores swaps the daemon-wide ignore policy on one
// live binding without replacing its fsnotify watcher. Event handling is paused
// behind policyMu while the matcher and watched directory set move together;
// fsnotify continues queueing events, so the generation swap has no blind gap.
func (sm *SessionManager) updateWatchBindingBuiltinIgnores(cfgSnapshot *config.Config, b *watchBinding, builtinIgnores []string, builtinFP string) {
	if b == nil {
		return
	}

	b.policyMu.Lock()
	defer b.policyMu.Unlock()

	// A stale reconcile that began before a newer reload must not overwrite the
	// newer policy after waiting for this binding lock.
	if sm.Config() != cfgSnapshot {
		return
	}

	b.bmu.Lock()
	if b.canceled {
		b.bmu.Unlock()
		return
	}

	matcher := b.matcher
	b.bmu.Unlock()

	if matcher != nil && b.watcher != nil {
		matcher.setBuiltinIgnores(builtinIgnores)
		sm.reconcileWatchDirs(b, matcher)

		// Drop changes collected under the old policy that the new matcher now
		// suppresses. Newly-unignored paths remain event-driven; the queued watcher
		// stream is processed after policyMu is released.
		b.bmu.Lock()
		for path := range b.changed {
			if !matcher.fires(path) {
				delete(b.changed, path)
			}
		}

		b.builtinFingerprint = builtinFP
		b.bmu.Unlock()

		return
	}

	// Degraded bindings have no live matcher. Stamping the current policy keeps
	// their normal backoff schedule; creation on retry uses cfgSnapshot itself.
	b.bmu.Lock()
	b.builtinFingerprint = builtinFP
	b.bmu.Unlock()
}

func (sm *SessionManager) updateActiveWatchBuiltinIgnores(cfgSnapshot *config.Config) {
	if sm.triggers == nil {
		return
	}

	sm.triggers.mu.Lock()

	bindings := make([]*watchBinding, 0, len(sm.triggers.bindings))

	for _, b := range sm.triggers.bindings {
		bindings = append(bindings, b)
	}
	sm.triggers.mu.Unlock()

	builtinIgnores := cfgSnapshot.TriggersRuntime.WatchBuiltinIgnores()
	builtinFP := watchBuiltinFingerprint(builtinIgnores)

	for _, b := range bindings {
		sm.updateWatchBindingBuiltinIgnores(cfgSnapshot, b, builtinIgnores, builtinFP)
	}
}

// watchAddFunc returns the directory-registration function used when building a
// binding's watch set. It normally delegates to the fsnotify watcher; tests
// override sm.watchAdd to simulate an exhausted watch limit.
func (sm *SessionManager) watchAddFunc(w *fsnotify.Watcher) func(string) error {
	if sm.watchAdd != nil {
		return func(path string) error { return sm.watchAdd(w, path) }
	}

	return w.Add
}

// runBinding is the per-binding event loop: filter events, coalesce with a
// debounce timer, and fire on quiet.
func (sm *SessionManager) runBinding(ctx context.Context, triggerName string, b *watchBinding, matcher *watchMatcher) {
	debounce := sm.bindingDebounce(triggerName)

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-b.watcher.Events:
			if !ok {
				return
			}

			b.policyMu.RLock()
			sm.handleWatchEvent(ctx, triggerName, b, matcher, ev, debounce)
			b.policyMu.RUnlock()
		case err, ok := <-b.watcher.Errors:
			if !ok {
				return
			}

			sm.log.Warn("trigger: watcher error", "trigger", triggerName, "err", err)
		}
	}
}

func (sm *SessionManager) bindingDebounce(triggerName string) time.Duration {
	t := sm.triggerByName(triggerName)
	if t == nil || t.Watch == nil {
		return 30 * time.Second
	}

	return t.Watch.DebounceDuration()
}

func (sm *SessionManager) handleWatchEvent(ctx context.Context, triggerName string, b *watchBinding, matcher *watchMatcher, ev fsnotify.Event, debounce time.Duration) {
	// A newly created directory needs recursive registration + scan.
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			rel := matcher.rel(ev.Name)
			if !matcher.ignoredDir(rel) {
				// Best-effort: a post-creation Add failure (e.g. the watch limit
				// exhausted only after a healthy start) is not routed through the
				// degraded/retry path — that covers creation-time degradation only
				// (#1029). Runtime subtree-add recovery is a separate follow-up.
				_ = b.watcher.Add(ev.Name)
				sm.scanNewDir(ctx, b, matcher, ev.Name, debounce, triggerName)
			}

			return
		}
	}

	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
		return
	}

	rel := matcher.rel(ev.Name)

	// A change to any .gitignore (added, edited, or removed) alters which paths
	// and directories are ignored. Rebuild the git matcher and reconcile the
	// live watch set so the edit takes effect without recreating the binding —
	// otherwise the matcher and pruned-directory set are frozen at binding
	// creation. Runs in this (the binding's) goroutine, so the matcher and
	// watcher need no extra locking.
	if filepath.Base(ev.Name) == gitignoreFilename {
		sm.reloadIgnores(b, matcher)
	}

	if !matcher.fires(rel) {
		return
	}

	sm.noteChange(ctx, triggerName, b, rel, debounce)
}

// reloadIgnores rebuilds the git ignore matcher from the current on-disk sources
// and reconciles the live watch set against the new rules. Called from the
// binding's event goroutine when a .gitignore changes.
func (sm *SessionManager) reloadIgnores(b *watchBinding, matcher *watchMatcher) {
	matcher.reloadGit()
	sm.reconcileWatchDirs(b, matcher)

	sm.log.Info("trigger: reloaded ignore rules", "trigger", b.triggerName, "worktree", b.worktree)
}

// reconcileWatchDirs brings the fsnotify watch set in line with matcher after an
// ignore-rule change: directories that are now un-ignored are added
// (idempotently) and directories that are now ignored are removed. A
// newly-un-ignored directory is watched but not scanned for pre-existing files —
// only subsequent events fire it — so merely un-ignoring a populated tree does
// not synthesise a burst of changes. Runs in the binding's event goroutine.
func (sm *SessionManager) reconcileWatchDirs(b *watchBinding, matcher *watchMatcher) {
	desired := make(map[string]bool)

	_ = filepath.WalkDir(b.worktree, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip unreadable dirs, keep walking
		}

		if !d.IsDir() {
			return nil
		}

		rel := matcher.rel(path)
		if rel != "." && matcher.ignoredDir(rel) {
			return filepath.SkipDir
		}

		desired[path] = true
		_ = b.watcher.Add(path) // idempotent; picks up newly-un-ignored subtrees

		return nil
	})

	// Prune directories that are now ignored but still watched. WatchList holds
	// only this binding's paths (each binding owns its watcher); the rel guard is
	// belt-and-braces so a stray entry outside the worktree is never touched.
	for _, w := range b.watcher.WatchList() {
		if desired[w] {
			continue
		}

		if rel, err := filepath.Rel(b.worktree, w); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			_ = b.watcher.Remove(w)
		}
	}
}

// scanNewDir handles a newly-created (or moved-in) directory: it registers
// watches for the whole non-ignored subtree and notes any existing files as
// changes, so a tool that atomically creates a nested tree isn't missed.
func (sm *SessionManager) scanNewDir(ctx context.Context, b *watchBinding, matcher *watchMatcher, dir string, debounce time.Duration, triggerName string) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip unreadable entries, keep walking
		}

		rel := matcher.rel(path)

		if d.IsDir() {
			if rel != "." && matcher.ignoredDir(rel) {
				return filepath.SkipDir
			}

			_ = b.watcher.Add(path)

			return nil
		}

		if matcher.fires(rel) {
			sm.noteChange(ctx, triggerName, b, rel, debounce)
		}

		return nil
	})
}

// noteChange records a changed path and (re)arms the debounce timer.
func (sm *SessionManager) noteChange(ctx context.Context, triggerName string, b *watchBinding, rel string, debounce time.Duration) {
	b.bmu.Lock()
	defer b.bmu.Unlock()

	b.changed[rel] = true
	if b.debounce != nil {
		b.debounce.Stop()
	}

	b.debounce = time.AfterFunc(debounce, func() {
		sm.watchFire(ctx, triggerName, b)
	})
}

// watchFire is the debounce callback: snapshot the coalesced changes, apply the
// overlap guard, and run the action.
func (sm *SessionManager) watchFire(ctx context.Context, triggerName string, b *watchBinding) {
	cfgSnapshot := sm.Config()

	t := sm.triggerByNameFromConfig(triggerName, cfgSnapshot)
	if t == nil || !t.IsWatch() || !t.TriggerEnabled() {
		return
	}
	// The trigger may have been paused during the up-to-2s reconcile window
	// before its binding was torn down; don't fire a paused trigger.
	if rt := sm.getTriggerRuntime(t.Name); rt != nil && rt.Paused {
		return
	}
	// The binding captured its matcher and debounce for one definition
	// generation. If the definition changed under it (a hot-reload landed but
	// reconcile hasn't recreated the binding yet), don't fire the new action
	// from an event the old matcher collected — the recreated binding takes
	// over. This also stops the stale binding starting a fresh serialised
	// action, so its inFlight guard can only clear (see reconcileBindings).
	definitionFP, builtinFP := b.fingerprints()
	if triggerFingerprint(t) != definitionFP ||
		watchBuiltinFingerprint(cfgSnapshot.TriggersRuntime.WatchBuiltinIgnores()) != builtinFP {
		return
	}

	// Serialise per-binding for actions where a concurrent fire would duplicate
	// work or race: command runs, and ensure-reviewer (whose reactor
	// reserve→create must not overlap).
	serialise := t.Action.Type == config.ActionCommand ||
		(t.Action.Type == config.ActionSession && t.Action.Ensure)

	b.bmu.Lock()
	// The binding may have been torn down after this timer fired but before it
	// ran; don't fire for a canceled binding (stopped/soft-deleted source).
	if b.canceled {
		b.bmu.Unlock()
		return
	}

	if serialise && b.inFlight {
		b.bmu.Unlock()
		sm.log.Info("trigger: watch skipped (in flight)", "trigger", triggerName)

		return
	}

	// Nothing coalesced (e.g. a superseded timer that already had its changes
	// drained by an earlier fire) — don't fire an empty event.
	if len(b.changed) == 0 {
		b.bmu.Unlock()
		return
	}

	changed := make([]string, 0, len(b.changed))
	for p := range b.changed {
		changed = append(changed, p)
	}

	b.changed = make(map[string]bool)
	if serialise {
		b.inFlight = true
	}
	b.bmu.Unlock()

	defer func() {
		if serialise {
			b.bmu.Lock()
			b.inFlight = false
			b.bmu.Unlock()
		}
	}()

	fc := fireContext{
		cause:        causeFile,
		now:          time.Now(),
		sessionID:    b.sessionID,
		sessionName:  sm.sessionName(b.sessionID),
		worktree:     b.worktree,
		changedFiles: changed,
	}
	sm.fireWatch(ctx, t, fc)
}

// stopWatcherResources shuts down a file watcher's goroutine and fsnotify
// handle, then marks it canceled and stops any pending debounce under bmu.
// Shared by the trigger file-watch and PR-ref-watch teardown paths — cancel and
// watcher are read by the caller before the lock (matching the original code),
// while canceled/debounce are touched under bmu (both are goroutine-mutated).
func stopWatcherResources(cancel func(), watcher *fsnotify.Watcher, bmu *sync.Mutex, canceled *bool, debounce **time.Timer) {
	if cancel != nil {
		cancel()
	}

	if watcher != nil {
		_ = watcher.Close()
	}

	bmu.Lock()

	*canceled = true

	if *debounce != nil {
		(*debounce).Stop()
	}

	bmu.Unlock()
}

func (sm *SessionManager) teardownBinding(key string) {
	sm.triggers.mu.Lock()
	b := sm.triggers.bindings[key]
	delete(sm.triggers.bindings, key)
	sm.triggers.mu.Unlock()

	if b == nil {
		return
	}

	stopWatcherResources(b.cancel, b.watcher, &b.bmu, &b.canceled, &b.debounce)
}

func (sm *SessionManager) teardownAllBindings() {
	sm.triggers.mu.Lock()

	keys := make([]string, 0, len(sm.triggers.bindings))
	for k := range sm.triggers.bindings {
		keys = append(keys, k)
	}
	sm.triggers.mu.Unlock()

	for _, k := range keys {
		sm.teardownBinding(k)
	}
}

// addWatchRecursive walks the worktree and adds a watch per non-ignored
// directory via add. Returns a non-empty degraded reason if it hits the watch
// limit. add is a seam (normally the fsnotify watcher's Add) so the degraded
// path is testable without exhausting fs.inotify.max_user_watches for real.
func addWatchRecursive(add func(string) error, root string, matcher *watchMatcher) string {
	var degraded string

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip unreadable dirs, keep walking
		}

		if !d.IsDir() {
			return nil
		}

		rel := matcher.rel(path)
		if rel != "." && matcher.ignoredDir(rel) {
			return filepath.SkipDir
		}

		if aerr := add(path); aerr != nil {
			degraded = "watcher.Add failed: " + aerr.Error()
			return filepath.SkipDir
		}

		return nil
	})

	return degraded
}

// --- matching ---

type watchMatcher struct {
	mu      sync.RWMutex
	root    string
	git     ignore.Matcher // .git/info/exclude + .gitignore files under root
	builtin ignore.Matcher // always-applied, non-overridable ignores
	userIgn ignore.Matcher // config watch.ignore; nil unless set
	include ignore.Matcher // config watch.paths; nil unless set
}

// newWatchMatcher builds a matcher for root. builtinIgnores is the daemon-wide
// watch ignore list (from [triggers.advanced] watch_builtin_ignores); a nil list
// uses config.DefaultWatchBuiltinIgnores. mandatoryWatchIgnores (.git) is always
// applied on top so it can never be un-ignored.
func newWatchMatcher(root string, w *config.WatchConfig, builtinIgnores []string) *watchMatcher {
	m := &watchMatcher{root: root}

	if builtinIgnores == nil {
		builtinIgnores = config.DefaultWatchBuiltinIgnores
	}

	builtin := append(append([]string(nil), mandatoryWatchIgnores...), builtinIgnores...)
	m.builtin = ignore.Lines(builtin...)
	m.git = ignore.Dir(root)

	if len(w.Ignore) > 0 {
		m.userIgn = ignore.Lines(w.Ignore...)
	}

	if len(w.Paths) > 0 {
		m.include = ignore.Lines(w.Paths...)
	}

	return m
}

func (m *watchMatcher) setBuiltinIgnores(builtinIgnores []string) {
	if builtinIgnores == nil {
		builtinIgnores = config.DefaultWatchBuiltinIgnores
	}

	builtin := append(append([]string(nil), mandatoryWatchIgnores...), builtinIgnores...)

	m.mu.Lock()
	m.builtin = ignore.Lines(builtin...)
	m.mu.Unlock()
}

// reloadGit rebuilds the git ignore matcher (.git/info/exclude + the tree's
// .gitignore files) from disk. Called when a .gitignore changes; the builtin,
// user-ignore, and include matchers are config-derived and left untouched.
func (m *watchMatcher) reloadGit() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.git = ignore.Dir(m.root)
}

func (m *watchMatcher) rel(path string) string {
	r, err := filepath.Rel(m.root, path)
	if err != nil {
		return path
	}

	return filepath.ToSlash(r)
}

// ignoredDir reports whether a directory should be pruned from the watch set.
// The path names a directory, so directory-only patterns (a trailing "/") are
// evaluated with isDir=true, exactly as Git would.
func (m *watchMatcher) ignoredDir(rel string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if rel == "" || rel == "." {
		return false
	}

	if m.builtin.Match(rel, true) {
		return true
	}

	if m.git.Match(rel, true) {
		return true
	}

	if m.userIgn != nil && m.userIgn.Match(rel, true) {
		return true
	}

	return false
}

// fires reports whether a changed file path should fire the action.
func (m *watchMatcher) fires(rel string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if rel == "" || rel == "." {
		return false
	}

	if m.builtin.Match(rel, false) {
		return false
	}

	if m.git.Match(rel, false) {
		return false
	}

	if m.userIgn != nil && m.userIgn.Match(rel, false) {
		return false
	}

	if m.include != nil && !m.include.Match(rel, false) {
		return false
	}

	return true
}
