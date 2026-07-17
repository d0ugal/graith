package git

import (
	"context"
	"errors"
	"strings"
)

// DiscoverDefaultBranch resolves the repository's default branch. It makes
// several git calls (remote check, rev-parse, ref existence), so within a
// multi-step operation it must run on a pinned Runner to stay generation
// coherent (#1287).
func (r Runner) DiscoverDefaultBranch(repoPath string) (string, error) {
	hasOrigin := r.HasRemote(repoPath, "origin")
	if hasOrigin {
		out, err := r.RunOutput(repoPath, "rev-parse", "--abbrev-ref", "origin/HEAD")
		if err == nil && out != "origin/HEAD" {
			return strings.TrimPrefix(out, "origin/"), nil
		}

		for _, branch := range []string{"main", "master"} {
			if r.RefExists(repoPath, "origin/"+branch) {
				return branch, nil
			}
		}
	}

	for _, branch := range []string{"main", "master"} {
		if r.RefExists(repoPath, branch) {
			return branch, nil
		}
	}

	return "", errors.New("cannot determine default branch; use --base to specify one")
}

func DiscoverDefaultBranch(repoPath string) (string, error) {
	return NewRunner().DiscoverDefaultBranch(repoPath)
}

func (r Runner) CreateBranch(repoPath, branchName, fromRef string) error {
	_, err := r.RunOutput(repoPath, "branch", branchName, fromRef)
	return err
}

func CreateBranch(repoPath, branchName, fromRef string) error {
	return NewRunner().CreateBranch(repoPath, branchName, fromRef)
}

func (r Runner) DeleteBranch(repoPath, branchName string) error {
	_, err := r.RunOutput(repoPath, "branch", "-D", branchName)
	return err
}

func DeleteBranch(repoPath, branchName string) error {
	return NewRunner().DeleteBranch(repoPath, branchName)
}

func FetchOrigin(repoPath string) error {
	_, err := RunOutput(repoPath, "fetch", "origin")
	return err
}

func (r Runner) FetchOriginContext(ctx context.Context, repoPath string) error {
	_, err := r.RunOutputContext(ctx, repoPath, "fetch", "origin")
	return err
}

func FetchOriginContext(ctx context.Context, repoPath string) error {
	return NewRunner().FetchOriginContext(ctx, repoPath)
}
