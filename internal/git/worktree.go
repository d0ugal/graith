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

// isBrokenWorktreeErr reports whether a `git worktree remove` failure is due to
// the worktree itself being missing/broken/unregistered — as opposed to the
// source repo being unreachable. Git returns exit 128 with one of these
// messages when the worktree's .git link is broken or its registration is gone,
// while the repo itself is still valid. In those cases teardown can safely fall
// back to removing the directory and pruning the stale registration (#741).
func isBrokenWorktreeErr(err error) bool {
	msg := err.Error()

	return strings.Contains(msg, "is not a working tree") ||
		strings.Contains(msg, "cannot remove working tree")
}

// TeardownSession removes a session's worktree and branch. It is idempotent: a
// missing or broken worktree is treated as already-removed and never blocks
// dropping the session. `git worktree remove --force` fails with exit 128 when
// the directory is gone or its .git link is broken; in those cases we remove
// the directory directly and prune the now-stale registration so a broken
// worktree can't wedge delete forever (#741). A failure that instead points at
// the source repo (unreachable / not a git repo) is surfaced so the session is
// kept for retry rather than silently dropped.
func TeardownSession(repoPath, worktreePath, branchName string) error {
	var errs []error

	switch _, statErr := os.Stat(worktreePath); {
	case statErr == nil:
		if err := RemoveWorktree(repoPath, worktreePath); err != nil {
			if isBrokenWorktreeErr(err) {
				// Broken or unregistered worktree: remove the directory
				// ourselves, then prune the stale git registration.
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
