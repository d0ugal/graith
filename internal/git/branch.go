package git

import (
	"context"
	"errors"
	"strings"
)

func DiscoverDefaultBranch(repoPath string) (string, error) {
	hasOrigin := HasRemote(repoPath, "origin")
	if hasOrigin {
		out, err := RunOutput(repoPath, "rev-parse", "--abbrev-ref", "origin/HEAD")
		if err == nil && out != "origin/HEAD" {
			return strings.TrimPrefix(out, "origin/"), nil
		}

		for _, branch := range []string{"main", "master"} {
			if RefExists(repoPath, "origin/"+branch) {
				return branch, nil
			}
		}
	}

	for _, branch := range []string{"main", "master"} {
		if RefExists(repoPath, branch) {
			return branch, nil
		}
	}

	// A freshly initialized repository has a symbolic HEAD but no branch ref
	// until its first commit. Preserve that initial branch name so callers can
	// create an isolated orphan worktree instead of requiring a synthetic
	// bootstrap commit in the source checkout.
	if branch, ok := unbornBranch(repoPath); ok {
		return branch, nil
	}

	return "", errors.New("cannot determine default branch; use --base to specify one")
}

func unbornBranch(repoPath string) (string, bool) {
	if RefExists(repoPath, "HEAD") {
		return "", false
	}

	branch, err := RunOutput(repoPath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch == "" {
		return "", false
	}

	return branch, true
}

func CreateBranch(repoPath, branchName, fromRef string) error {
	_, err := RunOutput(repoPath, "branch", branchName, fromRef)
	return err
}

func DeleteBranch(repoPath, branchName string) error {
	_, err := RunOutput(repoPath, "branch", "-D", branchName)
	return err
}

func FetchOrigin(repoPath string) error {
	_, err := RunOutput(repoPath, "fetch", "origin")
	return err
}

func FetchOriginContext(ctx context.Context, repoPath string) error {
	_, err := RunOutputContext(ctx, repoPath, "fetch", "origin")
	return err
}
