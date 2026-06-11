package git

import (
	"errors"
	"fmt"
)

func CreateWorktree(repoPath, worktreePath, branchName string) error {
	_, err := RunOutput(repoPath, "worktree", "add", worktreePath, branchName)
	return err
}

func RemoveWorktree(repoPath, worktreePath string) error {
	_, err := RunOutput(repoPath, "worktree", "remove", "--force", worktreePath)
	return err
}

func SetupSession(repoPath, worktreePath, branchName, baseBranch string, fetch bool) error {
	if fetch {
		if err := FetchOrigin(repoPath); err != nil {
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

func TeardownSession(repoPath, worktreePath, branchName string) error {
	var errs []error
	if err := RemoveWorktree(repoPath, worktreePath); err != nil {
		errs = append(errs, fmt.Errorf("remove worktree: %w", err))
	}
	if err := DeleteBranch(repoPath, branchName); err != nil {
		errs = append(errs, fmt.Errorf("delete branch: %w", err))
	}
	return errors.Join(errs...)
}
