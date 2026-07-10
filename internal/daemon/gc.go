package daemon

import (
	"os"
	"path/filepath"
	"time"

	"github.com/d0ugal/graith/internal/git"
)

// gcOrphanMinAge is the minimum age a worktree/scratch directory must have
// before GC will consider it an orphan. Directories are created early in a
// session's lifecycle (during StatusCreating, before the session is committed
// to state), so a young directory may belong to an in-flight create that GC
// would otherwise race and destroy.
const gcOrphanMinAge = 5 * time.Minute

// GCOrphanType classifies where an orphan directory lives.
const (
	GCOrphanWorktree = "worktree"
	GCOrphanScratch  = "scratch"
)

// GCOrphan describes a directory under the data dir that has no matching
// session. When RunGC runs with force=true, Removed/Skipped/Reason record the
// outcome of the cleanup attempt.
type GCOrphan struct {
	Type          string `json:"type"`
	Path          string `json:"path"`
	ID            string `json:"id"`
	IsGitWorktree bool   `json:"is_git_worktree,omitempty"`
	HasDirtyFiles bool   `json:"has_dirty_files,omitempty"`
	Removed       bool   `json:"removed,omitempty"`
	Skipped       bool   `json:"skipped,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// knownSessionIDs snapshots every session ID currently in state (any status),
// so GC never touches a directory belonging to a live or known session.
func (sm *SessionManager) knownSessionIDs() map[string]bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	ids := make(map[string]bool, len(sm.state.Sessions))
	for id := range sm.state.Sessions {
		ids[id] = true
	}

	return ids
}

// FindOrphans scans the data dir for worktree and scratch directories with no
// matching session that are older than gcOrphanMinAge. It reads state under the
// lock to build the known-session set, then walks the filesystem lock-free.
func (sm *SessionManager) FindOrphans(now time.Time) []GCOrphan {
	known := sm.knownSessionIDs()

	var orphans []GCOrphan

	// Worktrees: <DataDir>/worktrees/<repoName>/<repoHash>/<sessionID>
	worktreesDir := filepath.Join(sm.paths.DataDir, "worktrees")
	if repos, err := os.ReadDir(worktreesDir); err == nil {
		orphans = append(orphans, findOrphanWorktrees(repos, worktreesDir, known, now)...)
	}

	// Scratch dirs: <DataDir>/scratch/<sessionID>
	scratchDir := filepath.Join(sm.paths.DataDir, "scratch")
	if scratches, err := os.ReadDir(scratchDir); err == nil {
		for _, s := range scratches {
			if !s.IsDir() || known[s.Name()] {
				continue
			}

			info, err := s.Info()
			if err != nil || now.Sub(info.ModTime()) < gcOrphanMinAge {
				continue
			}

			orphans = append(orphans, GCOrphan{
				Type: GCOrphanScratch,
				Path: filepath.Join(scratchDir, s.Name()),
				ID:   s.Name(),
			})
		}
	}

	return orphans
}

func findOrphanWorktrees(repos []os.DirEntry, worktreesDir string, known map[string]bool, now time.Time) []GCOrphan {
	var orphans []GCOrphan

	for _, repo := range repos {
		if !repo.IsDir() {
			continue
		}

		repoDir := filepath.Join(worktreesDir, repo.Name())

		hashes, err := os.ReadDir(repoDir)
		if err != nil {
			continue
		}

		for _, hash := range hashes {
			if !hash.IsDir() {
				continue
			}

			hashDir := filepath.Join(repoDir, hash.Name())

			sessions, err := os.ReadDir(hashDir)
			if err != nil {
				continue
			}

			for _, sess := range sessions {
				if !sess.IsDir() || known[sess.Name()] {
					continue
				}

				info, err := sess.Info()
				if err != nil || now.Sub(info.ModTime()) < gcOrphanMinAge {
					continue
				}

				sessDir := filepath.Join(hashDir, sess.Name())
				o := GCOrphan{Type: GCOrphanWorktree, Path: sessDir, ID: sess.Name()}

				if git.IsInsideGitRepo(sessDir) {
					o.IsGitWorktree = true
					if dirty, err := git.HasUncommittedChanges(sessDir); err == nil && dirty {
						o.HasDirtyFiles = true
					}
				}

				orphans = append(orphans, o)
			}
		}
	}

	return orphans
}

// RunGC finds orphaned directories and, when force is true, removes those that
// are safe to delete. A worktree with uncommitted changes is never removed
// (its unreachable work is preserved for manual recovery) and is reported as
// skipped. When force is false the returned orphans are a dry-run listing with
// no filesystem changes.
func (sm *SessionManager) RunGC(force bool, now time.Time) []GCOrphan {
	orphans := sm.FindOrphans(now)

	if !force {
		return orphans
	}

	for i := range orphans {
		o := &orphans[i]

		if o.HasDirtyFiles {
			o.Skipped = true
			o.Reason = "uncommitted changes — remove manually"

			continue
		}

		var repoRoot string
		if o.IsGitWorktree {
			repoRoot, _ = git.RepoRootFromWorktree(o.Path)
		}

		if err := os.RemoveAll(o.Path); err != nil {
			o.Skipped = true
			o.Reason = err.Error()

			continue
		}

		o.Removed = true

		// Prune the now-stale worktree admin entry from the owning repo so git
		// stops listing a worktree whose directory is gone.
		if repoRoot != "" {
			_ = git.PruneWorktrees(repoRoot)
		}
	}

	return orphans
}
