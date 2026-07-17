package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/d0ugal/graith/internal/tools"
)

// Runner pins the git executable resolved from the tools registry at
// construction. A single multi-step git operation (git-pull's
// rev-parse→fetch→merge, username discovery, a store write) runs several git
// subprocesses; resolving tools.Git() independently for each one lets a config
// reload that swaps the executable land between subcommands, splitting one
// operation across two binaries (#1287). Snapshot a Runner once with NewRunner
// and thread it through every subprocess so the whole operation stays on one
// tools generation, while a later operation picks up the new one wholesale.
//
// The package-level Run/RunOutput/… functions delegate to a freshly-resolved
// Runner, preserving per-call resolution for one-shot callers.
type Runner struct {
	git string
}

// NewRunner snapshots the current git executable for one multi-step operation.
func NewRunner() Runner {
	return Runner{git: tools.Git()}
}

// NewRunnerWith pins an explicitly-resolved git executable, so a caller that
// captured a tools.Snapshot atomically with its config snapshot can build a
// Runner from the same generation (#1287).
func NewRunnerWith(gitBin string) Runner {
	return Runner{git: gitBin}
}

// bin returns the pinned executable, falling back to a live resolution for a
// zero-value Runner so an accidentally-unpinned Runner still runs git.
func (r Runner) bin() string {
	if r.git == "" {
		return tools.Git()
	}

	return r.git
}

func (r Runner) Run(dir string, args ...string) (string, string, error) {
	cmd := exec.Command(r.bin(), args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

func (r Runner) RunContext(ctx context.Context, dir string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, r.bin(), args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

func (r Runner) RunContextEnv(ctx context.Context, dir string, env []string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, r.bin(), args...)
	cmd.Dir = dir

	cmd.Env = append(os.Environ(), env...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

func (r Runner) RunOutput(dir string, args ...string) (string, error) {
	stdout, stderr, err := r.Run(dir, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, stderr)
	}

	return stdout, nil
}

func (r Runner) RunOutputContext(ctx context.Context, dir string, args ...string) (string, error) {
	stdout, stderr, err := r.RunContext(ctx, dir, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, stderr)
	}

	return stdout, nil
}

func (r Runner) RunCheck(dir string, args ...string) bool {
	cmd := exec.Command(r.bin(), args...)
	cmd.Dir = dir

	return cmd.Run() == nil
}

func (r Runner) RefExists(dir string, ref string) bool {
	return r.RunCheck(dir, "rev-parse", "--verify", ref)
}

func (r Runner) HasRemote(dir string, name string) bool {
	out, err := r.RunOutput(dir, "remote")
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

func (r Runner) HasUncommittedChanges(dir string) (bool, error) {
	out, err := r.RunOutput(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}

	return len(out) > 0, nil
}

func Run(dir string, args ...string) (string, string, error) {
	return NewRunner().Run(dir, args...)
}

func RunContext(ctx context.Context, dir string, args ...string) (string, string, error) {
	return NewRunner().RunContext(ctx, dir, args...)
}

func RunContextEnv(ctx context.Context, dir string, env []string, args ...string) (string, string, error) {
	return NewRunner().RunContextEnv(ctx, dir, env, args...)
}

func RunOutput(dir string, args ...string) (string, error) {
	return NewRunner().RunOutput(dir, args...)
}

func RunOutputContext(ctx context.Context, dir string, args ...string) (string, error) {
	return NewRunner().RunOutputContext(ctx, dir, args...)
}

func RunCheck(dir string, args ...string) bool {
	return NewRunner().RunCheck(dir, args...)
}

func (r Runner) IsInsideGitRepo(dir string) bool {
	return r.RunCheck(dir, "rev-parse", "--is-inside-work-tree")
}

func IsInsideGitRepo(dir string) bool {
	return NewRunner().IsInsideGitRepo(dir)
}

func RefExists(dir string, ref string) bool {
	return NewRunner().RefExists(dir, ref)
}

func HasRemote(dir string, name string) bool {
	return NewRunner().HasRemote(dir, name)
}

func HasUncommittedChanges(dir string) (bool, error) {
	return NewRunner().HasUncommittedChanges(dir)
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
func (r Runner) UnpushedCommitCount(worktreePath, baseBranch string) (int, error) {
	branch, err := r.RunOutput(worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil && branch != "" && branch != "HEAD" {
		if trackingRef := "origin/" + branch; r.RefExists(worktreePath, trackingRef) {
			return r.commitCount(worktreePath, trackingRef+"..HEAD")
		}
	}

	// Branch not pushed yet (no tracking ref): count commits ahead of the base.
	baseRef := "origin/" + baseBranch
	if !r.RefExists(worktreePath, baseRef) {
		baseRef = baseBranch
	}

	return r.commitCount(worktreePath, baseRef+"..HEAD")
}

func UnpushedCommitCount(worktreePath, baseBranch string) (int, error) {
	return NewRunner().UnpushedCommitCount(worktreePath, baseBranch)
}

func (r Runner) commitCount(worktreePath, revRange string) (int, error) {
	out, err := r.RunOutput(worktreePath, "rev-list", "--count", revRange)
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
func (r Runner) FetchRemote(ctx context.Context, worktreePath string) error {
	_, stderr, err := r.RunContext(ctx, worktreePath, "fetch", "--prune", "--quiet", "origin")
	if err != nil {
		return fmt.Errorf("git fetch origin: %w\nstderr: %s", err, stderr)
	}

	return nil
}

func FetchRemote(ctx context.Context, worktreePath string) error {
	return NewRunner().FetchRemote(ctx, worktreePath)
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

func (r Runner) UnpushedCommitSummaries(worktreePath, baseBranch string) ([]string, error) {
	baseRef := "origin/" + baseBranch
	if !r.RefExists(worktreePath, baseRef) {
		baseRef = baseBranch
	}

	out, err := r.RunOutput(worktreePath, "log", "--oneline", baseRef+"..HEAD")
	if err != nil {
		return nil, err
	}

	if out == "" {
		return nil, nil
	}

	return strings.Split(out, "\n"), nil
}

func UnpushedCommitSummaries(worktreePath, baseBranch string) ([]string, error) {
	return NewRunner().UnpushedCommitSummaries(worktreePath, baseBranch)
}

func (r Runner) RepoRootPath(dir string) (string, error) {
	return r.RunOutput(dir, "rev-parse", "--show-toplevel")
}

func RepoRootPath(dir string) (string, error) {
	return NewRunner().RepoRootPath(dir)
}
