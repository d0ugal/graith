package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func TeardownSession(repoPath, worktreePath, branchName string) error {
	var errs []error
	if _, err := os.Stat(worktreePath); err == nil {
		if err := RemoveWorktree(repoPath, worktreePath); err != nil {
			errs = append(errs, fmt.Errorf("remove worktree: %w", err))
		}
	} else if errors.Is(err, os.ErrNotExist) {
		_ = PruneWorktrees(repoPath)
	} else {
		errs = append(errs, fmt.Errorf("stat worktree: %w", err))
	}
	if branchName != "" && RefExists(repoPath, branchName) {
		if err := DeleteBranch(repoPath, branchName); err != nil {
			errs = append(errs, fmt.Errorf("delete branch: %w", err))
		}
	}
	return errors.Join(errs...)
}
