package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/git"
)

var gitNoPromptEnv = []string{
	"GIT_TERMINAL_PROMPT=0",
	"GIT_SSH_COMMAND=ssh -o BatchMode=yes",
}

func (sm *SessionManager) RunGitPullLoop(ctx context.Context) {
	for {
		sm.mu.RLock()
		cfg := sm.cfg
		sm.mu.RUnlock()

		interval := cfg.GitPull.IntervalDuration()

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}

		sm.mu.RLock()
		cfg = sm.cfg
		sm.mu.RUnlock()

		if cfg.GitPull.Enabled {
			sm.runGitPullTick(ctx)
		}
	}
}

func (sm *SessionManager) runGitPullTick(ctx context.Context) {
	repos, err := git.ListMaintenanceRepos(ctx)
	if err != nil {
		sm.log.Warn("git-pull: failed to list maintenance repos", "err", err)
		return
	}

	if len(repos) == 0 {
		return
	}

	sm.mu.RLock()
	cfg := sm.cfg
	sm.mu.RUnlock()

	seen := make(map[string]bool)

	var eligible []string

	for _, repo := range repos {
		resolved := config.ResolvePath(repo)
		if seen[resolved] {
			continue
		}

		seen[resolved] = true
		if !cfg.RepoPathAllowed(resolved) {
			continue
		}

		eligible = append(eligible, resolved)
	}

	var updated, skipped, errored int

	for _, repo := range eligible {
		if ctx.Err() != nil {
			return
		}

		pulled, err := sm.pullIfClean(ctx, repo)
		switch {
		case err != nil:
			sm.log.Warn("git-pull: error", "repo", repo, "err", err)

			errored++
		case pulled:
			updated++
		default:
			skipped++
		}
	}

	sm.log.Info("git-pull: cycle complete", "updated", updated, "skipped", skipped, "errors", errored)
}

func (sm *SessionManager) pullIfClean(ctx context.Context, repoPath string) (bool, error) {
	isBare, err := git.RunOutputContext(ctx, repoPath, "rev-parse", "--is-bare-repository")
	if err != nil {
		return false, fmt.Errorf("checking bare: %w", err)
	}

	if isBare == "true" {
		sm.log.Debug("git-pull: skipping bare repo", "repo", repoPath)
		return false, nil
	}

	gitDir, err := git.RunOutputContext(ctx, repoPath, "rev-parse", "--git-dir")
	if err != nil {
		return false, fmt.Errorf("resolving git dir: %w", err)
	}

	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repoPath, gitDir)
	}

	if hasInProgressOp(gitDir) {
		sm.log.Debug("git-pull: skipping repo with in-progress git operation", "repo", repoPath)
		return false, nil
	}

	branch, _, err := git.RunContext(ctx, repoPath, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil {
		sm.log.Debug("git-pull: skipping detached HEAD", "repo", repoPath)
		return false, nil
	}

	branch = strings.TrimSpace(branch)
	if branch == "" {
		sm.log.Debug("git-pull: skipping detached HEAD", "repo", repoPath)
		return false, nil
	}

	defaultBranch, err := git.DiscoverDefaultBranch(repoPath)
	if err != nil {
		sm.log.Warn("git-pull: cannot determine default branch", "repo", repoPath, "err", err)
		return false, nil
	}

	if branch != defaultBranch {
		sm.log.Debug("git-pull: skipping non-default branch", "repo", repoPath, "branch", branch, "default", defaultBranch)
		return false, nil
	}

	if sm.hasBlockingSessionForRepo(repoPath, defaultBranch) {
		sm.log.Debug("git-pull: skipping repo with active session on default branch", "repo", repoPath)
		return false, nil
	}

	remote, upstreamRef := resolveUpstream(ctx, repoPath, branch)
	if remote == "" {
		sm.log.Debug("git-pull: skipping repo with no upstream", "repo", repoPath)
		return false, nil
	}

	dirty, err := git.HasUncommittedChanges(repoPath)
	if err != nil {
		return false, fmt.Errorf("checking dirty state: %w", err)
	}

	if dirty {
		sm.log.Debug("git-pull: skipping dirty repo", "repo", repoPath)
		return false, nil
	}

	oldHead, err := git.RunOutputContext(ctx, repoPath, "rev-parse", "--short", "HEAD")
	if err != nil {
		return false, fmt.Errorf("capturing old HEAD: %w", err)
	}

	fetchCtx, fetchCancel := context.WithTimeout(ctx, gitFetchTimeout)
	defer fetchCancel()

	_, fetchStderr, err := git.RunContextEnv(fetchCtx, repoPath, gitNoPromptEnv, "-c", "core.hooksPath=/dev/null", "fetch", "--", remote)
	if err != nil {
		return false, fmt.Errorf("fetching %s: %w (stderr: %s)", remote, err, fetchStderr)
	}

	mergeTarget := upstreamRef
	if mergeTarget == "" {
		mergeTarget = remote + "/" + branch
	}

	if !git.RefExists(repoPath, mergeTarget) {
		sm.log.Debug("git-pull: remote tracking ref missing after fetch", "repo", repoPath, "ref", mergeTarget)
		return false, nil
	}

	headRev, err := git.RunOutputContext(ctx, repoPath, "rev-parse", "HEAD")
	if err != nil {
		return false, fmt.Errorf("rev-parse HEAD: %w", err)
	}

	remoteRev, err := git.RunOutputContext(ctx, repoPath, "rev-parse", mergeTarget)
	if err != nil {
		return false, fmt.Errorf("rev-parse %s: %w", mergeTarget, err)
	}

	if headRev == remoteRev {
		sm.log.Debug("git-pull: already up-to-date", "repo", repoPath)
		return false, nil
	}

	if !git.RunCheck(repoPath, "merge-base", "--is-ancestor", "HEAD", mergeTarget) {
		if git.RunCheck(repoPath, "merge-base", "--is-ancestor", mergeTarget, "HEAD") {
			sm.log.Debug("git-pull: local ahead of remote", "repo", repoPath)
		} else {
			sm.log.Debug("git-pull: branches diverged", "repo", repoPath)
		}

		return false, nil
	}

	dirty, err = git.HasUncommittedChanges(repoPath)
	if err != nil {
		return false, fmt.Errorf("re-checking dirty state: %w", err)
	}

	if dirty {
		sm.log.Debug("git-pull: skipping repo that became dirty during fetch", "repo", repoPath)
		return false, nil
	}

	if hasInProgressOp(gitDir) {
		sm.log.Debug("git-pull: skipping repo with git operation started during fetch", "repo", repoPath)
		return false, nil
	}

	currentBranch, _, err := git.RunContext(ctx, repoPath, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil || strings.TrimSpace(currentBranch) != branch {
		sm.log.Debug("git-pull: skipping repo whose branch changed during fetch", "repo", repoPath)
		return false, nil
	}

	if sm.hasBlockingSessionForRepo(repoPath, defaultBranch) {
		sm.log.Debug("git-pull: skipping repo with blocking session created during fetch", "repo", repoPath)
		return false, nil
	}

	mergeCtx, mergeCancel := context.WithTimeout(ctx, gitMergeTimeout)
	defer mergeCancel()

	_, stderr, err := git.RunContextEnv(mergeCtx, repoPath, gitNoPromptEnv, "-c", "core.hooksPath=/dev/null", "merge", "--ff-only", "--quiet", "--", mergeTarget)
	if err != nil {
		return false, fmt.Errorf("ff-only merge: %w (stderr: %s)", err, stderr)
	}

	newHead, _ := git.RunOutputContext(ctx, repoPath, "rev-parse", "--short", "HEAD")
	sm.log.Info("git-pull: updated", "repo", repoPath, "old", oldHead, "new", newHead)

	return true, nil
}

// hasBlockingSessionForRepo reports whether an active session would be
// disrupted by fast-forwarding defaultBranch in repoPath's source checkout.
//
// Sessions run in their own worktrees on feature branches, which share the
// object store but not the working tree or the default-branch ref — a
// fast-forward of the default branch cannot disturb them, so their presence
// must not block the pull (otherwise a repo you develop in via graith would
// never auto-update). Only two cases are unsafe: an in-place session working
// directly in the source checkout, and a worktree that has the default branch
// itself checked out. Those are the only sessions that block the pull.
func (sm *SessionManager) hasBlockingSessionForRepo(repoPath, defaultBranch string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	repoPath = config.ResolvePath(repoPath)

	blocks := func(sRepo, worktree, branch string) bool {
		if sRepo == "" || config.ResolvePath(sRepo) != repoPath {
			return false
		}

		// In-place session operating directly in the source checkout.
		if worktree != "" && config.ResolvePath(worktree) == repoPath {
			return true
		}

		// Worktree that has the branch we are about to move checked out.
		if defaultBranch != "" && branch == defaultBranch {
			return true
		}

		return false
	}

	for _, s := range sm.state.Sessions {
		if s.Status != StatusRunning && s.Status != StatusCreating {
			continue
		}

		if blocks(s.RepoPath, s.WorktreePath, s.Branch) {
			return true
		}

		for _, inc := range s.Includes {
			if blocks(inc.RepoPath, inc.WorktreePath, inc.Branch) {
				return true
			}
		}
	}

	return false
}

func resolveUpstream(ctx context.Context, repoPath, branch string) (remote string, upstreamRef string) {
	out, err := git.RunOutputContext(ctx, repoPath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", branch+"@{upstream}")
	if err == nil && out != "" {
		upstreamRef = out
		if idx := strings.Index(out, "/"); idx > 0 {
			remote = out[:idx]
		}

		return remote, upstreamRef
	}

	if git.HasRemote(repoPath, "origin") {
		return "origin", ""
	}

	return "", ""
}

func hasInProgressOp(gitDir string) bool {
	indicators := []string{
		"MERGE_HEAD",
		"REBASE_HEAD",
		"CHERRY_PICK_HEAD",
		"BISECT_LOG",
		"REVERT_HEAD",
		"rebase-merge",
		"rebase-apply",
		"sequencer",
	}
	for _, name := range indicators {
		if _, err := os.Stat(filepath.Join(gitDir, name)); err == nil {
			return true
		}
	}

	return false
}
