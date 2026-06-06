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
	if err := CreateBranch(repoPath, branchName, "origin/"+baseBranch); err != nil {
		return fmt.Errorf("create branch: %w", err)
	}
	if err := CreateWorktree(repoPath, worktreePath, branchName); err != nil {
		_ = DeleteBranch(repoPath, branchName)
		return fmt.Errorf("create worktree: %w", err)
	}
	return nil
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
