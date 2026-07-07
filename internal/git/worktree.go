package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func CreateWorktree(repoPath, worktreePath, branchName string) error {
	_, err := RunOutput(repoPath, "worktree", "add", worktreePath, branchName)
	return err
}

func RemoveWorktree(repoPath, worktreePath string) error {
	_, err := RunOutput(repoPath, "worktree", "remove", "--force", worktreePath)
	return err
}

func SetupSession(ctx context.Context, repoPath, worktreePath, branchName, baseBranch string, fetch bool) error {
	if fetch && HasRemote(repoPath, "origin") {
		if err := FetchOriginContext(ctx, repoPath); err != nil {
			return fmt.Errorf("fetch: %w", err)
		}
	}

	startRef := "origin/" + baseBranch
	if !RefExists(repoPath, startRef) {
		startRef = baseBranch
	}

	if err := CreateBranch(repoPath, branchName, startRef); err != nil {
		return fmt.Errorf("create branch: %w", err)
	}

	if err := CreateWorktree(repoPath, worktreePath, branchName); err != nil {
		_ = DeleteBranch(repoPath, branchName)
		return fmt.Errorf("create worktree: %w", err)
	}

	return nil
}

func WorktreeGitDirs(worktreePath string) (gitDir, commonDir string, err error) {
	gitDir, err = RunOutput(worktreePath, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", "", fmt.Errorf("resolve git dir: %w", err)
	}

	commonDir, err = RunOutput(worktreePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", "", fmt.Errorf("resolve git common dir: %w", err)
	}

	return gitDir, commonDir, nil
}

func DiscoverDefaultBranchOrHEAD(repoPath string) (string, error) {
	branch, err := DiscoverDefaultBranch(repoPath)
	if err == nil {
		return branch, nil
	}

	out, headErr := RunOutput(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if headErr != nil || out == "HEAD" {
		return "", err
	}

	return out, nil
}

func PruneWorktrees(repoPath string) error {
	_, err := RunOutput(repoPath, "worktree", "prune")
	return err
}

func RepoRootFromWorktree(worktreePath string) (string, error) {
	commonDir, err := RunOutput(worktreePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}

	return filepath.Dir(commonDir), nil
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
func IsRegisteredWorktree(repoPath, worktreePath string) bool {
	out, err := RunOutput(repoPath, "worktree", "list", "--porcelain")
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
func TeardownSession(repoPath, worktreePath, branchName string) error {
	var errs []error

	switch _, statErr := os.Stat(worktreePath); {
	case statErr == nil:
		if err := RemoveWorktree(repoPath, worktreePath); err != nil {
			if IsRegisteredWorktree(repoPath, worktreePath) {
				// graith owns this worktree but git can't cleanly remove it
				// (broken .git link, etc). Remove the directory ourselves and
				// prune the now-stale registration.
				if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
					errs = append(errs, fmt.Errorf("remove worktree: %w (git worktree remove: %v)", rmErr, err))
				}

				_ = PruneWorktrees(repoPath)
			} else {
				errs = append(errs, fmt.Errorf("remove worktree: %w", err))
			}
		}
	case errors.Is(statErr, os.ErrNotExist):
		// Worktree directory already gone; drop the stale registration.
		_ = PruneWorktrees(repoPath)
	default:
		errs = append(errs, fmt.Errorf("stat worktree: %w", statErr))
	}

	if branchName != "" && RefExists(repoPath, branchName) {
		if err := DeleteBranch(repoPath, branchName); err != nil {
			errs = append(errs, fmt.Errorf("delete branch: %w", err))
		}
	}

	return errors.Join(errs...)
}
