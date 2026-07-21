package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
	"github.com/fsnotify/fsnotify"
)

// prrefwatch.go accelerates PR detection. The PR/CI watcher (prwatch.go) resolves
// each session's PR by polling gh on a timer, so a freshly pushed branch is only
// noticed on the next tick (up to the base tick plus batch-cap/negative-cache
// latency). This accelerator shares one fsnotify backend per repository and,
// when a push/commit/checkout touches its refs, sends the affected session IDs
// to the PR-watch loop's kick channel for immediate polls. The GitHub poll stays
// the always-on fallback, so a degraded or missing watch only costs latency,
// never awareness. See
// docs/design/2026-07-14-pr-ref-watch-design.md.

// The reconcile cadence and per-watch debounce are now [pr_watch.advanced] config
// knobs (ref_reconcile_interval / ref_debounce), resolved through the
// config.PRWatchConfig accessors. The reconcile cadence is read once when the loop
// starts; the debounce is read whenever a watcher arms its timer.

type prRefWatchState struct {
	mu           sync.Mutex
	watchers     map[string]*prRefWatcher     // session ID -> debounce/local ownership
	repositories map[string]*prRefRepoWatcher // canonical common git dir -> shared watcher
}

func newPRRefWatchState() *prRefWatchState {
	return &prRefWatchState{
		watchers:     make(map[string]*prRefWatcher),
		repositories: make(map[string]*prRefRepoWatcher),
	}
}

// prRefWatcher is one session's membership in a repository watcher. Only the
// linked-worktree gitdir paths and debounce belong to the session; the expensive
// common refs/logs tree belongs to repo.
type prRefWatcher struct {
	sessionID string
	worktree  string
	repo      *prRefRepoWatcher
	localDirs []string

	bmu      sync.Mutex
	debounce *time.Timer
	canceled bool
}

// prRefRepoWatcher owns the sole fsnotify backend for a canonical Git common
// directory. This is important on kqueue (macOS/BSD): every watched path and
// directory entry consumes a real descriptor, so recursively adding the same
// refs/logs tree to one backend per worktree multiplies descriptor use.
type prRefRepoWatcher struct {
	commonDir string
	watcher   *fsnotify.Watcher
	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once

	mu       sync.Mutex
	closed   bool
	sessions map[string]*prRefWatcher
	dirs     map[string]*prRefWatchDir
}

// prRefWatchDir records why a directory is registered. Common directories fan
// events out to every repository session; local directories target only their
// owning linked worktree(s). A map is used for local owners so teardown remains
// correct if two sessions ever point at the same worktree.
type prRefWatchDir struct {
	common   bool
	sessions map[string]struct{}
}

type gitRefWatchPaths struct {
	gitDir     string
	commonDir  string
	localDirs  []string
	commonDirs []string
}

// RunPRRefWatchLoop reconciles session memberships in shared repository watchers
// each tick. Started from RunPRWatchLoop and sharing its lifecycle + gh gate; the
// loop's own poll is the fallback if this degrades.
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

// reconcilePRRefWatchers joins newly eligible sessions to their repository's
// watcher and tears down memberships whose session is gone/stopped/deleted.
// Locks are released before create/teardown (which lock) to avoid re-entrancy.
func (sm *SessionManager) reconcilePRRefWatchers(ctx context.Context) {
	desired := sm.prRefEligibleSessions()

	sm.prRefWatch.mu.Lock()

	var toRemove []string

	for id, watcher := range sm.prRefWatch.watchers {
		worktree, ok := desired[id]
		if !ok || watcher.worktree != worktree {
			toRemove = append(toRemove, id)
		}
	}
	sm.prRefWatch.mu.Unlock()

	for _, id := range toRemove {
		sm.teardownPRRefWatcher(id)
	}

	for id, worktree := range desired {
		sm.prRefWatch.mu.Lock()
		_, exists := sm.prRefWatch.watchers[id]
		sm.prRefWatch.mu.Unlock()

		if exists {
			continue
		}

		sm.createPRRefWatcher(ctx, id, worktree)
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

// createPRRefWatcher resolves a worktree's ref directories and joins it to the
// single watcher for its canonical common Git directory. Fail-open: if the dirs
// cannot be resolved or the repository watch cannot be established, the normal
// poll continues to cover the session.
func (sm *SessionManager) createPRRefWatcher(ctx context.Context, id, worktree string) {
	paths := resolveGitRefWatchPaths(worktree)
	if paths == nil {
		return
	}

	w := &prRefWatcher{
		sessionID: id,
		worktree:  worktree,
		localDirs: paths.localDirs,
	}

	// Avoid allocating another kqueue/inotify instance when the repository is
	// already live. A racing creator is handled by the second lookup below.
	sm.prRefWatch.mu.Lock()
	if _, exists := sm.prRefWatch.watchers[id]; exists {
		sm.prRefWatch.mu.Unlock()
		return
	}

	repo := sm.prRefWatch.repositories[paths.commonDir]
	sm.prRefWatch.mu.Unlock()

	var candidate *prRefRepoWatcher
	if repo == nil {
		candidate = sm.newPRRefRepoWatcher(ctx, id, paths)
		if candidate == nil {
			return
		}
	}

	var startRepo, discardCandidate bool

	sm.prRefWatch.mu.Lock()
	if _, exists := sm.prRefWatch.watchers[id]; exists {
		sm.prRefWatch.mu.Unlock()

		if candidate != nil {
			candidate.stop()
		}

		return
	}

	if current := sm.prRefWatch.repositories[paths.commonDir]; current != nil {
		repo = current
		discardCandidate = candidate != nil
	} else {
		// candidate can only be nil if the last repository membership raced this
		// creator and removed the repository after the first lookup. Retry with a
		// fresh backend rather than attaching to a closing watcher.
		if candidate == nil {
			sm.prRefWatch.mu.Unlock()
			sm.createPRRefWatcher(ctx, id, worktree)

			return
		}

		repo = candidate
		sm.prRefWatch.repositories[paths.commonDir] = repo
		startRepo = true
	}

	w.repo = repo
	repo.mu.Lock()
	localAdded, localFailed := repo.addDirsLocked(id, paths.localDirs, false)
	repo.sessions[id] = w
	repo.mu.Unlock()

	sm.prRefWatch.watchers[id] = w
	sm.prRefWatch.mu.Unlock()

	if discardCandidate {
		candidate.stop()
	}

	if startRepo {
		// repo.ctx is the repository watcher's lifetime context; the task and
		// watcher deliberately share that owner rather than a joining request.
		if !sm.startBackgroundTask(repo.ctx, func(context.Context) { //nolint:contextcheck // repo-owned lifetime is propagated explicitly
			sm.runPRRefRepoWatcher(repo)
		}) {
			repo.stop()
		}
	}

	if sm.log != nil {
		sm.log.Debug("pr-watch: ref watch joined", "session", id, "repository", paths.commonDir,
			"shared", !startRepo, "local_dirs_added", localAdded)

		if localFailed > 0 {
			sm.log.Warn("pr-watch: some worktree-local ref watches unavailable; polling remains active",
				"session", id, "repository", paths.commonDir, "failed_dirs", localFailed)
		}
	}
}

func (sm *SessionManager) newPRRefRepoWatcher(
	ctx context.Context,
	sessionID string,
	paths *gitRefWatchPaths,
) *prRefRepoWatcher {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		if sm.log != nil {
			sm.log.Warn("pr-watch: fsnotify unavailable for ref watch", "session", sessionID, "err", err)
		}

		return nil
	}

	wctx, cancel := context.WithCancel(ctx)
	repo := &prRefRepoWatcher{
		commonDir: paths.commonDir,
		watcher:   watcher,
		ctx:       wctx,
		cancel:    cancel,
		sessions:  make(map[string]*prRefWatcher),
		dirs:      make(map[string]*prRefWatchDir),
	}

	repo.mu.Lock()
	added, failed := repo.addDirsLocked("", paths.commonDirs, true)
	repo.mu.Unlock()

	if added == 0 {
		repo.stop()
		return nil
	}

	if sm.log != nil {
		sm.log.Debug("pr-watch: shared repository ref watch started", "repository", paths.commonDir,
			"registered_dirs", added)

		if failed > 0 {
			sm.log.Warn("pr-watch: some shared ref watches unavailable; polling remains active",
				"repository", paths.commonDir, "registered_dirs", added, "failed_dirs", failed)
		}
	}

	return repo
}

// resolveGitRefWatchPaths splits a worktree's watches into the expensive common
// refs/logs tree and its small linked-worktree gitdir. The split is the resource
// boundary: commonDirs are registered once per canonical repository, while
// localDirs are ref-counted per session. The object store is always excluded.
func resolveGitRefWatchPaths(worktree string) *gitRefWatchPaths {
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

	gitDir = canonicalGitPath(gitDir)
	commonDir = canonicalGitPath(commonDir)

	paths := &gitRefWatchPaths{gitDir: gitDir, commonDir: commonDir}
	paths.commonDirs = existingPRRefDirs(
		[]string{commonDir},
		[]string{filepath.Join(commonDir, "refs"), filepath.Join(commonDir, "logs")},
	)

	// For the primary worktree gitDir == commonDir, and the common registration
	// already covers HEAD and logs. Linked worktrees keep those paths local.
	if gitDir != commonDir {
		paths.localDirs = existingPRRefDirs(
			[]string{gitDir},
			[]string{filepath.Join(gitDir, "logs")},
		)
	}

	if len(paths.commonDirs) == 0 {
		return nil
	}

	return paths
}

func canonicalGitPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}

	return path
}

// existingPRRefDirs returns unique existing roots and recursively walks the
// supplied trees. Unreadable/missing subtrees degrade to polling instead of
// making repository watch setup fail.
func existingPRRefDirs(roots, trees []string) []string {
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

	for _, root := range roots {
		addDir(root)
	}

	for _, tree := range trees {
		addTree(tree)
	}

	return dirs
}

// gitRefWatchDirs retains the flattened view used by path-set tests.
func gitRefWatchDirs(worktree string) []string {
	paths := resolveGitRefWatchPaths(worktree)
	if paths == nil {
		return nil
	}

	dirs := make([]string, 0, len(paths.commonDirs)+len(paths.localDirs))
	dirs = append(dirs, paths.localDirs...)
	dirs = append(dirs, paths.commonDirs...)

	return dirs
}

// addDirsLocked registers new directories and merges their ownership. The
// caller holds repo.mu. Existing registrations are reused and therefore do not
// call fsnotify.Add again.
func (repo *prRefRepoWatcher) addDirsLocked(
	sessionID string,
	dirs []string,
	common bool,
) (added, failed int) {
	if repo.closed {
		return 0, len(dirs)
	}

	for _, dir := range dirs {
		if ownership := repo.dirs[dir]; ownership != nil {
			ownership.common = ownership.common || common
			if sessionID != "" {
				ownership.sessions[sessionID] = struct{}{}
			}

			continue
		}

		if err := repo.watcher.Add(dir); err != nil {
			failed++
			continue
		}

		ownership := &prRefWatchDir{
			common:   common,
			sessions: make(map[string]struct{}),
		}
		if sessionID != "" {
			ownership.sessions[sessionID] = struct{}{}
		}

		repo.dirs[dir] = ownership
		added++
	}

	return added, failed
}

// runPRRefRepoWatcher is the one event loop per common Git directory.
func (sm *SessionManager) runPRRefRepoWatcher(repo *prRefRepoWatcher) {
	for {
		select {
		case <-repo.ctx.Done():
			return
		case ev, ok := <-repo.watcher.Events:
			if !ok {
				return
			}

			sm.handlePRRefEvent(repo, ev)
		case err, ok := <-repo.watcher.Errors:
			if !ok {
				return
			}

			if sm.log != nil {
				sm.log.Debug("pr-watch: ref watcher error", "repository", repo.commonDir, "err", err)
			}
		}
	}
}

// handlePRRefEvent fans common changes out to every repository session and
// targets worktree-local changes to their owner. Newly created directory trees
// inherit their parent's ownership so nested branch namespaces stay reactive.
func (sm *SessionManager) handlePRRefEvent(repo *prRefRepoWatcher, ev fsnotify.Event) {
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
		return // Chmod-only noise
	}

	repo.mu.Lock()

	ownership := repo.eventOwnershipLocked(ev.Name)
	if ownership == nil {
		repo.mu.Unlock()
		return
	}

	recipients := repo.recipientsLocked(ownership)
	common := ownership.common

	localIDs := make([]string, 0, len(ownership.sessions))
	for id := range ownership.sessions {
		localIDs = append(localIDs, id)
	}

	removedWatchedDir := repo.dirs[ev.Name] != nil
	repo.mu.Unlock()

	if removedWatchedDir && ev.Op&(fsnotify.Rename|fsnotify.Remove) != 0 {
		repo.mu.Lock()
		for dir := range repo.dirs {
			if sameOrChildPath(ev.Name, dir) {
				delete(repo.dirs, dir)
				_ = repo.watcher.Remove(dir)
			}
		}
		repo.mu.Unlock()
	}

	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			// The common-dir top-level watch exists for files such as HEAD and
			// packed-refs. Do not recursively adopt unrelated directories created
			// there (especially worktrees/): linked gitdirs are per-session state.
			if !common || isCommonPRRefTree(repo.commonDir, ev.Name) {
				dirs := existingPRRefDirs(nil, []string{ev.Name})

				repo.mu.Lock()
				if common {
					repo.addDirsLocked("", dirs, true)
				} else {
					for _, id := range localIDs {
						if repo.sessions[id] != nil {
							repo.addDirsLocked(id, dirs, false)
						}
					}
				}
				repo.mu.Unlock()
			}
		}
	}

	for _, watcher := range recipients {
		sm.notePRRefChange(watcher)
	}
}

func (repo *prRefRepoWatcher) eventOwnershipLocked(name string) *prRefWatchDir {
	if ownership := repo.dirs[filepath.Dir(name)]; ownership != nil {
		return ownership
	}

	return repo.dirs[name]
}

func sameOrChildPath(parent, path string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isCommonPRRefTree(commonDir, path string) bool {
	return sameOrChildPath(filepath.Join(commonDir, "refs"), path) ||
		sameOrChildPath(filepath.Join(commonDir, "logs"), path)
}

func (repo *prRefRepoWatcher) recipientsLocked(ownership *prRefWatchDir) []*prRefWatcher {
	if ownership.common {
		out := make([]*prRefWatcher, 0, len(repo.sessions))
		for _, watcher := range repo.sessions {
			out = append(out, watcher)
		}

		return out
	}

	out := make([]*prRefWatcher, 0, len(ownership.sessions))
	for id := range ownership.sessions {
		if watcher := repo.sessions[id]; watcher != nil {
			out = append(out, watcher)
		}
	}

	return out
}

// notePRRefChange (re)arms the debounce timer that fires a single kick.
func (sm *SessionManager) notePRRefChange(w *prRefWatcher) {
	// Snapshot the effective config before taking the per-watcher timer lock. A
	// reload completed before this ref change therefore applies immediately to an
	// existing watcher, while a timer already armed under the previous generation
	// is left intact so its pending event cannot be dropped.
	defaultDur := (config.PRWatchConfig{}).RefDebounceDuration()

	dur := defaultDur
	if cfg := sm.Config(); cfg != nil {
		dur = cfg.PRWatch.RefDebounceDuration()
	}

	if dur <= 0 {
		// Preserve the existing runtime fallback for explicitly non-positive
		// values; hot reload must not change their effective behavior.
		dur = defaultDur
	}

	w.bmu.Lock()
	defer w.bmu.Unlock()

	if w.canceled {
		return
	}

	if w.debounce != nil {
		w.debounce.Stop()
	}

	w.debounce = time.AfterFunc(dur, func() {
		// Re-check canceled: the timer may fire after teardown stopped it (Stop
		// returns false once the callback is already scheduled). Mirrors filewatch's
		// watchFire guard — a post-teardown kick is otherwise harmless (pollKicked
		// re-validates) but would needlessly burn the kick cooldown / a gh call.
		w.bmu.Lock()
		canceled := w.canceled
		w.bmu.Unlock()

		if canceled {
			return
		}

		sm.kickPRWatch(w.sessionID)
	})
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
	if w == nil {
		sm.prRefWatch.mu.Unlock()
		return
	}

	// Mark the debounce canceled before removing repository membership. An event
	// handler that already snapshotted this session then cannot arm a late kick.
	w.bmu.Lock()

	w.canceled = true
	if w.debounce != nil {
		w.debounce.Stop()
	}
	w.bmu.Unlock()

	delete(sm.prRefWatch.watchers, id)

	repo := w.repo
	repo.mu.Lock()
	delete(repo.sessions, id)

	for dir, ownership := range repo.dirs {
		delete(ownership.sessions, id)

		if !ownership.common && len(ownership.sessions) == 0 {
			delete(repo.dirs, dir)
			_ = repo.watcher.Remove(dir)
		}
	}

	lastSession := len(repo.sessions) == 0
	if lastSession {
		repo.closed = true
		delete(sm.prRefWatch.repositories, repo.commonDir)
	}
	repo.mu.Unlock()
	sm.prRefWatch.mu.Unlock()

	if lastSession {
		repo.stop()
	}
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

func (repo *prRefRepoWatcher) stop() {
	repo.closeOnce.Do(func() {
		repo.mu.Lock()
		repo.closed = true
		repo.mu.Unlock()

		repo.cancel()
		_ = repo.watcher.Close()
	})
}
