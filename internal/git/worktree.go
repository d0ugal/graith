package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (r Runner) CreateWorktree(repoPath, worktreePath, branchName string) error {
	_, err := r.RunOutput(repoPath, "worktree", "add", worktreePath, branchName)
	return err
}

func CreateWorktree(repoPath, worktreePath, branchName string) error {
	return NewRunner().CreateWorktree(repoPath, worktreePath, branchName)
}

func (r Runner) RemoveWorktree(repoPath, worktreePath string) error {
	_, err := r.RunOutput(repoPath, "worktree", "remove", "--force", worktreePath)
	return err
}

func RemoveWorktree(repoPath, worktreePath string) error {
	return NewRunner().RemoveWorktree(repoPath, worktreePath)
}

// SetupSession creates a session's branch and worktree, optionally fetching
// origin first. It runs several git subprocesses (fetch → ref check → branch →
// worktree add) on one Runner to stay generation-coherent if a [tools] reload
// swaps the git executable mid-setup (#1287). The Runner-method form lets an
// enclosing create/fork operation share a single pinned Runner across all its
// git calls.
func (r Runner) SetupSession(ctx context.Context, repoPath, worktreePath, branchName, baseBranch string, fetch bool) error {
	if fetch && r.HasRemote(repoPath, "origin") {
		if err := r.FetchOriginContext(ctx, repoPath); err != nil {
			return fmt.Errorf("fetch: %w", err)
		}
	}

	startRef := "origin/" + baseBranch
	if !r.RefExists(repoPath, startRef) {
		startRef = baseBranch
	}

	if err := r.CreateBranch(repoPath, branchName, startRef); err != nil {
		return fmt.Errorf("create branch: %w", err)
	}

	if err := r.CreateWorktree(repoPath, worktreePath, branchName); err != nil {
		_ = r.DeleteBranch(repoPath, branchName)
		return fmt.Errorf("create worktree: %w", err)
	}

	return nil
}

func SetupSession(ctx context.Context, repoPath, worktreePath, branchName, baseBranch string, fetch bool) error {
	return NewRunner().SetupSession(ctx, repoPath, worktreePath, branchName, baseBranch, fetch)
}

func (r Runner) WorktreeGitDirs(worktreePath string) (gitDir, commonDir string, err error) {
	gitDir, err = r.RunOutput(worktreePath, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", "", fmt.Errorf("resolve git dir: %w", err)
	}

	commonDir, err = r.RunOutput(worktreePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", "", fmt.Errorf("resolve git common dir: %w", err)
	}

	return gitDir, commonDir, nil
}

func WorktreeGitDirs(worktreePath string) (gitDir, commonDir string, err error) {
	return NewRunner().WorktreeGitDirs(worktreePath)
}

// DiscoverDefaultBranchOrHEAD resolves the default branch, falling back to the
// current HEAD's short name. Both the primary discovery and the HEAD fallback
// run on one pinned Runner so the whole operation — including the fallback —
// stays on a single tools generation (#1287).
func (r Runner) DiscoverDefaultBranchOrHEAD(repoPath string) (string, error) {
	branch, err := r.DiscoverDefaultBranch(repoPath)
	if err == nil {
		return branch, nil
	}

	out, headErr := r.RunOutput(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if headErr != nil || out == "HEAD" {
		return "", err
	}

	return out, nil
}

func DiscoverDefaultBranchOrHEAD(repoPath string) (string, error) {
	return NewRunner().DiscoverDefaultBranchOrHEAD(repoPath)
}

func (r Runner) PruneWorktrees(repoPath string) error {
	_, err := r.RunOutput(repoPath, "worktree", "prune")
	return err
}

func PruneWorktrees(repoPath string) error {
	return NewRunner().PruneWorktrees(repoPath)
}

func (r Runner) RepoRootFromWorktree(worktreePath string) (string, error) {
	commonDir, err := r.RunOutput(worktreePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}

	return filepath.Dir(commonDir), nil
}

func RepoRootFromWorktree(worktreePath string) (string, error) {
	return NewRunner().RepoRootFromWorktree(worktreePath)
}

// resolvePath returns the canonical path for comparison, following symlinks
// when possible (macOS /var → /private/var) and falling back to a lexical
// clean when the path can't be resolved.
func resolvePath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}

	return filepath.Clean(p)
}

// IsRegisteredWorktree reports whether worktreePath is registered as a worktree
// of the repo at repoPath. It matches even a worktree whose .git link is broken,
// since git still lists a stale (prunable) registration — that registration is
// the signal graith owns the path and may remove it during teardown (#741). It
// returns false when the repo is unreachable or the path is not a registered
// worktree (an unrelated directory, an independent repo, or an already-orphaned
// entry), so teardown never deletes a directory graith doesn't own. Detection
// is based on the stable `--porcelain` listing rather than git's error text, so
// it is independent of git's locale and message wording.
func (r Runner) IsRegisteredWorktree(repoPath, worktreePath string) bool {
	out, err := r.RunOutput(repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}

	target := resolvePath(worktreePath)

	for line := range strings.SplitSeq(out, "\n") {
		p, ok := strings.CutPrefix(line, "worktree ")
		if ok && resolvePath(p) == target {
			return true
		}
	}

	return false
}

func IsRegisteredWorktree(repoPath, worktreePath string) bool {
	return NewRunner().IsRegisteredWorktree(repoPath, worktreePath)
}

// TeardownSession removes a session's worktree and branch. It is idempotent: a
// missing or broken worktree is treated as already-removed and never blocks
// dropping the session. `git worktree remove --force` fails with exit 128 when
// the directory is gone or its .git link is broken; when git still lists the
// path as a registered worktree of this repo (as it does for a broken link) we
// remove the directory directly and prune the stale registration so a broken
// worktree can't wedge delete forever (#741). A failure where the path is not a
// registered worktree — an unreachable/invalid source repo, or a stale path
// pointing somewhere graith doesn't own — is surfaced so the session is kept
// for retry rather than dropped (and nothing unowned is deleted).
//
// It runs on one Runner (remove worktree → prune → delete branch) so a
// mid-teardown [tools] reload can't split it across executables (#1287); the
// Runner-method form lets an enclosing create/fork cleanup share the operation's
// pinned Runner.
func (r Runner) TeardownSession(repoPath, worktreePath, branchName string) error {
	var errs []error

	switch _, statErr := os.Stat(worktreePath); {
	case statErr == nil:
		if err := r.RemoveWorktree(repoPath, worktreePath); err != nil {
			if r.IsRegisteredWorktree(repoPath, worktreePath) {
				// graith owns this worktree but git can't cleanly remove it
				// (broken .git link, etc). Remove the directory ourselves and
				// prune the now-stale registration.
				if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
					errs = append(errs, fmt.Errorf("remove worktree: %w (git worktree remove: %w)", rmErr, err))
				}

				_ = r.PruneWorktrees(repoPath)
			} else {
				errs = append(errs, fmt.Errorf("remove worktree: %w", err))
			}
		}
	case errors.Is(statErr, os.ErrNotExist):
		// Worktree directory already gone; drop the stale registration.
		_ = r.PruneWorktrees(repoPath)
	default:
		errs = append(errs, fmt.Errorf("stat worktree: %w", statErr))
	}

	if branchName != "" && r.RefExists(repoPath, branchName) {
		if err := r.DeleteBranch(repoPath, branchName); err != nil {
			errs = append(errs, fmt.Errorf("delete branch: %w", err))
		}
	}

	return errors.Join(errs...)
}

func TeardownSession(repoPath, worktreePath, branchName string) error {
	return NewRunner().TeardownSession(repoPath, worktreePath, branchName)
}
