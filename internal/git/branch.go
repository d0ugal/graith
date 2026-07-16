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

	return "", errors.New("cannot determine default branch; use --base to specify one")
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
