package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/d0ugal/graith/internal/git"
)

// WorktreePort is the Git lifecycle surface owned by the daemon's session
// creation path. Keeping this contract here lets lifecycle rollback tests use
// a deterministic provider without invoking a process-level Git command.
type WorktreePort interface {
	IsInsideRepo(path string) bool
	RepoRoot(path string) (string, error)
	DiscoverGitHubUsername(ctx context.Context, repoPath string) (string, error)
	DiscoverDefaultBranch(repoPath string) (string, error)
	DiscoverDefaultBranchOrHEAD(repoPath string) (string, error)
	Setup(ctx context.Context, repoPath, worktreePath, branchName, baseBranch string, fetch bool) error
	Teardown(repoPath, worktreePath, branchName string) error
}

type gitWorktreeAdapter struct{}

func (gitWorktreeAdapter) IsInsideRepo(path string) bool {
	return git.IsInsideGitRepo(path)
}

func (gitWorktreeAdapter) RepoRoot(path string) (string, error) {
	return git.RepoRootPath(path)
}

func (gitWorktreeAdapter) DiscoverGitHubUsername(ctx context.Context, repoPath string) (string, error) {
	return git.DiscoverGitHubUsername(ctx, repoPath)
}

func (gitWorktreeAdapter) DiscoverDefaultBranch(repoPath string) (string, error) {
	return git.DiscoverDefaultBranch(repoPath)
}

func (gitWorktreeAdapter) DiscoverDefaultBranchOrHEAD(repoPath string) (string, error) {
	return git.DiscoverDefaultBranchOrHEAD(repoPath)
}

func (gitWorktreeAdapter) Setup(ctx context.Context, repoPath, worktreePath, branchName, baseBranch string, fetch bool) error {
	return git.SetupSession(ctx, repoPath, worktreePath, branchName, baseBranch, fetch)
}

func (gitWorktreeAdapter) Teardown(repoPath, worktreePath, branchName string) error {
	return git.TeardownSession(repoPath, worktreePath, branchName)
}

func defaultWorktreePort() WorktreePort {
	return gitWorktreeAdapter{}
}

func (sm *SessionManager) teardownWorktreePort(port WorktreePort, mainRepoPath, mainWorktreePath, mainBranch string, includes []IncludedRepoState) {
	if err := teardownWorktreePort(port, mainRepoPath, mainWorktreePath, mainBranch, includes); err != nil {
		sm.log.Warn("failed to rollback worktree setup", "path", mainWorktreePath, "err", err)
	}
}

func teardownWorktreePort(port WorktreePort, mainRepoPath, mainWorktreePath, mainBranch string, includes []IncludedRepoState) error {
	var errs []error

	for i := len(includes) - 1; i >= 0; i-- {
		inc := includes[i]
		if err := port.Teardown(inc.RepoPath, inc.WorktreePath, inc.Branch); err != nil {
			errs = append(errs, err)
		}
	}

	if err := port.Teardown(mainRepoPath, mainWorktreePath, mainBranch); err != nil {
		errs = append(errs, err)
	}

	if err := os.RemoveAll(filepath.Dir(mainWorktreePath)); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}
