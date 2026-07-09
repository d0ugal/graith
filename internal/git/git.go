package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func Run(dir string, args ...string) (string, string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

func RunContext(ctx context.Context, dir string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

func RunContextEnv(ctx context.Context, dir string, env []string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir

	cmd.Env = append(os.Environ(), env...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

func RunOutput(dir string, args ...string) (string, error) {
	stdout, stderr, err := Run(dir, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, stderr)
	}

	return stdout, nil
}

func RunOutputContext(ctx context.Context, dir string, args ...string) (string, error) {
	stdout, stderr, err := RunContext(ctx, dir, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, stderr)
	}

	return stdout, nil
}

func RunCheck(dir string, args ...string) bool {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	return cmd.Run() == nil
}

func IsInsideGitRepo(dir string) bool {
	return RunCheck(dir, "rev-parse", "--is-inside-work-tree")
}

func RefExists(dir string, ref string) bool {
	return RunCheck(dir, "rev-parse", "--verify", ref)
}

func HasRemote(dir string, name string) bool {
	out, err := RunOutput(dir, "remote")
	if err != nil {
		return false
	}

	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}

	return false
}

func HasUncommittedChanges(dir string) (bool, error) {
	out, err := RunOutput(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}

	return len(out) > 0, nil
}

// UnpushedCommitCount returns the number of commits on HEAD that have not been
// pushed to the remote.
//
// It compares against the current branch's remote tracking ref
// (origin/<branch>) when that ref exists. This answers "have I pushed my
// commits?" and reflects real push state without any network I/O: `git push`
// updates the local tracking ref, and after a branch's PR is merged the
// tracking ref still points at the pushed tip, so the count correctly reads 0
// (no false "N ahead of main"). See issue #197.
//
// When the branch has never been pushed (no tracking ref) it falls back to
// counting commits ahead of the base branch — everything that would be pushed.
// The base ref itself may be stale without a fetch; a periodic fetch in the
// daemon keeps it reasonably fresh.
func UnpushedCommitCount(worktreePath, baseBranch string) (int, error) {
	branch, err := RunOutput(worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil && branch != "" && branch != "HEAD" {
		if trackingRef := "origin/" + branch; RefExists(worktreePath, trackingRef) {
			return commitCount(worktreePath, trackingRef+"..HEAD")
		}
	}

	// Branch not pushed yet (no tracking ref): count commits ahead of the base.
	baseRef := "origin/" + baseBranch
	if !RefExists(worktreePath, baseRef) {
		baseRef = baseBranch
	}

	return commitCount(worktreePath, baseRef+"..HEAD")
}

func commitCount(worktreePath, revRange string) (int, error) {
	out, err := RunOutput(worktreePath, "rev-list", "--count", revRange)
	if err != nil {
		return 0, err
	}

	var n int
	if _, err := fmt.Sscanf(out, "%d", &n); err != nil {
		return 0, fmt.Errorf("parse commit count %q: %w", out, err)
	}

	return n, nil
}

// FetchRemote updates remote tracking refs from origin. It prunes deleted
// remote branches and never rewrites local branches. It is best-effort:
// callers use it to keep base-branch refs fresh for the diverged-from-base
// count and should tolerate failures (offline, no remote).
func FetchRemote(ctx context.Context, worktreePath string) error {
	_, stderr, err := RunContext(ctx, worktreePath, "fetch", "--prune", "--quiet", "origin")
	if err != nil {
		return fmt.Errorf("git fetch origin: %w\nstderr: %s", err, stderr)
	}

	return nil
}

func DirtyFiles(dir string) ([]string, error) {
	out, err := RunOutput(dir, "status", "--porcelain")
	if err != nil {
		return nil, err
	}

	if out == "" {
		return nil, nil
	}

	return strings.Split(out, "\n"), nil
}

func UnpushedCommitSummaries(worktreePath, baseBranch string) ([]string, error) {
	baseRef := "origin/" + baseBranch
	if !RefExists(worktreePath, baseRef) {
		baseRef = baseBranch
	}

	out, err := RunOutput(worktreePath, "log", "--oneline", baseRef+"..HEAD")
	if err != nil {
		return nil, err
	}

	if out == "" {
		return nil, nil
	}

	return strings.Split(out, "\n"), nil
}

func RepoRootPath(dir string) (string, error) {
	return RunOutput(dir, "rev-parse", "--show-toplevel")
}
