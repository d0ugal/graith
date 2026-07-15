package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/fsnotify/fsnotify"
	gitignore "github.com/sabhiram/go-gitignore"
)

const fileWatchReconcileTick = 2 * time.Second

// builtinWatchIgnores are always applied and never overridable: watching these
// is never useful and they are prime feedback-loop / churn sources.
var builtinWatchIgnores = []string{".git/", ".git", ".hg/", ".svn/", "*.swp", "*.swx", "4913", ".DS_Store"}

// RunFileWatchLoop is the daemon-owned file-watch (#593) trigger source. It
// reconciles bindings (watch trigger × matching live session) against live
// fsnotify watchers each tick, and feeds debounced, filtered events into the
// shared trigger action executor.
func (sm *SessionManager) RunFileWatchLoop(ctx context.Context) {
	ticker := time.NewTicker(fileWatchReconcileTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			sm.teardownAllBindings()
			return
		case <-ticker.C:
			cfg := sm.Config()
			sm.reconcileBindings(ctx, cfg)
		}
	}
}

// reconcileBindings ensures one binding per (enabled watch trigger, matching
// running session) and tears down bindings whose session is gone/stopped.
func (sm *SessionManager) reconcileBindings(ctx context.Context, cfg *config.Config) {
	desired := make(map[string]*config.TriggerConfig) // bindingKey -> trigger

	for i := range cfg.Triggers {
		t := &cfg.Triggers[i]
		if !t.IsWatch() || !t.TriggerEnabled() {
			continue
		}

		if rt := sm.getTriggerRuntime(t.Name); rt != nil && rt.Paused {
			continue
		}

		for _, sess := range sm.matchingWatchSessions(t.Watch) {
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

	// Create newly desired bindings, and recreate any whose definition changed.
	for key, t := range desired {
		fp := triggerFingerprint(t)

		sm.triggers.mu.Lock()
		existing, exists := sm.triggers.bindings[key]
		sm.triggers.mu.Unlock()

		if exists {
			// A same-named watch trigger whose fire-affecting definition
			// (paths/ignore/debounce/action/policy) changed leaves the binding
			// key matching, so a plain existence check would keep the stale
			// matcher and debounce. Tear it down and recreate on fingerprint
			// change (mirrors reconcileSchedules' fingerprint reset).
			if existing.fingerprint == fp {
				continue
			}

			sm.teardownBinding(key)
		}

		sess := sm.sessionForBindingKey(key)
		if sess.id == "" {
			continue
		}

		sm.createBinding(ctx, t, sess)
	}
}

type watchSession struct {
	id, name, worktree string
}

// matchingWatchSessions returns running, non-soft-deleted sessions matching the
// watch selector (repo or role).
func (sm *SessionManager) matchingWatchSessions(w *config.WatchConfig) []watchSession {
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
			match = s.ScenarioRole == w.Role
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
// its event goroutine.
func (sm *SessionManager) createBinding(ctx context.Context, t *config.TriggerConfig, sess watchSession) {
	matcher := newWatchMatcher(sess.worktree, t.Watch)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		sm.log.Warn("trigger: fsnotify unavailable", "trigger", t.Name, "err", err)
		return
	}

	b := &watchBinding{
		triggerName: t.Name,
		sessionID:   sess.id,
		worktree:    sess.worktree,
		fingerprint: triggerFingerprint(t),
		watcher:     watcher,
		changed:     make(map[string]bool),
		// Re-adopt an existing reactor (tagged TriggerID/TriggerReactor) so
		// ensure-reviewer reuse survives a daemon restart or binding recreation
		// instead of spawning a duplicate.
		reactorID: sm.findReactor(t.Name, sess.id),
	}

	degraded := addWatchRecursive(watcher, sess.worktree, matcher)
	if degraded != "" {
		b.degraded = degraded
		sm.log.Warn("trigger: watcher degraded, disabling binding", "trigger", t.Name, "reason", degraded)

		_ = watcher.Close()
		// Record the binding as degraded so status reflects it, but don't run it.
		sm.triggers.mu.Lock()
		sm.triggers.bindings[bindingKey(t.Name, sess.id)] = b
		sm.triggers.mu.Unlock()

		return
	}

	bctx, cancel := context.WithCancel(ctx)
	b.cancel = cancel

	sm.triggers.mu.Lock()
	sm.triggers.bindings[bindingKey(t.Name, sess.id)] = b
	sm.triggers.mu.Unlock()

	go sm.runBinding(bctx, t.Name, b, matcher)

	sm.log.Info("trigger: watching", "trigger", t.Name, "session", sess.name, "worktree", sess.worktree)
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

			sm.handleWatchEvent(ctx, triggerName, b, matcher, ev, debounce)
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
	if !matcher.fires(rel) {
		return
	}

	sm.noteChange(ctx, triggerName, b, rel, debounce)
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
	t := sm.triggerByName(triggerName)
	if t == nil || !t.IsWatch() || !t.TriggerEnabled() {
		return
	}
	// The trigger may have been paused during the up-to-2s reconcile window
	// before its binding was torn down; don't fire a paused trigger.
	if rt := sm.getTriggerRuntime(t.Name); rt != nil && rt.Paused {
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

func (sm *SessionManager) teardownBinding(key string) {
	sm.triggers.mu.Lock()
	b := sm.triggers.bindings[key]
	delete(sm.triggers.bindings, key)
	sm.triggers.mu.Unlock()

	if b == nil {
		return
	}

	if b.cancel != nil {
		b.cancel()
	}

	if b.watcher != nil {
		_ = b.watcher.Close()
	}

	b.bmu.Lock()

	b.canceled = true
	if b.debounce != nil {
		b.debounce.Stop()
	}
	b.bmu.Unlock()
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
// directory. Returns a non-empty degraded reason if it hits the watch limit.
func addWatchRecursive(w *fsnotify.Watcher, root string, matcher *watchMatcher) string {
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

		if aerr := w.Add(path); aerr != nil {
			degraded = "watcher.Add failed: " + aerr.Error()
			return filepath.SkipDir
		}

		return nil
	})

	return degraded
}

// --- matching ---

type watchMatcher struct {
	root    string
	git     *gitignore.GitIgnore
	builtin *gitignore.GitIgnore
	userIgn *gitignore.GitIgnore
	include *gitignore.GitIgnore // nil unless paths set
}

func newWatchMatcher(root string, w *config.WatchConfig) *watchMatcher {
	m := &watchMatcher{root: root}

	m.builtin = gitignore.CompileIgnoreLines(builtinWatchIgnores...)
	if gi, err := gitignore.CompileIgnoreFile(filepath.Join(root, ".gitignore")); err == nil {
		m.git = gi
	}

	if len(w.Ignore) > 0 {
		m.userIgn = gitignore.CompileIgnoreLines(w.Ignore...)
	}

	if len(w.Paths) > 0 {
		m.include = gitignore.CompileIgnoreLines(w.Paths...)
	}

	return m
}

func (m *watchMatcher) rel(path string) string {
	r, err := filepath.Rel(m.root, path)
	if err != nil {
		return path
	}

	return filepath.ToSlash(r)
}

// ignoredDir reports whether a directory should be pruned from the watch set.
func (m *watchMatcher) ignoredDir(rel string) bool {
	if rel == "" || rel == "." {
		return false
	}

	if m.builtin.MatchesPath(rel) || m.builtin.MatchesPath(rel+"/") {
		return true
	}

	if m.git != nil && (m.git.MatchesPath(rel) || m.git.MatchesPath(rel+"/")) {
		return true
	}

	if m.userIgn != nil && (m.userIgn.MatchesPath(rel) || m.userIgn.MatchesPath(rel+"/")) {
		return true
	}

	return false
}

// fires reports whether a changed file path should fire the action.
func (m *watchMatcher) fires(rel string) bool {
	if rel == "" || rel == "." {
		return false
	}

	if m.builtin.MatchesPath(rel) {
		return false
	}

	if m.git != nil && m.git.MatchesPath(rel) {
		return false
	}

	if m.userIgn != nil && m.userIgn.MatchesPath(rel) {
		return false
	}

	if m.include != nil && !m.include.MatchesPath(rel) {
		return false
	}

	return true
}
